package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-45(问题清单 #61 ③④):摘除即止损与牧者操作范围 ----
// 摘除端点的止损联动(响应笔数如实)、影响预览端点、面板路径与牧者
// 路径(swarm 资产范围操作)同源对账。

// p445SeedScopeTask 建红队任务 + 种子资产(x/y)+ 覆盖 x 的在跑(s-run)
// 与排队(s-q)步骤、覆盖 y 的排队步骤(s-other);返回任务 ID 与
// x/y 的图上资产 ID(建任务生成的 ID 形态以面板读侧为准)。
func p445SeedScopeTask(t *testing.T, h http.Handler, taskID string) (string, string, string) {
	t.Helper()
	post(t, h, http.MethodPost, "/api/tasks",
		`{"task_type":"redteam","id":"`+taskID+`","goals":["目标"],"assets":[{"class":"domain","title":"x.example.com"},{"class":"domain","title":"y.example.com"}]}`,
		http.StatusOK, nil)
	var tree struct {
		Assets []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+taskID+"/assets", http.StatusOK, &tree)
	var ax, ay string
	for _, a := range tree.Assets {
		switch a.Title {
		case "x.example.com":
			ax = a.ID
		case "y.example.com":
			ay = a.ID
		}
	}
	if ax == "" || ay == "" {
		t.Fatalf("未找到种子资产: %+v", tree.Assets)
	}
	s := storeOfP444(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, taskID,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-run", NodeType: rings.NodeStep, Title: "在跑探测", Status: "running",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-q", NodeType: rings.NodeStep, Title: "排队探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-other", NodeType: rings.NodeStep, Title: "别处探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-run", ToNode: ax}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-q", ToNode: ax}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-other", ToNode: ay}},
	); err != nil {
		t.Fatal(err)
	}
	return taskID, ax, ay
}

func TestP445RoEAssetScopeStopLoss(t *testing.T) {
	h, store := newP444Server(t)
	const task = "p445-panel"
	_, ax, _ := p445SeedScopeTask(t, h, task)
	ctx := context.Background()

	// 影响预览:1 在跑 + 1 排队。
	var impact struct {
		Units  []map[string]any `json:"units"`
		Queued []map[string]any `json:"queued"`
	}
	get(t, h, "/api/tasks/"+task+"/roe/assets/impact?assets="+ax, http.StatusOK, &impact)
	if len(impact.Units) != 1 || len(impact.Queued) != 1 {
		t.Fatalf("影响预览应 1 在跑 1 排队: %+v %+v", impact.Units, impact.Queued)
	}
	if impact.Units[0]["step_id"] != "s-run" || impact.Queued[0]["step_id"] != "s-q" {
		t.Fatalf("命中步骤不符: %+v %+v", impact.Units, impact.Queued)
	}
	// 参数缺失 400 人话。
	get(t, h, "/api/tasks/"+task+"/roe/assets/impact?assets=", http.StatusBadRequest, nil)

	// 面板摘除:排队步骤立即取消(响应笔数如实;在跑步骤经舵杆 kill,
	// 测试环境无执行器钩子——如实不计入,步骤由引擎侧活体验收实证)。
	var res struct {
		ExcludedAssets []string `json:"excluded_assets"`
		StoppedUnits   int      `json:"stopped_units"`
		StoppedSteps   int      `json:"stopped_steps"`
	}
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe/assets",
		`{"excluded":["`+ax+`"]}`, http.StatusOK, &res)
	if len(res.ExcludedAssets) != 1 || res.ExcludedAssets[0] != ax {
		t.Fatalf("摘除清单不符: %+v", res.ExcludedAssets)
	}
	if res.StoppedSteps != 1 {
		t.Fatalf("应取消 1 个排队步骤: %+v", res)
	}
	if s, err := store.Node(ctx, task, "s-q"); err != nil || s.Status != "aborted" {
		t.Fatalf("s-q 应已中止: %+v %v", s, err)
	}
	if s, err := store.Node(ctx, task, "s-other"); err != nil || s.Status != "dispatched" {
		t.Fatalf("无关步骤不应被动: %+v %v", s, err)
	}
	// 摘除留痕(Note=范围摘除)入账本。
	evs, _ := store.Events(ctx, task, 0, 0)
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == "s-q" &&
			p.Status == "aborted" && p.Note == "范围摘除" {
			found = true
		}
	}
	if !found {
		t.Fatal("账本应留范围摘除取消痕")
	}
}

// TestP445PanelAndShepherdSameSource 纯面板路径与牧者路径同源对账:
// 同构两任务各走一路摘除,roe 状态与步骤终态一致(同一账本事件语义、
// 同一止损本体)。
func TestP445PanelAndShepherdSameSource(t *testing.T) {
	h, store := newP444Server(t)
	const tPanel, tShep = "p445-same-panel", "p445-same-shep"
	_, axPanel, _ := p445SeedScopeTask(t, h, tPanel)
	_, axShep, _ := p445SeedScopeTask(t, h, tShep)
	ctx := context.Background()

	// 面板路径:整体快照提交摘除 x。
	post(t, h, http.MethodPost, "/api/tasks/"+tPanel+"/roe/assets",
		`{"excluded":["`+axPanel+`"]}`, http.StatusOK, nil)
	// 牧者路径:swarm 资产范围操作(增量摘除同一资产)。
	if _, err := swarm.ExcludeAssets(ctx, store, nil, tShep, []string{axShep}, "shepherd"); err != nil {
		t.Fatal(err)
	}

	roeP, _ := store.CurrentRoE(ctx, tPanel)
	roeS, _ := store.CurrentRoE(ctx, tShep)
	// 两任务资产 ID 各自生成,按语义对账:各摘除了自己的 x、且都只摘除
	// 这一个(同一 roe.updated 账本状态形态)。
	if len(roeP.ExcludedAssets) != 1 || roeP.ExcludedAssets[0] != axPanel {
		t.Fatalf("面板路径摘除清单不符: %+v", roeP.ExcludedAssets)
	}
	if len(roeS.ExcludedAssets) != 1 || roeS.ExcludedAssets[0] != axShep {
		t.Fatalf("牧者路径摘除清单不符: %+v", roeS.ExcludedAssets)
	}
	if len(roeP.ScopeApproved) != 0 || len(roeS.ScopeApproved) != 0 {
		t.Fatalf("两路放行清单都应保持现值: %+v %+v", roeP.ScopeApproved, roeS.ScopeApproved)
	}
	for _, id := range []string{"s-q", "s-other"} {
		sp, errP := store.Node(ctx, tPanel, id)
		ss, errS := store.Node(ctx, tShep, id)
		if errP != nil || errS != nil || sp.Status != ss.Status {
			t.Fatalf("步骤 %s 两路终态应一致: 面板 %s 牧者 %s", id, sp.Status, ss.Status)
		}
	}
	if sp, _ := store.Node(ctx, tPanel, "s-q"); sp.Status != "aborted" {
		t.Fatalf("两路 s-q 都应已中止: %+v", sp)
	}
	if ss, _ := store.Node(ctx, tShep, "s-other"); ss.Status != "dispatched" {
		t.Fatalf("两路 s-other 都不应被动: %+v", ss)
	}
}
