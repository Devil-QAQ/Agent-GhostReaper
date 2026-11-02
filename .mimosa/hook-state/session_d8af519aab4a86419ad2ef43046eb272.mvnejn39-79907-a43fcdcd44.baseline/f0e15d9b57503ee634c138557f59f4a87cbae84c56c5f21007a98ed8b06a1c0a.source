package server

// P4-12(问题清单 #21)链路 UI 数据侧:统一图投影与资产详情载荷带发现型
// 资产的出生血缘(birth_step/birth_unit/birth_fact)——链路从成果走通到
// "谁发现的它"的数据来源;种子/手动资产零值(如实,不编造)。

import (
	"context"
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// TestGraphBirthFields 图投影的出生字段:发现型资产带三锚,种子资产不带。
func TestGraphBirthFields(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	ctx := context.Background()
	const task = "p412-graph"

	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "链路补全"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: "a-seed", NodeType: rings.NodeAsset,
				Title: "example.com", Status: "active", AssetClass: rings.AssetDomain,
				Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "枚举子域", Status: "done", Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u5",
			Payload: rings.NodeAdded{NodeID: "f-disc", NodeType: rings.NodeFact,
				Title: "发现子域 admin.example.com", Status: "confirmed",
				Detail: map[string]any{"evidence": "DNS 应答", "unit_id": "u5"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u5",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-disc"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "a-born", NodeType: rings.NodeAsset,
				Title: "admin.example.com", Status: "active", AssetClass: rings.AssetSubdomain,
				Detail: map[string]any{"source": rings.AssetSourceDiscovered,
					"unit_id": "u5", "step_id": "s1", "discovered_fact": "f-disc"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "a-born"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f-disc", ToNode: "a-born"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/graph", "")
	if code != http.StatusOK {
		t.Fatalf("图投影应 200: %d", code)
	}
	nodes, _ := out["nodes"].([]any)
	birth := map[string]map[string]any{}
	for _, raw := range nodes {
		n, _ := raw.(map[string]any)
		birth[n["id"].(string)] = n
	}
	b, ok := birth["a-born"]
	if !ok {
		t.Fatalf("发现型资产应在图上: %v", out)
	}
	if b["source"] != "discovered" || b["birth_step"] != "s1" || b["birth_unit"] != "u5" || b["birth_fact"] != "f-disc" {
		t.Fatalf("出生三锚应随图投影下发: %v", b)
	}
	s, ok := birth["a-seed"]
	if !ok {
		t.Fatalf("种子资产应在图上: %v", out)
	}
	if s["source"] != "seed" {
		t.Fatalf("种子资产来源归一: %v", s)
	}
	if _, has := s["birth_step"]; has {
		t.Fatalf("种子资产无出生三锚(零值不编造): %v", s)
	}
}

// TestAssetDetailBirthFields 资产详情端点的出生字段(资产抽屉数据源)。
func TestAssetDetailBirthFields(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	ctx := context.Background()
	const task = "p412-asset"

	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "资产出生"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "a-born", NodeType: rings.NodeAsset,
				Title: "api.example.com", Status: "active", AssetClass: rings.AssetAPI,
				Detail: map[string]any{"source": rings.AssetSourceDiscovered,
					"unit_id": "u3", "step_id": "s2", "discovered_fact": "f-api", "transcript_id": "swarm-u3"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/assets/a-born", "")
	if code != http.StatusOK {
		t.Fatalf("资产详情应 200: %d", code)
	}
	a, _ := out["asset"].(map[string]any)
	if a == nil || a["source"] != "discovered" {
		t.Fatalf("资产来源应下发: %v", out)
	}
	if a["birth_unit"] != "u3" || a["birth_step"] != "s2" || a["birth_fact"] != "f-api" {
		t.Fatalf("出生三锚应随资产详情下发: %v", a)
	}
}
