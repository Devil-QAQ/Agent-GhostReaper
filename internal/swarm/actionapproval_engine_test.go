package swarm

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Replaces the retired whole-step and proxy-group pass contracts. Two distinct
// actions in one real engine session require two model decisions; approving the
// first resumes the exact invocation without granting the second action.
func TestActionApprovalResumesOriginalSessionWithoutStepPass(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "action-approval-engine"
	seedGraph(t, store, task, "核对两个本地输出")
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "bash", "review-first-ok"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "one", Name: "bash", Args: `{"command":"echo review-first-ok"}`}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "review-second-ok"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "two", Name: "bash", Args: `{"command":"echo review-second-ok"}`}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "两个本地输出均已核对", Finish: "stop"}
		}
	})
	defer model.Close()
	model.review = func(*recordedRequest) scriptedReply {
		return scriptedReply{Text: `{"decision":"approval","reason":"测试要求逐次确认"}`}
	}
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	ex := NewExecutor(store, provider, filepath.Join(dir, "tasks"), Budget{MaxTurns: 10, RunSeconds: 15})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		res *RunResult
		err error
	}
	done := make(chan outcome, 1)
	go func() { r, e := ex.RunStep(ctx, task, "s1"); done <- outcome{r, e} }()
	var ids []string
	for i := range 2 {
		waitFor(t, 10*time.Second, "本次动作审批", func() bool { return len(gate.Pending(task)) == 1 })
		pending := gate.Pending(task)[0]
		ids = append(ids, pending.ID)
		if got := successfulToolCalls(toolCallRecords(t, store, task, "bash")); got != i {
			t.Fatalf("approval %d: executed=%d", i, got)
		}
		if err := gate.Decide(t.Context(), task, pending.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case out := <-done:
		if out.err != nil || out.res.StepStatus != "done" {
			t.Fatalf("run outcome=%+v err=%v", out.res, out.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("approved session did not finish")
	}
	if ids[0] == ids[1] || successfulToolCalls(toolCallRecords(t, store, task, "bash")) != 2 {
		t.Fatal("distinct actions did not execute exactly once")
	}
	starts := 0
	for _, r := range swarmRecords(t, store, task) {
		if r.Type == "session_started" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("approval rebuilt unit instead of continuing: sessions=%d", starts)
	}
	rows, err := store.TokenUsageRows(t.Context(), task)
	reviews := 0
	for _, r := range rows {
		if r.Purpose == "action_review" {
			reviews++
		}
	}
	if err != nil || reviews != 2 {
		t.Fatalf("policy requests must be counted once, not on approved replay: reviews=%d err=%v", reviews, err)
	}
}
