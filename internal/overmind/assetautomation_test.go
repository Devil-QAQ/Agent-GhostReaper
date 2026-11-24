package overmind

import (
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"testing"
)

func TestAutoJoinAppliesToFutureEntriesWithoutGrantingActions(t *testing.T) {
	store := openStore(t)
	task := "auto-join"
	seedDiscoveredGraph(t, store, task)
	if err := store.SetApprovalAutomationFlag(t.Context(), task, "auto_join", true); err != nil {
		t.Fatal(err)
	}
	submitDiscoveryFact(t, store, task, "auto-fact", "u1", &swarm.NewAssetDecl{Class: "service", Title: "https://independent.test:1123/"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	assets, _ := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	if len(assets) != 2 || len(cards) != 0 {
		t.Fatal("future entry not automatically joined", assets, cards)
	}
	rows, _ := store.ApprovalRows(t.Context(), task)
	for _, row := range rows {
		if row.Type == rings.ApprovalRecApproved {
			t.Fatal("joining granted a business permission")
		}
	}
	if err := store.SetApprovalAutomationFlag(t.Context(), task, "auto_join", false); err != nil {
		t.Fatal(err)
	}
	submitDiscoveryFact(t, store, task, "manual-fact", "u1", &swarm.NewAssetDecl{Class: "service", Title: "https://independent.test:1124/"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	cards, _ = store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 1 {
		t.Fatal("disabled auto join still admitted a new system", cards)
	}
}
