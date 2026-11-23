package overmind

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestAssetAdoptionDeclineDoesNotDenyAccessOrDiscardEvidence(t *testing.T) {
	store := openStore(t)
	const task = "adoption-decline"
	seedDiscoveredGraph(t, store, task)
	submitDiscoveryFact(t, store, task, "f1", "u1", &swarm.NewAssetDecl{Class: "subdomain", Title: "new.example.org"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	assets, _ := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	cards, err := store.PendingAssetProposals(t.Context(), task)
	if err != nil || len(assets) != 1 || len(cards) != 1 {
		t.Fatalf("candidate leaked into inventory: assets=%d cards=%d err=%v", len(assets), len(cards), err)
	}
	if _, err := DecideAssetProposal(t.Context(), store, task, cards[0].RequestID, false, ""); err != nil {
		t.Fatal(err)
	}
	roe, err := store.CurrentRoE(t.Context(), task)
	if err != nil || len(roe.ExcludedAssets) != 0 || roe.UpdatedRev != 0 {
		t.Fatalf("declining changed action policy: %+v %v", roe, err)
	}
	step, _ := store.Node(t.Context(), task, "s1")
	fact, _ := store.Node(t.Context(), task, "f1")
	if step.Status != "dispatched" || fact.Status != "confirmed" || fact.Detail["evidence"] == "" {
		t.Fatal("decline terminated work or lost evidence")
	}
	submitDiscoveryFact(t, store, task, "f2", "u2", &swarm.NewAssetDecl{Class: "domain", Title: "NEW.example.org."})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	cards, _ = store.PendingAssetProposals(t.Context(), task)
	assets, _ = store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if len(cards) != 0 || len(assets) != 1 {
		t.Fatal("repeated discovery reprompted or adopted a declined identity")
	}
}

func TestAssetAdoptionAddsOneAssetWithOriginalProvenance(t *testing.T) {
	store := openStore(t)
	const task = "adoption-add"
	seedDiscoveredGraph(t, store, task)
	submitDiscoveryFact(t, store, task, "f1", "u1", &swarm.NewAssetDecl{Class: "api", Title: "https://api.example.org:443/v1/list"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 1 {
		t.Fatalf("cards=%d", len(cards))
	}
	if _, err := DecideAssetProposal(t.Context(), store, task, cards[0].RequestID, true, ""); err != nil {
		t.Fatal(err)
	}
	submitDiscoveryFact(t, store, task, "f2", "u2", &swarm.NewAssetDecl{Class: "service", Title: "https://API.example.org/v1/detail"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	assets, _ := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if len(assets) != 3 {
		t.Fatalf("resources did not converge to their website: assets=%d", len(assets))
	}
	var added rings.NodeFull
	for _, a := range assets {
		if a.ID != "a-seed" && a.AssetClass == rings.AssetService {
			added = a
		}
	}
	if added.AssetClass != rings.AssetService || added.Detail["discovered_fact"] != "f1" || added.Detail["step_id"] != "s1" || added.Title != "https://api.example.org" {
		t.Fatalf("lost identity/address/provenance: %+v", added)
	}
	refs, _ := store.Edges(t.Context(), task, rings.EdgeAbout)
	bound := 0
	for _, e := range refs {
		if e.To == added.ID && (e.From == "f1" || e.From == "f2") {
			bound++
		}
	}
	if bound != 2 {
		t.Fatal("repeated observation not bound to existing service")
	}
	roe, _ := store.CurrentRoE(t.Context(), task)
	if roe.UpdatedRev != 0 {
		t.Fatal("inventory adoption granted action permission")
	}
}

func TestAssetAdoptionConcurrentDecisionsCommitOneOutcome(t *testing.T) {
	store := openStore(t)
	const task = "adoption-race"
	seedDiscoveredGraph(t, store, task)
	submitDiscoveryFact(t, store, task, "f1", "u1", &swarm.NewAssetDecl{Class: "domain", Title: "new.example.org"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := DecideAssetProposal(t.Context(), store, task, cards[0].RequestID, i%2 == 0, "")
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrAssetProposalDecided) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("committed outcomes=%d", won.Load())
	}
	cards, _ = store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 0 {
		t.Fatal("decided card remains pending")
	}
}

func TestAssetAdoptionBackgroundRemainsObservation(t *testing.T) {
	store := openStore(t)
	const task = "adoption-background"
	seedDiscoveredGraph(t, store, task)
	submitDiscoveryFact(t, store, task, "f1", "u1", &swarm.NewAssetDecl{Class: "organization", Title: "登记资料中的组织"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	assets, _ := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	fact, _ := store.Node(t.Context(), task, "f1")
	if len(assets) != 1 || len(cards) != 0 || fact.Status != "confirmed" {
		t.Fatal("background was lost or promoted into a target")
	}
}

func adoptPendingAssets(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	cards, err := store.PendingAssetProposals(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Fatal("expected an asset adoption request")
	}
	for _, card := range cards {
		if _, err := DecideAssetProposal(t.Context(), store, task, card.RequestID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
}
