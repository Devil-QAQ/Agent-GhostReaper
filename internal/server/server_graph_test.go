package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-09 统一图投影 API(三视图共用,任务投影首个接入)----

func graphSetup(t *testing.T) (http.Handler, string, string) {
	t.Helper()
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

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],
		"title":"图投影验证",
		"assets":[{"class":"host","title":"192.0.2.1"},{"class":"service","title":"http://10.0.0.5"}]}`,
		http.StatusOK, &created)
	task := created["id"]

	// 种子资产 node_id 由服务端生成,按标题自资产树取回。
	var tree struct {
		Assets []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	hostID := ""
	for _, a := range tree.Assets {
		if a.Title == "192.0.2.1" {
			hostID = a.ID
		}
	}
	if hostID == "" {
		t.Fatalf("种子资产 192.0.2.1 未取回")
	}

	// 血缘链:g1 → s1(归属 g1,覆盖 192.0.2.1)→ f1(产出,关于 192.0.2.1)
	//           → tc-1(派生自 f1,关于 192.0.2.1)
	// 废弃分支:g1 → s2(aborted)→ h1(产出,已废弃,关于 192.0.2.1)
	// 越界引用:s3 的 goal_id 指向非 goal 节点 → 不产 goal 派生边。
	// (旧 falsify 派生边已随反证流程退役删除,P5-03③——testclaim_id
	// 绑定不再上图投影。)
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "摸清 192.0.2.1 暴露面", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测服务与目录", Status: "done",
			Detail: map[string]any{"goal_id": "g1"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "尝试旁路(放弃)", Status: "aborted",
			Detail: map[string]any{"goal_id": "g1", "testclaim_id": "f1"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "归属异常的步骤", Status: "dispatched",
			Detail: map[string]any{"goal_id": "f1", "testclaim_id": "tc-1"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f1", NodeType: rings.NodeFact, Title: "/old-backup/ 匿名可列举", Status: "confirmed"}},

		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "备份目录可匿名列举", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: hostID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f1"}},

		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-1", ToNode: "f1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f1", ToNode: hostID}},

		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-1", ToNode: hostID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDependsOn, FromNode: "s2", ToNode: "s1"}},
	)
	return h, task, hostID
}

// TestServerTaskGraph 统一图投影端点:六类节点全量、全部类型化边 +
// goal 派生边、资产派生计数、任务隔离、404。
func TestServerTaskGraph(t *testing.T) {
	h, task, hostID := graphSetup(t)

	var body struct {
		Nodes []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Status     string `json:"status"`
			AssetClass string `json:"asset_class"`
			Covered    bool   `json:"covered"`
			FactCount  int    `json:"fact_count"`
		} `json:"nodes"`
		Edges []struct {
			Type string `json:"type"`
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	get(t, h, "/api/tasks/"+task+"/graph", http.StatusOK, &body)

	byType := map[string]int{}
	nodeIDs := map[string]bool{}
	for _, n := range body.Nodes {
		byType[n.Type]++
		nodeIDs[n.ID] = true
	}
	for _, typ := range []string{"goal", "step", "fact", "asset", "testclaim"} {
		if byType[typ] == 0 {
			t.Fatalf("图投影缺 %s 类节点: %v", typ, byType)
		}
	}
	if byType["asset"] != 2 || byType["step"] != 3 {
		t.Fatalf("资产应 2(两枚种子)、步骤应 3: %v", byType)
	}

	hasEdge := func(typ, from, to string) bool {
		for _, e := range body.Edges {
			if e.Type == typ && e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	// goal 派生边只对归属合法的步骤产出(s3 的 goal_id 指向 fact 不产边)。
	if !hasEdge("goal", "g1", "s1") || !hasEdge("goal", "g1", "s2") {
		t.Fatalf("缺 goal 派生边 g1→s1/g1→s2: %+v", body.Edges)
	}
	if hasEdge("goal", "f1", "s3") {
		t.Fatalf("goal_id 指向非 goal 节点不应产出派生边: %+v", body.Edges)
	}
	if hasEdge("falsify", "s3", "tc-1") {
		t.Fatalf("falsify 派生边应已退役(P5-03③): %+v", body.Edges)
	}
	for _, e := range [][3]string{
		{"produces", "s1", "f1"},
		{"derived_from", "tc-1", "f1"}, {"about", "f1", hostID},
		{"about", "tc-1", hostID}, {"covers", "s1", hostID},
		{"depends_on", "s2", "s1"},
	} {
		if !hasEdge(e[0], e[1], e[2]) {
			t.Fatalf("缺类型化边 %s %s→%s", e[0], e[1], e[2])
		}
	}

	for _, n := range body.Nodes {
		if n.ID != hostID {
			continue
		}
		if !n.Covered || n.FactCount != 1 {
			t.Fatalf("192.0.2.1 派生计数不符: %+v", n)
		}
	}

	// 任务隔离:另一任务的图不含本任务节点。
	var created2 map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"隔离验证"}`, http.StatusOK, &created2)
	var body2 struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	get(t, h, "/api/tasks/"+created2["id"]+"/graph", http.StatusOK, &body2)
	for _, n := range body2.Nodes {
		if nodeIDs[n.ID] {
			t.Fatalf("任务隔离破裂: %s 同时出现在两任务图中", n.ID)
		}
	}

	get(t, h, "/api/tasks/no-such/graph", http.StatusNotFound, nil)
}
