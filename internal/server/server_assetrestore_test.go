package server

import (
	"encoding/json"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"net/http"
	"sync"
	"testing"
)

func TestAssetRestorePersistsBehindExcludedHostAndMergesHistory(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "asset-restore"
	events := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Title: "恢复测试", Detail: map[string]any{"task_type": "pentest"}}}}
	for _, a := range []struct{ id, cl, title string }{{"ip", "host", "192.0.2.1"}, {"bare", "service", "192.0.2.1:2222"}, {"url", "service", "http://192.0.2.1:2222"}, {"path", "service", "http://192.0.2.1:2222/sssss"}, {"other", "service", "http://192.0.2.1:3333"}} {
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: a.id, NodeType: rings.NodeAsset, AssetClass: rings.AssetClass(a.cl), Title: a.title, Status: "active", Detail: map[string]any{"source": "manual"}}})
	}
	events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: "f", NodeType: rings.NodeFact, Title: "原始证据", Status: "confirmed", Detail: map[string]any{"evidence": "原文"}}}, rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f", ToNode: "path"}})
	if _, err := srv.Store.Append(t.Context(), task, events...); err != nil {
		t.Fatal(err)
	}
	graph, err := srv.Store.AssetGraph(t.Context(), task)
	if err != nil || len(graph.Assets) != 3 || graph.ResourceAliases["path"] != "bare" {
		t.Fatal(graph, err)
	}
	for _, a := range graph.Assets {
		if a.ID == "bare" && a.FactCount != 1 {
			t.Fatal("alias lost evidence", a)
		}
	}
	for _, body := range []string{`{"asset_id":"ip","include":false}`, `{"asset_id":"bare","include":true}`} {
		code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/roe/assets", body)
		if code != http.StatusOK {
			t.Fatal(code, out)
		}
	}
	graph, err = srv.Store.AssetGraph(t.Context(), task)
	if err != nil || len(graph.Assets) != 1 || graph.Assets[0].ID != "bare" {
		t.Fatal("restored endpoint disappeared", graph, err)
	}
	if err := overmind.SeedGoal(t.Context(), srv.Store, task, "g", "核对恢复", nil); err != nil {
		t.Fatal(err)
	}
	if err := overmind.DispatchStep(t.Context(), srv.Store, task, "restored-step", "继续核对恢复的端口", overmind.DispatchOptions{GoalID: "g", ActionLevel: "只读探测", CoversAssets: []string{"bare"}}); err != nil {
		t.Fatal("restored endpoint cannot be planned", err)
	}
	code, body := callJSON(t, mux, "GET", "/api/tasks/"+task+"/roe", "")
	if code != 200 {
		t.Fatal(body)
	}
	var view roeView
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.ScopeTargets) != 3 || len(view.ExcludedAssets) != 2 {
		t.Fatal("scope controls lost hidden targets", view)
	}
	code, out := callJSON(t, mux, "POST", "/api/tasks/"+task+"/roe/assets", `{"asset_id":"url","include":false}`)
	if code != 200 {
		t.Fatal(out)
	}
	graph, _ = srv.Store.AssetGraph(t.Context(), task)
	if len(graph.Assets) != 0 {
		t.Fatal("later withdrawal did not revoke rejoin", graph)
	}
	code, out = callJSON(t, mux, "POST", "/api/tasks/"+task+"/roe/assets", `{"asset_id":"path","include":true}`)
	if code != 200 {
		t.Fatal(out)
	}
	graph, _ = srv.Store.AssetGraph(t.Context(), task)
	if len(graph.Assets) != 1 || graph.Assets[0].ID != "bare" {
		t.Fatal(graph)
	}
	// Reading again models page reload: the inventory is derived from durable RoE.
	graph, _ = srv.Store.AssetGraph(t.Context(), task)
	if len(graph.Assets) != 1 {
		t.Fatal(graph)
	}
}

func TestConcurrentAssetRestoresDoNotOverwriteEachOther(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "concurrent-restores"
	events := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created", Detail: map[string]any{"task_type": "pentest"}}}}
	for _, a := range []struct {
		id    string
		cl    rings.AssetClass
		title string
	}{{"ip", rings.AssetHost, "192.0.2.1"}, {"one", rings.AssetService, "192.0.2.1:2222"}, {"two", rings.AssetService, "192.0.2.1:3333"}} {
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{NodeID: a.id, NodeType: rings.NodeAsset, AssetClass: a.cl, Title: a.title, Status: "active", Detail: map[string]any{"source": "manual"}}})
	}
	events = append(events, rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{ExcludedAssets: []string{"ip"}}})
	if _, err := srv.Store.Append(t.Context(), task, events...); err != nil {
		t.Fatal(err)
	}
	ids := []string{"one", "two"}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/roe/assets", `{"asset_id":"`+id+`","include":true}`)
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code == 409 {
			code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/roe/assets", `{"asset_id":"`+ids[i]+`","include":true}`)
		}
		if code != 200 {
			t.Fatal(codes)
		}
	}
	graph, err := srv.Store.AssetGraph(t.Context(), task)
	if err != nil || len(graph.Assets) != 2 {
		t.Fatal("scope operation lost a concurrent rejoin", graph, err)
	}
}
