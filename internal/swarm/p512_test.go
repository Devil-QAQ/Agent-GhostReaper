package swarm

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"ghostreaper/internal/rings"
)

func TestP512ConcurrentContinueCommitsOnceAndKeepsSession(t *testing.T) {
	store := openStore(t, t.TempDir())
	ctx := context.Background()
	const task = "p512-continue"
	_, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "step", NodeType: rings.NodeStep, Title: "暂停样例", Status: rings.StepPauseWait}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{StepID: "step", UnitID: "u1", TranscriptID: "swarm-u1", Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{StepID: "step", UnitID: "u1", TranscriptID: "swarm-u1", Type: rings.SwarmRecSessionEnd, Note: "reason=pause_yield status=pause_wait turns=1", Yield: &rings.YieldNote{Source: "pause"}}})
	if err != nil {
		t.Fatal(err)
	}
	ex := NewExecutor(store, nil, filepath.Join(t.TempDir(), "tasks"), Budget{})
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ex.ResumeUnit(ctx, task, "step", "creator") == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("concurrent continues accepted %d times", accepted.Load())
	}
	step, _ := store.Node(ctx, task, "step")
	if step.Status != "dispatched" || step.Detail["scope_head"] != true {
		t.Fatal("step was not queued at the existing priority")
	}
	sessions, _ := store.UnitSessions(ctx, task)
	if len(sessions) != 1 || sessions[0].Transcript != "swarm-u1" {
		t.Fatal("continue rebuilt the original session")
	}
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: "step", Status: "aborted"}}); err != nil {
		t.Fatal(err)
	}
	if ex.ResumeUnit(ctx, task, "step", "creator") == nil {
		t.Fatal("continue revived a terminated step")
	}
}

func TestP512ContinueWriteFailureKeepsPauseLatch(t *testing.T) {
	store := openStore(t, t.TempDir())
	ex := NewExecutor(store, nil, t.TempDir(), Budget{})
	key := steerKey("task", "step")
	latch := make(chan struct{})
	ex.unitLatches[key] = latch
	ex.stepUnits[key] = "u1"
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if ex.ResumeUnit(context.Background(), "task", "step", "creator") == nil {
		t.Fatal("closed ledger accepted a continue")
	}
	if !ex.UnitPaused("task", "step") {
		t.Fatal("failed audit opened the pause latch")
	}
	select {
	case <-latch:
		t.Fatal("failed audit resumed execution")
	default:
	}
}
