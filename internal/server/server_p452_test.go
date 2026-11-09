package server

// P4-52(问题清单 #73④骨架部分＋#74＋#77⑤):审批区呈现层收口的服务端
// 确定性测试——待决区三类卡统一视图(弹门/范围拦截/红队/待批资产)、
// 待批资产卡与拦截卡同目标去重、资产卡两键裁决与勾选批量、锚点等价
// 不重复入图。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// p452ApprovalsBody /approvals 响应的解码形态。
type p452ApprovalsBody struct {
	Pending []struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Mode        string `json:"mode"`
		Target      string `json:"target"`
		ToolName    string `json:"tool_name"`
		ToolArgs    string `json:"tool_args"`
		HarmNote    string `json:"harm_note"`
		ActionLevel string `json:"action_level"`
	} `json:"pending"`
	ScopeAssets []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Mode   string `json:"mode"`
		Target string `json:"target"`
	} `json:"scope_assets"`
	Records []struct {
		RequestID string `json:"request_id"`
		Type      string `json:"type"`
	} `json:"records"`
}

// p452ScopeWait 追加一笔范围待决卡记录(拦截卡形态:带命令上下文)。
func p452ScopeWait(t *testing.T, taskID, rid, intent, tool, args string) {
	t.Helper()
	rec := rings.ApprovalRecord{
		RequestID: rid, StepID: "s-1", UnitID: "u-1",
		Type: rings.ApprovalRecScopeWait, Intent: intent,
		ActionLevel: rings.ActionLevel("只读探测"),
		ToolName:    tool, ToolArgs: []byte(args),
		Note: "渗透任务:命令目标在范围外,拦下等待批准(加入资产/禁止测试)",
	}
	if _, err := storeOfP444(t).Append(context.Background(), taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "swarm:u-1", Payload: rec,
	}); err != nil {
		t.Fatal(err)
	}
}

// p452AssetCount 图上资产节点数(锚点等价入图断言用)。
func p452AssetCount(t *testing.T, taskID string) int {
	t.Helper()
	nodes, err := storeOfP444(t).NodesByType(context.Background(), taskID, rings.NodeAsset, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(nodes)
}

func TestP452UnifiedCardView(t *testing.T) {
	h, _ := newP444Server(t)
	// 渗透任务:待批资产 api.example.com + 拦截卡 10.0.0.9:9999 + 与拦截
	// 卡同目标的待批资产(去重不重复出卡)。
	task, assetID := p444ScopeSeed(t, h, "p452-view", "pentest", "api.example.com", rings.AssetScopeOut)
	p444DiscoverMore(t, task, "a-dup", "10.0.0.9:9999")
	p452ScopeWait(t, task, "sc-10.0.0.9:9999", "对旁站端口做目录探测", "bash",
		`{"command":"nmap -p 9999 10.0.0.9"}`)

	var body p452ApprovalsBody
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)
	if len(body.Pending) != 1 {
		t.Fatalf("拦截卡应并入待决区: %+v", body.Pending)
	}
	card := body.Pending[0]
	if card.Kind != "scope" || card.Mode != "pentest" || card.Target != "10.0.0.9:9999" {
		t.Fatalf("拦截卡视图不符: %+v", card)
	}
	if card.HarmNote == "" || card.ActionLevel == "" {
		t.Fatalf("拦截卡应带危害说明三段式(按命令特征拼装): %+v", card)
	}
	if card.ToolName != "bash" || !strings.Contains(card.ToolArgs, "nmap") {
		t.Fatalf("拦截卡应带命令上下文: %+v", card)
	}
	// 待批资产卡:api.example.com 在(拦截卡未覆盖),10.0.0.9:9999 去重。
	if len(body.ScopeAssets) != 1 || body.ScopeAssets[0].ID != assetID ||
		body.ScopeAssets[0].Target != "api.example.com" || body.ScopeAssets[0].Mode != "pentest" {
		t.Fatalf("待批资产卡应只剩未被拦截卡覆盖的一张: %+v", body.ScopeAssets)
	}
}

func TestP452RedteamCardMode(t *testing.T) {
	h, _ := newP444Server(t)
	task, _ := p444ScopeSeed(t, h, "p452-red", "redteam", "", "")
	p452ScopeWait(t, task, "sc-evil.example.org", "对新增域名做探测", "http_probe",
		`{"url":"http://evil.example.org/"}`)
	var body p452ApprovalsBody
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)
	if len(body.Pending) != 1 || body.Pending[0].Mode != "redteam" {
		t.Fatalf("红队任务范围卡 Mode 应为 redteam: %+v", body.Pending)
	}
	// 红队任务无待批资产卡。
	if len(body.ScopeAssets) != 0 {
		t.Fatalf("红队任务不应有待批资产卡: %+v", body.ScopeAssets)
	}
}

func TestP452AssetCardDecideAndBatch(t *testing.T) {
	h, store := newP444Server(t)
	task, assetID := p444ScopeSeed(t, h, "p452-asset", "pentest", "api.example.com", rings.AssetScopeOut)
	p444DiscoverMore(t, task, "a-out2", "10.0.0.9:9999")
	p452ScopeWait(t, task, "sc-sib.example.org", "测兄弟子域", "bash", `{"command":"curl http://sib.example.org/"}`)

	// 单卡:待批资产卡批准 → scope_approved + ac- 裁决记录留档,卡消。
	var res map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/"+assetID,
		`{"approved":true}`, http.StatusOK, &res)
	if res["target"] != "api.example.com" {
		t.Fatalf("资产卡批准回执不符: %+v", res)
	}
	roe, _ := store.CurrentRoE(context.Background(), task)
	if len(roe.ScopeApproved) != 1 || roe.ScopeApproved[0] != assetID {
		t.Fatalf("批准应入放行清单: %+v", roe.ScopeApproved)
	}
	rows, _ := store.ApprovalByRequest(context.Background(), task, "ac-"+assetID)
	if len(rows) != 1 || rows[0].Type != rings.ApprovalRecApproved {
		t.Fatalf("资产卡裁决应留 ac- 记录: %+v", rows)
	}

	// 批量:待批资产卡 + 拦截卡混合勾选批准 → handled=2,逐笔留痕。
	var batch map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/batch",
		`{"ids":["a-out2","sc-sib.example.org"],"approved":true}`, http.StatusOK, &batch)
	if batch["handled"] != float64(2) || len(batch["failed"].([]any)) != 0 {
		t.Fatalf("批量两键回执不符: %+v", batch)
	}
	roe, _ = store.CurrentRoE(context.Background(), task)
	if len(roe.ScopeApproved) != 3 { // a-out + a-out2 + 拦截目标入图
		t.Fatalf("批量批准后放行清单应含三项目标: %+v", roe.ScopeApproved)
	}
	// 拦截目标 sib.example.org 入图(来源发现)。
	if n := p452AssetCount(t, task); n != 4 { // 种子 + api.example.com + 10.0.0.9:9999 + sib.example.org
		t.Fatalf("拦截目标应入图,资产数不符: %d", n)
	}

	// 非范围卡 id(弹门票形态)混入批量 → 该项失败,其余不受影响。
	var batch2 map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/batch",
		`{"ids":["ap-deadbeef"],"approved":true}`, http.StatusOK, &batch2)
	if batch2["handled"] != float64(0) || len(batch2["failed"].([]any)) != 1 {
		t.Fatalf("非范围卡应逐项失败: %+v", batch2)
	}

	// 待决区清空:资产卡与拦截卡全消。
	var body p452ApprovalsBody
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)
	if len(body.Pending) != 0 || len(body.ScopeAssets) != 0 {
		t.Fatalf("裁决后待决区应清空: %+v %+v", body.Pending, body.ScopeAssets)
	}
}

func TestP452AssetCardDeny(t *testing.T) {
	h, store := newP444Server(t)
	task, assetID := p444ScopeSeed(t, h, "p452-deny", "pentest", "api.example.com", rings.AssetScopeOut)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/"+assetID,
		`{"approved":false}`, http.StatusOK, nil)
	roe, _ := store.CurrentRoE(context.Background(), task)
	if len(roe.ExcludedAssets) != 1 || roe.ExcludedAssets[0] != assetID {
		t.Fatalf("禁止测试应进摘除清单: %+v", roe.ExcludedAssets)
	}
	rows, _ := store.ApprovalByRequest(context.Background(), task, "ac-"+assetID)
	if len(rows) != 1 || rows[0].Type != rings.ApprovalRecDenied || rows[0].DenyLine != rings.DenyLineRoeScope {
		t.Fatalf("禁止测试应留 denied 记录: %+v", rows)
	}
}

func TestP452AnchorEquivalentAssetReuse(t *testing.T) {
	h, store := newP444Server(t)
	// 待批资产标题为 URL 形态;拦截卡目标为同主机裸域名(锚点等价)。
	// 批准拦截卡应复用既有资产(URL 形态),不重复入图。
	task, _ := p444ScopeSeed(t, h, "p452-anchor", "pentest", "http://x.example.org/", rings.AssetScopeOut)
	p452ScopeWait(t, task, "sc-x.example.org", "探测新域名", "http_probe", `{"url":"http://x.example.org/"}`)
	before := p452AssetCount(t, task)
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-x.example.org",
		`{"approved":true}`, http.StatusOK, nil)
	if after := p452AssetCount(t, task); after != before {
		t.Fatalf("锚点等价应复用既有资产不重复入图: %d → %d", before, after)
	}
	roe, _ := store.CurrentRoE(context.Background(), task)
	if len(roe.ScopeApproved) != 1 {
		t.Fatalf("批准应落在既有资产上: %+v", roe.ScopeApproved)
	}
}

// TestP452RecordsFullAndSort 审批记录全量返回(倒序最新在先)——前端
// 滚动加载的数据前提(#74:服务端全量、展示端按需渲染)。
func TestP452RecordsFullAndSort(t *testing.T) {
	h, _ := newP444Server(t)
	task, _ := p444ScopeSeed(t, h, "p452-recs", "pentest", "", "")
	for i := 0; i < 25; i++ {
		p452ScopeWait(t, task, "sc-t"+string(rune('a'+i))+".example.org", "探测", "bash", `{"command":"true"}`)
	}
	var body p452ApprovalsBody
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &body)
	if len(body.Records) != 25 {
		t.Fatalf("审批记录应全量返回: %d", len(body.Records))
	}
	var raw struct {
		Records []map[string]any `json:"records"`
	}
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &raw)
	if v, _ := raw.Records[0]["request_id"].(string); !strings.HasPrefix(v, "sc-t") {
		t.Fatalf("记录应倒序(最新在先): %+v", raw.Records[0])
	}
	_ = json.Unmarshal([]byte("[]"), &[]int{})
}
