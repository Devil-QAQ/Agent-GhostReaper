package overmind

import (
	"fmt"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func TestReproFactsKeepsAllExplicitProofAndBoundedContext(t *testing.T) {
	for _, count := range []int{4, 60, 61, 13027} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store, ex := p473Base(t, "proof-context", "tc-proof")
			ref := p473ExchangeBlob(t, ex.Blobs, "synthetic", "http://t.example:8080/proof", "fixed proof bytes")
			l := &Loop{Store: store, Swarm: ex, TaskID: "proof-context"}
			rec := &rings.TestClaimRecord{}
			for i := range count {
				rec.Steps = append(rec.Steps, rings.ReproStep{Seq: i + 1, URL: fmt.Sprintf("http://t.example:8080/item/%d", i), BlobRef: ref})
			}
			proofs := []int{1, count / 2, count}
			facts := l.reproFacts(rec, proofs)
			seen := map[int]bool{}
			for _, fact := range facts {
				seen[fact.Seq] = fact.Seed
				if fact.RespHead != "fixed proof bytes" {
					t.Fatal("selected body was not read")
				}
			}
			for _, seq := range proofs {
				if !seen[seq] {
					t.Fatalf("proof %d disappeared in %d candidates", seq, count)
				}
			}
			if len(facts) > reproFactAskCap+len(proofs) || len(rec.Steps) != count {
				t.Fatal("unbounded model context or candidate ledger modified", len(facts), len(rec.Steps))
			}
			if count > reproFactAskCap && (!seen[1] || !seen[count]) {
				t.Fatal("first/last proof missing")
			}
			t.Logf("候选%d项，读取并提供模型的正文%d项，原候选和证据引用保留", count, len(facts))
		})
	}
}

func TestSubmissionEvidenceViewIsLocalAndKeepsFullProcess(t *testing.T) {
	const task, claim = "evidence-view", "tc-view"
	store, ex := p473Base(t, task, claim)
	p473FalsifyRound(t, store, task, claim, "s-f", "固定证据", []string{"proof"}, "fixed")
	ref := p473ExchangeBlob(t, ex.Blobs, "proof", "http://t.example:8080/proof", "fixed")
	p473Mirror(t, store, task, "s-f", "proof", "http://t.example:8080/proof", 200, ref)
	view, err := readClaimEvidence(t.Context(), store, task, claim)
	if err != nil {
		t.Fatal(err)
	}
	// Evidence arriving later is deliberately absent from this operation's
	// view, but appears in the next operation. No cross-call decision cache.
	p473Mirror(t, store, task, "s-f", "later", "http://t.example:8080/other", 200, ref)
	seeds, err := resolveEvidenceSeedsFrom(ex.Blobs, task, claim, view)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _, err := assembleReproFrom(t.Context(), store, ex.Blobs, rings.DefaultScopeResolver, task, claim, "high", seeds, view)
	if err != nil || len(first.Process) != 1 {
		t.Fatal("operation view changed during assembly", first, err)
	}
	fresh, err := readClaimEvidence(t.Context(), store, task, claim)
	if err != nil {
		t.Fatal(err)
	}
	seeds, err = resolveEvidenceSeedsFrom(ex.Blobs, task, claim, fresh)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := assembleReproFrom(t.Context(), store, ex.Blobs, rings.DefaultScopeResolver, task, claim, "high", seeds, fresh)
	if err != nil || len(second.Process) != 2 {
		t.Fatal("new evidence was cached away", second, err)
	}
	for _, record := range second.Process {
		if record.BlobRef != ref {
			t.Fatal("original process reference changed")
		}
	}
}

func TestPlannerSnapshotShowsFactProducerAndExistingClaim(t *testing.T) {
	const task = "planner-ownership"
	store := openStore(t)
	mkTask(t, store, task)
	if _, err := store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Status: "running", Title: "核查测试记录"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "tc-one", NodeType: rings.NodeTestClaim, Status: rings.TestClaimConfirmed, Title: "已确认问题"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "f-one", NodeType: rings.NodeFact, Status: "confirmed", Title: "新证据补充已有问题", Detail: map[string]any{"claim_id": "tc-one"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-one"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "tc-one", ToNode: "f-one"}},
	); err != nil {
		t.Fatal(err)
	}
	l := &Loop{Store: store, TaskID: task}
	snapshot, _, _, _, err := l.buildSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "f-one: 新证据补充已有问题 (产出意图 [s1]) (已归属漏洞 [tc-one])") {
		t.Fatal("planner lost recorded ownership", snapshot)
	}
}
