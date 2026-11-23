package server

import (
	"net/http"
	"testing"
)

func TestHypothesisAPIHasNoRoutes(t *testing.T) {
	h, task, _ := graphSetup(t)
	h.(*http.ServeMux).HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("app shell")) })
	for _, path := range []string{"/hypotheses", "/hypotheses/h-1", "/missing-api"} {
		get(t, h, "/api/tasks/"+task+path, http.StatusNotFound, nil)
	}
	var assets map[string]any
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &assets)
	for _, v := range assets["assets"].([]any) {
		if _, ok := v.(map[string]any)["hypothesis_count"]; ok {
			t.Fatal("retired asset counter remains")
		}
	}
	var graph map[string]any
	get(t, h, "/api/tasks/"+task+"/graph", http.StatusOK, &graph)
	for _, v := range graph["nodes"].([]any) {
		n := v.(map[string]any)
		if n["type"] == "hypothesis" {
			t.Fatal("retired node in graph")
		}
		if _, ok := n["hypothesis_count"]; ok {
			t.Fatal("retired graph counter remains")
		}
	}
}
