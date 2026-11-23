package server

import (
	"reflect"
	"testing"

	"ghostreaper/internal/rings"
)

func TestPanoFocusUsesOriginalFactInputsAndStopsAtClaimBoundary(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{NodeID: "tc-3", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "unrelated", NodeType: rings.NodeFact, Title: "同一意图的其他事实", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "unrelated"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "unrelated", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "next", NodeType: rings.NodeStep, Title: "根据已有结果继续", Status: "done"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "next", ToNode: "tc-3"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "later", NodeType: rings.NodeTestClaim, Title: "后续产出", Status: rings.TestClaimConfirmed, Detail: map[string]any{"step_id": "next"}}},
	)
	before := p509View(t, h, task)
	rev, _ := store.CurrentRevision(t.Context(), task)
	var focus panoLinksView
	get(t, h, "/api/tasks/"+task+"/pview/links/tc-3", 200, &focus)
	if !reflect.DeepEqual(focus.IDs, []string{"a-root", "s1", "tc-3"}) {
		t.Fatalf("claim production scope: %v", focus.IDs)
	}
	if len(focus.Results["s1"]) != 1 || focus.Results["s1"][0].ID != "f-1" {
		t.Fatalf("other results leaked through the merged card: %v", focus.Results)
	}
	if !reflect.DeepEqual(focus.Targets, []string{"next"}) {
		t.Fatal("real downstream reference was deleted")
	}
	var explicit panoLinksView
	get(t, h, "/api/tasks/"+task+"/pview/links/tc-3?target=next", 200, &explicit)
	seen := map[string]bool{}
	for _, id := range explicit.IDs {
		seen[id] = true
	}
	if !seen["next"] || !seen["later"] {
		t.Fatal("explicit downstream exploration lost actual output")
	}
	var downstream panoLinksView
	get(t, h, "/api/tasks/"+task+"/pview/links/later", 200, &downstream)
	if !reflect.DeepEqual(downstream.IDs, []string{"tc-3", "next", "later"}) {
		t.Fatalf("previous claim branch expanded automatically: %v", downstream.IDs)
	}
	var scoped, all map[string]any
	get(t, h, "/api/tasks/"+task+"/pview/details/s1?facts=f-1,not-produced", 200, &scoped)
	get(t, h, "/api/tasks/"+task+"/pview/details/s1", 200, &all)
	if len(scoped["results"].([]any)) != 1 || scoped["results"].([]any)[0].(map[string]any)["id"] != "f-1" || len(all["results"].([]any)) != 3 {
		t.Fatal("scoped detail lost precision or full detail lost original results")
	}
	get(t, h, "/api/tasks/"+task+"/pview/details/s1?facts=", 200, &scoped)
	if len(scoped["results"].([]any)) != 0 {
		t.Fatal("missing fact lineage was replaced with all facts")
	}
	afterRev, _ := store.CurrentRevision(t.Context(), task)
	if rev != afterRev || !reflect.DeepEqual(before, p509View(t, h, task)) {
		t.Fatal("focus selection rewrote original provenance")
	}
}

func TestPanoFactFocusDoesNotExpandSiblingResultsThroughAlias(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "sibling-fact", NodeType: rings.NodeFact, Title: "另一事实", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "sibling-fact"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "sibling-claim", NodeType: rings.NodeTestClaim, Title: "另一事实的产出", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "sibling-claim", ToNode: "sibling-fact"}},
	)
	var view panoLinksView
	get(t, h, "/api/tasks/"+task+"/pview/links/f-1", 200, &view)
	for _, id := range view.IDs {
		if id == "sibling-claim" {
			t.Fatal("fact alias expanded another result branch")
		}
	}
	if len(view.Results["s1"]) != 1 || view.Results["s1"][0].ID != "f-1" {
		t.Fatalf("fact selection was lost: %v", view.Results)
	}
	if !reflect.DeepEqual(view.Targets, []string{"tc-3"}) {
		t.Fatalf("fact branch choices contain sibling outputs: %v", view.Targets)
	}
	get(t, h, "/api/tasks/"+task+"/pview/links/f-1?target=sibling-claim", 400, nil)
	var hits []panoSearchRow
	get(t, h, "/api/tasks/"+task+"/pview/search?q=f-1", 200, &hits)
	if len(hits) == 0 || hits[0].ID != "s1" || hits[0].Ref != "f-1" {
		t.Fatalf("search lost the original fact reference: %v", hits)
	}
	hits = nil
	get(t, h, "/api/tasks/"+task+"/pview/search?q=s1", 200, &hits)
	if len(hits) == 0 || hits[0].ID != "s1" || hits[0].Ref != "" {
		t.Fatal("intent search narrowed itself to one of its facts")
	}
}

func TestPanoIntentFocusKeepsMultipleProductionPathsWithoutSiblingFanout(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "join", NodeType: rings.NodeStep, Title: "汇合两条来源", Status: "done"}},
		{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "join", ToNode: "f-1"}},
		{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "join", ToNode: "tc-1"}},
	}
	for _, branch := range []string{"left", "right"} {
		events = append(events,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: branch, NodeType: rings.NodeStep, Title: branch, Status: "done"}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: branch, ToNode: "join"}},
			rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: branch + "-claim", NodeType: rings.NodeTestClaim, Title: branch + " output", Status: rings.TestClaimConfirmed, Detail: map[string]any{"step_id": branch}}},
		)
	}
	appendEvents(t, store, task, events...)
	for query, want := range map[string][]string{
		"":                          {"a-root", "a-two", "s1", "s2", "tc-1", "join", "left", "left-claim", "right", "right-claim"},
		"?source=s1":                {"a-root", "s1", "join", "left", "left-claim", "right", "right-claim"},
		"?target=left":              {"a-root", "a-two", "s1", "s2", "tc-1", "join", "left", "left-claim"},
		"?source=tc-1&target=right": {"a-two", "s2", "tc-1", "join", "right", "right-claim"},
	} {
		var view panoLinksView
		get(t, h, "/api/tasks/"+task+"/pview/links/join"+query, 200, &view)
		actual := map[string]bool{}
		for _, id := range view.IDs {
			if actual[id] {
				t.Fatal("shared card duplicated")
			}
			actual[id] = true
		}
		expected := map[string]bool{}
		for _, id := range want {
			expected[id] = true
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("multi-path %s: got %v want %v", query, actual, expected)
		}
		for _, edge := range view.Edges {
			if !actual[edge.From] || !actual[edge.To] {
				t.Fatal("edge left outside selected paths")
			}
		}
	}
}
