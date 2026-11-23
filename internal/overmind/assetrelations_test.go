package overmind

import (
	"context"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssetRelationsExecutorToHarvest(t *testing.T) {
	s := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const task = "relations-executor"
	seedDiscoveredGraph(t, s, task)
	seedAssetWith(t, s, task, "a-other", "other.example.test", map[string]any{"source": "seed"})
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "observation", Name: "submit_candidate_fact", Args: `{"statement":"两个域名解析到同一IP","evidence":"确定性执行样本中的DNS原文：192.0.2.77","new_asset":{"class":"host","title":"192.0.2.77"},"asset_relations":[{"from":"a-seed","to":"$new","kind":"resolves_to"},{"from":"a-other","to":"$new","kind":"resolves_to"}]}`}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "观察已提交，等待主宰确认。", Finish: "stop"}
	})
	model.release()
	defer model.Close()
	provider, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	ex := swarm.NewExecutor(s, provider, filepath.Join(t.TempDir(), "tasks"), swarm.Budget{})
	result, err := ex.RunStep(ctx, task, "s1")
	if err != nil || result.StepStatus != "done" {
		t.Fatalf("executor %+v %v", result, err)
	}
	before, err := s.AssetGraph(ctx, task)
	if err != nil || len(before.Relations) != 0 {
		t.Fatal("unconfirmed relation shown")
	}
	if _, err = Harvest(ctx, s, task); err != nil {
		t.Fatal(err)
	}
	adoptPendingAssets(t, s, task)
	after, err := s.AssetGraph(ctx, task)
	if err != nil || len(after.Assets) != 3 || len(after.Relations) != 2 || len(after.Hierarchy) != 0 {
		t.Fatalf("chain failed %+v %v", after, err)
	}
	if after.Relations[0].To != after.Relations[1].To {
		t.Fatal("shared asset duplicated")
	}
	for _, r := range after.Relations {
		for _, id := range r.FactIDs {
			fact, e := s.Node(ctx, task, id)
			if e != nil || fact.Status != "confirmed" || fact.Detail["transcript_id"] == nil {
				t.Fatal("missing production provenance", fact, e)
			}
		}
	}
}

func TestAssetRelationsHarvestAndPlanning(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	const task = "relation-harvest"
	seedDiscoveredGraph(t, s, task)
	seedAssetWith(t, s, task, "a-second", "other.example.test", map[string]any{"source": "seed"})
	for _, sample := range []struct{ id, from string }{{"f-a", "a-seed"}, {"f-b", "a-second"}} {
		decl := &swarm.NewAssetDecl{Class: "host", Title: "192.0.2.77"}
		_, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u", Payload: rings.NodeAdded{NodeID: sample.id, NodeType: rings.NodeFact, Title: "DNS observation", Status: "candidate", Detail: map[string]any{"evidence": "DNS response:192.0.2.77", "new_asset": map[string]any{"class": decl.Class, "title": decl.Title}, "asset_relations": []rings.AssetRelation{{From: sample.from, To: "$new", Kind: "resolves_to"}}}}}, rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u", Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: sample.id}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Harvest(ctx, s, task); err != nil {
			t.Fatal(err)
		}
	}
	adoptPendingAssets(t, s, task)
	g, err := s.AssetGraph(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Assets) != 3 || len(g.Relations) != 2 || len(g.Hierarchy) != 0 {
		t.Fatalf("shared asset projection %+v", g)
	}
	if g.Relations[0].To != g.Relations[1].To || g.Relations[0].FactIDs[0] == g.Relations[1].FactIDs[0] {
		t.Fatal("shared identity or evidence lost")
	}
	l := &Loop{Store: s, TaskID: task}
	snapshot, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range g.Relations {
		if !strings.Contains(snapshot, r.Summary()) {
			t.Fatal("planning snapshot missing actual relationship")
		}
	}
}
