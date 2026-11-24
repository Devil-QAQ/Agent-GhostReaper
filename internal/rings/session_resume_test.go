package rings

import (
	"reflect"
	"testing"
)

func TestSessionResumeReopensProjectionAndPreservesHistory(t *testing.T) {
	for _, reason := range []string{"checkpointed", "model_blocked", "approval_yield", "adopted"} {
		t.Run(reason, func(t *testing.T) {
			s := openTestStore(t)
			const task, step, sid = "resume", "s1", "swarm-original"
			start := func(unit string) int64 {
				t.Helper()
				revs, err := s.Append(t.Context(), task, Event{Kind: EventSwarmRecord, Actor: "swarm:" + unit, Payload: SwarmRecord{Type: SwarmRecSessionStarted, StepID: step, UnitID: unit, TranscriptID: sid}})
				if err != nil {
					t.Fatal(err)
				}
				return revs[0]
			}
			end := func(unit, status string, cost float64) {
				t.Helper()
				note := "reason=" + reason + " turns=9 status=" + status + " billable=1.5s"
				if cost == 2 {
					note = "reason=completed turns=4 status=done billable=2s"
				}
				mustAppend(t, s, task, Event{Kind: EventSwarmRecord, Actor: "swarm:" + unit, Payload: SwarmRecord{Type: SwarmRecSessionEnd, StepID: step, UnitID: unit, TranscriptID: sid, Note: note}})
			}
			start("u-original")
			if reason != "adopted" {
				end("u-original", "dispatched", 1.5)
			}
			before, _ := s.UnitSessions(t.Context(), task)
			resumedRev := start("u-resumed")
			check := func() {
				t.Helper()
				rows, err := s.UnitSessions(t.Context(), task)
				if err != nil || len(rows) != 1 {
					t.Fatal(rows, err)
				}
				row := rows[0]
				if row.UnitID != "u-resumed" || row.Transcript != sid || row.EndStatus != "" || row.FinishedAt != "" || row.Reason != "" || row.Turns != 0 || row.BillableSec != before[0].BillableSec || row.StartedAt != before[0].StartedAt || row.StartRev != resumedRev {
					t.Fatal("resumed row retained a previous execution or lost history", row)
				}
				for _, unit := range []string{"u-original", "u-resumed"} {
					anchor, err := s.SessionAnchorOf(t.Context(), task, unit, step, 0)
					if err != nil || anchor != sid {
						t.Fatal("lost unit provenance", unit, anchor, err)
					}
				}
				if anchor, err := s.SessionAnchorOf(t.Context(), task, "u-resumed", step, resumedRev); err != nil || anchor != "" {
					t.Fatal("resolved a future start at an earlier revision", anchor, err)
				}
			}
			check()
			mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: "tc-old-unit", NodeType: NodeTestClaim, Status: TestClaimCandidate, Title: "历史单位发现", Detail: map[string]any{"unit_id": "u-original", "step_id": step}}})
			if claim, _, err := s.ClaimCaseOf(t.Context(), task, "tc-old-unit"); err != nil || claim.DiscoverSess != sid {
				t.Fatal("historical finding lost its session anchor", claim, err)
			}
			// A late closeout from the replaced unit stays in the audit log,
			// but cannot close the current execution or add its cost twice.
			end("u-original", "dispatched", 1.5)
			check()
			if err := s.RebuildProjections(t.Context()); err != nil {
				t.Fatal(err)
			}
			check()
			end("u-resumed", "done", 2)
			finished, _ := s.UnitSessions(t.Context(), task)
			if finished[0].EndStatus != "done" || finished[0].BillableSec != before[0].BillableSec+2 || finished[0].Reason != "completed" {
				t.Fatal("closeout lost cumulative billing", finished)
			}
			if err := s.RebuildProjections(t.Context()); err != nil {
				t.Fatal(err)
			}
			replayed, _ := s.UnitSessions(t.Context(), task)
			if !reflect.DeepEqual(finished, replayed) {
				t.Fatal("replay diverged", finished, replayed)
			}
		})
	}
}
