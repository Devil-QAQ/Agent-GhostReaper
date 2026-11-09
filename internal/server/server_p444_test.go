package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-44(问题清单 #61):资产范围归属与任务类型 ----
// ② 建任务必填任务类型(缺省/词表外拒建人话)、列表徽标数据
//(task_type/pending_scope)、存量任务按"未标"呈现;③ 待批资产的
// 批量放行/忽略端点与 RoE 快照透传保护;④ 红队无待批。

// p444ScopeSeed 建渗透任务 + 模拟收割入图一个范围外发现资产(冻结
// scope=out),返回任务 ID 与资产 ID。
func p444ScopeSeed(t *testing.T, h http.Handler, taskID, taskType, discoverTitle, frozen string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks",
		`{"task_type":"`+taskType+`","id":"`+taskID+`","goals":["目标"],"assets":[{"class":"domain","title":"www.example.com"}]}`,
		http.StatusOK, &created)
	if discoverTitle == "" {
		return taskID, ""
	}
	s := storeOfP444(t)
	detail := map[string]any{"source": rings.AssetSourceDiscovered}
	if frozen != "" {
		detail["scope"] = frozen
	}
	if _, err := s.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: "a-out", NodeType: rings.NodeAsset,
			Title: discoverTitle, Status: "active", AssetClass: rings.AssetHost, Detail: detail},
	}); err != nil {
		t.Fatal(err)
	}
	return taskID, "a-out"
}

var p444StoreRef *rings.Store

func storeOfP444(t *testing.T) *rings.Store {
	t.Helper()
	if p444StoreRef == nil {
		t.Fatal("测试脚手架未初始化 store")
	}
	return p444StoreRef
}

// newP444Server 建面板测试后端并登记 store 供直写事件。
func newP444Server(t *testing.T) (http.Handler, *rings.Store) {
	t.Helper()
	h, store := newP405Server(t)
	p444StoreRef = store
	t.Cleanup(func() { p444StoreRef = nil })
	return h, store
}

func TestP444TaskTypeRequired(t *testing.T) {
	h, _ := newP444Server(t)
	// 缺 task_type:拒建人话。
	var errBody map[string]string
	post(t, h, http.MethodPost, "/api/tasks",
		`{"goals":["目标"],"assets":[{"class":"domain","title":"www.example.com"}]}`,
		http.StatusBadRequest, &errBody)
	if !strings.Contains(errBody["error"], "任务类型必填") {
		t.Fatalf("拒建信息应人话指明任务类型必填: %v", errBody)
	}
	// 词表外值同样拒。
	post(t, h, http.MethodPost, "/api/tasks",
		`{"task_type":"purple","goals":["目标"],"assets":[{"class":"domain","title":"www.example.com"}]}`,
		http.StatusBadRequest, &errBody)
	if !strings.Contains(errBody["error"], "任务类型必填") {
		t.Fatalf("词表外值应拒: %v", errBody)
	}
	// 合法两值照建。
	for _, tt := range []string{"pentest", "redteam"} {
		post(t, h, http.MethodPost, "/api/tasks",
			`{"task_type":"`+tt+`","id":"tt-`+tt+`","goals":["目标"],"assets":[{"class":"domain","title":"www.example.com"}]}`,
			http.StatusOK, nil)
	}
	// 列表透出 task_type。
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	got := map[string]string{}
	for _, tk := range tasks {
		if id, _ := tk["id"].(string); id == "tt-pentest" || id == "tt-redteam" {
			got[id], _ = tk["task_type"].(string)
		}
	}
	if got["tt-pentest"] != "pentest" || got["tt-redteam"] != "redteam" {
		t.Fatalf("列表应透出任务类型: %+v", got)
	}
}

// p444DiscoverMore 对既有任务再入图一个发现资产(不重复建任务)。
func p444DiscoverMore(t *testing.T, taskID, nodeID, title string) {
	t.Helper()
	if _, err := storeOfP444(t).Append(context.Background(), taskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: nodeID, NodeType: rings.NodeAsset,
			Title: title, Status: "active", AssetClass: rings.AssetHost,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestP444ScopeEndpoints(t *testing.T) {
	h, store := newP444Server(t)
	task, assetID := p444ScopeSeed(t, h, "p444-pen", "pentest", "api.example.com", rings.AssetScopeOut)

	// 列表:渗透任务待批计数 1。
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	for _, tk := range tasks {
		if id, _ := tk["id"].(string); id == task {
			if n, _ := tk["pending_scope"].(float64); n != 1 {
				t.Fatalf("渗透任务待批计数应为 1: %v", tk["pending_scope"])
			}
		}
	}
	// 资产树:scope 字段与 task_type 透出。
	var tree struct {
		TaskType string `json:"task_type"`
		Assets   []struct {
			ID         string `json:"id"`
			ScopeOut   bool   `json:"scope_out"`
			ScopeState string `json:"scope_state"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	if tree.TaskType != "pentest" {
		t.Fatalf("资产树应带任务类型: %q", tree.TaskType)
	}
	hit := false
	for _, a := range tree.Assets {
		if a.ID == assetID {
			hit = true
			if !a.ScopeOut || a.ScopeState != "pending" {
				t.Fatalf("范围外发现资产应为待批态: %+v", a)
			}
		}
	}
	if !hit {
		t.Fatal("资产树应含发现资产")
	}

	// 放行(P4-52 ⑤ 审批集中 RoE:待批资产经审批卡端点裁决,卡 ID=
	// 资产 ID):待批转范围内(清单入 scope_approved,审计 note 人话)。
	var res map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/"+assetID,
		`{"approved":true}`, http.StatusOK, &res)
	if res["target"] != "api.example.com" {
		t.Fatalf("放行回执不符: %+v", res)
	}
	roe, err := store.CurrentRoE(context.Background(), task)
	if err != nil || len(roe.ScopeApproved) != 1 || roe.ScopeApproved[0] != assetID {
		t.Fatalf("放行清单应入 RoE 快照: %+v %v", roe.ScopeApproved, err)
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	for _, a := range tree.Assets {
		if a.ID == assetID && a.ScopeState != "" {
			t.Fatalf("放行后应转范围内: %+v", a)
		}
	}

	// 忽略路径:再造一项待批 → 禁止测试 → 进摘除清单、待批清零。
	p444DiscoverMore(t, task, "a-out2", "10.0.0.9:9999")
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/a-out2",
		`{"approved":false}`, http.StatusOK, &res)
	if res["target"] != "10.0.0.9:9999" {
		t.Fatalf("禁止测试回执不符: %+v", res)
	}
	roe, _ = store.CurrentRoE(context.Background(), task)
	if len(roe.ExcludedAssets) != 1 || roe.ExcludedAssets[0] != "a-out2" {
		t.Fatalf("禁止测试应进摘除清单: %+v", roe.ExcludedAssets)
	}

	// 非待批条目如实拒收(重复裁决 409);批量空清单拒收。
	var errBody map[string]string
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/"+assetID,
		`{"approved":true}`, http.StatusConflict, &errBody)
	if !strings.Contains(errBody["error"], "不是待批资产") {
		t.Fatalf("非待批条目应人话拒收: %v", errBody)
	}
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/batch",
		`{"ids":[],"approved":true}`, http.StatusBadRequest, &errBody)
	if !strings.Contains(errBody["error"], "至少勾选一张范围卡") {
		t.Fatalf("空清单应拒收: %v", errBody)
	}
}

func TestP444ScopeEndpointsRedteam(t *testing.T) {
	h, _ := newP444Server(t)
	task, _ := p444ScopeSeed(t, h, "p444-red", "redteam", "api.example.com", rings.AssetScopeOut)
	// 红队任务:范围外发现直接可测,无待批——待批资产卡端点人话拒绝。
	var errBody map[string]string
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/a-out",
		`{"approved":true}`, http.StatusConflict, &errBody)
	if !strings.Contains(errBody["error"], "不是待批资产") {
		t.Fatalf("红队任务应人话拒绝: %v", errBody)
	}
	// 资产树:范围外呈现态为 out_open(发现的其他资产分组)。
	var tree struct {
		Assets []struct {
			ID         string `json:"id"`
			ScopeState string `json:"scope_state"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree)
	for _, a := range tree.Assets {
		if a.ID == "a-out" && a.ScopeState != "out_open" {
			t.Fatalf("红队范围外应为 out_open: %+v", a)
		}
	}
	// 列表:红队无待批计数。
	var tasks []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &tasks)
	for _, tk := range tasks {
		if id, _ := tk["id"].(string); id == task {
			if n, _ := tk["pending_scope"].(float64); n != 0 {
				t.Fatalf("红队任务无待批: %v", tk["pending_scope"])
			}
		}
	}
}

func TestP444RoEPassthroughScopeApproved(t *testing.T) {
	h, store := newP444Server(t)
	task, assetID := p444ScopeSeed(t, h, "p444-roe", "pentest", "api.example.com", rings.AssetScopeOut)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/"+assetID,
		`{"approved":true}`, http.StatusOK, nil)
	// RoE 整体快照提交(不带放行清单):现值保持,不误清。
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe",
		`{"allowed_tools":[],"denied_tools":[]}`, http.StatusOK, nil)
	roe, err := store.CurrentRoE(context.Background(), task)
	if err != nil || len(roe.ScopeApproved) != 1 {
		t.Fatalf("RoE 快照提交不应清空放行清单: %+v %v", roe.ScopeApproved, err)
	}
	// 摘除清单端点同样透传(种子资产 ID 由资产树实查,面板侧随机 ID)。
	var tree2 struct {
		Assets []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/"+task+"/assets", http.StatusOK, &tree2)
	seedID := ""
	for _, a := range tree2.Assets {
		if a.Title == "www.example.com" {
			seedID = a.ID
		}
	}
	if seedID == "" {
		t.Fatal("种子资产应在图上")
	}
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe/assets",
		`{"excluded":["`+seedID+`"]}`, http.StatusOK, nil)
	roe, _ = store.CurrentRoE(context.Background(), task)
	if len(roe.ScopeApproved) != 1 {
		t.Fatalf("摘除清单提交不应清空放行清单: %+v", roe.ScopeApproved)
	}
}

func TestP444ExtractTaskType(t *testing.T) {
	out, issues := parseTaskExtract(`{"title":"官网渗透","task_type":"pentest","assets":[],"goals":[],"constraints":[]}`)
	if len(issues) != 0 || out.TaskType != "pentest" {
		t.Fatalf("提取应带回任务类型倾向: %+v %v", out.TaskType, issues)
	}
	out, issues = parseTaskExtract(`{"task_type":"purple","assets":[],"goals":[],"constraints":[]}`)
	if out.TaskType != "" || len(issues) == 0 {
		t.Fatalf("词表外类型应丢弃并如实入 issues: %+v %v", out.TaskType, issues)
	}
	// 未给类型:空(面板默认渗透)。
	out, _ = parseTaskExtract(`{"assets":[],"goals":[],"constraints":[]}`)
	if out.TaskType != "" {
		t.Fatalf("未给类型应为空: %q", out.TaskType)
	}
	// 回复形态携带(序列化字段名)。
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "task_type") && out.TaskType == "" {
		t.Fatal("空类型不应序列化字段(omitempty)")
	}
}
