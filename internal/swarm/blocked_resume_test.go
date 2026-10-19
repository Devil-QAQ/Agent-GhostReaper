package swarm

// P3-05c ① 恢复闭环·受阻保留会话与原会话续:模型故障受阻(blocked)
// 收场时 session_finished 带 Resumable 标记——恢复重开该步骤后,新领取
// 的单位优先从转录恢复原会话原地续跑(上下文零重建,transcript 连续、
// 任务包不重注入);预算耗尽(exhausted)不保留会话,重派走新单位接力
// (语义不变)。

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/rings"
)

// TestBlockedSessionKeptResumable 受阻收场保留会话:session_finished 带
// Resumable;pendingSession 报 (原会话, blocked=true)。收场事件换成
// 非 Resumable(如 exhausted 接力收场)后不再报可续。
func TestBlockedSessionKeptResumable(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "blk-keep"
	seedGraph(t, store, task, "探测本地靶标端口状态")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "不可达", Finish: "stop"}
	})
	defer model.Close()
	gate := newFlakyGateway(t, model, 1<<30, http.StatusServiceUnavailable)
	prov, err := gate.provider("scripted-dead")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.ModelRetryLimit, ex.ModelRetryBackoff = 1, time.Millisecond

	res, err := ex.RunStep(context.Background(), task, "s1")
	if err != nil || res.StepStatus != StepBlocked {
		t.Fatalf("应落 blocked: %v %+v", err, res)
	}

	// session_finished 带 Resumable;pendingSession 报原会话 + blocked 态。
	var finished *rings.SwarmRecord
	for i, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionEnd {
			finished = &swarmRecordsOf(t, store, task)[i]
		}
	}
	if finished == nil || !finished.Resumable {
		t.Fatalf("受阻收场的 session_finished 应带 Resumable: %+v", finished)
	}
	tid, kind := ex.pendingSession(context.Background(), task, "s1")
	if tid != res.TranscriptID || kind != resumeBlocked {
		t.Fatalf("受阻会话应可续: got (%q,%v) want (%q,%v)", tid, kind, res.TranscriptID, resumeBlocked)
	}

	// 补一笔非 Resumable 收场(exhausted 接力语义):不再可续。
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:u9",
		Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u9", TranscriptID: "swarm-u9",
			Type: rings.SwarmRecSessionEnd, Note: "reason=timeout turns=9 status=exhausted"},
	}); err != nil {
		t.Fatalf("铺收场失败: %v", err)
	}
	if tid, kind := ex.pendingSession(context.Background(), task, "s1"); tid != "" || kind != resumeNone {
		t.Fatalf("非受阻收场不应可续: got (%q,%v)", tid, kind)
	}
}

// TestReopenBlockedStepResumesOriginalSession 恢复重开·原会话续主契径:
// 受阻 → 换可用模型 → 重开回 dispatched → 重领单位从转录恢复原会话
// ——TranscriptID 沿用、模型看到受阻前的任务包历史、新输入是【恢复 ·
// 续跑】短指令(不重注入任务包)、转录同文件续写、token 行归因原会话。
func TestReopenBlockedStepResumesOriginalSession(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "blk-reopen"
	seedGraph(t, store, task, "探测本地靶标端口状态")
	root := filepath.Join(dir, "tasks")

	// 第一阶段:模型全败 → 受阻(会话里已有注入的任务包 user 消息)。
	dead := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "不可达", Finish: "stop"}
	})
	defer dead.Close()
	deadGate := newFlakyGateway(t, dead, 1<<30, http.StatusServiceUnavailable)
	deadProv, err := deadGate.provider("scripted-dead")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := NewExecutor(store, deadProv, root, Budget{})
	ex.ModelRetryLimit, ex.ModelRetryBackoff = 1, time.Millisecond
	blocked, err := ex.RunStep(context.Background(), task, "s1")
	if err != nil || blocked.StepStatus != StepBlocked {
		t.Fatalf("第一阶段应落 blocked: %v %+v", err, blocked)
	}
	origTID := blocked.TranscriptID
	ts := transcript.NewStore(filepath.Join(root, task, "transcripts"))
	preRecs, err := ts.Load(ts.MainPath(origTID))
	if err != nil || len(preRecs) == 0 {
		t.Fatalf("受阻会话的转录应已有内容(任务包注入): %v %d", err, len(preRecs))
	}

	// 第二阶段:换可用模型(恢复),重开步骤回 dispatched,重领执行。
	resumedReq := make(chan *recordedRequest, 4)
	okModel := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "恢复 · 续跑") {
			select {
			case resumedReq <- rr:
			default:
			}
		}
		return scriptedReply{Text: "模型恢复后继续执行,本步骤完成。", Finish: "stop"}
	})
	defer okModel.Close()
	okGate := newFlakyGateway(t, okModel, 0, 0)
	okProv, err := okGate.provider("scripted-ok")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex2 := NewExecutor(store, okProv, root, Budget{})
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "failover",
		Payload: rings.NodeStateChanged{NodeID: "s1", Status: "dispatched"},
	}); err != nil {
		t.Fatalf("重开步骤失败: %v", err)
	}
	res, err := ex2.RunStep(context.Background(), task, "s1")
	if err != nil || res.StepStatus != "done" {
		t.Fatalf("恢复重开后应 done: %v %+v", err, res)
	}
	if res.TranscriptID != origTID {
		t.Fatalf("应沿用原会话转录 ID %s,得到 %s", origTID, res.TranscriptID)
	}

	// 模型侧直证:历史携带受阻前注入的任务包原文,新输入是恢复续跑指令。
	select {
	case rr := <-resumedReq:
		sawPack := false
		for _, msg := range rr.Messages {
			if msg.Role == string(llm.RoleUser) && strings.Contains(msg.Content, "一、当前目标") {
				sawPack = true
			}
		}
		if !sawPack {
			t.Fatalf("恢复的请求应携带受阻前的任务包历史")
		}
		if !strings.Contains(rr.LastUser, "恢复 · 续跑") {
			t.Fatalf("新输入应为恢复续跑指令: %.120s", rr.LastUser)
		}
		if c := strings.Count(rr.LastUser, "一、当前目标"); c > 1 {
			t.Fatalf("续跑指令不应重注入任务包,出现 %d 次", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("未观察到恢复续跑请求")
	}

	// 账本侧:新单位 session_started 注明受阻重开·原会话续;转录同文件续写。
	var reopened *rings.SwarmRecord
	for _, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionStarted && rec.UnitID != blockedUnitOf(blocked) {
			reopened = &rec
		}
	}
	if reopened == nil {
		t.Fatal("缺少重开单位的 session_started")
	}
	if reopened.TranscriptID != origTID || !strings.Contains(reopened.Note, "受阻重开:原会话续") {
		t.Fatalf("session_started 应注明受阻重开·原会话续: %+v", reopened)
	}
	postRecs, err := ts.Load(ts.MainPath(origTID))
	if err != nil {
		t.Fatalf("读转录: %v", err)
	}
	if len(postRecs) <= len(preRecs) {
		t.Fatalf("转录应同文件续写: 阻碍前 %d 条,恢复后 %d 条", len(preRecs), len(postRecs))
	}
	// token 行归因原会话文件(上下文零重建的记账面)。
	rows, err := store.TokenUsageRows(context.Background(), task)
	if err != nil || len(rows) == 0 {
		t.Fatalf("恢复续跑应有 token 行: %v %d", err, len(rows))
	}
	for _, r := range rows {
		if r.TranscriptID != origTID {
			t.Fatalf("token 行应归因原会话 %s,得到 %s", origTID, r.TranscriptID)
		}
	}
}

// blockedUnitOf 从 RunResult 取单位号(测试小件)。
func blockedUnitOf(res *RunResult) string { return res.UnitID }
