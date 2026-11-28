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

func TestAutoJoinSkipsMembershipJudgmentAndRechecksRecordedAssets(t *testing.T) {
	store := openStore(t)
	const task = "join-before-judgment"
	seedDiscoveredGraph(t, store, task)
	evidence := "已发现应用 业务入口"
	if _, err := store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "observed-app", NodeType: rings.NodeFact, Status: "confirmed", Title: evidence, Detail: map[string]any{"evidence": evidence}}}); err != nil {
		t.Fatal(err)
	}
	loop := &Loop{Store: store, TaskID: task, Provider: noAssetModel{t}}
	fact, _ := store.Node(t.Context(), task, "observed-app")
	observed := rings.AssetObservations{Assets: []rings.AssetObservation{{Class: rings.AssetApp, Title: "业务入口", Evidence: evidence}}}
	if err := loop.recordFactAssets(t.Context(), *fact, observed); err != nil {
		t.Fatal(err)
	}
	assets, _ := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if len(assets) != 1 {
		t.Fatal("disabled mode skipped original membership judgment", assets)
	}
	if err := store.SetApprovalAutomationFlag(t.Context(), task, "auto_join", true); err != nil {
		t.Fatal(err)
	}
	loop.syncRecordedAssets(t.Context())
	assets, _ = store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	if len(assets) != 2 || len(cards) != 0 {
		t.Fatal("enabled mode still required membership judgment", assets, cards)
	}
	for _, a := range assets {
		if a.Title == "业务入口" {
			if adopted, _ := a.Detail["adopted"].(bool); !adopted {
				t.Fatal(a)
			}
		}
	}
	before, _ := store.CurrentRevision(t.Context(), task)
	loop.syncRecordedAssets(t.Context())
	after, _ := store.CurrentRevision(t.Context(), task)
	if before != after {
		t.Fatal("repeat placement changed stable inventory", before, after)
	}
	if err := store.SetApprovalAutomationFlag(t.Context(), task, "auto_join", false); err != nil {
		t.Fatal(err)
	}
	submitDiscoveryFact(t, store, task, "new-manual", "u1", &swarm.NewAssetDecl{Class: "service", Title: "https://another.test:9555"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	cards, _ = store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 1 {
		t.Fatal("disabled mode did not restore membership choice", cards)
	}
}
