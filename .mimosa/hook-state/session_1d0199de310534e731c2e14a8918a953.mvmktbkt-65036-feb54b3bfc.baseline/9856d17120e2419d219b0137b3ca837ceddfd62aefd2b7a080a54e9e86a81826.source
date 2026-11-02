package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/rings"
)

// P2-06f 重启收养·原会话续(第七章「恢复粒度」):崩溃残留 running 的
// 步骤被幽灵清理重置回可领取后,新领取的单位优先从转录恢复原会话
// 原地续跑(与冬眠唤醒同款,省上下文重建成本);Norma 会话恢复经查证
// 支持(agentcore.Resume 重建消息与累计用量,cleanResumed 丢弃崩溃时
// 未收尾的尾部 tool_use),本文件是该结论的回归锚点。

// preCrashTranscript 预写一份"上一进程崩溃残留"的会话转录:任务包
// 原文(user)+ 一条带用量的 assistant 回复。返回写入的累计用量。
func preCrashTranscript(t *testing.T, rootDir, task, sessionID, packMarker string) llm.Usage {
	t.Helper()
	ts := transcript.NewStore(filepath.Join(rootDir, task, "transcripts"))
	w := ts.NewWriter(sessionID, "")
	pack := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(
		"【任务包】" + packMarker + "\n七要素原文(上一进程注入)。")}}
	w.RecordMessage(pack, llm.Usage{})
	usage := llm.Usage{InputTokens: 400, OutputTokens: 50}
	asst := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(
		"已按任务包开始执行,完成了第一阶段观察。")}}
	w.RecordMessage(asst, usage)
	if err := w.Err(); err != nil {
		t.Fatalf("预写转录失败: %v", err)
	}
	return usage
}

// crashResidue 账本侧崩溃残迹:session_started 无 session_finished
// (进程崩溃的精确签名)。
func crashResidue(t *testing.T, store *rings.Store, task, stepID, unitID string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:" + unitID,
		Payload: rings.SwarmRecord{
			StepID: stepID, UnitID: unitID, TranscriptID: "swarm-" + unitID,
			Type: rings.SwarmRecSessionStarted,
		},
	}); err != nil {
		t.Fatalf("铺崩溃残迹失败: %v", err)
	}
}

func swarmRecordsOf(t *testing.T, store *rings.Store, task string) []rings.SwarmRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.SwarmRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			t.Fatalf("解码 swarm.record: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// TestPendingSessionDetection 待决会话判定:最近一笔 session_started
// 之后没有 session_finished 才算未收场;多次崩溃残留取最近一笔。
func TestPendingSessionDetection(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedGraph(t, store, "t1", "意图")
	// 有始无终:待决。
	crashResidue(t, store, "t1", "s1", "u1")
	e := NewExecutor(store, nil, dir, Budget{})
	if got, kind := e.pendingSession(context.Background(), "t1", "s1"); got != "swarm-u1" || kind != resumeAdoption {
		t.Fatalf("有始无终应报待决会话 swarm-u1,得到 (%q,%v)", got, kind)
	}
	// 收场后:无待决。
	if _, err := store.Append(context.Background(), "t1", rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
		Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
			Type: rings.SwarmRecSessionEnd, Note: "reason=completed turns=1 status=done"},
	}); err != nil {
		t.Fatalf("铺收场失败: %v", err)
	}
	if got, kind := e.pendingSession(context.Background(), "t1", "s1"); got != "" || kind != resumeNone {
		t.Fatalf("已收场不应报待决会话,得到 (%q,%v)", got, kind)
	}
	// 二次崩溃残留(收养续跑后再崩):取最近一笔。
	crashResidue(t, store, "t1", "s1", "u2")
	if got, _ := e.pendingSession(context.Background(), "t1", "s1"); got != "swarm-u2" {
		t.Fatalf("二次残留应取最近待决会话 swarm-u2,得到 %q", got)
	}
	// 别的步骤的会话不算本步骤待决。
	crashResidue(t, store, "t1", "s2", "u3") // s2 不是本步骤
	if got, _ := e.pendingSession(context.Background(), "t1", "s1"); got != "swarm-u2" {
		t.Fatalf("他步骤会话不应影响本步骤判定,得到 %q", got)
	}
}

// TestRunStepResumesOriginalSession 原会话续主契径:重领的崩溃步骤从
// 转录恢复原会话——模型看到完整历史(含崩溃前的任务包原文),收到的
// 新输入是短续跑指令而非重注入的任务包;token 增量从恢复点起算,
// 不重复计历史;账本归因钉在原会话文件上。
func TestRunStepResumesOriginalSession(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	task := "t-resume"
	seedGraph(t, store, task, "确认本地靶标暴露面")
	crashResidue(t, store, task, "s1", "u9")
	preCrash := preCrashTranscript(t, dir, task, "swarm-u9", "重启前任务包原文-P2-06F")

	resumedReq := make(chan *recordedRequest, 4)
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "重启接管 · 续跑") {
			select {
			case resumedReq <- rr:
			default:
			}
			return scriptedReply{Text: "基于已恢复的上下文继续执行,本步骤完成。", Finish: "stop"}
		}
		return scriptedReply{Text: "意外形态:未收到续跑指令", Finish: "stop"}
	})
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}
	exec := NewExecutor(store, provider, dir, Budget{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := exec.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("续跑后步骤应完成,得到 %s(%s)", res.StepStatus, res.Reason)
	}

	// 模型侧直证:历史完整携带(崩溃前任务包原文在请求里),新输入是
	// 续跑指令(不是重注入的完整任务包)。
	select {
	case rr := <-resumedReq:
		sawHistory := false
		for _, msg := range rr.Messages {
			if msg.Role == string(llm.RoleUser) && strings.Contains(msg.Content, "重启前任务包原文-P2-06F") {
				sawHistory = true
			}
		}
		if !sawHistory {
			t.Fatalf("恢复的请求应携带崩溃前的任务包原文历史")
		}
		if !strings.Contains(rr.LastUser, "重启接管 · 续跑") {
			t.Fatalf("新输入应为续跑指令,得到尾部 user: %.120s", rr.LastUser)
		}
		if strings.Count(rr.LastUser, "七要素") > 0 {
			t.Fatalf("续跑指令不应重注入完整任务包")
		}
		// token 不重复计历史:新行 input = 本次请求规模(段首基线清零,
		// 崩溃前历史 400 不进本段增量——其记账属上一进程的原单位)。
		rows, rerr := store.TokenUsageRows(context.Background(), task)
		if rerr != nil {
			t.Fatalf("查 token 行: %v", rerr)
		}
		if len(rows) != 1 {
			t.Fatalf("应恰一行新记账,得到 %d", len(rows))
		}
		if want := rr.BodyChars / 4; rows[0].InputTokens != want {
			t.Fatalf("新行 input 应为本次请求规模 %d(历史 %d 不重复计),得到 %d",
				want, preCrash.InputTokens, rows[0].InputTokens)
		}
		if rows[0].TranscriptID != "swarm-u9" {
			t.Fatalf("token 行应归因原会话文件,得到 %s", rows[0].TranscriptID)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("未观察到续跑请求")
	}

	// 账本侧:收养单位的 session_started 归因原会话并注明原会话续;
	// 步骤终态 done;转录文件被续写(同一文件追加,不另起新档)。
	var adopted *rings.SwarmRecord
	finished := false
	for _, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionStarted && rec.UnitID != "u9" {
			adopted = &rec
		}
		if rec.Type == rings.SwarmRecSessionEnd {
			finished = true
		}
	}
	if adopted == nil {
		t.Fatalf("缺少收养单位的 session_started")
	}
	if adopted.TranscriptID != "swarm-u9" {
		t.Fatalf("收养单位应沿用原会话转录 ID,得到 %s", adopted.TranscriptID)
	}
	if !strings.Contains(adopted.Note, "原会话续") {
		t.Fatalf("session_started 应注明原会话续,得到 %q", adopted.Note)
	}
	if !finished {
		t.Fatalf("缺少 session_finished")
	}
	step, err := store.Node(context.Background(), task, "s1")
	if err != nil {
		t.Fatalf("查步骤: %v", err)
	}
	if step.Status != "done" {
		t.Fatalf("步骤应 done,得到 %s", step.Status)
	}
	ts := transcript.NewStore(filepath.Join(dir, task, "transcripts"))
	recs, err := ts.Load(ts.MainPath("swarm-u9"))
	if err != nil {
		t.Fatalf("读转录: %v", err)
	}
	var sawAdoptionInput, sawPostCrashAssistant bool
	for _, r := range recs {
		if r.Message == nil {
			continue
		}
		if r.Message.Role == llm.RoleUser && strings.Contains(r.Message.Text(), "重启接管 · 续跑") {
			sawAdoptionInput = true
		}
		if r.Message.Role == llm.RoleAssistant && strings.Contains(r.Message.Text(), "本步骤完成") {
			sawPostCrashAssistant = true
		}
	}
	if !sawAdoptionInput || !sawPostCrashAssistant {
		t.Fatalf("转录应原文件续写(续跑指令 %v / 续跑回复 %v)", sawAdoptionInput, sawPostCrashAssistant)
	}
}

// TestRunStepResumeFallsBackWithoutTranscript 降级契约:账本有待决会话
// 但转录文件缺失/为空(如工作目录被清)——降级为新单位接力(exhausted
// 同款语义):重注入完整任务包,全新会话起飞,不报错不挡领取。
func TestRunStepResumeFallsBackWithoutTranscript(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	task := "t-fallback"
	seedGraph(t, store, task, "确认本地靶标暴露面")
	crashResidue(t, store, task, "s1", "u7") // 有残迹、不写转录文件

	freshReq := make(chan *recordedRequest, 4)
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(rr.LastUser, "【任务包 · 七要素】") {
			select {
			case freshReq <- rr:
			default:
			}
		}
		return scriptedReply{Text: "接力完成。", Finish: "stop"}
	})
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}
	exec := NewExecutor(store, provider, dir, Budget{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := exec.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("降级接力后步骤应完成,得到 %s(%s)", res.StepStatus, res.Reason)
	}
	select {
	case rr := <-freshReq:
		if !strings.Contains(rr.LastUser, "【任务包 · 七要素】") {
			t.Fatalf("降级应重注入完整任务包,得到尾部 user: %.120s", rr.LastUser)
		}
		if strings.Contains(rr.LastUser, "重启接管 · 续跑") {
			t.Fatalf("转录缺失应降级重注入任务包,不应发续跑指令")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("未观察到接力请求")
	}
}

// TestUnitSeqSeededFromLedger 单位号跨进程不撞号(P2-14 实跑发现:
// 重启后 unitSeq 归零,新单位复用 u1 会把两个单位的会话写进同一个
// swarm-uN transcript 文件):重建执行器(模拟重启)后领取新步骤,
// 单位号必须从账本最大值之后继续,会话文件不得与历史单位重名。
func TestUnitSeqSeededFromLedger(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	task := "t-unitseq"
	seedGraph(t, store, task, "确认本地靶标暴露面")

	// 上一进程的完整单位残迹:u7 有始有终(非待决,不触发原会话续)。
	crashResidue(t, store, task, "s0", "u7")
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventSwarmRecord, Actor: "swarm:u7",
		Payload: rings.SwarmRecord{
			StepID: "s0", UnitID: "u7", TranscriptID: "swarm-u7",
			Type: rings.SwarmRecSessionEnd, Note: "reason=completed",
		},
	}); err != nil {
		t.Fatalf("补 session_finished 失败: %v", err)
	}
	// 历史会话文件存在(撞号时新单位会续写进它)。
	preCrashTranscript(t, dir, task, "swarm-u7", "上一进程的历史会话")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "本步骤完成。", Finish: "stop"}
	})
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatalf("构建 Provider: %v", err)
	}

	// 重启后的新执行器:同一账本、同一工作目录。
	exec := NewExecutor(store, provider, dir, Budget{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := exec.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("步骤应完成,得到 %s(%s)", res.StepStatus, res.Reason)
	}
	if res.UnitID != "u8" {
		t.Fatalf("重启后新单位应从账本最大值之后继续(u8),得到 %s", res.UnitID)
	}
	if res.TranscriptID != "swarm-u8" {
		t.Fatalf("会话文件应为 swarm-u8,得到 %s", res.TranscriptID)
	}

	// 历史会话文件未被污染:仍只含上一进程的两条消息。
	ts := transcript.NewStore(filepath.Join(dir, task, "transcripts"))
	recs, err := ts.Load(ts.MainPath("swarm-u7"))
	if err != nil {
		t.Fatalf("读历史会话: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("历史会话 swarm-u7 应保持 2 条消息不被新单位续写,得到 %d", len(recs))
	}
}
