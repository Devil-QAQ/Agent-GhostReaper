package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-51 ②③(问题清单 #73):范围待决卡两键裁决的面板 API 侧 ----
// 加入资产=目标入图转范围+对账唤醒;禁止测试=排除+收口;同卡不可
// 重复裁决;待决卡与红点计数随裁决消。

// p451ServerEnv 面板后端 + 渗透任务(种子 www.example.com)+ 一张
// sc-evilette.com:8080 待决卡 + 冻结等待步骤 s1。
func p451ServerEnv(t *testing.T) (*Server, *httptest.Server, *rings.Store, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	const task = "p451-srv"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		// 范围外目标资产(裁决时按标题复用,免随机入图)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-evilette", NodeType: rings.NodeAsset, Title: "evilette.com:8080",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// 步骤冻结在 scope_wait(带目标标记;意图锚定该目标——P4-69
		// 分族语义下挂等单位按意图覆盖收口,锚定保持"覆盖者终止"口径)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测 evilette",
			Status: "dispatched", Detail: map[string]any{"goal_id": "g1"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a-evilette"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: rings.StepScopeWait,
				Note: "scope_wait evilette.com 8080"}},
		// 待决卡(拦截通道出的卡,带命令上下文)。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: "sc-evilette.com:8080", StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, Intent: "探测 evilette", ToolName: "bash",
			ToolArgs: json.RawMessage(`{"command":"curl http://evilette.com:8080/x"}`),
		}},
	); err != nil {
		t.Fatal(err)
	}
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	gate := swarm.NewLedgerGate(store)
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return srv, ts, store, srv.Handler()
}

func TestP451ScopeDecideAllowEndToEnd(t *testing.T) {
	_, ts, store, h := p451ServerEnv(t)
	ctx := context.Background()

	// 待决区含范围卡(kind=scope,目标可见);红点计数并入待审批。
	var body map[string]any
	get(t, h, "/api/tasks/p451-srv/approvals", http.StatusOK, &body)
	pl, _ := body["pending"].([]any)
	if len(pl) != 1 {
		t.Fatalf("待决区应含 1 张范围卡: %v", pl)
	}
	card, _ := pl[0].(map[string]any)
	if card["kind"] != "scope" || card["target"] != "evilette.com:8080" {
		t.Fatalf("范围卡视图不符: %v", card)
	}
	if n, err := store.ScopePendingCount(ctx, "p451-srv"); err != nil || n != 1 {
		t.Fatalf("红点计数应为 1: %d %v", n, err)
	}

	// 加入资产:瞬间返回(响应携带唤醒数)。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p451-srv/approvals/sc-evilette.com:8080",
		`{"approved":true}`, http.StatusOK, &out)
	if out["approved"] != true || out["target"] != "evilette.com:8080" {
		t.Fatalf("加入资产响应不符: %v", out)
	}
	if woken, _ := out["woken_units"].(float64); woken != 1 {
		t.Fatalf("应唤醒 1 个挂等单位: %v", out)
	}

	// 账本效果:目标入图(来源发现)转范围(roe.updated scope_approved)
	//＋裁决记录 approved;步骤回可领取且带队首标记;卡消(计数归零)。
	assets, _ := store.NodesByType(ctx, "p451-srv", rings.NodeAsset, "")
	found := false
	for _, a := range assets {
		if a.Title == "evilette.com:8080" {
			found = true
		}
	}
	if !found {
		t.Fatalf("目标应入图: %+v", assets)
	}
	roe, _ := store.CurrentRoE(ctx, "p451-srv")
	if len(roe.ScopeApproved) != 1 {
		t.Fatalf("scope_approved 应并入目标资产: %+v", roe)
	}
	step, _ := store.Node(ctx, "p451-srv", "s1")
	if step.Status != "dispatched" {
		t.Fatalf("步骤应回可领取: %+v", step)
	}
	if v, _ := step.Detail["scope_head"].(bool); !v {
		t.Fatalf("应带队首标记: %+v", step.Detail)
	}
	if n, _ := store.ScopePendingCount(ctx, "p451-srv"); n != 0 {
		t.Fatalf("裁决后卡消计数应归零: %d", n)
	}

	// 同卡重复裁决:409。
	post(t, h, http.MethodPost, "/api/tasks/p451-srv/approvals/sc-evilette.com:8080",
		`{"approved":true}`, http.StatusConflict, &out)

	_ = ts
}

func TestP451ScopeDecideDenyEndToEnd(t *testing.T) {
	_, _, store, h := p451ServerEnv(t)
	ctx := context.Background()

	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p451-srv/approvals/sc-evilette.com:8080",
		`{"approved":false}`, http.StatusOK, &out)
	if out["approved"] != false {
		t.Fatalf("禁止测试响应不符: %v", out)
	}
	// 目标入图并转排除态(与 RoE 摘除同一机制);挂等单位收口中止;
	// 卡消。
	roe, _ := store.CurrentRoE(ctx, "p451-srv")
	if len(roe.ExcludedAssets) != 1 {
		t.Fatalf("目标应转排除态: %+v", roe)
	}
	step, _ := store.Node(ctx, "p451-srv", "s1")
	if step.Status != "aborted" {
		t.Fatalf("挂等单位应终止: %+v", step)
	}
	if n, _ := store.ScopePendingCount(ctx, "p451-srv"); n != 0 {
		t.Fatalf("裁决后卡消: %d", n)
	}
}
