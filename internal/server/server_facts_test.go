package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-10 事实页签数据面与修复建议同源 ----

func factsSetup(t *testing.T) (http.Handler, *rings.Store, string, string) {
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
		"title":"事实页签验证",
		"assets":[{"class":"host","title":"backup-01"}]}`,
		http.StatusOK, &created)
	task := created["id"]

	var tree struct {
		Assets []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	hostID := ""
	for _, a := range tree.Assets {
		if a.Title == "backup-01" {
			hostID = a.ID
		}
	}
	if hostID == "" {
		t.Fatalf("种子资产 backup-01 未取回")
	}

	// 候选 → 收割转正(candidate 不入事实页签,confirmed 才入);
	// 直接以 confirmed 入账的种子事实走 created 退化口径。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-cred", NodeType: rings.NodeFact, Title: "备份文件含后台账号与明文密码", Status: "candidate",
			Detail: map[string]any{"unit_id": "u1", "evidence": "GET /old/config.bak → admin:Passw0rd!"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "f-cred", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-data", NodeType: rings.NodeFact, Title: "用户表可整表导出,共 812 条记录", Status: "confirmed",
			Detail: map[string]any{"unit_id": "u1"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "f-plain", NodeType: rings.NodeFact, Title: "8080 端口运行 nginx 1.18.0", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "f-cand", NodeType: rings.NodeFact, Title: "疑似测试环境凭据泄露", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f-cred", ToNode: hostID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f-data", ToNode: hostID}},
		// 一条 Confirmed 声明带 vulnclass,验证详情抽屉修复建议与报告同源。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-weak", NodeType: rings.NodeTestClaim, Title: "后台存在弱口令登录", Status: "confirmed",
			Detail: map[string]any{"vulnclass": "弱口令"}}},
	)
	return h, store, task, hostID
}

// TestServerFactsList 事实页签端点:已确认 + 新进候选(P4-56 标"新")、
// 成果类分类、资产锚点、时刻口径、任务隔离、404。
func TestServerFactsList(t *testing.T) {
	h, _, task, hostID := factsSetup(t)

	var body struct {
		Facts []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			Statement string `json:"statement"`
			Loot      string `json:"loot"`
			Assets    []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"assets"`
			UnitID       string `json:"unit_id"`
			CreatedAt    string `json:"created_at"`
			ConfirmedAt  string `json:"confirmed_at"`
			ConfirmedRev int64  `json:"confirmed_rev"`
			Kind         string `json:"kind"`
			IsNew        bool   `json:"is_new"`
		} `json:"facts"`
	}
	get(t, h, "/api/tasks/"+task+"/facts", http.StatusOK, &body)

	// P4-56:已确认 3 条 + 新进候选 f-cand(标"新")一并返回。
	if len(body.Facts) != 4 {
		t.Fatalf("事实应 4 条(已确认 3 + 新进 1),得 %d", len(body.Facts))
	}
	byID := map[string]int{}
	for i, f := range body.Facts {
		byID[f.ID] = i
	}
	if f := body.Facts[byID["f-cand"]]; !f.IsNew || f.Kind != "fact" {
		t.Errorf("f-cand 应为新进事实(is_new, kind=fact),得 is_new=%v kind=%q", f.IsNew, f.Kind)
	} else if f.Status != "candidate" {
		t.Errorf("f-cand status 应 candidate(P4-61 三区归类键),得 %q", f.Status)
	}
	if i, ok := byID["f-cred"]; ok {
		f := body.Facts[i]
		if f.Loot != "credential" {
			t.Errorf("f-cred 成果类应 credential,得 %q", f.Loot)
		}
		if f.Status != "confirmed" {
			t.Errorf("f-cred status 应 confirmed(P4-61 三区归类键),得 %q", f.Status)
		}
		if len(f.Assets) != 1 || f.Assets[0].ID != hostID || f.Assets[0].Title != "backup-01" {
			t.Errorf("f-cred 资产锚点不符: %+v", f.Assets)
		}
		if f.UnitID != "u1" {
			t.Errorf("f-cred 发现单位应 u1,得 %q", f.UnitID)
		}
		// 候选转正:created_at/confirmed_at 都来自事件流,confirmed_at 非空。
		if f.CreatedAt == "" || f.ConfirmedAt == "" {
			t.Errorf("f-cred 时刻口径缺失: created=%q confirmed=%q", f.CreatedAt, f.ConfirmedAt)
		}
	} else {
		t.Fatal("f-cred 缺失")
	}
	if f := body.Facts[byID["f-data"]]; f.Loot != "data" {
		t.Errorf("f-data 成果类应 data,得 %q", f.Loot)
	}
	if f := body.Facts[byID["f-plain"]]; f.Loot != "" {
		t.Errorf("f-plain 普通观察应无成果类,得 %q", f.Loot)
	}

	// 404:任务不存在。
	get(t, h, "/api/tasks/t-none/facts", http.StatusNotFound, &struct{}{})

	// 任务隔离:另一任务的事实清单为空。
	var created2 map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"隔离验证"}`, http.StatusOK, &created2)
	var body2 struct {
		Facts []struct {
			ID string `json:"id"`
		} `json:"facts"`
	}
	get(t, h, "/api/tasks/"+created2["id"]+"/facts", http.StatusOK, &body2)
	if len(body2.Facts) != 0 {
		t.Fatalf("任务隔离破裂: 事实 %v 泄入他任务", body2.Facts)
	}
}

// TestClaimDetailRemediationSyncWithReport 修复建议同源(P2-10 完成标准):
// 详情抽屉的 remediation 与报告导出的修复建议逐字一致——两侧同一查表
// 函数(report.Remediation),面板所见即报告所印。
func TestClaimDetailRemediationSyncWithReport(t *testing.T) {
	h, store, task, _ := factsSetup(t)

	var detail struct {
		Remediation []string `json:"remediation"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-weak", http.StatusOK, &detail)

	want := report.Remediation("弱口令")
	if len(want) == 0 || len(detail.Remediation) != len(want) {
		t.Fatalf("详情修复建议应命中查表: got %v want %v", detail.Remediation, want)
	}
	for i := range want {
		if detail.Remediation[i] != want[i] {
			t.Fatalf("第 %d 条修复建议与查表不一致: %q vs %q", i, detail.Remediation[i], want[i])
		}
	}

	// 报告端点出 md 纯文本(零 JSON 包装),直接读体比对。
	md := doReq(t, h, http.MethodGet, "/api/tasks/"+task+"/report", "").Body.String()
	if md == "" {
		t.Fatal("报告为空")
	}
	for _, item := range want {
		if !strings.Contains(md, item) {
			t.Fatalf("报告缺同源修复建议条目: %q", item)
		}
	}

	// 未声明 vulnclass 的声明:无建议(空数组),不编造。
	appendEvents(t, store, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-plain", NodeType: rings.NodeTestClaim, Title: "某观察入账", Status: "confirmed"}},
	)
	var detail2 struct {
		Remediation []string `json:"remediation"`
	}
	get(t, h, "/api/tasks/"+task+"/claims/tc-plain", http.StatusOK, &detail2)
	if len(detail2.Remediation) != 0 {
		t.Fatalf("未声明类型不应有建议: %v", detail2.Remediation)
	}
}
