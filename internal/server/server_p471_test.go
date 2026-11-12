package server

// P4-71(问题清单 #87 ④⑤⑥,RoE 复核修正批次第二阶·末阶)面板 API 侧:
// 退役资产复活三入口(卡上加入资产/RoE 勾回/反向键)、审批记录反向
// 操作五类(取消加入=摘除全量联动、恢复纳入不复活旧会话、攻击档双向、
// 撤销信息源)、弹门批准无反向键、反向后零僵尸对账。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p471SeedPentest 渗透任务种子(种子资产 + 范围外目标资产 a-ex)。
func p471SeedPentest(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "gitee.com:443",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
}

// p471ScopeCardOf 落一张该目标的待决卡(渗透命令拦截形态)。
func p471ScopeCardOf(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("gitee.com", "443"), StepID: "s-cover", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, Intent: "收集代码情报", ToolName: "bash",
			ToolArgs: json.RawMessage(`{"command":"curl https://gitee.com:443/x"}`),
		}},
	); err != nil {
		t.Fatal(err)
	}
}

// TestP471CardJoinRevives 完成标准①·卡上加入资产入口:目标经"允许访问"
// 退役后(信息源撤销重出卡形态)——卡上两键"加入资产"裁决,资产复活
// (retired→active 留痕)+入批准清单+资产图复现。
func TestP471CardJoinRevives(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p471-card-join"
	p471SeedPentest(t, store, task)
	// 允许访问退役(host 入信息源清单)→ 撤销信息源(粘性,重出卡)→
	// 重出待决卡。
	if err := swarm.AllowScopeAccess(ctx, store, task, "gitee.com", "测试", "creator"); err != nil {
		t.Fatal(err)
	}
	if a, _ := store.Node(ctx, task, "a-ex"); a.Status != rings.AssetStatusRetired {
		t.Fatalf("前置:应已退役: %s", a.Status)
	}
	if err := swarm.RevokeInfoSource(ctx, store, task, "gitee.com", "creator"); err != nil {
		t.Fatal(err)
	}
	p471ScopeCardOf(t, store, task)

	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-gitee.com:443",
		`{"approved":true}`, http.StatusOK, &out)
	a, _ := store.Node(ctx, task, "a-ex")
	if a.Status != "active" {
		t.Fatalf("卡上加入资产应复活退役目标: %s", a.Status)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.ScopeApprovedSet()["a-ex"] {
		t.Fatalf("复活应入批准清单: %+v", roe.ScopeApproved)
	}
	ag, _ := store.AssetGraph(ctx, task)
	found := false
	for _, n := range ag.Assets {
		if n.ID == "a-ex" {
			found = true
		}
	}
	if !found {
		t.Fatal("复活后资产应在资产图复现")
	}
	// 复活留痕 Note"纳入复活"。
	evs, _ := store.Events(ctx, task, 0, 0)
	note := false
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == "a-ex" &&
			p.Status == "active" && strings.Contains(p.Note, "纳入复活") {
			note = true
		}
	}
	if !note {
		t.Fatal("卡上加入资产入口复活应留痕(纳入复活)")
	}
}

// TestP471RoECheckinRevives 完成标准①·RoE 勾回入口:retired 资产(且因
// 历史脏数据并存于摘除清单)经 RoE 面板勾回 → 复活+入批准清单+互斥移回。
func TestP471RoECheckinRevives(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p471-checkin"
	p471SeedPentest(t, store, task)
	// 摘除(入排除清单)→ 允许访问退役(并存脏形态)。
	if _, err := swarm.ExcludeAssets(ctx, store, nil, task, []string{"a-ex"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if err := swarm.AllowScopeAccess(ctx, store, task, "gitee.com", "测试", "creator"); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe/assets",
		`{"excluded":[]}`, http.StatusOK, &out)
	a, _ := store.Node(ctx, task, "a-ex")
	if a.Status != "active" {
		t.Fatalf("RoE 勾回应复活退役资产: %s", a.Status)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if roe.ExcludedAssetSet()["a-ex"] {
		t.Fatalf("勾回复活应互斥移回(摘除清单清空): %+v", roe.ExcludedAssets)
	}
	if !roe.ScopeApprovedSet()["a-ex"] {
		t.Fatalf("勾回复活应入批准清单: %+v", roe.ScopeApproved)
	}
}

// TestP471ReverseKeyRevivesAndKeepsSessionsDead 完成标准①③·反向键入口
// +不复活旧会话:禁止测试(止损已杀步骤)→ 反向"恢复纳入"→ 资产复活入
// 范围;原相关会话不恢复不续跑(账本无新 session_started、步骤仍 aborted);
// reversed 记录留痕(含原记录引用);现值闭包含它(主宰重规划的确定性面)。
func TestP471ReverseKeyRevivesAndKeepsSessionsDead(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p471-rev-key"
	p471SeedPentest(t, store, task)
	// 意图覆盖目标的步骤(禁止测试时被止损终止)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-cover", NodeType: rings.NodeStep, Title: "测目标", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-cover", ToNode: "a-ex"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: "s-cover", UnitID: "u1", Type: rings.SwarmRecSessionStarted}},
	); err != nil {
		t.Fatal(err)
	}
	// 禁止测试(走真实两键裁决:入图资产已在,卡裁决 deny → 排除+止损)。
	p471ScopeCardOf(t, store, task)
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-gitee.com:443",
		`{"approved":false}`, http.StatusOK, &out)
	if s, _ := store.Node(ctx, task, "s-cover"); s.Status != "aborted" {
		t.Fatalf("前置:禁止测试应止损终止步骤: %s", s.Status)
	}
	revBefore, _ := store.CurrentRevision(ctx, task)

	// 反向"恢复纳入"。
	var rev map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-gitee.com:443/reverse",
		`{"action":"reinclude"}`, http.StatusOK, &rev)
	if msg, _ := rev["message"].(string); !strings.Contains(msg, "移回测试范围") && !strings.Contains(msg, "复活") {
		t.Fatalf("反向恢复纳入答复应含移回/复活: %+v", rev)
	}
	// 资产复活入范围。
	a, _ := store.Node(ctx, task, "a-ex")
	if a.Status != "active" {
		t.Fatalf("反向恢复纳入应复活资产: %s", a.Status)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.ScopeApprovedSet()["a-ex"] {
		t.Fatalf("反向恢复纳入应入批准清单: %+v", roe.ScopeApproved)
	}
	// 现值闭包含它(领取重规划自然覆盖——主宰新派意图的确定性面)。
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	closure := rings.BuildScopeClosure(rings.ScopeSeedsOf(assets), roe.ScopeApprovedSet(), roe.ExcludedAssetSet())
	if !closure.CoversTarget("gitee.com", "443", func(string) []string { return nil }) {
		t.Fatal("复活后现值闭包应含目标(主宰重规划可证)")
	}
	// 原相关会话不恢复不续跑:步骤仍 aborted、账本 session_started 仍
	//恰前置那 1 笔(禁止时已销毁,复活的是资产不是会话)。
	if s, _ := store.Node(ctx, task, "s-cover"); s.Status != "aborted" {
		t.Fatalf("恢复纳入不得复活旧会话(步骤应仍 aborted): %s", s.Status)
	}
	started := 0
	for _, ev := range mustSwarmEvents(t, store, task) {
		var p rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &p) == nil && p.Type == rings.SwarmRecSessionStarted {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("账本 session_started 应仍恰 1 笔(前置那笔),得 %d", started)
	}
	// reversed 记录留痕:含原记录引用。
	recs, _ := store.ApprovalRows(ctx, task)
	found := false
	for _, r := range recs {
		if r.Type == rings.ApprovalRecReversed && strings.Contains(r.Note, "反向 sc-gitee.com:443") {
			found = true
		}
	}
	if !found {
		t.Fatal("反向裁决应落 reversed 记录(含原记录引用)")
	}
	// 零僵尸:无未决卡、红点归零。
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("反向后红点应归零: %d", n)
	}
	if cards, _ := store.PendingScopeCards(ctx, task); len(cards) != 0 {
		t.Fatalf("反向后零僵尸卡: %+v", cards)
	}
	_ = revBefore
}

// mustSwarmEvents 任务的 swarm.record 事件流(测试用)。
func mustSwarmEvents(t *testing.T, store *rings.Store, task string) []rings.RawEvent {
	t.Helper()
	evs, err := store.EventsByKinds(context.Background(), task, rings.EventSwarmRecord)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// TestP471ReverseReincludeAlreadyInScope 完成标准③边角:该 host 此后经
// 新发现重新收编(已在范围——等效现值态经其他通道纳入)→ 反向键如实
// 答复"已在范围"、仅收口旧排除态零变更(版本推进恰 reversed 留痕一笔)。
func TestP471ReverseReincludeAlreadyInScope(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p471-already"
	p471SeedPentest(t, store, task)
	p471ScopeCardOf(t, store, task)
	// 两键裁决:先禁止(denied 记录)。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-gitee.com:443",
		`{"approved":false}`, http.StatusOK, &out)
	// 此后经其他通道重新收编(等效"经新发现重新收编"的现值态:资产
	// active+approved+不在排除清单)。
	roe0, _ := store.CurrentRoE(ctx, task)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{
			AllowedTools: roe0.AllowedTools, DeniedTools: roe0.DeniedTools,
			ApprovalThreshold: roe0.ApprovalThreshold, IntentRules: roe0.IntentRules,
			ScopeApproved: []string{"a-ex"}, Note: "新发现收编",
		},
	}); err != nil {
		t.Fatal(err)
	}
	revBefore, _ := store.CurrentRevision(ctx, task)

	var rev map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-gitee.com:443/reverse",
		`{"action":"reinclude"}`, http.StatusOK, &rev)
	if msg, _ := rev["message"].(string); !strings.Contains(msg, "已在测试范围") {
		t.Fatalf("边角应如实答复已在范围: %+v", rev)
	}
	revAfter, _ := store.CurrentRevision(ctx, task)
	// 零变更断言:版本推进恰 1(reversed 留痕一笔),范围面零变更
	//(approved/excluded/资产状态与反向前全等)。
	if revAfter-revBefore != 1 {
		t.Fatalf("边角应仅落 reversed 留痕一笔(范围面零变更): %d→%d", revBefore, revAfter)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.ScopeApprovedSet()["a-ex"] {
		t.Fatal("前置失效:目标应已在范围")
	}
	if a, _ := store.Node(ctx, task, "a-ex"); a.Status != "active" {
		t.Fatal("前置失效:目标应为 active")
	}
}

// TestP471ReverseUnincludeStopsIntentCover 完成标准②·取消加入=摘除全量:
// 卡上两键批准(加入资产)→ 反向"取消加入" → ExcludeAssets 全量联动
// (意图覆盖者销毁留痕、互斥清理)——与 RoE 摘除同源对账(P4-69 同一
// 联动链);顺带触碰者(意图不覆盖)不动、零僵尸。
func TestP471ReverseUnincludeStopsIntentCover(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p471-uninclude"
	// p469 同构种子(目标在批准列+双挂等步骤+待决卡)。
	p469SeedWaiters(t, store, task)
	// 卡上两键"加入资产"(真实裁决:目标入图转范围+挂等单位唤醒回队)。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-ex-target.com:80",
		`{"approved":true}`, http.StatusOK, &out)

	// 反向"取消加入"(原记录=该卡批准裁决)。
	var rev map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-ex-target.com:80/reverse",
		`{"action":"uninclude"}`, http.StatusOK, &rev)
	if msg, _ := rev["message"].(string); !strings.Contains(msg, "摘除") {
		t.Fatalf("取消加入应按摘除处理: %+v", rev)
	}
	// 与 RoE 摘除同源对账(P4-69 联动链):意图覆盖者终止、互斥(批准
	// 清单零残留、目标转排除态)、红点归零、零僵尸卡。
	sCover, _ := store.Node(ctx, task, "s-cover")
	if sCover.Status != "aborted" {
		t.Fatalf("覆盖者应被终止留痕: %+v", sCover.Status)
	}
	sTouch, _ := store.Node(ctx, task, "s-touch")
	if sTouch.Status != "dispatched" {
		t.Fatalf("顺带触碰者(意图不覆盖)不应被动: %+v", sTouch.Status)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	for _, id := range roe.ScopeApproved {
		if id == "a-ex" {
			t.Fatalf("取消加入后批准清单零残留: %+v", roe.ScopeApproved)
		}
	}
	found := false
	for _, id := range roe.ExcludedAssets {
		if id == "a-ex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("目标应转排除态: %+v", roe.ExcludedAssets)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("取消加入后红点应归零: %d", n)
	}
	if cards, _ := store.PendingScopeCards(ctx, task); len(cards) != 0 {
		t.Fatalf("取消加入后零僵尸卡: %+v", cards)
	}
	// reversed 留痕含原记录引用。
	recs, _ := store.ApprovalRows(ctx, task)
	found = false
	for _, r := range recs {
		if r.Type == rings.ApprovalRecReversed && strings.Contains(r.Note, "反向 sc-ex-target.com:80") {
			found = true
		}
	}
	if !found {
		t.Fatal("取消加入应落 reversed 记录(含原记录引用)")
	}
}

// TestP471ReverseAttackUnitFinished 完成标准④·server 侧:攻击卡裁决记录
// 的反向键,单位已终态(不在册)→ 键如实置灰(读面 unavailable)+ 调用
// 如实报错。
func TestP471ReverseAttackUnitFinished(t *testing.T) {
	srv, store, h := p469ServerEnv(t)
	const task = "p471-attack-dead"
	// 装配执行器(无在跑单位——攻击档登记表为空,单位 u9 恒不在册)。
	srv.Swarm = swarm.NewExecutor(store, nil, t.TempDir(), swarm.Budget{})
	// 攻击卡裁决记录(批准;单位 u9 已终态——不在册)。
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: "ap-deadbeef", StepID: "s1", UnitID: "u9",
			Type: rings.ApprovalRecApproved, ToolName: "bash", ActionLevel: "主动测试",
			Note:      "攻击行为卡:gitee.com:443 是信息源(查资料用的公开服务);批准=本会话内对该信息源的攻击动作放行",
			DecidedBy: "creator",
		}},
	); err != nil {
		t.Fatal(err)
	}
	// 读面:反向键在,但置灰(单位已结束)。
	var list struct {
		Records []struct {
			RequestID string `json:"request_id"`
			Type      string `json:"type"`
			Reverse   *struct {
				Action      string `json:"action"`
				Label       string `json:"label"`
				Unavailable string `json:"unavailable"`
			} `json:"reverse"`
		} `json:"records"`
	}
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &list)
	found := false
	for _, r := range list.Records {
		if r.RequestID != "ap-deadbeef" {
			continue
		}
		found = true
		if r.Reverse == nil || r.Reverse.Action != "unallow_att" || r.Reverse.Label != "撤销允许" {
			t.Fatalf("攻击卡批准应有反向键(撤销允许): %+v", r.Reverse)
		}
		if !strings.Contains(r.Reverse.Unavailable, "单位已结束") {
			t.Fatalf("单位已终态的反向键应置灰: %+v", r.Reverse)
		}
	}
	if !found {
		t.Fatal("记录未返回")
	}
	// 写面:调用如实报错。
	var rev map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/ap-deadbeef/reverse",
		`{"action":"unallow_att"}`, http.StatusConflict, &rev)
	_ = srv
}

// TestP471GateApprovalNoReverse 完成标准⑤:弹门批准(已执行的一次性动作)
// 无反向键——读面 null、写面拒绝。
func TestP471GateApprovalNoReverse(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	const task = "p471-gate-noop"
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		// 弹门请求 + 面板批准(普通弹门,Note 非攻击卡)。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: "ap-plain1", StepID: "s1", UnitID: "u1", Type: rings.ApprovalRecRequested,
			ToolName: "bash", ActionLevel: "修改与持久化", Intent: "注册账号"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: "ap-plain1", StepID: "s1", UnitID: "u1", Type: rings.ApprovalRecApproved,
			ToolName: "bash", ActionLevel: "修改与持久化", DecidedBy: "creator", Reason: "批准",
		}},
	); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Records []struct {
			RequestID string                 `json:"request_id"`
			Type      string                 `json:"type"`
			Reverse   map[string]interface{} `json:"reverse"`
		} `json:"records"`
	}
	get(t, h, "/api/tasks/"+task+"/approvals", http.StatusOK, &list)
	for _, r := range list.Records {
		if r.RequestID != "ap-plain1" {
			continue
		}
		if r.Type == rings.ApprovalRecApproved && r.Reverse != nil {
			t.Fatalf("弹门批准不得有反向键: %+v", r.Reverse)
		}
	}
	var rev map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/ap-plain1/reverse",
		`{"action":"uninclude"}`, http.StatusConflict, &rev)
}

// TestP471ReverseAccessRevokesInfoSource 完成标准⑥·存量 access 反向:
// sc- 形态(第三键裁决)与 ac- 形态(待批资产卡上的允许访问)各一 →
// 反向"撤销信息源"=RevokeInfoSource(清单移出+revoked 粘性标记),
// 资产不复活(撤销不自动复活,P4-68 既有语义)。
func TestP471ReverseAccessRevokesInfoSource(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p471-access-rev"
	p471SeedPentest(t, store, task)
	// 存量形态一:sc- 请求号的 access 记录(v1.2.3 历史第三键裁决),
	// host 在信息源清单、图上资产已退役。
	if err := swarm.GrantInfoSource(ctx, store, task, "gitee.com", "分诊依据", "swarm:u1"); err != nil {
		t.Fatal(err)
	}
	if err := swarm.AllowScopeAccess(ctx, store, task, "gitee.com", "退役", "creator"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("gitee.com", "443"), Type: rings.ApprovalRecAccess,
			DecidedBy: "creator", Note: "范围待批资产 gitee.com:443",
			Reason: "允许访问: gitee.com:443(只授访问权,不是测试目标)",
		}},
	); err != nil {
		t.Fatal(err)
	}
	// 存量形态二:ac- 请求号的 access 记录(待批资产卡上的允许访问)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: rings.AssetCardRecPrefix + "a-ex", Type: rings.ApprovalRecAccess,
			DecidedBy: "creator", Note: "范围待批资产 gitee.com:443",
			Reason: "允许访问: gitee.com:443(只授访问权,不是测试目标)",
		}},
	); err != nil {
		t.Fatal(err)
	}

	// 反向形态一(sc-)。
	var rev map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/sc-gitee.com:443/reverse",
		`{"action":"revoke_src"}`, http.StatusOK, &rev)
	if msg, _ := rev["message"].(string); !strings.Contains(msg, "撤销") {
		t.Fatalf("撤销信息源答复: %+v", rev)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if roe.InfoSourceHit("gitee.com") {
		t.Fatal("反向后 host 应移出信息源清单")
	}
	// 撤销粘性:tg- revoked 标记在账。
	rows, _ := store.ApprovalByRequest(ctx, task, rings.TriageCardID("gitee.com"))
	revoked := false
	for _, r := range rows {
		if strings.Contains(r.Note, "revoked") {
			revoked = true
		}
	}
	if !revoked {
		t.Fatal("撤销应落 revoked 粘性标记")
	}
	// 资产不复活(撤销信息源不自动复活资产,P4-68 注意事项既有)。
	if a, _ := store.Node(ctx, task, "a-ex"); a.Status != rings.AssetStatusRetired {
		t.Fatalf("撤销信息源不得自动复活资产: %s", a.Status)
	}
	// reversed 留痕。
	recs, _ := store.ApprovalRows(ctx, task)
	found := false
	for _, r := range recs {
		if r.Type == rings.ApprovalRecReversed && strings.Contains(r.Note, "反向 sc-gitee.com:443") {
			found = true
		}
	}
	if !found {
		t.Fatal("撤销信息源应落 reversed 记录")
	}

	// 反向形态二(ac-):host 已不在清单 → 如实答复零变更(可能已撤销过)。
	if err := swarm.GrantInfoSource(ctx, store, task, "gitee.com", "再标记", "creator"); err != nil {
		t.Fatal(err)
	}
	var rev2 map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/approvals/ac-a-ex/reverse",
		`{"action":"revoke_src"}`, http.StatusOK, &rev2)
	if msg, _ := rev2["message"].(string); !strings.Contains(msg, "撤销") {
		t.Fatalf("ac- 形态反向应同样撤销: %+v", rev2)
	}
	roe2, _ := store.CurrentRoE(ctx, task)
	if roe2.InfoSourceHit("gitee.com") {
		t.Fatal("ac- 形态反向后 host 应移出清单")
	}
}
