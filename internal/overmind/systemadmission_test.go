package overmind

import (
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"testing"
)

func TestSystemAdmissionIsIndependentOfActionPermission(t *testing.T) {
	store := openStore(t)
	task := "system-admission"
	seedDiscoveredGraph(t, store, task)
	_, err := store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "site", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://example.com:1122/", Status: "active"}})
	if err != nil {
		t.Fatal(err)
	}
	// API variants remain the same site; a shared host does not admit new systems.
	submitDiscoveryFact(t, store, task, "resource", "u1", &swarm.NewAssetDecl{Class: "service", Title: "https://example.com:1122/api/users?q=x"})
	submitDiscoveryFact(t, store, task, "unknown", "u1", &swarm.NewAssetDecl{Class: "service", Title: "https://example.com:1123/"})
	submitDiscoveryFact(t, store, task, "unknown-again", "u1", &swarm.NewAssetDecl{Class: "service", Title: "https://example.com:1123/other/path"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 1 {
		t.Fatalf("one unknown system must produce one choice: %+v", cards)
	}
	if _, err := DecideAssetProposal(t.Context(), store, task, cards[0].RequestID, false, ""); err != nil {
		t.Fatal(err)
	}
	roe, _ := store.CurrentRoE(t.Context(), task)
	if roe.UpdatedRev != 0 {
		t.Fatal("membership choice changed action policy")
	}
	// Existing evidence of the same system admits an alternate entry without
	// requiring an action grant or treating every port as the same system.
	_, err = store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{NodeID: "same", NodeType: rings.NodeFact, Status: "candidate", Title: "证据确认同系统的第二入口", Detail: map[string]any{"evidence": "observed same backend identity", "new_asset": map[string]any{"class": "service", "title": "https://alias.example.com:1124/"}, "asset_relations": []rings.AssetRelation{{From: "$new", To: "site", Kind: "same_system"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	graph, err := store.AssetGraph(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range graph.Assets {
		if a.Title == "https://alias.example.com:1124/" {
			found = true
			if len(a.SubjectIDs) != 1 || a.SubjectIDs[0] != "site" {
				t.Fatal("same system split into another subject", a)
			}
		}
	}
	if !found {
		t.Fatal("same-system entry missing")
	}
	cards, _ = store.PendingAssetProposals(t.Context(), task)
	if len(cards) != 0 {
		t.Fatal(cards)
	}
	// A fingerprint is evidence about its site, not a new independent app.
	submitDiscoveryFact(t, store, task, "component", "u1", &swarm.NewAssetDecl{Class: "app", Title: "Spring OAuth2 授权组件"})
	if _, err := Harvest(t.Context(), store, task); err != nil {
		t.Fatal(err)
	}
	graph, err = store.AssetGraph(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range graph.Assets {
		if a.Title == "Spring OAuth2 授权组件" {
			t.Fatal("component became an independent system")
		}
	}
}
