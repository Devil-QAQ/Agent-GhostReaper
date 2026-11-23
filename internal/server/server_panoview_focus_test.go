package server

import (
	"fmt"
	"math"
	"net/http"
	"reflect"
	"testing"
)

func TestPanoPortsSeparateIncomingAndOutgoingOnSameFace(t *testing.T) {
	for _, delta := range [][2]float64{{0, -120}, {0, 120}, {-240, 0}, {240, 0}} {
		view := &panoSkeletonView{
			Nodes: []panoNodeRow{{ID: "center", X: 0, Y: 0}, {ID: "source", X: delta[0], Y: delta[1]}, {ID: "target", X: delta[0] * 2, Y: delta[1] * 2}},
			Edges: []panoEdgeRow{{From: "source", To: "center", Rel: "sourced_from"}, {From: "center", To: "target", Rel: "sourced_from"}},
		}
		finishPanoLayout(view)
		incoming, outgoing := view.Edges[0].In, view.Edges[1].Out
		if incoming == outgoing {
			t.Fatalf("incoming/outgoing share one attachment on face %v", delta)
		}
		bound := 48.0
		if delta[1] == 0 {
			bound = 16
		}
		if math.Abs(incoming) > bound || math.Abs(outgoing) > bound {
			t.Fatal("attachment outside card edge")
		}
		view.Edges[0], view.Edges[1] = view.Edges[1], view.Edges[0]
		finishPanoLayout(view)
		if view.Edges[1].In != incoming || view.Edges[0].Out != outgoing {
			t.Fatal("route depends on edge input order")
		}
	}
}

func TestPanoFocusChainUsesDirectionInsteadOfOverviewRows(t *testing.T) {
	view := layoutPanoFocus([]panoNodeRow{
		{ID: "a", T: "step", X: 100, Y: 158},
		{ID: "b", T: "claim", X: 100, Y: 454},
		{ID: "c", T: "step", X: 500, Y: 278},
	}, []panoEdgeRow{{From: "a", To: "b", Rel: "produces"}, {From: "b", To: "c", Rel: "sourced_from"}, {From: "a", To: "c", Rel: "sourced_from"}}, 1152, 716)
	if !(view.Nodes[0].Y < view.Nodes[1].Y && view.Nodes[1].Y < view.Nodes[2].Y) {
		t.Fatal("a real A→B→C path was bent back into the overview's A→C→B rows")
	}
}

func TestPanoFocusCompactLayout(t *testing.T) {
	cases := []struct {
		name                 string
		count, width, height int
	}{
		{"single", 1, 1152, 720}, {"sparse", 3, 1152, 720},
		{"wide", 600, 1152, 720}, {"narrow", 80, 390, 720},
		{"invalidViewport", 3, -1, 0}, {"largeViewport", 200, 999999, 999999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes := []panoNodeRow{{ID: "root", T: "asset", X: 50000, Y: 20}}
			edges := []panoEdgeRow{}
			for i := 1; i < tc.count; i++ {
				id := fmt.Sprintf("s%d", i)
				nodes = append(nodes, panoNodeRow{ID: id, T: "step", X: 50000 + float64(i)*10000, Y: 158})
				edges = append(edges, panoEdgeRow{From: "root", To: id, Rel: "covers", Refs: []string{"real-source"}})
			}
			beforeNodes := append([]panoNodeRow(nil), nodes...)
			beforeEdges := make([]panoEdgeRow, len(edges))
			copy(beforeEdges, edges)
			v := layoutPanoFocus(nodes, edges, tc.width, tc.height)
			if !reflect.DeepEqual(nodes, beforeNodes) || !reflect.DeepEqual(edges, beforeEdges) {
				t.Fatal("local layout mutated overview")
			}
			if len(v.Nodes) != len(nodes) || len(v.Edges) != len(edges) {
				t.Fatal("members or relations lost")
			}
			for i, n := range v.Nodes {
				if math.IsNaN(n.X) || math.IsInf(n.X, 0) || math.IsNaN(n.Y) || math.IsInf(n.Y, 0) {
					t.Fatal("invalid coordinates")
				}
				for j := 0; j < i; j++ {
					other := v.Nodes[j]
					if math.Abs(n.X-other.X) < panoCardW+16 && math.Abs(n.Y-other.Y) < panoCardH+16 {
						t.Fatalf("overlapping cards: %s / %s", n.ID, other.ID)
					}
				}
			}
			for i, e := range v.Edges {
				if e.From != edges[i].From || e.To != edges[i].To || e.Rel != edges[i].Rel || !reflect.DeepEqual(e.Refs, edges[i].Refs) {
					t.Fatal("invented or changed provenance")
				}
			}
			if tc.count > 1 && v.Bounds[2] >= float64(tc.count-1)*10000 {
				t.Fatal("sparse distance not compacted")
			}
			if tc.count > 50 && v.Bounds[3] <= 216 {
				t.Fatal("wide layer was not wrapped")
			}
			reverse := append([]panoNodeRow(nil), nodes...)
			for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
				reverse[i], reverse[j] = reverse[j], reverse[i]
			}
			permuted := layoutPanoFocus(reverse, edges, tc.width, tc.height)
			positions := map[string][2]float64{}
			for _, n := range v.Nodes {
				positions[n.ID] = [2]float64{n.X, n.Y}
			}
			for _, n := range permuted.Nodes {
				if positions[n.ID] != [2]float64{n.X, n.Y} {
					t.Fatal("layout depended on input order")
				}
			}
		})
	}
	if v := layoutPanoFocus(nil, nil, 0, 0); len(v.Nodes) != 0 || v.Bounds != [4]float64{} {
		t.Fatal("empty layout")
	}
}

func TestP509FocusPositionsKeepLedgerAndMembership(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	before := p509View(t, h, task)
	rev, _ := store.CurrentRevision(t.Context(), task)
	for _, id := range []string{"s1", "f-1", "a-sub", "tc-1"} {
		var view panoLinksView
		get(t, h, "/api/tasks/"+task+"/pview/links/"+id+"?width=1152&height=720", http.StatusOK, &view)
		if len(view.Positions) != len(view.IDs) || view.Bounds[2] <= 0 || view.Bounds[3] <= 0 {
			t.Fatalf("incomplete local layout for %s", id)
		}
		members := map[string]bool{}
		for _, id := range view.IDs {
			members[id] = true
		}
		for _, p := range view.Positions {
			if !members[p.ID] {
				t.Fatal("unrelated node added")
			}
			delete(members, p.ID)
		}
		if len(members) != 0 {
			t.Fatal("missing or duplicated local position")
		}
		if view.Node.ID != before.Aliases[id][0] {
			t.Fatal("fact or subasset alias changed")
		}
	}
	after := p509View(t, h, task)
	afterRev, _ := store.CurrentRevision(t.Context(), task)
	if rev != afterRev || !reflect.DeepEqual(before, after) {
		t.Fatal("focus endpoint changed overview or ledger")
	}
}
