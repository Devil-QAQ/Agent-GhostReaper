package server

import (
	"ghostreaper/internal/rings"
	"testing"
)

func TestPanoramaSubjectsUseWebsiteAddressesAndDirectObservedLinks(t *testing.T) {
	srv, _, _ := newInterjectTestServer(t)
	const task = "site-subjects"
	events := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "网站主体"}}}
	for _, a := range []struct {
		id    string
		class rings.AssetClass
		title string
	}{
		{"ip", rings.AssetHost, "192.0.2.1"}, {"unmapped-address", rings.AssetDomain, "unmapped.example.test"}, {"one", rings.AssetService, "https://192.0.2.1:1122/"}, {"two", rings.AssetService, "https://192.0.2.1:1123/"}, {"login", rings.AssetAPI, "POST https://192.0.2.1:1122/login"},
	} {
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: a.id, NodeType: rings.NodeAsset, AssetClass: a.class, Title: a.title, Status: "active"}})
	}
	if _, err := srv.Store.Append(t.Context(), task, events...); err != nil {
		t.Fatal(err)
	}
	before, err := srv.panoSkeletonOf(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Nodes) != 2 || len(before.Edges) != 0 {
		t.Fatal("shared address became a subject or direct relationship", before.Nodes, before.Edges)
	}
	if len(before.Aliases["ip"]) != 2 || before.Aliases["login"][0] != "one" {
		t.Fatal(before.Aliases)
	}
	for _, n := range before.Nodes {
		if n.Y != 54 || (n.Ti != "192.0.2.1:1122" && n.Ti != "192.0.2.1:1123") {
			t.Fatal("first-row entry address missing", n)
		}
	}
	_, err = srv.Store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Status: "confirmed", Title: "入口跳转", Detail: map[string]any{"evidence": "HTTP redirect original", "asset_relations": []rings.AssetRelation{{From: "one", To: "two", Kind: "redirects_to"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := srv.panoSkeletonOf(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Nodes) != 2 || len(after.Edges) != 1 || after.Edges[0].Label != "跳转到" || !after.Edges[0].Dashed || len(after.Edges[0].Refs) != 1 || after.Edges[0].Refs[0] != "f" {
		t.Fatal("direct relationship missing or invented", after.Edges)
	}
	for _, n := range after.Nodes {
		if n.Y != 54 {
			t.Fatal("relationship changed first-row placement")
		}
	}
}

func TestNamedSystemSubjectUsesItsPrimaryEntryAddress(t *testing.T) {
	srv, _, _ := newInterjectTestServer(t)
	const task = "named-system"
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "系统"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "system", NodeType: rings.NodeAsset, AssetClass: rings.AssetApp, Title: "业务门户", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "entry", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://portal.example.test:1122/", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Status: "confirmed", Title: "系统入口", Detail: map[string]any{"evidence": "observed system entry", "asset_relations": []rings.AssetRelation{{From: "system", To: "entry", Kind: "hosted_on"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := srv.panoSkeletonOf(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Nodes) != 1 || view.Nodes[0].ID != "system" || view.Nodes[0].Ti != "portal.example.test:1122" || view.Aliases["entry"][0] != "system" {
		t.Fatal(view.Nodes, view.Aliases)
	}
}

func TestLegacyResourceReferenceOpensOwningWebsiteWithItsEvidence(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "resource-reference"
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "站内资源"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: "site", NodeType: rings.NodeAsset, AssetClass: rings.AssetService, Title: "https://example.test:1122/", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "old-resource", NodeType: rings.NodeAsset, AssetClass: rings.AssetAPI, Title: "POST https://example.test:1122/ui/metadata/hints?a=one", Status: "active", Detail: map[string]any{"source": "discovered", "adopted": true}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Status: "confirmed", Title: "站内资源响应", Detail: map[string]any{"evidence": "original request and response"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f", ToNode: "old-resource"}})
	if err != nil {
		t.Fatal(err)
	}
	code, tree := callJSON(t, mux, "GET", "/api/tasks/"+task+"/assets", "")
	if code != 200 || len(tree["assets"].([]any)) != 1 {
		t.Fatal("resource became an independent asset", tree)
	}
	asset := tree["assets"].([]any)[0].(map[string]any)
	if asset["id"] != "site" || asset["fact_count"] != float64(1) {
		t.Fatal("website lost resource evidence", asset)
	}
	code, detail := callJSON(t, mux, "GET", "/api/tasks/"+task+"/assets/old-resource", "")
	if code != 200 || detail["asset"].(map[string]any)["id"] != "site" || len(detail["facts"].([]any)) != 1 {
		t.Fatal("legacy reference did not resolve to website", detail)
	}
}
