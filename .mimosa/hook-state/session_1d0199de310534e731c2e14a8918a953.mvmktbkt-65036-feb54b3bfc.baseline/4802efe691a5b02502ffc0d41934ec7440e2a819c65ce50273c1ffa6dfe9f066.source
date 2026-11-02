package swarm

// P3-07 断点续接·单位侧:存档断点武装后,单位在工具调用边界收场——
// 当前调用完成后落地,session_finished 带 Checkpointed 标记(会话保留
// 可续),步骤回可领取(dispatched);恢复领取时优先原会话 Resume 续跑
//(【断点 · 续跑】短指令,任务包不重注入,transcript 同文件续写)。
// 与崩溃残留(adoption)和受阻保留(blocked)三种可续来源由
// pendingSession 的 kind 区分。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/rings"
)

// waitToolRecorded 轮询账本直到出现含标记的 bash 工具记录(单位第一段
// 工作已执行的证据);超时返回 false。
func waitToolRecorded(t *testing.T, store *rings.Store, task, marker string) bool {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, rec := range swarmRecordsOf(t, store, task) {
			if rec.Type == rings.SwarmRecToolCall && rec.ToolName == "bash" &&
				strings.Contains(rec.ResultHead, marker) {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestCheckpointLandsUnitResumable 断点落地主契径:单位在飞中途武装
// 断点 → 下一个工具调用边界被暂缓、段收拢 → 段循环顶落地:步骤回
// dispatched、session_finished 带 Checkpointed(不带 Resumable——来源
// 不同)、pendingSession 报 checkpoint 态。
func TestCheckpointLandsUnitResumable(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	task := "ckpt-land"
	seedGraph(t, store, task, "确认本地靶标暴露面")

	gate := ckpt.New()
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "bash", "pre-checkpoint-work"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "bash",
				Args: `{"command":"echo pre-checkpoint-work"}`,
			}}, Finish: "tool_calls"}
		default:
			// 武装后再要下一条工具调用:guard 边界见 ckpt 即暂缓 + 收段,
			// 单位在段循环顶落地,本分支不会被完整消费。
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "bash",
				Args: `{"command":"echo should-be-deferred"}`,
			}}, Finish: "tool_calls"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := NewExecutor(store, prov, dir, Budget{})
	ex.Ckpt = gate

	done := make(chan *RunResult, 1)
	go func() {
		res, err := ex.RunStep(context.Background(), task, "s1")
		if err != nil {
			t.Errorf("RunStep: %v", err)
		}
		done <- res
	}()
	if !waitToolRecorded(t, store, task, "pre-checkpoint-work") {
		t.Fatalf("未观察到第一段工作入账")
	}
	gate.Arm(task)
	select {
	case res := <-done:
		if res.StepStatus != "dispatched" || res.Reason != "checkpointed" || !res.Checkpointed {
			t.Fatalf("断点落地应收场 dispatched/checkpointed: %+v", res)
		}
		assertCheckpointLedger(t, store, task, res.TranscriptID)
	case <-time.After(30 * time.Second):
		t.Fatalf("单位未在期限内断点落地")
	}
}

// assertCheckpointLedger 断点落地后的账本断言:session_finished 带
// Checkpointed 不带 Resumable;步骤回 dispatched;pendingSession 报
// checkpoint 态原会话。
func assertCheckpointLedger(t *testing.T, store *rings.Store, task, wantTID string) {
	t.Helper()
	var finished *rings.SwarmRecord
	for i, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionEnd {
			finished = &swarmRecordsOf(t, store, task)[i]
		}
	}
	if finished == nil {
		t.Fatalf("缺少 session_finished")
	}
	if !finished.Checkpointed {
		t.Fatalf("断点收场的 session_finished 应带 Checkpointed: %+v", finished)
	}
	if finished.Resumable {
		t.Fatalf("断点收场不应带 Resumable(来源区分:受阻保留≠断点存档): %+v", finished)
	}
	if !strings.Contains(finished.Note, "checkpointed") {
		t.Fatalf("session_finished note 应含 reason=checkpointed: %q", finished.Note)
	}
	step, err := store.Node(context.Background(), task, "s1")
	if err != nil {
		t.Fatalf("查步骤: %v", err)
	}
	if step.Status != "dispatched" {
		t.Fatalf("断点后步骤应回可领取(dispatched),得到 %s", step.Status)
	}
	ex := NewExecutor(store, nil, t.TempDir(), Budget{})
	tid, kind := ex.pendingSession(context.Background(), task, "s1")
	if tid != wantTID || kind != resumeCheckpoint {
		t.Fatalf("断点会话应可续(checkpoint 态): got (%q,%v) want (%q,%v)",
			tid, kind, wantTID, resumeCheckpoint)
	}
}

// TestCheckpointResumeOriginalSession 断点续接主契径(完成标准"恢复后
// 任务原地续跑,账本/transcript 可证"的单位侧):断点落地后重领(用新
// 执行器模拟进程重启),模型收到【断点 · 续跑】短指令 + 断点前的完整
// 会话历史(任务包原文在),不重注入任务包;转录同文件续写;账本
// session_started 注明"断点续接:原会话续"。
func TestCheckpointResumeOriginalSession(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	task := "ckpt-resume"
	seedGraph(t, store, task, "确认本地靶标暴露面")

	gate := ckpt.New()
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "bash", "pre-checkpoint-work"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "bash",
				Args: `{"command":"echo pre-checkpoint-work"}`,
			}}, Finish: "tool_calls"}
		case strings.Contains(rr.LastUser, "断点 · 续跑"):
			return scriptedReply{Text: "已从断点恢复,继续完成并收尾。", Finish: "stop"}
		default:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "bash",
				Args: `{"command":"echo after-arming"}`,
			}}, Finish: "tool_calls"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}

	// 第一进程:起飞 → 武装 → 断点落地。
	ex1 := NewExecutor(store, prov, dir, Budget{})
	ex1.Ckpt = gate
	done := make(chan *RunResult, 1)
	go func() {
		res, err := ex1.RunStep(context.Background(), task, "s1")
		if err != nil {
			t.Errorf("第一段 RunStep: %v", err)
		}
		done <- res
	}()
	if !waitToolRecorded(t, store, task, "pre-checkpoint-work") {
		t.Fatalf("未观察到第一段工作入账")
	}
	gate.Arm(task)
	var firstTID string
	select {
	case res := <-done:
		if res.StepStatus != "dispatched" || !res.Checkpointed {
			t.Fatalf("断点落地不符: %+v", res)
		}
		firstTID = res.TranscriptID
	case <-time.After(30 * time.Second):
		t.Fatalf("单位未在期限内断点落地")
	}
	assertCheckpointLedger(t, store, task, firstTID)

	// "重启":新执行器(同账本同目录),断点信号清零(Manager.launch 语义)。
	ex2 := NewExecutor(store, prov, dir, Budget{})
	gate.Clear(task)
	res2, err := ex2.RunStep(context.Background(), task, "s1")
	if err != nil {
		t.Fatalf("续跑 RunStep: %v", err)
	}
	if res2.StepStatus != "done" {
		t.Fatalf("续跑后步骤应完成,得到 %s(%s)", res2.StepStatus, res2.Reason)
	}
	if res2.TranscriptID != firstTID {
		t.Fatalf("续跑应沿用原会话转录 %s,得到 %s", firstTID, res2.TranscriptID)
	}

	// 账本侧:收养单位 session_started 注明断点续接:原会话续。
	var adopted *rings.SwarmRecord
	for _, rec := range swarmRecordsOf(t, store, task) {
		if rec.Type == rings.SwarmRecSessionStarted && rec.UnitID == res2.UnitID {
			r := rec
			adopted = &r
		}
	}
	if adopted == nil || !strings.Contains(adopted.Note, "断点续接:原会话续") {
		t.Fatalf("续跑 session_started 应注明断点续接:原会话续,得到 %+v", adopted)
	}

	// 转录同文件续写:断点前的任务包、断点后的续跑指令与回复都在。
	ts := transcript.NewStore(filepath.Join(dir, task, "transcripts"))
	recs, err := ts.Load(ts.MainPath(firstTID))
	if err != nil {
		t.Fatalf("读转录: %v", err)
	}
	var sawPack, sawResumeInput, sawPostReply bool
	for _, r := range recs {
		if r.Message == nil {
			continue
		}
		text := r.Message.Text()
		if r.Message.Role == llm.RoleUser && strings.Contains(text, "【任务包") {
			sawPack = true
		}
		if r.Message.Role == llm.RoleUser && strings.Contains(text, "断点 · 续跑") {
			sawResumeInput = true
		}
		if r.Message.Role == llm.RoleAssistant && strings.Contains(text, "已从断点恢复") {
			sawPostReply = true
		}
	}
	if !sawPack || !sawResumeInput || !sawPostReply {
		t.Fatalf("转录应原文件续写(任务包 %v / 断点续跑指令 %v / 续跑回复 %v)",
			sawPack, sawResumeInput, sawPostReply)
	}
	step, err := store.Node(context.Background(), task, "s1")
	if err != nil {
		t.Fatalf("查步骤: %v", err)
	}
	if step.Status != "done" {
		t.Fatalf("续跑后步骤应 done,得到 %s", step.Status)
	}
}
