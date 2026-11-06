package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// P4-30 ③(问题清单 #51):复核标记的面板可见性——发现列表与详情
// API 携带 report_excluded 与理由(最新为准),前端据此显示"不纳入报告"。

func TestServerClaimReportExcluded(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"复核标记验证"}`, http.StatusOK, &created)
	task := created["id"]

	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "审计主站", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-a", NodeType: rings.NodeTestClaim, Title: "登录滑块验证码可被脚本解算",
			Status: rings.TestClaimConfirmed, Detail: map[string]any{
				"unit_id": "u1", "step_id": "s1", "impact": "可自动完成滑块验证"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-a", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-a", ToNode: "s1"}},
		// 复核标记(最新一条为准)。
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-a", Type: rings.TestClaimRecReportReview, Reason: "滑块可解只是登录前置条件,无直接危害"}},
	)

	var claims []map[string]any
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &claims)
	if len(claims) != 1 {
		t.Fatalf("应列 1 条发现: %+v", claims)
	}
	if claims[0]["report_excluded"] != true {
		t.Fatalf("列表须携带 report_excluded: %+v", claims[0])
	}
	if reason, _ := claims[0]["report_excluded_reason"].(string); reason != "滑块可解只是登录前置条件,无直接危害" {
		t.Fatalf("列表须携带复核理由: %+v", claims[0]["report_excluded_reason"])
	}

	var detail map[string]any
	get(t, h, "/api/tasks/"+task+"/claims/tc-a", http.StatusOK, &detail)
	claim := detail["claim"].(map[string]any)
	if claim["report_excluded"] != true || claim["report_excluded_reason"] != "滑块可解只是登录前置条件,无直接危害" {
		t.Fatalf("详情须携带复核标记与理由: %+v", claim)
	}
}
