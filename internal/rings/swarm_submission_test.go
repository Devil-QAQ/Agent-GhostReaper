package rings

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func admissionSubmit(claim string) Event {
	return Event{Kind: EventTestClaimRecord, Actor: "swarm:u1", Payload: TestClaimRecord{ClaimID: claim, StepID: "s", UnitID: "u1", Type: TestClaimRecSubmit, Summary: "固定结论", Severity: "low", EvidenceRefs: []string{"record-one"}, ProvingMarker: "marker", CountercheckNote: "固定自查"}}
}

func TestSwarmParallelSubmissionsDeduplicateInsideWriter(t *testing.T) {
	s := openTestStore(t)
	const task = "submission-atomic"
	owner := seedExecution(t, s, task, "u1")
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: "asset", NodeType: NodeAsset, AssetClass: AssetDomain, Title: "fixture.example.test", Status: "active"}})
	var wg sync.WaitGroup
	var accepted atomic.Int32
	var winnerMu sync.Mutex
	var winner string
	for i := range 16 {
		wg.Go(func() {
			id := fmt.Sprintf("claim-%d", i)
			_, err := s.AppendForExecution(t.Context(), task, owner,
				Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{NodeID: id, NodeType: NodeTestClaim, Title: "相同结论", Status: TestClaimCandidate}},
				Event{Kind: EventEdgeAdded, Actor: "swarm:u1", Payload: EdgeAdded{EdgeType: EdgeAbout, FromNode: id, ToNode: "asset"}}, admissionSubmit(id))
			if err == nil {
				accepted.Add(1)
				winnerMu.Lock()
				winner = id
				winnerMu.Unlock()
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("duplicate new submissions accepted=%d", accepted.Load())
	}
	retakeVerdict := func() {
		mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "arbiter", Payload: TestClaimRecord{ClaimID: winner, Type: TestClaimRecVerdict, Decision: VerdictRetake, PendingReason: "固定补做", Conditions: []ArbiterConditionResult{{Kind: "evidence_traceable", Required: 1}}}})
	}
	retakeVerdict()
	accepted.Store(0)
	for range 16 {
		wg.Go(func() {
			if _, err := s.AppendForExecution(t.Context(), task, owner, admissionSubmit(winner)); err == nil {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("parallel retake submissions accepted=%d", accepted.Load())
	}
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "creator", Payload: TestClaimRecord{ClaimID: winner, Type: TestClaimRecReopen, Trigger: ReopenManual, Reason: "本地重验"}})
	if _, err := s.AppendForExecution(t.Context(), task, owner, admissionSubmit(winner)); err != nil {
		t.Fatalf("first submission after reopen: %v", err)
	}
	if _, err := s.AppendForExecution(t.Context(), task, owner, admissionSubmit(winner)); err == nil {
		t.Fatal("resubmission accepted before any retake feedback")
	}
	retakeVerdict()
	if _, err := s.AppendForExecution(t.Context(), task, owner, admissionSubmit(winner)); err != nil {
		t.Fatalf("real retake after reopen: %v", err)
	}
	if _, err := s.AppendForExecution(t.Context(), task, owner, admissionSubmit(winner)); err == nil {
		t.Fatal("retake cap bypassed")
	}
}
