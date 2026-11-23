package overmind

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestDuplicateInternalResultWakeDoesNotRetryEmptyPlan(t *testing.T) {
	store := openStore(t)
	const task = "result-wake"
	ctx := context.Background()
	mkTask(t, store, task)
	p454Goal(t, store, task, "g1", "人工目标", nil)
	model := p442ZeroIntentModel()
	defer model.Close()
	model.release()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store, Provider: provider, Swarm: swarm.NewExecutor(store, provider, t.TempDir(), swarm.Budget{})}
	l.cycle(ctx, true)
	if len(model.decomposeRequests()) != 1 {
		t.Fatal("initial wake did not plan")
	}
	for range 5 {
		l.cycle(ctx, false)
	}
	if got := len(model.decomposeRequests()); got != 1 {
		t.Fatalf("duplicate internal notifications retried unchanged snapshot: %d", got)
	}
	// Actual new confirmed facts still trigger planning through an internal wake.
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Title: "新观察", Status: "candidate", Detail: map[string]any{"evidence": "人工原文"}}}); err != nil {
		t.Fatal(err)
	}
	if err := l.processResults(ctx); err != nil {
		t.Fatal(err)
	}
	l.cycle(ctx, false)
	if got := len(model.decomposeRequests()); got != 2 {
		t.Fatalf("real fact did not plan exactly once: %d", got)
	}
	l.cycle(ctx, false)
	if len(model.decomposeRequests()) != 2 {
		t.Fatal("consumed fact was planned again")
	}
	// The existing heartbeat/manual liveness floor remains active.
	l.cycle(ctx, true)
	if len(model.decomposeRequests()) != 3 {
		t.Fatal("heartbeat/manual retry was suppressed")
	}
	l.polishWG.Wait()
}
