package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P3-10 命令面板搜索:跨任务搜任务与发现(声明) ----

// TestSearchAPI 命令面板数据面:任务按标题/ID 子串命中、发现按声明
// 标题命中并携带任务定位;LIKE 通配符按字面匹配;空关键词 400;
// 墓碑任务与其声明不搜出。
func TestSearchAPI(t *testing.T) {
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
	post(t, h, http.MethodPost, "/api/tasks", `{"title":"DVWA 深挖"}`, http.StatusOK, &created)
	dvwa := created["id"]
	post(t, h, http.MethodPost, "/api/tasks", `{"title":"边界梳理"}`, http.StatusOK, &created)
	edge := created["id"]
	post(t, h, http.MethodPost, "/api/tasks", `{"title":"已删任务"}`, http.StatusOK, &created)
	gone := created["id"]

	// 铺垫:两任务的声明各一条 + 已删任务一条(不应搜出)。
	appendEvents(t, store, dvwa,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "SQL 注入:登录表单可联合查询", Status: rings.TestClaimCandidate}},
	)
	appendEvents(t, store, edge,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-2", NodeType: rings.NodeTestClaim, Title: "备份目录匿名可列举", Status: rings.TestClaimConfirmed}},
		// 步骤节点同名也不该混进发现结果(类型限定 testclaim)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s9", NodeType: rings.NodeStep, Title: "SQL 注入验证步骤", Status: "done"}},
	)
	appendEvents(t, store, gone,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-3", NodeType: rings.NodeTestClaim, Title: "已删任务里的注入发现", Status: rings.TestClaimCandidate}},
	)
	post(t, h, http.MethodDelete, "/api/tasks/"+gone, `{"reason":"测试墓碑"}`, http.StatusOK, nil)

	// 任务命中:标题子串 + ID 子串。
	var body struct {
		Tasks []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"tasks"`
		Claims []struct {
			TaskID    string `json:"task_id"`
			ClaimID   string `json:"claim_id"`
			Title     string `json:"title"`
			Status    string `json:"status"`
			TaskTitle string `json:"task_title"`
		} `json:"claims"`
	}
	get(t, h, "/api/search?q=DVWA", http.StatusOK, &body)
	if len(body.Tasks) != 1 || body.Tasks[0].ID != dvwa {
		t.Fatalf("任务标题命中 = %+v, want 1 条 %s", body.Tasks, dvwa)
	}
	if len(body.Claims) != 0 {
		t.Fatalf("无声明命中时 claims 应为空,得到 %+v", body.Claims)
	}

	get(t, h, "/api/search?q="+dvwa, http.StatusOK, &body)
	if len(body.Tasks) != 1 || body.Tasks[0].ID != dvwa {
		t.Fatalf("任务 ID 命中 = %+v, want 1 条 %s", body.Tasks, dvwa)
	}

	// 发现命中:标题子串,带任务定位与状态;步骤节点不混入;
	// 任务标题不含关键词时任务组为空(发现独立命中)。
	get(t, h, "/api/search?q=注入", http.StatusOK, &body)
	if len(body.Tasks) != 0 {
		t.Fatalf("任务标题不含「注入」,任务组应为空,得到 %+v", body.Tasks)
	}
	if len(body.Claims) != 1 {
		t.Fatalf("声明命中 = %+v, want 1 条(步骤不混入,墓碑不搜出)", body.Claims)
	}
	c := body.Claims[0]
	if c.TaskID != dvwa || c.ClaimID != "tc-1" || c.TaskTitle != "DVWA 深挖" ||
		c.Status != string(rings.TestClaimCandidate) || c.Title != "SQL 注入:登录表单可联合查询" {
		t.Fatalf("声明命中字段不全: %+v", c)
	}

	// LIKE 通配符按字面匹配:% 与 _ 不当通配用(% 走 URL 转义 %25)。
	get(t, h, "/api/search?q=100%25", http.StatusOK, &body)
	if len(body.Tasks) != 0 || len(body.Claims) != 0 {
		t.Fatalf("通配符应按字面匹配(无命中),得到 tasks=%d claims=%d", len(body.Tasks), len(body.Claims))
	}
	get(t, h, "/api/search?q=tc_", http.StatusOK, &body)
	if len(body.Claims) != 0 {
		t.Fatalf("下划线应按字面匹配(无命中),得到 %d 条", len(body.Claims))
	}

	// 空关键词 400(只走 handler 校验,不依赖具体错误文案)。
	get(t, h, "/api/search?q=", http.StatusBadRequest, nil)
	get(t, h, "/api/search?q=%20%20", http.StatusBadRequest, nil)
}
