package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// Opt-in, fixed synthetic workloads through the formal read handlers. This
// measures backend reads/layout only, never browser paint, FPS or historical
// ledger equivalence. No scheduler, model or target service is exercised.
func TestReviewScaleReadHandlers(t *testing.T) {
	out := os.Getenv("GHOSTREAPER_SCALE_EVIDENCE")
	if out == "" {
		t.Skip("opt-in scale evidence")
	}
	type sample struct {
		Dataset, Route string
		ColdMS         float64
		HotMS          []float64
		Bytes          int
	}
	var results []sample
	for _, tc := range []struct {
		name                  string
		assets, steps, claims int
	}{
		{"synthetic-4840-cards", 40, 4000, 800},
		{"synthetic-5000-assets", 5000, 200, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := p509Server(t)
			task := "review-" + tc.name
			var events []rings.Event
			add := func(p rings.NodeAdded) {
				events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "review-fixture", Payload: p})
			}
			edge := func(kind rings.EdgeType, from, to string) {
				events = append(events, rings.Event{Kind: rings.EventEdgeAdded, Actor: "review-fixture", Payload: rings.EdgeAdded{EdgeType: kind, FromNode: from, ToNode: to}})
			}
			events = append(events, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "review-fixture", Payload: rings.TaskLifecycle{Action: "created", Title: tc.name}})
			add(rings.NodeAdded{NodeID: "g", NodeType: rings.NodeGoal, Title: "人工规模材料", Status: "active"})
			for i := 0; i < tc.assets; i++ {
				add(rings.NodeAdded{NodeID: fmt.Sprintf("a%d", i), NodeType: rings.NodeAsset, AssetClass: rings.AssetDomain, Title: fmt.Sprintf("a%d.example.test", i), Status: "active", Detail: map[string]any{"source": rings.AssetSourceSeed}})
			}
			for i := 0; i < tc.steps; i++ {
				s, f, a := fmt.Sprintf("s%d", i), fmt.Sprintf("f%d", i), fmt.Sprintf("a%d", i%tc.assets)
				add(rings.NodeAdded{NodeID: s, NodeType: rings.NodeStep, Title: "核对人工材料", Status: "done", Detail: map[string]any{"goal_id": "g"}})
				add(rings.NodeAdded{NodeID: f, NodeType: rings.NodeFact, Title: "人工观察", Status: "confirmed", Detail: map[string]any{"evidence": "固定人工原文，非真实发现"}})
				edge(rings.EdgeCovers, s, a)
				edge(rings.EdgeProduces, s, f)
				edge(rings.EdgeAbout, f, a)
				if i > 0 && i%5 != 0 {
					edge(rings.EdgeSourcedFrom, s, fmt.Sprintf("f%d", i-1))
				}
				if i < tc.claims {
					c := fmt.Sprintf("tc%d", i)
					add(rings.NodeAdded{NodeID: c, NodeType: rings.NodeTestClaim, Title: "人工判断样本", Status: rings.TestClaimConfirmed, Detail: map[string]any{"step_id": s}})
					edge(rings.EdgeDerivedFrom, c, f)
					edge(rings.EdgeAbout, c, a)
				}
			}
			for start := 0; start < len(events); start += 500 {
				if _, err := store.Append(t.Context(), task, events[start:min(start+500, len(events))]...); err != nil {
					t.Fatal(err)
				}
			}
			assets, _ := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
			if len(assets) != tc.assets {
				t.Fatalf("fixture lost assets: %d", len(assets))
			}
			base := "/api/tasks/" + task
			for _, route := range []string{"/workbench", "/watermark", "/assets", "/pview/skeleton", "/pview/links/tc0", "/pview/details/s0", "/graph"} {
				row := sample{Dataset: tc.name, Route: route}
				for i := 0; i < 6; i++ {
					w := httptest.NewRecorder()
					start := time.Now()
					h.ServeHTTP(w, httptest.NewRequest("GET", base+route, nil))
					ms := float64(time.Since(start).Nanoseconds()) / 1e6
					if w.Code != 200 {
						t.Fatalf("%s returned %d", route, w.Code)
					}
					if !json.Valid(w.Body.Bytes()) {
						t.Fatalf("%s returned invalid JSON", route)
					}
					if i == 0 {
						row.ColdMS = ms
						row.Bytes = w.Body.Len()
					} else {
						row.HotMS = append(row.HotMS, ms)
					}
					if route == "/pview/skeleton" {
						var v panoSkeletonView
						if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
							t.Fatal(err)
						}
						if len(v.Nodes) != tc.assets+tc.steps+tc.claims {
							t.Fatalf("skeleton changed workload: got %d want %d", len(v.Nodes), tc.assets+tc.steps+tc.claims)
						}
					}
				}
				results = append(results, row)
			}
		})
	}
	b, err := json.MarshalIndent(map[string]any{"scope": "synthetic backend first-handler-read and five warm reads; not OS cold cache, historical-ledger replay, browser rendering, interaction or final scale acceptance", "samples": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, b, 0600); err != nil {
		t.Fatal(err)
	}
}
