package rings

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestAttributedEvidenceVersionConcurrencyAndLegacyReplay(t *testing.T) {
	s := openTestStore(t)
	const task = "fact-evidence"
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: "tc-original", NodeType: NodeTestClaim, Status: TestClaimConfirmed, Title: "原漏洞"}},
		Event{Kind: EventTestClaimRecord, Actor: "test", Payload: TestClaimRecord{ClaimID: "tc-original", Type: TestClaimRecRepro, Statement: "原说明", Impact: "原影响", Severity: "high"}})
	legacySource, _ := s.ClaimTextSourceRev(t.Context(), task, "tc-original")
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: "f-legacy", NodeType: NodeFact, Status: "confirmed", Title: "历史来源事实"}},
		Event{Kind: EventEdgeAdded, Actor: "test", Payload: EdgeAdded{EdgeType: EdgeSourcedFrom, FromNode: "tc-original", ToNode: "f-legacy"}},
		Event{Kind: EventClaimText, Actor: "test", Payload: ClaimTextPolish{ClaimID: "tc-original", SourceRev: legacySource, Title: "历史有效稿", Statement: "历史有效稿"}})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			id := fmt.Sprintf("f-new-%02d", i)
			_, err := s.Append(t.Context(), task,
				Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: id, NodeType: NodeFact, Status: "confirmed", Title: "已确认的补充证据", Detail: map[string]any{"claim_id": "tc-original", "evidence": "原始观察"}}},
				Event{Kind: EventEdgeAdded, Actor: "test", Payload: EdgeAdded{EdgeType: EdgeSourcedFrom, FromNode: "tc-original", ToNode: id}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if _, err := s.AppendDisplay(t.Context(), task, Event{Kind: EventClaimText, Actor: "test", Payload: ClaimTextPolish{ClaimID: "tc-original", SourceRev: legacySource, Title: "过期稿", Statement: "过期稿"}}); err == nil {
		t.Fatal("new evidence accepted an earlier source version")
	}
	current, _ := s.ClaimTextSourceRev(t.Context(), task, "tc-original")
	mustAppend(t, s, task, Event{Kind: EventClaimText, Actor: "test", Payload: ClaimTextPolish{ClaimID: "tc-original", SourceRev: current, Title: "当前稿", Statement: "当前稿", Facts: []FactStatement{}}})
	check := func() {
		t.Helper()
		row, _, err := s.ClaimCaseOf(t.Context(), task, "tc-original")
		if err != nil || len(row.Summary.Facts) != 17 || row.Summary.Title != "当前稿" {
			t.Fatal("concurrent sources lost or display erased facts", row, err)
		}
		versions, total, err := s.EvidenceVersions(t.Context(), task)
		if err != nil || versions["tc-original"] != 17 || total != 17 {
			t.Fatal("version lost new evidence or recounted historical sources", versions, total, err)
		}
	}
	check()
	if err := s.RebuildProjections(t.Context()); err != nil {
		t.Fatal("historical display source versions broke on replay", err)
	}
	check()
}

// The path must be a disposable backup, never the running database. This
// opt-in check exercises actual historical event shapes without a model call.
func TestAttributedEvidenceHistoricalCopy(t *testing.T) {
	path := os.Getenv("GHOSTREAPER_REPLAY_COPY")
	if path == "" {
		t.Skip("disposable historical copy not supplied")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
}
