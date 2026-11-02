package server

import (
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"net/http"
	"testing"
)

func TestP429QueuePanelSameGraphAndConfig(t *testing.T) {
	srv, store := newSettingsServer(t)
	h := srv.Handler()
	var agents agentsReply
	get(t, h, "/api/agents", http.StatusOK, &agents)
	if agents.Params.Queue.Value != overmind.DefaultQueueLimit || agents.Params.Queue.Default != overmind.DefaultQueueLimit {
		t.Fatalf("默认上限: %+v", agents.Params.Queue)
	}
	post(t, h, "POST", "/api/agents/params", `{"queue_limit":9}`, http.StatusOK, &agents)
	if agents.Params.Queue.Value != 9 || !agents.Params.Queue.Overridden || agents.Params.Queue.By != "creator" {
		t.Fatalf("配置未落权威路径: %+v", agents.Params.Queue)
	}
	for _, body := range []string{`{"queue_limit":0}`, `{"queue_limit":1001}`} {
		post(t, h, "POST", "/api/agents/params", body, http.StatusBadRequest, nil)
	}
	var task map[string]string
	post(t, h, "POST", "/api/tasks", `{"title":"水位对账","goals":["目标"],"assets":[{"class":"host","title":"127.0.0.1"}]}`, http.StatusOK, &task)
	tid := task["id"]
	appendEvents(t, store, tid,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep, Status: "running"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep, Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: "s3", NodeType: rings.NodeStep, Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "s3", ToNode: "s1"}},
	)
	var graph struct {
		Queue overmind.QueueWatermark `json:"queue"`
	}
	get(t, h, "/api/tasks/"+tid+"/graph", http.StatusOK, &graph)
	if graph.Queue != (overmind.QueueWatermark{Running: 1, Queued: 1, Waiting: 1, Available: 6, Limit: 9}) {
		t.Fatalf("水位与图不一致: %+v", graph.Queue)
	}
	rev, err := store.CurrentRevision(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, store, tid, rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind", Payload: rings.OvermindRecord{
		Type: rings.OvermindRecAssessment, SessionID: "p429-round", EvaluatedRev: rev, Note: "等待结果", Queue: &graph.Queue,
	}})
	var view struct {
		Rounds []OvermindRound `json:"rounds"`
	}
	get(t, h, "/api/tasks/"+tid+"/overmind", http.StatusOK, &view)
	if len(view.Rounds) != 1 || view.Rounds[0].Queue == nil || *view.Rounds[0].Queue != graph.Queue {
		t.Fatalf("研判卡未保留快照水位: %+v", view.Rounds)
	}
}
