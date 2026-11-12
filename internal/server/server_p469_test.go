package server

// P4-69(问题清单 #86 D,摘除联动收口与状态一致性)面板 API 侧:
// RoE 面板摘除的瞬间联动收口(挂等冻结单位分族收口+未决卡拒绝收口+
// 排除与批准互斥),与卡上禁止测试、牧者摘除三通道等效对账。

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p469ServerEnv 面板后端(无在跑循环,对账走裸循环兜底路径)。
func p469ServerEnv(t *testing.T) (*Server, *rings.Store, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	gate := swarm.NewLedgerGate(store)
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatal(err)
	}
	return srv, store, srv.Handler()
}

// p469SeedWaiters 渗透任务同构种子:种子资产 + 目标资产 a-ex(范围外,
// 已在批准列——验证摘除互斥)+ 挂等步骤 s-cover(意图覆盖目标)与
// s-touch(锚定种子,顺带触碰)+ 一张该目标的待决卡。
func p469SeedWaiters(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "ex-target.com:80",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// 旧形态脏起点:目标既在批准列(摘除端应同步剔除)。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-ex"}, Note: "加入资产"}},
		// s-cover:意图锚定目标(覆盖者)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-cover", NodeType: rings.NodeStep, Title: "测目标", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-cover", ToNode: "a-ex"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s-cover", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s-cover", Status: rings.StepScopeWait,
				Note: "scope_wait ex-target.com 80"}},
		// s-touch:意图锚定种子(顺带触碰者,挂等只因一条连通性命令)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-touch", NodeType: rings.NodeStep, Title: "范围内工作", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-touch", ToNode: "a-seed"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s-touch", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s-touch", Status: rings.StepScopeWait,
				Note: "scope_wait ex-target.com 80"}},
		// 待决卡(渗透命令拦截形态)。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("ex-target.com", "80"), StepID: "s-cover", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, Intent: "探测目标", ToolName: "bash",
			ToolArgs: json.RawMessage(`{"command":"curl http://ex-target.com:80/x"}`),
		}},
	); err != nil {
		t.Fatal(err)
	}
}

// p469AssertClosed 联动收口的统一断言(三通道等效的逐类对账面):
// s-cover 终止留痕、s-touch 唤醒回队队首、卡按拒绝语义收口且红点归零、
// 排除与批准互斥(批准清单零残留)。
func p469AssertClosed(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	sCover, _ := store.Node(ctx, task, "s-cover")
	if sCover.Status != "aborted" {
		t.Fatalf("[%s] 覆盖者应终止留痕: %+v", task, sCover.Status)
	}
	sTouch, _ := store.Node(ctx, task, "s-touch")
	if sTouch.Status != "dispatched" {
		t.Fatalf("[%s] 顺带触碰者应唤醒回队: %+v", task, sTouch.Status)
	}
	if v, _ := sTouch.Detail["scope_head"].(bool); !v {
		t.Fatalf("[%s] 唤醒应带队首标记: %+v", task, sTouch.Detail)
	}
	rows, _ := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("ex-target.com", "80"))
	if len(rows) == 0 {
		t.Fatalf("[%s] 应有卡记录", task)
	}
	last := rows[len(rows)-1]
	if last.Type != rings.ApprovalRecDenied || last.DenyLine != rings.DenyLineRoeScope {
		t.Fatalf("[%s] 卡应按拒绝语义收口: type=%s deny=%s", task, last.Type, last.DenyLine)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("[%s] 卡消红点消: %d", task, n)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	for _, id := range roe.ScopeApproved {
		if id == "a-ex" {
			t.Fatalf("[%s] 排除后批准清单零残留: %+v", task, roe.ScopeApproved)
		}
	}
	found := false
	for _, id := range roe.ExcludedAssets {
		if id == "a-ex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("[%s] 目标应转排除态: %+v", task, roe.ExcludedAssets)
	}
}

// TestP469RoERemoveClosesWaitersAndCards 完成标准①·RoE 面板通道:
// 摘除目标 → 挂等冻结单位分族收口(覆盖者终止留痕、不覆盖者唤醒换路)
// + 卡按目标类拒绝收口留痕(卡消红点消)+ 批准清单同步剔除(互斥)。
func TestP469RoERemoveClosesWaitersAndCards(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	const task = "p469-roe"
	p469SeedWaiters(t, store, task)

	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/"+task+"/roe/assets",
		`{"excluded":["a-ex"]}`, http.StatusOK, &out)
	p469AssertClosed(t, store, task)
}

// TestP469AccessClosedCardNotCounted 红点计数与显示层口径一致(P4-68
// 遗留口径分裂收口):access(允许访问)裁决收口的卡不再计数——待决区
// 显示层早已消卡,计数层 EXCEPT 此前不含 access,僵尸红点由本阶段
// 状态一致性主题顺手根治。
func TestP469AccessClosedCardNotCounted(t *testing.T) {
	_, store, _ := p469ServerEnv(t)
	ctx := context.Background()
	const task = "p469-access-count"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("a.com", "80"), Type: rings.ApprovalRecScopeWait}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("a.com", "80"), Type: rings.ApprovalRecAccess}},
	); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("access 收口的卡不应计数: %d", n)
	}
	if cards, _ := store.PendingScopeCards(ctx, task); len(cards) != 0 {
		t.Fatalf("access 收口的卡不在未决列: %+v", cards)
	}
}

// TestP469ThreeChannelParity 完成标准①③·三通道等效:同构任务分别经
// RoE 面板摘除、卡上禁止测试、牧者摘除(set_asset_scope exclude 的同一
// 执行链:swarm.ExcludeAssets + 对账)——三路联动行为逐类一致(同源
// 对账)。
func TestP469ThreeChannelParity(t *testing.T) {
	_, store, h := p469ServerEnv(t)
	ctx := context.Background()
	channels := []string{"p469-ch-roe", "p469-ch-card", "p469-ch-shep"}
	for _, task := range channels {
		p469SeedWaiters(t, store, task)
	}
	// 通道 1:RoE 面板摘除。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p469-ch-roe/roe/assets",
		`{"excluded":["a-ex"]}`, http.StatusOK, &out)
	// 通道 2:卡上禁止测试。
	post(t, h, http.MethodPost, "/api/tasks/p469-ch-card/approvals/sc-ex-target.com:80",
		`{"approved":false}`, http.StatusOK, &out)
	// 通道 3:牧者摘除(set_asset_scope exclude 同一执行链——服务无
	// 在跑循环时对账走裸 Loop,与 shepherd.resolveScopeWakes 兜底同款)。
	if _, err := swarm.ExcludeAssets(ctx, store, nil, "p469-ch-shep", []string{"a-ex"}, "shepherd"); err != nil {
		t.Fatal(err)
	}
	l := &overmind.Loop{TaskID: "p469-ch-shep", Store: store}
	l.ResolveScopeWaits(ctx)
	// 三路统一断言。
	for _, task := range channels {
		p469AssertClosed(t, store, task)
	}
}
