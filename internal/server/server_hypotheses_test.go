package server

import (
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P2-03 完成标准(面板侧):假设列表与详情可见节点、回流轨迹边与
// 完整时间线(提交 → 主宰评估三去向 → 验证结论 → 状态迁移)。 ----

// seedHypotheses 铺一条全链路假设(提交 → 转验证 → 成立)+ 挂账 + 废弃
// 各一条(与 P2-03 受控入口产出的事件形态同构)。
func seedHypotheses(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web2", NodeType: rings.NodeAsset, Title: "http://mock:8081", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "审计主站", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		// h1 全链路:候选 → 转验证(评估 + 验证 Step + verifies 边)→
		// 独立单位结论 supported → 收割迁移 confirmed。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h1", NodeType: rings.NodeHypothesis, Title: "web2 疑似管理后台",
			Status: rings.HypothesisCandidate,
			Detail: map[string]any{"basis": "独立端口形态", "unit_id": "u1", "step_id": "s1"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "h1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "h1", ToNode: "web2"}},
		rings.Event{Kind: rings.EventHypothesisRecord, Actor: "overmind", Payload: rings.HypothesisRecord{
			HypothesisID: "h1", StepID: "s2", Type: rings.HypothesisRecEvaluation,
			Decision: rings.HypothesisVerify, Reason: "可低成本证伪", VerifyIntent: "探测 web2 根路径"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "验证假设: web2 疑似管理后台", Status: "dispatched",
			Detail: map[string]any{"kind": "verify", "hypothesis_id": "h1", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeVerifies, FromNode: "s2", ToNode: "h1"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "h1", Status: rings.HypothesisTesting}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2", Payload: rings.SwarmRecord{
			StepID: "s2", UnitID: "u2", Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", Payload: rings.NodeStateChanged{
			NodeID: "s2", Status: "done"}},
		rings.Event{Kind: rings.EventHypothesisRecord, Actor: "swarm:u2", Payload: rings.HypothesisRecord{
			HypothesisID: "h1", StepID: "s2", UnitID: "u2", Type: rings.HypothesisRecResult,
			Verdict: rings.HypothesisSupported, EvidenceHead: "401 + mock admin console"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "h1", Status: rings.HypothesisConfirmed}},
		// h2 挂账(带理由);h3 废弃(带理由,R10)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "h3", NodeType: rings.NodeHypothesis, Title: "config.bak 可能含凭据",
			Status: rings.HypothesisCandidate,
			Detail: map[string]any{"basis": "文件名风格", "unit_id": "u1", "step_id": "s1"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "h3"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "h3", ToNode: "web1"}},
		rings.Event{Kind: rings.EventHypothesisRecord, Actor: "overmind", Payload: rings.HypothesisRecord{
			HypothesisID: "h3", Type: rings.HypothesisRecEvaluation,
			Decision: rings.HypothesisDiscard, Reason: "无具体可验证线索"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "h3", Status: rings.HypothesisDiscarded}},
	)
}

func TestServerHypotheses(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	_, h := newClaimTestServer(t, store, gate, dir)

	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"假设验证"}`, http.StatusOK, &created)
	task := created["id"]
	seedHypotheses(t, store, task)

	// 列表:节点 + 状态 + 裁决/结论摘要 + 提出者。
	var list []map[string]any
	get(t, h, "/api/tasks/"+task+"/hypotheses", http.StatusOK, &list)
	if len(list) != 2 {
		t.Fatalf("应恰 2 条假设(挂账一条未铺,列表=成立+废弃): %+v", list)
	}
	byID := map[string]map[string]any{}
	for _, hv := range list {
		byID[hv["id"].(string)] = hv
	}
	h1 := byID["h1"]
	if h1["status"] != rings.HypothesisConfirmed || h1["verdict"] != rings.HypothesisSupported ||
		h1["decision"] != rings.HypothesisVerify || h1["verify_step"] != "s2" ||
		h1["verifier_unit"] != "u2" || h1["discoverer_unit"] != "u1" || h1["discover_step"] != "s1" {
		t.Fatalf("h1 列表视图不符: %+v", h1)
	}
	// 立账时刻下发(P4-61 排序口径:假设页三区时间倒序的比较键)。
	if at, _ := h1["added_at"].(string); at == "" {
		t.Fatalf("h1 应带立账时刻(P4-61): %+v", h1)
	}
	if at, _ := byID["h3"]["added_at"].(string); at == "" {
		t.Fatalf("h3 应带立账时刻(P4-61): %+v", byID["h3"])
	}
	if ids, _ := h1["asset_ids"].([]any); len(ids) != 1 || ids[0] != "web2" {
		t.Fatalf("h1 应锚定 web2: %+v", h1["asset_ids"])
	}
	h3 := byID["h3"]
	if h3["status"] != rings.HypothesisDiscarded || h3["decision"] != rings.HypothesisDiscard ||
		!strings.Contains(h3["reason"].(string), "线索") {
		t.Fatalf("h3 废弃视图应带理由(R10): %+v", h3)
	}

	// 详情:完整时间线 + 回流轨迹(全程类型化边)。
	var d map[string]any
	get(t, h, "/api/tasks/"+task+"/hypotheses/h1", http.StatusOK, &d)
	tl, _ := d["timeline"].([]any)
	var kinds []string
	for _, e := range tl {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	if got := strings.Join(kinds, ","); got != "created,evaluation,state,result,state" {
		t.Fatalf("h1 时间线不符: %s", got)
	}
	traj := d["trajectory"].(map[string]any)
	if traj["discover_step"] != "s1" {
		t.Fatalf("回流轨迹缺提出步骤: %+v", traj)
	}
	if ids, _ := traj["assets"].([]any); len(ids) != 1 || ids[0] != "web2" {
		t.Fatalf("轨迹资产锚点不符: %+v", traj["assets"])
	}
	vs, _ := traj["verify_steps"].([]any)
	if len(vs) != 1 {
		t.Fatalf("轨迹应恰 1 个验证步骤: %+v", vs)
	}
	v0 := vs[0].(map[string]any)
	if v0["step_id"] != "s2" || v0["status"] != "done" || v0["unit"] != "u2" ||
		v0["verdict"] != rings.HypothesisSupported {
		t.Fatalf("验证步骤视图不符: %+v", v0)
	}

	// 404:任务外/不存在假设。
	get(t, h, "/api/tasks/"+task+"/hypotheses/h-404", http.StatusNotFound, nil)
}
