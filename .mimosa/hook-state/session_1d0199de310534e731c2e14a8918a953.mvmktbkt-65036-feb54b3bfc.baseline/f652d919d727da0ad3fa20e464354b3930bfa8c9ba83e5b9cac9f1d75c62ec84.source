package server

// P4-11 ② 研判卡在途全景表:按研判所读账本版本回放派生——行数与该时刻
// 在跑+排队步骤数对账相等、等前置条目带标注、排序与领取次序同源
// (overmind.StepLaunchLess)、快照不随后续事件漂移(版本号快照)。

import (
	"context"
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// TestOvermindPanorama ②:两轮研判的全景回放——轮次快照定格在各自
// 研判版本,高优先级在前、同级按创建版本、等前置如实标注、目标归属
// 带 covers 资产标题、已完成步骤不进表。
func TestOvermindPanorama(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	ctx := context.Background()
	const task = "p411-panorama"

	revs := func(ns ...int64) int64 { return ns[len(ns)-1] }
	// 批一:任务 + 资产 + 三条在途步骤(s4 已完成,不进表)+ 边。
	b1, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "全景回放"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator",
			Payload: rings.NodeAdded{NodeID: "a1", NodeType: rings.NodeAsset, Title: "演示站", Status: "active", AssetClass: rings.AssetService}},
		// s1:normal 探索,covers a1,无前置。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "对 10.0.0.5:8080 做目录枚举,找未授权入口", Status: "dispatched",
				Detail: map[string]any{"priority": "normal", "action_level": "只读探测"}}},
		// s2:normal,前置 s3(未完成 → 等前置)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "用 s3 结果核对后台路径", Status: "dispatched",
				Detail: map[string]any{"priority": "normal", "action_level": "只读探测"}}},
		// s3:high 反证(创建版本晚于 s1/s2,凭优先级排最前)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s3", NodeType: rings.NodeStep,
				Title: "反证(第 1 轮): 未授权访问", Status: "dispatched",
				Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-1",
					"round": 1, "priority": "high", "action_level": "只读探测"}}},
		// s4:已完成——该时刻不在途。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s4", NodeType: rings.NodeStep,
				Title: "指纹识别", Status: "dispatched",
				Detail: map[string]any{"priority": "normal", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "s2", ToNode: "s3"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s3", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s4", Status: "done"}},
	)
	if err != nil {
		t.Fatalf("铺批一失败: %v", err)
	}
	// 轮一:研判所读版本 = 批一末(s1 派发未领、s2 等前置、s3 在跑、s4 已完)。
	r1, err := store.Append(ctx, task, rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
		Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-p411-d1",
			EvaluatedRev: revs(b1...), QAccess: "首轮"}})
	if err != nil {
		t.Fatalf("铺轮一失败: %v", err)
	}
	// 批二:s1 被领取(轮一快照不得漂移为 running);s3 反证收完 done
	// → s2 前置完成不再等。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s3", Status: "done"}},
	); err != nil {
		t.Fatalf("铺批二失败: %v", err)
	}
	// 轮二:研判所读版本 = 批二末。
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
		Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "overmind-p411-d2",
			EvaluatedRev: revs(r1...) + 2, Note: "反证已收,接力在途"}}); err != nil {
		t.Fatalf("铺轮二失败: %v", err)
	}

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != http.StatusOK {
		t.Fatalf("主宰状态应 200: %d", code)
	}
	rounds, _ := out["rounds"].([]any)
	if len(rounds) != 2 {
		t.Fatalf("应恰 2 轮研判: %v", rounds)
	}
	rows := func(r int) []map[string]any {
		var out []map[string]any
		pan, _ := rounds[r].(map[string]any)["panorama"].([]any)
		for _, p := range pan {
			m, _ := p.(map[string]any)
			out = append(out, m)
		}
		return out
	}

	// 轮一:3 行(s1 排队、s2 排队·等前置、s3 执行中),次序 = 领取次序
	// (high 反证 s3 → 同级创建版本 s1 → s2);s4 已完成不进表。
	r1rows := rows(0)
	if len(r1rows) != 3 {
		t.Fatalf("轮一全景应 3 行(在跑 1 + 排队 2),实际 %d: %v", len(r1rows), r1rows)
	}
	wantOrder := []string{"s3", "s1", "s2"}
	for i, w := range wantOrder {
		if r1rows[i]["id"] != w {
			t.Fatalf("轮一次序应与领取次序一致 %v: %v", wantOrder, r1rows)
		}
	}
	if r1rows[0]["status"] != "running" || r1rows[1]["status"] != "dispatched" {
		t.Fatalf("轮一 s3 应 running、s1 应 dispatched(快照定格): %v", r1rows)
	}
	if _, has := r1rows[1]["waiting"]; has {
		t.Fatalf("轮一 s1 不应带等前置标注: %v", r1rows[1])
	}
	if r1rows[2]["waiting"] != true {
		t.Fatalf("轮一 s2 应带等前置标注: %v", r1rows[2])
	}
	// 目标归属:covers 资产带标题。
	assets, _ := r1rows[1]["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("轮一 s1 应带 1 个目标归属资产: %v", r1rows[1])
	}
	a, _ := assets[0].(map[string]any)
	if a["id"] != "a1" || a["title"] != "演示站" {
		t.Fatalf("s1 目标归属应为 a1 演示站: %v", a)
	}

	// 轮二:s1 已在跑、s3 已收完不进表;s2 前置已完成,不再等前置。
	r2rows := rows(1)
	if len(r2rows) != 2 {
		t.Fatalf("轮二全景应 2 行(s1 在跑 + s2 排队),实际 %d: %v", len(r2rows), r2rows)
	}
	if r2rows[0]["id"] != "s1" || r2rows[0]["status"] != "running" {
		t.Fatalf("轮二 s1 应 running: %v", r2rows)
	}
	if r2rows[1]["id"] != "s2" {
		t.Fatalf("轮二第二行应为 s2: %v", r2rows)
	}
	if _, has := r2rows[1]["waiting"]; has {
		t.Fatalf("轮二 s2 前置已完成,不应再带等前置标注: %v", r2rows[1])
	}
}

// TestOvermindPanoramaEmpty 无在途步骤的研判轮:全景为空数组(面板呈
// 空态行),不缺字段。
func TestOvermindPanoramaEmpty(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	ctx := context.Background()
	const task = "p411-empty"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "空全景"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "指纹识别", Status: "dispatched",
				Detail: map[string]any{"priority": "normal", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}},
		rings.Event{Kind: rings.EventOvermindRecord, Actor: "overmind",
			Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, SessionID: "d1", EvaluatedRev: 4, Note: "全部收口"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != http.StatusOK {
		t.Fatalf("主宰状态应 200: %d", code)
	}
	rounds, _ := out["rounds"].([]any)
	if len(rounds) != 1 {
		t.Fatalf("应恰 1 轮研判: %v", rounds)
	}
	pan, ok := rounds[0].(map[string]any)["panorama"].([]any)
	if !ok || len(pan) != 0 {
		t.Fatalf("无在途步骤时全景应为空数组: %v", rounds[0])
	}
}
