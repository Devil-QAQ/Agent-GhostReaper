package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-09b 面板返工配套 API ----
// ① GET /api/config(设置最简只读视图);
// ② GET /api/tasks/{id}/assets/{nid}(资产树 Sheet 抽屉详情)。

// TestServerConfigAPI 设置页读 API:并发/预算取各包默认值,LLM 状态由
// 装配方(main.go)填充,零值如实呈现"未注入"。
func TestServerConfigAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	var cfg ConfigView
	get(t, h, "/api/config", http.StatusOK, &cfg)
	if cfg.Swarm.MaxConcurrentUnits != 5 {
		t.Fatalf("虫群并发默认值 = %d, want 5", cfg.Swarm.MaxConcurrentUnits)
	}
	if cfg.LLM.Configured || cfg.LLM.Model != "" {
		t.Fatalf("零值装配应如实呈现未注入: %+v", cfg.LLM)
	}

	// 装配方注入后如实回显(main.go 同构)。
	srv.Config.LLM.Configured = true
	srv.Config.LLM.Model = "glm-5.3"
	srv.Config.LLM.Format = "anthropic"
	get(t, h, "/api/config", http.StatusOK, &cfg)
	if !cfg.LLM.Configured || cfg.LLM.Model != "glm-5.3" || cfg.LLM.Format != "anthropic" {
		t.Fatalf("注入后回显不符: %+v", cfg.LLM)
	}
}

// TestServerAssetDetail 资产详情:节点字段 + about 边反查挂载的
// 事实/假设/发现;错误路径(节点不存在 404、非资产节点 400)。
func TestServerAssetDetail(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()
	ctx := context.Background()

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],
		"id":"detail-t","title":"资产详情验证",
		"assets":[
			{"class":"service","title":"http://10.10.0.5:8080"},
			{"class":"host","title":"web-01"}
		]}`, http.StatusOK, &created)

	// 资产 node_id 面板侧生成,先从资产树取回。
	var rawTree struct {
		Assets []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/detail-t/assets", http.StatusOK, &rawTree)
	byTitle := map[string]string{}
	for _, a := range rawTree.Assets {
		byTitle[a.Title] = a.ID
	}
	svcID := byTitle["http://10.10.0.5:8080"]
	hostID := byTitle["web-01"]
	if svcID == "" || hostID == "" {
		t.Fatalf("资产树应含两个种子资产: %+v", rawTree.Assets)
	}

	// 图上手工挂:confirmed 事实 + 候选假设(另一资产)+ TestClaim 到 svc。
	_, err = store.Append(ctx, "detail-t",
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f1", NodeType: rings.NodeFact, Title: "8080 端口运行 HTTP 服务", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f1", ToNode: svcID}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm", Payload: rings.NodeAdded{
			NodeID: "h1", NodeType: rings.NodeHypothesis, Title: "可能存在备份目录", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "h1", ToNode: hostID}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm", Payload: rings.NodeAdded{
			NodeID: "tc1", NodeType: rings.NodeTestClaim, Title: "/old-backup/ 目录可列举", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc1", ToNode: svcID}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "active"}},
	)
	if err != nil {
		t.Fatalf("挂载测试节点失败: %v", err)
	}

	type item struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	var detail struct {
		Asset struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			Class           string `json:"class"`
			Covered         bool   `json:"covered"`
			FactCount       int    `json:"fact_count"`
			HypothesisCount int    `json:"hypothesis_count"`
		} `json:"asset"`
		Facts      []item `json:"facts"`
		Hypotheses []item `json:"hypotheses"`
		Claims     []item `json:"claims"`
	}
	get(t, h, "/api/tasks/detail-t/assets/"+svcID, http.StatusOK, &detail)
	if detail.Asset.ID != svcID || detail.Asset.Class != "service" || detail.Asset.Title != "http://10.10.0.5:8080" {
		t.Fatalf("资产字段不符: %+v", detail.Asset)
	}
	if detail.Asset.FactCount != 1 || detail.Asset.HypothesisCount != 0 {
		t.Fatalf("svc 资产计数不符: %+v", detail.Asset)
	}
	if len(detail.Facts) != 1 || detail.Facts[0].ID != "f1" || detail.Facts[0].Status != "confirmed" {
		t.Fatalf("svc 挂载事实不符: %+v", detail.Facts)
	}
	if len(detail.Claims) != 1 || detail.Claims[0].ID != "tc1" {
		t.Fatalf("svc 挂载发现不符: %+v", detail.Claims)
	}
	if len(detail.Hypotheses) != 0 {
		t.Fatalf("svc 不应挂假设: %+v", detail.Hypotheses)
	}

	// host 资产:假设挂载面。
	get(t, h, "/api/tasks/detail-t/assets/"+hostID, http.StatusOK, &detail)
	if len(detail.Hypotheses) != 1 || detail.Hypotheses[0].ID != "h1" || detail.Hypotheses[0].Status != "candidate" {
		t.Fatalf("host 挂载假设不符: %+v", detail.Hypotheses)
	}
	if len(detail.Facts) != 0 || len(detail.Claims) != 0 {
		t.Fatalf("host 不应挂事实/发现: %+v %+v", detail.Facts, detail.Claims)
	}

	// 错误路径:节点不存在 404;非资产节点 400。
	get(t, h, "/api/tasks/detail-t/assets/nope", http.StatusNotFound, nil)
	get(t, h, "/api/tasks/detail-t/assets/g1", http.StatusBadRequest, nil)
	get(t, h, "/api/tasks/ghost/assets/"+svcID, http.StatusNotFound, nil)
}
