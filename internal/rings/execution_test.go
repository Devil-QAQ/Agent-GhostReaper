package rings

import (
	"errors"
	"sync"
	"testing"
)

func seedExecution(t *testing.T, s *Store, task, unit string) ExecutionOwner {
	t.Helper()
	revs, err := s.Append(t.Context(), task,
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: "s", NodeType: NodeStep, Title: "观察", Status: "running"}},
		Event{Kind: EventSwarmRecord, Actor: "swarm:" + unit, Payload: SwarmRecord{Type: SwarmRecSessionStarted, StepID: "s", UnitID: unit, TranscriptID: "session"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return ExecutionOwner{StepID: "s", UnitID: unit, StartedRev: revs[1]}
}
func executionApproval(owner ExecutionOwner, id string) Event {
	return Event{Kind: EventApprovalRecord, Actor: "swarm:" + owner.UnitID, Payload: ApprovalRecord{RequestID: id, StepID: owner.StepID, UnitID: owner.UnitID, Type: ApprovalRecRequested, ActionLevel: ActionReadOnly, ToolName: "read", ToolArgs: []byte(`{"path":"notes.txt"}`)}}
}
func endExecution(t *testing.T, s *Store, task string, owner ExecutionOwner, status string, yield *YieldNote) int64 {
	t.Helper()
	revs, err := s.Append(t.Context(), task,
		Event{Kind: EventNodeStateChanged, Actor: "swarm:" + owner.UnitID, Payload: NodeStateChanged{NodeID: owner.StepID, Status: status}},
		Event{Kind: EventSwarmRecord, Actor: "swarm:" + owner.UnitID, Payload: SwarmRecord{Type: SwarmRecSessionEnd, StepID: owner.StepID, UnitID: owner.UnitID, TranscriptID: "session", Yield: yield, Note: "status=" + status}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return revs[0]
}
func TestExecutionOwnershipRejectsLateBusinessButKeepsAudit(t *testing.T) {
	s := openTestStore(t)
	owner := seedExecution(t, s, "task", "u1")
	if _, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, "before")); err != nil {
		t.Fatal(err)
	}
	endExecution(t, s, "task", owner, "aborted", nil)
	rev, _ := s.CurrentRevision(t.Context(), "task")
	if _, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, "late")); !errors.Is(err, ErrExecutionEnded) {
		t.Fatal("late business accepted", err)
	}
	after, _ := s.CurrentRevision(t.Context(), "task")
	if rev != after {
		t.Fatal("rejected write changed ledger")
	}
	if _, err := s.Append(t.Context(), "task", Event{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{Type: SwarmRecToolCall, StepID: "s", UnitID: "u1", ToolName: "read", ResultHead: "actual returned observation"}}); err != nil {
		t.Fatal("actual audit discarded", err)
	}
}
func TestExecutionOwnershipConvergesWithConcurrentEnd(t *testing.T) {
	s := openTestStore(t)
	owner := seedExecution(t, s, "task", "u1")
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, string(rune('a'+i))))
			if err != nil && !errors.Is(err, ErrExecutionEnded) {
				t.Error(err)
			}
		}()
	}
	endRev := endExecution(t, s, "task", owner, "done", nil)
	wg.Wait()
	evs, _ := s.Events(t.Context(), "task", 0, 0)
	for _, ev := range evs {
		if ev.Kind == EventApprovalRecord && ev.Revision > endRev {
			t.Fatal("approval committed after terminal", ev.Revision)
		}
	}
	for range 8 {
		if _, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, "late")); !errors.Is(err, ErrExecutionEnded) {
			t.Fatal(err)
		}
	}
}
func TestExecutionOwnershipYieldIsExactAndNeverRevivesTerminal(t *testing.T) {
	s := openTestStore(t)
	owner := seedExecution(t, s, "task", "u1")
	endExecution(t, s, "task", owner, StepGateWait, &YieldNote{Source: "approval", Request: "ap-one"})
	owner.ApprovalRequest = "ap-other"
	if _, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, "wrong")); !errors.Is(err, ErrExecutionEnded) {
		t.Fatal("wrong yield accepted", err)
	}
	owner.ApprovalRequest = "ap-one"
	if _, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, "right")); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, s, "task", Event{Kind: EventNodeStateChanged, Actor: "creator", Payload: NodeStateChanged{NodeID: "s", Status: "aborted"}})
	if _, err := s.AppendForExecution(t.Context(), "task", owner, executionApproval(owner, "late")); !errors.Is(err, ErrExecutionEnded) {
		t.Fatal("terminal revived", err)
	}
}
func TestExecutionOwnershipRejectsReplacedUnit(t *testing.T) {
	s := openTestStore(t)
	old := seedExecution(t, s, "task", "u1")
	endExecution(t, s, "task", old, "dispatched", nil)
	revs, err := s.Append(t.Context(), "task", Event{Kind: EventNodeStateChanged, Actor: "swarm:u2", Payload: NodeStateChanged{NodeID: "s", Status: "running"}}, Event{Kind: EventSwarmRecord, Actor: "swarm:u2", Payload: SwarmRecord{Type: SwarmRecSessionStarted, StepID: "s", UnitID: "u2", TranscriptID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	next := ExecutionOwner{StepID: "s", UnitID: "u2", StartedRev: revs[1]}
	if _, err := s.AppendForExecution(t.Context(), "task", old, executionApproval(old, "old")); !errors.Is(err, ErrExecutionEnded) {
		t.Fatal("old unit retained ownership", err)
	}
	if _, err := s.AppendForExecution(t.Context(), "task", next, executionApproval(next, "next")); err != nil {
		t.Fatal(err)
	}
}
