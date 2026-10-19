package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-05 手动新增资产(问题清单 #10) ----
// 面板"新增资产"端点:入账入图(来源 manual)+ 默认纳入授权范围 +
// 事件唤醒;RoE 资产范围(纳入/摘除)端点;快照透传不清空摘除清单;
// 任务列表目标域不被手动资产污染。

// newP405Server 建一套面板测试后端(任务建好即回)。
func newP405Server(t *testing.T) (http.Handler, *rings.Store) {
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
	return srv.Handler(), store
}

// TestManualAssetCreate 手动新增资产:批量入账(含批内父子层级与图上
// 父引用)、来源标 manual、种子来源仍标 seed、账本事件 actor=造物主
// 带来源明细(全程记账可审计);坏输入 400 零残留。
func TestManualAssetCreate(t *testing.T) {
	h, store := newP405Server(t)
	post(t, h, http.MethodPost, "/api/tasks", `{
		"id":"p405-a","title":"手动资产验证",
		"assets":[{"class":"domain","title":"example.com"}]}`, http.StatusOK, nil)

	// 批量:两个新资产(批内父子)+ 一个挂到图上既有资产,共 3 节点 2 边。
	var added map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p405-a/assets", `{
		"assets":[
			{"class":"subdomain","title":"api.example.com","parent":"example.com"},
			{"class":"service","title":"http://10.10.0.9:8080","parent":"api.example.com"},
			{"class":"host","title":"10.10.0.10"}
		]}`, http.StatusOK, &added)
	if added["added"] != float64(3) || added["edges"] != float64(2) {
		t.Fatalf("入账计数不符: %+v", added)
	}

	// 资产树读投影:来源 manual、默认纳入授权(excluded=false)、层级边在场。
	var tree struct {
		Assets []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Source   string `json:"source"`
			Excluded bool   `json:"excluded"`
		} `json:"assets"`
		Tree []struct{ From, To string } `json:"hierarchy"`
	}
	get(t, h, "/api/tasks/p405-a/assets", http.StatusOK, &tree)
	byTitle := map[string]string{}
	for _, a := range tree.Assets {
		byTitle[a.Title] = a.ID
		if a.Excluded {
			t.Fatalf("新入账资产默认应纳入授权: %+v", a)
		}
		switch a.Title {
		case "example.com":
			if a.Source != rings.AssetSourceSeed {
				t.Fatalf("种子资产来源应为 seed: %+v", a)
			}
		case "api.example.com", "http://10.10.0.9:8080", "10.10.0.10":
			if a.Source != rings.AssetSourceManual {
				t.Fatalf("手动资产来源应为 manual: %+v", a)
			}
		}
	}
	if len(tree.Tree) != 2 {
		t.Fatalf("批内+图上父子边共 2 条,实得 %d", len(tree.Tree))
	}

	// 账本审计:node.added 事件 actor=creator,detail.source=manual。
	evs, err := store.Events(t.Context(), "p405-a", 0, 0)
	if err != nil {
		t.Fatalf("读事件流失败: %v", err)
	}
	manualEvents := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeAdded {
			continue
		}
		var p rings.NodeAdded
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.NodeType != rings.NodeAsset || rings.AssetSourceOf(p.Detail) != rings.AssetSourceManual {
			continue
		}
		if ev.Actor != "creator" {
			t.Fatalf("手动资产事件 actor 应为 creator: %s", ev.Actor)
		}
		manualEvents++
	}
	if manualEvents != 3 {
		t.Fatalf("手动资产事件应 3 条,实得 %d", manualEvents)
	}

	// 坏输入:重复标识 / 与图上同标识 / 坏类 / 父标识不存在 / 批内成环 / 空清单。
	for _, bad := range []struct{ name, body string }{
		{"批内重复", `{"assets":[{"class":"host","title":"dup"},{"class":"host","title":"dup"}]}`},
		{"图上同标识", `{"assets":[{"class":"host","title":"example.com"}]}`},
		{"坏类", `{"assets":[{"class":"printer","title":"x"}]}`},
		{"父标识不存在", `{"assets":[{"class":"host","title":"h9","parent":"no-such"}]}`},
		{"批内成环", `{"assets":[{"class":"host","title":"c1","parent":"c2"},{"class":"host","title":"c2","parent":"c1"}]}`},
		{"空清单", `{"assets":[]}`},
	} {
		var errBody map[string]any
		post(t, h, http.MethodPost, "/api/tasks/p405-a/assets", bad.body, http.StatusBadRequest, &errBody)
		if errBody["error"] == nil {
			t.Fatalf("%s: 400 应带错误说明", bad.name)
		}
	}

	// 坏输入零残留:资产数不变(仍 4)。
	get(t, h, "/api/tasks/p405-a/assets", http.StatusOK, &tree)
	if len(tree.Assets) != 4 {
		t.Fatalf("坏输入后资产数应仍为 4,实得 %d", len(tree.Assets))
	}
}

// TestRoEAssetScope 授权资产范围:摘除 → 读面(资产树/图/详情)标范围外、
// RoE 视图带清单、自动备注含摘除明细;纳入恢复;不在图上的 ID 400。
func TestRoEAssetScope(t *testing.T) {
	h, _ := newP405Server(t)
	post(t, h, http.MethodPost, "/api/tasks", `{
		"id":"p405-b","title":"范围摘除验证",
		"assets":[
			{"class":"host","title":"10.10.0.5"},
			{"class":"service","title":"http://10.10.0.6:8080"}
		]}`, http.StatusOK, nil)

	var tree struct {
		Assets []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Excluded bool   `json:"excluded"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/p405-b/assets", http.StatusOK, &tree)
	var svcID string
	for _, a := range tree.Assets {
		if a.Title == "http://10.10.0.6:8080" {
			svcID = a.ID
		}
	}
	if svcID == "" {
		t.Fatalf("未找到目标资产")
	}

	// 摘除:提交只含该资产的新清单。
	var roeView struct {
		ExcludedAssets []string `json:"excluded_assets"`
		Note           string   `json:"note"`
	}
	post(t, h, http.MethodPost, "/api/tasks/p405-b/roe/assets",
		`{"excluded":["`+svcID+`"]}`, http.StatusOK, &roeView)
	if len(roeView.ExcludedAssets) != 1 || roeView.ExcludedAssets[0] != svcID {
		t.Fatalf("摘除清单不符: %+v", roeView.ExcludedAssets)
	}
	if !strings.Contains(roeView.Note, "摘除") || !strings.Contains(roeView.Note, svcID) {
		t.Fatalf("自动备注应含摘除明细: %q", roeView.Note)
	}

	// 三个读面同源标范围外。
	get(t, h, "/api/tasks/p405-b/assets", http.StatusOK, &tree)
	for _, a := range tree.Assets {
		if a.ID == svcID && !a.Excluded {
			t.Fatalf("资产树未标范围外: %+v", a)
		}
		if a.ID != svcID && a.Excluded {
			t.Fatalf("未摘除资产被误标: %+v", a)
		}
	}
	var graph struct {
		Nodes []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Excluded bool   `json:"excluded"`
		} `json:"nodes"`
	}
	get(t, h, "/api/tasks/p405-b/graph", http.StatusOK, &graph)
	for _, n := range graph.Nodes {
		if n.Type == "asset" && n.ID == svcID && !n.Excluded {
			t.Fatalf("图投影未标范围外: %+v", n)
		}
	}
	var detail struct {
		Asset struct {
			ID       string `json:"id"`
			Excluded bool   `json:"excluded"`
		} `json:"asset"`
	}
	get(t, h, "/api/tasks/p405-b/assets/"+svcID, http.StatusOK, &detail)
	if detail.Asset.ID != svcID || !detail.Asset.Excluded {
		t.Fatalf("资产详情未标范围外: %+v", detail.Asset)
	}

	// 纳入恢复 + 不在图上的 ID 拒绝。
	post(t, h, http.MethodPost, "/api/tasks/p405-b/roe/assets", `{"excluded":[]}`, http.StatusOK, &roeView)
	if len(roeView.ExcludedAssets) != 0 {
		t.Fatalf("纳入后清单应为空: %+v", roeView.ExcludedAssets)
	}
	get(t, h, "/api/tasks/p405-b/assets", http.StatusOK, &tree)
	for _, a := range tree.Assets {
		if a.Excluded {
			t.Fatalf("纳入恢复后不应有范围外资产: %+v", a)
		}
	}
	post(t, h, http.MethodPost, "/api/tasks/p405-b/roe/assets", `{"excluded":["a-nonexist"]}`,
		http.StatusBadRequest, nil)
}

// TestRoEUpdateKeepsExcluded 其他 RoE 快照提交(约束变更/规则增删)透传
// 摘除清单:省略字段不清空,显式提交同值不漂移。
func TestRoEUpdateKeepsExcluded(t *testing.T) {
	h, _ := newP405Server(t)
	post(t, h, http.MethodPost, "/api/tasks", `{
		"id":"p405-c","title":"快照透传验证",
		"assets":[{"class":"host","title":"10.10.0.7"}]}`, http.StatusOK, nil)

	var tree struct {
		Assets []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/p405-c/assets", http.StatusOK, &tree)
	hostID := tree.Assets[0].ID
	post(t, h, http.MethodPost, "/api/tasks/p405-c/roe/assets",
		`{"excluded":["`+hostID+`"]}`, http.StatusOK, nil)

	// 约束快照提交(不带 excluded_assets):摘除保持。
	var roeView struct {
		ExcludedAssets []string `json:"excluded_assets"`
	}
	post(t, h, http.MethodPost, "/api/tasks/p405-c/roe",
		`{"denied_tools":["nmap"],"approval_threshold":"主动测试"}`, http.StatusOK, &roeView)
	if len(roeView.ExcludedAssets) != 1 || roeView.ExcludedAssets[0] != hostID {
		t.Fatalf("约束变更不应清空摘除清单: %+v", roeView.ExcludedAssets)
	}
	// 规则逐条端点(新增一条)同样保持。
	post(t, h, http.MethodPost, "/api/tasks/p405-c/roe/rules",
		`{"action":"上传文件","disposition":"forbid"}`, http.StatusOK, &roeView)
	if len(roeView.ExcludedAssets) != 1 || roeView.ExcludedAssets[0] != hostID {
		t.Fatalf("规则变更不应清空摘除清单: %+v", roeView.ExcludedAssets)
	}
}

// TestTaskTargetsSkipManualAssets 任务列表目标域只收种子资产:手动补入
// 资产是测试中途的授权扩大,不混入建任务目标。
func TestTaskTargetsSkipManualAssets(t *testing.T) {
	h, _ := newP405Server(t)
	post(t, h, http.MethodPost, "/api/tasks", `{
		"id":"p405-d","title":"目标域验证",
		"assets":[{"class":"domain","title":"seed.example.com"}]}`, http.StatusOK, nil)
	post(t, h, http.MethodPost, "/api/tasks/p405-d/assets",
		`{"assets":[{"class":"host","title":"10.10.0.99"}]}`, http.StatusOK, nil)

	var list []struct {
		ID      string   `json:"id"`
		Targets []string `json:"targets"`
	}
	get(t, h, "/api/tasks", http.StatusOK, &list)
	for _, tk := range list {
		if tk.ID != "p405-d" {
			continue
		}
		if len(tk.Targets) != 1 || tk.Targets[0] != "seed.example.com" {
			t.Fatalf("目标域应只含种子资产: %+v", tk.Targets)
		}
	}
}
