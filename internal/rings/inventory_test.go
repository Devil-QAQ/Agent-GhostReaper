package rings

import (
	"reflect"
	"sort"
	"testing"
)

func TestInventoryMembershipSharedWithoutRewritingHistory(t *testing.T) {
	s := openTestStore(t)
	const task = "inventory"
	mk := func(id, title string, class AssetClass, status, source, scope string, adopted bool) Event {
		return Event{Kind: EventNodeAdded, Actor: "test", Payload: NodeAdded{NodeID: id, NodeType: NodeAsset, Title: title, AssetClass: class, Status: status, Detail: map[string]any{"source": source, "scope": scope, "adopted": adopted}}}
	}
	mustAppend(t, s, task,
		mk("seed", "example.org", AssetSubdomain, "active", AssetSourceSeed, AssetScopeIn, false),
		mk("waiting", "new.example.org", AssetDomain, "active", AssetSourceDiscovered, AssetScopeOut, false),
		mk("background", "登记机构", AssetOrganization, "active", AssetSourceDiscovered, AssetScopeIn, false),
		mk("joined", "https://app.example.org/", AssetService, "active", AssetSourceDiscovered, AssetScopeIn, true),
		mk("excluded", "excluded.example.org", AssetDomain, "active", AssetSourceSeed, AssetScopeIn, false),
		mk("retired", "192.0.2.4", AssetHost, AssetStatusRetired, AssetSourceManual, AssetScopeIn, false),
		Event{Kind: EventNodeAdded, Actor: "swarm:u", Payload: NodeAdded{NodeID: "fact", NodeType: NodeFact, Title: "登记资料观察", Status: "confirmed", Detail: map[string]any{"evidence": "original evidence"}}},
		Event{Kind: EventEdgeAdded, Actor: "test", Payload: EdgeAdded{EdgeType: EdgeAbout, FromNode: "fact", ToNode: "background"}},
		Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{ExcludedAssets: []string{"excluded"}}},
	)
	check := func(want []string) {
		t.Helper()
		ag, err := s.AssetGraph(t.Context(), task)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, a := range ag.Assets {
			ids = append(ids, a.ID)
			if a.ID == "seed" && a.AssetClass != AssetDomain {
				t.Fatal("subdomain remains a separate class")
			}
		}
		sort.Strings(ids)
		sort.Strings(want)
		if !reflect.DeepEqual(ids, want) {
			t.Fatal("inventory", ids, "want", want)
		}
		g, err := s.Graph(t.Context(), task)
		if err != nil {
			t.Fatal(err)
		}
		other := []string{}
		visible := map[string]bool{}
		for _, n := range g.Nodes {
			visible[n.ID] = true
			if n.Type == NodeAsset {
				other = append(other, n.ID)
			}
		}
		sort.Strings(other)
		if !reflect.DeepEqual(ids, other) {
			t.Fatal("shared graph membership drift", ids, other)
		}
		for _, e := range g.Edges {
			if !visible[e.From] || !visible[e.To] {
				t.Fatal("dangling current edge")
			}
		}
		if !visible["fact"] {
			t.Fatal("background evidence removed")
		}
	}
	check([]string{"seed", "joined"})
	all, _ := s.NodesByType(t.Context(), task, NodeAsset, "")
	if len(all) != 6 {
		t.Fatal("historical nodes rewritten")
	}
	bg, _ := s.Node(t.Context(), task, "background")
	if bg.Status != "active" || bg.Title != "登记机构" {
		t.Fatal("background history rewritten")
	}
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{ExcludedAssets: []string{"excluded"}, ScopeApproved: []string{"waiting"}}})
	check([]string{"seed", "joined", "waiting"})
	if err := s.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
	check([]string{"seed", "joined", "waiting"})
}
