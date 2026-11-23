package rings

import (
	"context"
	"errors"
	"testing"
)

func TestClaimSubmissionVersionRejectsOldAndDuplicateResults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task, claim = "claim-version", "tc-one"
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: claim, NodeType: NodeTestClaim, Status: TestClaimCandidate}}, Event{Kind: EventTestClaimRecord, Actor: "test", Payload: TestClaimRecord{ClaimID: claim, StepID: "s1", UnitID: "u1", Type: TestClaimRecSubmit, Summary: "固定结论", Severity: "low", EvidenceRefs: []string{"fixture"}, ProvingMarker: "fixture", CountercheckNote: "固定自查"}})
	rev, _ := s.CurrentRevision(ctx, task)
	v := ClaimSubmissionVersion{ClaimID: claim, SubmitRev: rev}
	verdict := Event{Kind: EventTestClaimRecord, Actor: "arbiter", Payload: TestClaimRecord{ClaimID: claim, Type: TestClaimRecVerdict, Decision: VerdictInsufficient, PendingReason: "固定失败", EvaluatedRev: rev, Conditions: []ArbiterConditionResult{{Kind: "evidence_traceable", Required: 1}}}}
	if _, err := s.AppendForClaimSubmission(ctx, task, v, nil, verdict); err != nil {
		t.Fatal(err)
	}
	after, _ := s.CurrentRevision(ctx, task)
	if _, err := s.AppendForClaimSubmission(ctx, task, v, nil, verdict); !errors.Is(err, ErrClaimCheckApplied) {
		t.Fatalf("duplicate: %v", err)
	}
	mustAppend(t, s, task, Event{Kind: EventTestClaimRecord, Actor: "creator", Payload: TestClaimRecord{ClaimID: claim, Type: TestClaimRecReopen, Trigger: ReopenManual, Reason: "新证据"}})
	if _, err := s.AppendForClaimSubmission(ctx, task, v, nil, verdict); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("old campaign: %v", err)
	}
	current, _ := s.CurrentRevision(ctx, task)
	if current != after+1 {
		t.Fatal("rejected stale or duplicate result wrote events")
	}
}

func TestClaimDisplayCannotChangeOrDropSealedProof(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task, claim = "sealed-display", "tc-one"
	base := []ReproStep{{Seq: 1, StepID: "s1", UnitID: "u1", URL: "https://fixture.example.test/one", Method: "GET", BlobRef: "blob-one", Desc: "原说明"}, {Seq: 2, StepID: "s2", UnitID: "u2", URL: "https://fixture.example.test/two", Method: "GET", BlobRef: "blob-two"}}
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: claim, NodeType: NodeTestClaim, Status: TestClaimConfirmed}}, Event{Kind: EventTestClaimRecord, Actor: "overmind", Payload: TestClaimRecord{ClaimID: claim, Type: TestClaimRecRepro, Steps: base, ProofSeqs: []int{2}}})
	source, _ := s.ClaimTextSourceRev(ctx, task, claim)
	for _, mode := range []string{"drop-proof", "change-url", "change-blob", "duplicate"} {
		steps := append([]ReproStep(nil), base...)
		switch mode {
		case "drop-proof":
			steps = steps[:1]
		case "change-url":
			steps[1].URL += "/other"
		case "change-blob":
			steps[1].BlobRef = "other"
		case "duplicate":
			steps = append(steps, steps[1])
		}
		if _, err := s.Append(ctx, task, Event{Kind: EventClaimText, Actor: "overmind", Payload: ClaimTextPolish{ClaimID: claim, SourceRev: source, Repro: &ReproDigest{Steps: steps}}}); err == nil {
			t.Fatalf("accepted %s", mode)
		}
	}
	chosen := base[1]
	chosen.Desc = "可读的操作说明"
	mustAppend(t, s, task, Event{Kind: EventClaimText, Actor: "overmind", Payload: ClaimTextPolish{ClaimID: claim, SourceRev: source, Repro: &ReproDigest{Steps: []ReproStep{chosen}, Narrative: "两次实际观察后的结论"}}})
	row, _, _ := s.ClaimCaseOf(ctx, task, claim)
	if len(row.Repro.Steps) != 1 || row.Repro.Steps[0].BlobRef != "blob-two" || row.Repro.Steps[0].Desc != chosen.Desc {
		t.Fatal("display did not retain exact proof")
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, _, _ := s.ClaimCaseOf(ctx, task, claim)
	if len(rebuilt.Repro.Steps) != 1 || rebuilt.Repro.Steps[0] != chosen {
		t.Fatal("display replay diverged")
	}
}
