package server

import (
	"reflect"
	"testing"

	"ghostreaper/internal/rings"
)

func TestPanoFocusAssetEntrancesFollowSelectedPaths(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "entry-join", NodeType: rings.NodeStep, Title: "两条实际来源汇合", Status: "done"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "entry-join", ToNode: "f-1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeSourcedFrom, FromNode: "entry-join", ToNode: "tc-1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "entry-join", ToNode: "a-root"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "entry-join", ToNode: "a-two"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "entry-output", NodeType: rings.NodeTestClaim, Title: "两条来源的产出", Status: rings.TestClaimConfirmed, Detail: map[string]any{"step_id": "entry-join"}}},
		// Only a subject binding exists here: it cannot invent an asset→claim arrow.
		rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: "subject-only", NodeType: rings.NodeTestClaim, Title: "没有产出来源", Status: rings.TestClaimConfirmed}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "subject-only", ToNode: "a-root"}},
	)
	base := "/api/tasks/" + task + "/pview/links/"
	rev, _ := store.CurrentRevision(t.Context(), task)
	var view panoLinksView
	get(t, h, base+"entry-join", 200, &view)
	pairs := map[string]bool{}
	for _, edge := range view.Edges {
		key := edge.From + "→" + edge.To
		if pairs[key] || len(edge.Refs) == 0 {
			t.Fatalf("repeated/unreferenced/nonuniform path: %+v", edge)
		}
		pairs[key] = true
	}
	want := map[string]bool{"a-root→s1": true, "a-two→s2": true, "s1→entry-join": true, "s2→tc-1": true, "tc-1→entry-join": true, "entry-join→entry-output": true}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("asset entrances or directed production paths: got %v want %v", pairs, want)
	}
	get(t, h, base+"entry-output", 200, &view)
	priorInput := false
	for _, edge := range view.Edges {
		priorInput = priorInput || edge.From == "tc-1" && edge.To == "entry-join"
		if edge.From == "s2" {
			t.Fatal("an earlier claim's own production paths expanded automatically")
		}
	}
	if !priorInput {
		t.Fatal("a genuine earlier claim input was silently removed")
	}
	get(t, h, base+"subject-only", 200, &view)
	if !reflect.DeepEqual(view.IDs, []string{"subject-only"}) || len(view.Edges) != 0 {
		t.Fatalf("a subject binding was invented into a path: %+v", view)
	}
	get(t, h, base+"a-root?target=s1", 200, &view)
	for _, edge := range view.Edges {
		if edge.From == "a-root" && edge.To != "s1" {
			t.Fatal("shared asset expanded another assignment branch")
		}
	}
	if len(view.Edges) == 0 || len(view.Targets) < 2 {
		t.Fatal("asset focus lost its actual assignment entries or branch choices")
	}
	after, _ := store.CurrentRevision(t.Context(), task)
	if rev != after {
		t.Fatal("path presentation changed the ledger")
	}
}

func TestPanoEveryFocusSelectsOnlyOverviewRelations(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	overview := p509View(t, h, task)
	pairs := map[string]panoEdgeRow{}
	for _, e := range overview.Edges {
		pairs[e.From+"\x00"+e.To] = e
	}
	for _, node := range overview.Nodes {
		var view panoLinksView
		get(t, h, "/api/tasks/"+task+"/pview/links/"+node.ID, 200, &view)
		for _, e := range view.Edges {
			original, ok := pairs[e.From+"\x00"+e.To]
			if !ok || original.Rel != e.Rel || original.Dashed != e.Dashed {
				t.Fatalf("%s: focus changed a relationship: %+v", node.ID, e)
			}
			for _, ref := range e.Refs {
				found := false
				for _, r := range original.Refs {
					found = found || r == ref
				}
				if !found {
					t.Fatal("focus fabricated source reference")
				}
			}
		}
	}
}
