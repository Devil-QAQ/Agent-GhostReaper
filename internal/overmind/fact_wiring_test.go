package overmind

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// Submit through the actual unit tool while the planner is blocked. No UI
// request, extra wake, heartbeat or unit completion may be needed to confirm it.
func TestFactSubmissionNotifiesHeldPlanner(t *testing.T) {
	store := openStore(t)
	const task = "fact-wiring"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mkTask(t, store, task)
	p454Goal(t, store, task, "goal", "核对固定材料", nil)
	planGate, submitGate, finishGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	planSeen, unitSeen, afterSubmit := make(chan struct{}), make(chan struct{}), make(chan struct{})
	markPlan, markUnit, markSubmit := sync.OnceFunc(func() { close(planSeen) }), sync.OnceFunc(func() { close(unitSeen) }), sync.OnceFunc(func() { close(afterSubmit) })
	releasePlan, releaseSubmit, releaseFinish := sync.OnceFunc(func() { close(planGate) }), sync.OnceFunc(func() { close(submitGate) }), sync.OnceFunc(func() { close(finishGate) })
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			markPlan()
			<-planGate
			return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
		}
		if seenToolResult(rr, "submit_candidate_fact", "已提交新进事实") {
			return scriptedReply{Text: "已记录固定观察。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "actual-fact-submit", Name: "submit_candidate_fact", Args: `{"statement":"固定清单有4项，其中1项重复","evidence":"人工资料原文：items=4 duplicate=1"}`}}, Finish: "tool_calls"}
	})
	model.holdReq = func(rr *recordedRequest) <-chan struct{} {
		if seenToolResult(rr, "submit_candidate_fact", "已提交新进事实") {
			markSubmit()
			return finishGate
		}
		markUnit()
		return submitGate
	}
	model.release()
	defer model.Close()
	defer func() { releasePlan(); releaseSubmit(); releaseFinish() }()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "tasks")
	ex := swarm.NewExecutor(store, provider, root, swarm.Budget{})
	l := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: provider, RootDir: root, HeartbeatInterval: time.Hour}
	wake, done := make(chan struct{}, 1), make(chan struct{})
	go func() { defer close(done); l.Run(ctx, wake) }()
	defer func() {
		releasePlan()
		releaseSubmit()
		releaseFinish()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("loop failed to stop")
		}
	}()
	wake <- struct{}{}
	select {
	case <-planSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("planner not held")
	}
	if err := DispatchStep(ctx, store, task, "s1", "记录人工材料", DispatchOptions{GoalID: "goal", ActionLevel: "只读探测"}); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	select {
	case <-unitSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("unit not started")
	}
	start := time.Now()
	releaseSubmit()
	waitFor(t, time.Second, "事实不等主宰、心跳或单位落地即确认", func() bool { return countNodes(t, store, task, rings.NodeFact, "confirmed") == 1 })
	t.Logf("actual fact submission to confirmed: %dms", time.Since(start).Milliseconds())
	select {
	case <-afterSubmit:
	case <-time.After(5 * time.Second):
		t.Fatal("unit did not receive submission result")
	}
	n, err := store.Node(ctx, task, "s1")
	if err != nil || n.Status != "running" {
		t.Fatalf("fact waited for completed unit: %+v %v", n, err)
	}
	triggers, _, err := l.planningTriggers(ctx)
	if err != nil || len(triggers) == 0 {
		t.Fatalf("confirmed fact did not trigger incremental planning: %v %v", triggers, err)
	}
}
