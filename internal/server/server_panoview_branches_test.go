package server

import (
	"reflect"
	"testing"

	"ghostreaper/internal/overmind"
)

func TestP509BranchSelectionAndSharedNodes(t *testing.T) {
	h, store := p509Server(t)
	task := p509Seed(t, h, store)
	for _, row := range []struct {
		id   string
		refs []string
	}{
		{"s-center", []string{"f-1", "s2", "tc-1"}},
		{"s-left", []string{"s-center"}},
		{"s-right", []string{"s-center"}},
	} {
		if err := overmind.DispatchStep(t.Context(), store, task, row.id, "分支核对", overmind.DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{"a-two"}, SourceRefs: row.refs}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/tasks/" + task + "/pview/links/"
	var full panoLinksView
	get(t, h, base+"s-center", 200, &full)
	if len(full.Sources) != 3 || len(full.Targets) != 2 {
		t.Fatalf("direct branch choices missing: %v / %v", full.Sources, full.Targets)
	}
	var throughClaim panoLinksView
	get(t, h, base+"tc-1", 200, &throughClaim)
	for _, edge := range throughClaim.Edges {
		if edge.From == "s2" && edge.To == "s-center" {
			t.Fatal("a shortcut bypassing the focused claim was included")
		}
	}
	rev, _ := store.CurrentRevision(t.Context(), task)
	for _, query := range []string{"?source=tc-1", "?target=s-left", "?source=tc-1&target=s-left"} {
		var view panoLinksView
		get(t, h, base+"s-center"+query, 200, &view)
		if !reflect.DeepEqual(view.Sources, full.Sources) || !reflect.DeepEqual(view.Targets, full.Targets) {
			t.Fatal("filtering removed the other branch choices")
		}
		if len(view.Positions) != len(view.IDs) {
			t.Fatal("branch was not laid out as a complete view")
		}
		seen := map[string]bool{}
		for _, id := range view.IDs {
			if seen[id] {
				t.Fatal("shared node copied")
			}
			seen[id] = true
		}
		for _, edge := range view.Edges {
			if !seen[edge.From] || !seen[edge.To] {
				t.Fatal("branch contains an endpoint outside its view")
			}
			if query != "?target=s-left" && edge.To == "s-center" && edge.From != "tc-1" {
				t.Fatal("unselected direct source leaked through a shared ancestor")
			}
			if query != "?source=tc-1" && edge.From == "s-center" && edge.To != "s-left" {
				t.Fatal("unselected downstream branch is visible")
			}
		}
		if !seen["s-center"] || (query != "?target=s-left" && (!seen["tc-1"] || !seen["s2"])) {
			t.Fatal("focus or selected source ancestors lost")
		}
		if query != "?source=tc-1" && seen["s-right"] {
			t.Fatal("unselected leaf pulled into branch")
		}
	}
	// tc-1→s-center and s2→s-center share s2 upstream. Choosing tc-1
	// retains s2→tc-1 as a real production input, excluding the direct shortcut.
	var branch panoLinksView
	get(t, h, base+"s2?target=tc-1", 200, &branch)
	for _, edge := range branch.Edges {
		if edge.From == "s2" && edge.To != "tc-1" {
			t.Fatal("direct shortcut survived downstream selection")
		}
	}
	get(t, h, base+"s-center?source=missing", 400, nil)
	get(t, h, base+"s-center?target=s2", 400, nil)
	after, _ := store.CurrentRevision(t.Context(), task)
	if rev != after {
		t.Fatal("selecting a branch modified the ledger")
	}
}
