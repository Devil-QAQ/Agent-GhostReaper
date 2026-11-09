package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-54:面板 RoE 目标管理修改/删除(与牧者同源)的面板 API 侧 ----

// p454ServerEnv 面板后端 + 任务 + 两目标两排队步骤。
func p454ServerEnv(t *testing.T) (*httptest.Server, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	const task = "p454-srv"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "保留方向", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g2", NodeType: rings.NodeGoal, Title: "要删的方向", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "保留探测", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "删除方向探测", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g2", "action_level": "只读探测"}}},
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
	return ts, store
}

// TestP454GoalUpdateEndpoint 面板修改目标:标题+完成条件重新冻结;列表
// 读回新值;非法谓词 400。
func TestP454GoalUpdateEndpoint(t *testing.T) {
	ts, store := p454ServerEnv(t)
	ctx := context.Background()

	body := `{"title":"改后的方向","predicate":{"op":"all_of","conditions":[{"kind":"fact_confirmed","asset_id":"a1","min":1}]}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/tasks/p454-srv/goals/g1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("修改目标应 200: %d", res.StatusCode)
	}
	g, _ := store.Node(ctx, "p454-srv", "g1")
	if g.Title != "改后的方向" {
		t.Fatalf("标题应已改: %q", g.Title)
	}
	if _, ok := g.Detail["predicate"].(map[string]any); !ok {
		t.Fatalf("谓词应已重新冻结: %+v", g.Detail)
	}
	// 列表读回新值。
	list, err := http.Get(ts.URL + "/api/tasks/p454-srv/goals")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var goals []map[string]any
	if err := json.NewDecoder(list.Body).Decode(&goals); err != nil {
		t.Fatal(err)
	}
	for _, gv := range goals {
		if gv["id"] == "g1" && gv["title"] != "改后的方向" {
			t.Fatalf("列表应读回新标题: %+v", gv)
		}
	}
	// 非法谓词:400。
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/tasks/p454-srv/goals/g1",
		strings.NewReader(`{"predicate":{"op":"bad"}}`))
	req2.Header.Set("Content-Type", "application/json")
	res2, _ := http.DefaultClient.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法谓词应 400: %d", res2.StatusCode)
	}
}

// TestP454GoalDeleteEndpoint 面板删除目标:影响预览计数 → 删除(排队
// 取消留痕、目标已删除、其他方向不动);重复删除 400。
func TestP454GoalDeleteEndpoint(t *testing.T) {
	ts, store := p454ServerEnv(t)
	ctx := context.Background()

	imp, err := http.Get(ts.URL + "/api/tasks/p454-srv/goals/g2/impact")
	if err != nil {
		t.Fatal(err)
	}
	defer imp.Body.Close()
	var impact map[string]any
	if err := json.NewDecoder(imp.Body).Decode(&impact); err != nil {
		t.Fatal(err)
	}
	if n, _ := impact["queued"].(float64); n != 1 {
		t.Fatalf("影响预览应计 1 条排队: %+v", impact)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/tasks/p454-srv/goals/g2/delete",
		strings.NewReader(`{"reason":"不要了"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200: %d %v", res.StatusCode, out)
	}
	if out["status"] != overmind.GoalStatusDiscarded {
		t.Fatalf("删除后状态应为已删除: %+v", out)
	}
	if n, _ := out["aborted_steps"].(float64); n != 1 {
		t.Fatalf("删除应取消 1 条排队: %+v", out)
	}
	g2, _ := store.Node(ctx, "p454-srv", "g2")
	if g2.Status != overmind.GoalStatusDiscarded {
		t.Fatalf("目标应转已删除: %+v", g2)
	}
	if st, _ := store.Node(ctx, "p454-srv", "s2"); st.Status != "cancelled" {
		t.Fatalf("该方向排队步骤应取消: %+v", st)
	}
	if st, _ := store.Node(ctx, "p454-srv", "s1"); st.Status != "dispatched" {
		t.Fatalf("其他方向不应被动: %+v", st)
	}
	// 重复删除:400。
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/tasks/p454-srv/goals/g2/delete", nil)
	res2, _ := http.DefaultClient.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("重复删除应 400: %d", res2.StatusCode)
	}
}

// TestP454GoalFocusFlagInList 聚焦标记的读侧可见面:经牧者聚焦后面板
// 列表读回 focused 标记(双通道互见)。
func TestP454GoalFocusFlagInList(t *testing.T) {
	ts, store := p454ServerEnv(t)
	ctx := context.Background()
	if _, err := overmind.FocusGoal(ctx, store, nil, "p454-srv", "g1", "shepherd"); err != nil {
		t.Fatal(err)
	}
	list, err := http.Get(ts.URL + "/api/tasks/p454-srv/goals")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	var goals []map[string]any
	if err := json.NewDecoder(list.Body).Decode(&goals); err != nil {
		t.Fatal(err)
	}
	for _, gv := range goals {
		if gv["id"] == "g1" && gv["focused"] != true {
			t.Fatalf("聚焦目标应带 focused 标记: %+v", gv)
		}
		if gv["id"] == "g2" && gv["focused"] == true {
			t.Fatalf("未聚焦目标不应带标记: %+v", gv)
		}
	}
}
