package rings

import (
	"context"
	"reflect"
	"testing"
)

func relationSeed(t *testing.T, s *Store, task string) {
	t.Helper()
	for _, id := range []string{"a", "b", "ip", "service"} {
		mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: id, NodeType: NodeAsset, Title: id, Status: "active", AssetClass: AssetHost}})
	}
	mustAppend(t, s, task, Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: "step", NodeType: NodeStep, Title: "observe", Status: "done"}})
}

func TestAssetRelationValidation(t *testing.T) {
	cases := []struct {
		name  string
		r     AssetRelation
		extra map[string]any
		bad   bool
	}{
		{"resolve", AssetRelation{"a", "ip", "resolves_to"}, nil, false},
		{"deployment", AssetRelation{"service", "ip", "hosted_on"}, nil, false},
		{"related", AssetRelation{"a", "b", "related_to"}, nil, false},
		{"wrong kind", AssetRelation{"a", "ip", "sub_of"}, nil, true},
		{"missing", AssetRelation{"a", "missing", "related_to"}, nil, true},
		{"non asset", AssetRelation{"a", "step", "related_to"}, nil, true},
		{"self", AssetRelation{"a", "a", "related_to"}, nil, true},
		{"empty", AssetRelation{"a", "", "related_to"}, nil, true},
		{"inferred", AssetRelation{"a", "ip", "related_to"}, map[string]any{"inferred": true}, true},
		{"no evidence", AssetRelation{"a", "ip", "related_to"}, map[string]any{"evidence": ""}, true},
		{"undeclared new", AssetRelation{"a", "$new", "related_to"}, nil, true},
		{"declared new", AssetRelation{"a", "$new", "resolves_to"}, map[string]any{"new_asset": map[string]any{"class": "host", "title": "192.0.2.9"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			relationSeed(t, s, "t")
			d := map[string]any{"evidence": "actual response", "asset_relations": []AssetRelation{tc.r}}
			for k, v := range tc.extra {
				d[k] = v
			}
			_, err := s.Append(context.Background(), "t", Event{Kind: EventNodeAdded, Actor: "swarm:u", Payload: NodeAdded{NodeID: "f", NodeType: NodeFact, Title: "observation", Status: "candidate", Detail: d}})
			if (err != nil) != tc.bad {
				t.Fatalf("bad=%v err=%v", tc.bad, err)
			}
			if tc.bad {
				if _, err = s.Node(context.Background(), "t", "f"); err == nil {
					t.Fatal("invalid relation partially written")
				}
			}
		})
	}
}

func TestAssetRelationsConfirmedProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	relationSeed(t, s, "t")
	relationSeed(t, s, "other")
	add := func(id string) {
		mustAppend(t, s, "t", Event{Kind: EventNodeAdded, Actor: "swarm:u", Payload: NodeAdded{NodeID: id, NodeType: NodeFact, Title: "observed", Status: "candidate", Detail: map[string]any{"evidence": "DNS original reply", "asset_relations": []AssetRelation{{"a", "ip", "resolves_to"}, {"b", "ip", "resolves_to"}}}}})
	}
	state := func(id, status string) {
		mustAppend(t, s, "t", Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: id, Status: status}})
	}
	graph := func() *AssetGraphView {
		g, e := s.AssetGraph(ctx, "t")
		if e != nil {
			t.Fatal(e)
		}
		return g
	}
	add("f1")
	if len(graph().Relations) != 0 {
		t.Fatal("candidate relation exposed")
	}
	state("f1", "confirmed")
	g := graph()
	if len(g.Relations) != 2 || len(g.Hierarchy) != 0 {
		t.Fatalf("projection %+v", g)
	}
	other, _ := s.AssetGraph(ctx, "other")
	if len(other.Relations) != 0 {
		t.Fatal("task isolation")
	}
	add("f2")
	state("f2", "confirmed")
	g = graph()
	if len(g.Relations) != 2 || len(g.Relations[0].FactIDs) != 2 {
		t.Fatal("duplicate observations must share relation but keep both proofs")
	}
	state("f1", "discarded")
	g = graph()
	if len(g.Relations) != 2 || !reflect.DeepEqual(g.Relations[0].FactIDs, []string{"f2"}) {
		t.Fatal("discarded proof still supports relation")
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, graph()) {
		t.Fatal("replay changed relationships")
	}
	state("f2", "discarded")
	if len(graph().Relations) != 0 {
		t.Fatal("unsupported relation remains")
	}
}

func TestAssetRelationsNewReference(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	relationSeed(t, s, "t")
	mustAppend(t, s, "t", Event{Kind: EventNodeAdded, Actor: "swarm:u", Payload: NodeAdded{NodeID: "f", NodeType: NodeFact, Title: "observed", Status: "candidate", Detail: map[string]any{"evidence": "DNS reply", "new_asset": map[string]any{"class": "host", "title": "new ip"}, "asset_relations": []AssetRelation{{"a", "$new", "resolves_to"}, {"b", "$new", "resolves_to"}}}}})
	mustAppend(t, s, "t", Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "f", Status: "confirmed"}}, Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{NodeID: "new", NodeType: NodeAsset, Title: "new ip", AssetClass: AssetHost, Status: "active"}}, Event{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeObservedAsset, FromNode: "f", ToNode: "new"}})
	g, e := s.AssetGraph(ctx, "t")
	if e != nil {
		t.Fatal(e)
	}
	if len(g.Relations) != 2 || g.Relations[0].To != "new" || g.Relations[1].To != "new" || len(g.Hierarchy) != 0 {
		t.Fatalf("new resolution %+v", g)
	}
	// A second object with the same display title must not steal the binding.
	mustAppend(t, s, "t", Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: "twin", NodeType: NodeAsset, Title: "new ip", AssetClass: AssetHost, Status: "active"}})
	if _, err := s.Append(ctx, "t", Event{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeObservedAsset, FromNode: "f", ToNode: "twin"}}); err == nil {
		t.Fatal("observation rebound to another asset")
	}
	before, _ := s.AssetGraph(ctx, "t")
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := s.AssetGraph(ctx, "t")
	if !reflect.DeepEqual(before, after) || after.Relations[0].To != "new" {
		t.Fatal("replay changed stable asset identity")
	}
	mustAppend(t, s, "t", Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "new", Status: AssetStatusRetired}})
	g, e = s.AssetGraph(ctx, "t")
	if e != nil || len(g.Relations) != 0 {
		t.Fatalf("retired endpoint remains: %+v %v", g, e)
	}
}

func TestAssetObservedBindingValidation(t *testing.T) {
	for _, tc := range []struct {
		name, status, target string
		usesNew              bool
	}{
		{"candidate", "candidate", "new", true},
		{"undeclared", "confirmed", "new", false},
		{"wrong asset", "confirmed", "ip", true},
		{"retired", "confirmed", "retired", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			relationSeed(t, s, "t")
			for _, id := range []string{"new", "retired"} {
				status := "active"
				if id == "retired" {
					status = AssetStatusRetired
				}
				mustAppend(t, s, "t", Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{NodeID: id, NodeType: NodeAsset, Title: "new ip", AssetClass: AssetHost, Status: status}})
			}
			d := map[string]any{"evidence": "DNS reply"}
			if tc.usesNew {
				d["new_asset"] = map[string]any{"class": "host", "title": "new ip"}
				d["asset_relations"] = []AssetRelation{{"a", "$new", "resolves_to"}}
			}
			mustAppend(t, s, "t", Event{Kind: EventNodeAdded, Actor: "swarm:u", Payload: NodeAdded{NodeID: "f", NodeType: NodeFact, Title: "observed", Status: tc.status, Detail: d}})
			if _, err := s.Append(context.Background(), "t", Event{Kind: EventEdgeAdded, Actor: "overmind", Payload: EdgeAdded{EdgeType: EdgeObservedAsset, FromNode: "f", ToNode: tc.target}}); err == nil {
				t.Fatal("invalid observation binding accepted")
			}
		})
	}
}
