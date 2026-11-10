package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P1-08 完成标准(面板侧):发现可见、待确认带搁置理由 ----

// TestServerClaimsList 发现列表 API:读投影(节点+边)+ 状态机审计流
// 补足。铺垫按受控入口产出的事件形态直写账本,走完两条声明的完整
// 状态机(一 Confirmed 一待确认),断言面板视图逐字段。
func TestServerClaimsList(t *testing.T) {
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
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"发现列表验证"}`, http.StatusOK, &created)
	task := created["id"]

	// 铺垫:资产 + 发现步骤(u1)+ 两条候选声明(与 submit_test_claim
	// 受控入口产出同构:冻结标准/发现单位/血缘/资产锚点齐全)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "审计主站", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		// 声明 A:将走完全程 → Confirmed。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-a", NodeType: rings.NodeTestClaim, Title: "备份目录可公开匿名列举",
			Status: rings.TestClaimCandidate, Detail: map[string]any{
				"unit_id": "u1", "step_id": "s1", "impact": "备份文件可被未授权下载",
				"vulnclass": "信息暴露", "standard": arbiter.DefaultClaimStandard()}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-a", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-a", ToNode: "s1"}},
		// 声明 B:将走完状态机 → 待确认(搁置理由)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-b", NodeType: rings.NodeTestClaim, Title: "/api/version 公开暴露服务版本号",
			Status: rings.TestClaimCandidate, Detail: map[string]any{
				"unit_id": "u1", "step_id": "s1", "impact": "版本号可支撑定向利用",
				"vulnclass": "信息暴露", "standard": arbiter.DefaultClaimStandard()}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-b", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-b", ToNode: "s1"}},
	)

	// 声明 A 反证:u2(≠发现者 u1)写回结构化结论 → 仲裁者判 Confirmed。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "反证: 备份目录", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-a", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "tc-a", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-a", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecFalsify,
			ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high",
			EvidenceRef: "sha256:aa", EvidenceHead: "curl 重放 /old-backup/ 仍返回列举"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-a", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecVerdict, Satisfied: true,
			Conditions: []rings.ArbiterConditionResult{
				{Kind: "falsify_completed", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "existence_not_refuted", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "impact_supported", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "verifier_independent", Required: 1, Actual: 1, Satisfied: true},
			}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-a", Status: rings.TestClaimConfirmed}},
	)

	// 声明 B 反证:u3 写回"影响面证据不足"→ 待确认(R4 搁置理由)。
	appendEvents(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "反证: 版本暴露", Status: "done",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-b", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "tc-b", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u3", Payload: rings.TestClaimRecord{
			ClaimID: "tc-b", StepID: "s3", UnitID: "u3", Type: rings.TestClaimRecFalsify,
			ExistenceRefuted: false, ImpactSupported: false,
			EvidenceRef: "sha256:bb", EvidenceHead: "版本在但影响面证据不足"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-b", StepID: "s3", UnitID: "u3", Type: rings.TestClaimRecVerdict,
			PendingReason: "反证未推翻存在性,但影响面证据不足以支撑所声明的实际影响",
			Conditions: []rings.ArbiterConditionResult{
				{Kind: "falsify_completed", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "existence_not_refuted", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "impact_supported", Required: 1, Actual: 0, Satisfied: false},
				{Kind: "verifier_independent", Required: 1, Actual: 1, Satisfied: true},
			}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{
			NodeID: "tc-b", Status: rings.TestClaimPending}},
	)

	var claims []map[string]any
	get(t, h, "/api/tasks/"+task+"/claims", http.StatusOK, &claims)
	if len(claims) != 2 {
		t.Fatalf("应列 2 条发现: %+v", claims)
	}
	byID := map[string]map[string]any{}
	for _, c := range claims {
		byID[c["id"].(string)] = c
	}
	a, b := byID["tc-a"], byID["tc-b"]
	if a == nil || b == nil {
		t.Fatalf("两条声明都应可见: %+v", claims)
	}

	// Confirmed 条目:状态/双声明/锚点/发现者→验证者(R12 面板可见)。
	if a["status"] != rings.TestClaimConfirmed || a["statement"] != "备份目录可公开匿名列举" ||
		a["impact"] != "备份文件可被未授权下载" || a["vulnclass"] != "信息暴露" {
		t.Fatalf("Confirmed 条目字段不符: %+v", a)
	}
	// 立账时刻下发(P4-61 排序口径:nodes 投影 added_at,成果时间倒序的
	// 比较键;两条声明都有)。
	if aa, _ := a["added_at"].(string); aa == "" {
		t.Fatalf("Confirmed 条目应带立账时刻(P4-61): %+v", a)
	}
	if ba, _ := b["added_at"].(string); ba == "" {
		t.Fatalf("待确认条目应带立账时刻(P4-61): %+v", b)
	}
	if ids, _ := a["asset_ids"].([]any); len(ids) != 1 || ids[0] != "web1" {
		t.Fatalf("资产锚点不符: %+v", a["asset_ids"])
	}
	if a["discoverer_unit"] != "u1" || a["verifier_unit"] != "u2" || a["falsify_step"] != "s2" {
		t.Fatalf("发现者/验证者不符(R12): %+v", a)
	}
	if a["pending_reason"] != nil {
		t.Fatalf("Confirmed 条目不应带搁置理由: %+v", a)
	}

	// 待确认条目:必须带明确搁置理由(R4),发现者 ≠ 验证者。
	if b["status"] != rings.TestClaimPending {
		t.Fatalf("声明 B 应待确认: %+v", b)
	}
	reason, _ := b["pending_reason"].(string)
	if reason == "" || !strings.Contains(reason, "影响面") {
		t.Fatalf("待确认必须带明确搁置理由(R4): %q", reason)
	}
	if b["discoverer_unit"] == b["verifier_unit"] {
		t.Fatalf("R12:发现者与验证者不得相同: %+v", b)
	}

	// 不存在的任务 404;无发现任务返回空列表。
	var errBody map[string]string
	get(t, h, "/api/tasks/no-such/claims", http.StatusNotFound, &errBody)
	post(t, h, http.MethodPost, "/api/tasks", `{"task_type":"redteam","goals":["种子目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"title":"空发现任务"}`, http.StatusOK, &created)
	var empty []map[string]any
	get(t, h, "/api/tasks/"+created["id"]+"/claims", http.StatusOK, &empty)
	if len(empty) != 0 {
		t.Fatalf("无发现任务应返回空列表: %+v", empty)
	}
}

// appendEvents server 测试辅助:直写事件(受控入口产出形态)。
func appendEvents(t *testing.T, store *rings.Store, task string, events ...rings.Event) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("铺垫事件失败: %v", err)
	}
}
