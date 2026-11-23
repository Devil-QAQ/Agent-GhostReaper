package swarm

import (
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

func TestReopenAcceptsFirstActualSubmissionAndPreservesExecutionSource(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	const task = "reopen-submit"
	ctx := t.Context()
	_, err := s.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "asset", NodeType: rings.NodeAsset, AssetClass: rings.AssetDomain, Status: "active", Title: "fixture.example.test"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "new-fact", NodeType: rings.NodeFact, Status: "confirmed", Title: "新的已确认观察"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "old-step", NodeType: rings.NodeStep, Status: "done", Title: "原验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "claim", NodeType: rings.NodeTestClaim, Status: rings.TestClaimConfirmed, Title: "固定结论", Detail: map[string]any{"standard": arbiter.DefaultClaimStandard()}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "claim", ToNode: "asset"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "creator", Payload: rings.TestClaimRecord{ClaimID: "claim", Type: rings.TestClaimRecReopen, Trigger: rings.ReopenManual, Reason: "新观察需要重新核对"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: "claim", Status: rings.TestClaimCandidate}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "new-step", NodeType: rings.NodeStep, Status: "running", Title: "重验"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{StepID: "new-step", UnitID: "u1", TranscriptID: "session", Type: rings.SwarmRecSessionStarted}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := s.CurrentRevision(ctx, task)
	ex := NewExecutor(s, nil, dir, Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "new-step", UnitID: "u1", TranscriptID: "session", owner: &rings.ExecutionOwner{StepID: "new-step", UnitID: "u1", StartedRev: rev}}
	id, result, err := env.SubmitClaim(ctx, "重新核对后的同一结论", "low", []string{"new-proof-record"}, "marker", "固定重验自查", "", "asset", "", "claim", "new-fact")
	if err != nil || id != "claim" || result != nil {
		t.Fatalf("first reopened submission: id=%s result=%+v err=%v", id, result, err)
	}
	derived, _ := s.Edges(ctx, task, rings.EdgeDerivedFrom)
	sources, _ := s.Edges(ctx, task, rings.EdgeSourcedFrom)
	if len(derived) != 1 || derived[0].From != "claim" || derived[0].To != "new-step" || len(sources) != 1 || sources[0].To != "new-fact" {
		t.Fatalf("reverification provenance missing: %v %v", derived, sources)
	}
	n, _ := s.Node(ctx, task, "claim")
	if n.Status != rings.TestClaimCandidate {
		t.Fatal("receipt without machine checks was presented as confirmation")
	}
}
