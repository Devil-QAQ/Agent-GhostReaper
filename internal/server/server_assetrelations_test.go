package server

import (
	"context"
	"encoding/json"
	"ghostreaper/internal/rings"
	"net/http"
	"testing"
)

func TestAssetRelationsAPIAndPanoramaIsolation(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	s := srv.Store
	ctx := context.Background()
	const task = "relation-api"
	events := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "关系"}}}
	for _, id := range []string{"a", "b", "ip"} {
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset, Title: id + ".test", AssetClass: rings.AssetHost, Status: "active"}})
	}
	if _, err := s.Append(ctx, task, events...); err != nil {
		t.Fatal(err)
	}
	before, err := srv.panoSkeletonOf(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Title: "observed", Status: "confirmed", Detail: map[string]any{"evidence": "DNS original", "new_asset": map[string]any{"class": "host", "title": "ip.test"}, "asset_relations": []rings.AssetRelation{{From: "a", To: "$new", Kind: "resolves_to"}, {From: "b", To: "$new", Kind: "resolves_to"}}}}}, rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeObservedAsset, FromNode: "f", ToNode: "ip"}}); err != nil {
		t.Fatal(err)
	}
	after, err := srv.panoSkeletonOf(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	before.Rev = after.Rev
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("asset associations changed existing panorama projection")
	}
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/assets", "")
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	relations, _ := out["relations"].([]any)
	hierarchy, _ := out["hierarchy"].([]any)
	if len(relations) != 2 || len(hierarchy) != 0 {
		t.Fatal(out)
	}
	code, detail := callJSON(t, mux, "GET", "/api/tasks/"+task+"/assets/ip", "")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	rs, _ := detail["relations"].([]any)
	if len(rs) != 2 {
		t.Fatal("detail lost associations", detail)
	}
	for _, raw := range rs {
		r := raw.(map[string]any)
		if r["label"] != "解析到" || len(r["fact_ids"].([]any)) != 1 {
			t.Fatal("missing explanation or provenance", r)
		}
	}
}
