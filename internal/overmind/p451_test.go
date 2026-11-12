package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-51(问题清单 #73②③④⑦):范围等待对账唤醒＋队首插队＋红队卡 ----

// p451FreezeStep 种一个冻结在 scope_wait 的步骤(带目标标记,等价
// finishUnit 落账后的投影形态)。
func p451FreezeStep(t *testing.T, store *rings.Store, task, stepID, host, port string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "探测 " + host,
			Status: "dispatched", Detail: map[string]any{"goal_id": "g1"},
		}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: stepID, Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: stepID, Status: rings.StepScopeWait,
				Note: "scope_wait " + host + " " + port}},
	); err != nil {
		t.Fatal(err)
	}
}

// TestP451StepLaunchLessScopeHeadFirst 完成标准⑤:范围批准回队步骤排
// 队首——先于一切新排队者(含 high 优先级与反证)。
func TestP451StepLaunchLessScopeHeadFirst(t *testing.T) {
	plain := rings.NodeFull{NodeView: rings.NodeView{ID: "s-new"}, Detail: map[string]any{"priority": "high"}}
	head := rings.NodeFull{NodeView: rings.NodeView{ID: "s-woken"}, Detail: map[string]any{"scope_head": true}}
	if !StepLaunchLess(head, plain) {
		t.Fatal("范围批准回队步骤应排在 high 优先级新步骤之前(队首)")
	}
	falsify := rings.NodeFull{NodeView: rings.NodeView{ID: "s-fal"}, Detail: map[string]any{"kind": "falsify", "priority": "high"}}
	if !StepLaunchLess(head, falsify) {
		t.Fatal("范围批准回队步骤应排在反证之前(队首)")
	}
	// 同为范围批准回队:按创建序稳定排序。
	a := rings.NodeFull{NodeView: rings.NodeView{ID: "s-a", CreatedRev: 3}, Detail: map[string]any{"scope_head": true}}
	b := rings.NodeFull{NodeView: rings.NodeView{ID: "s-b", CreatedRev: 5}, Detail: map[string]any{"scope_head": true}}
	if !StepLaunchLess(a, b) {
		t.Fatal("同为队首标记应按创建序")
	}
}

// TestP451ResolveScopeWaitsWakeAndTerminate 完成标准①②③:对账唤醒
// (已入范围 → 回队队首)与收口(已排除 → 中止),幂等可重入。
func TestP451ResolveScopeWaitsWakeAndTerminate(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p451-resolve"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")

	// 范围外资产 evil9.com(已放行)+ 冻结步骤 s1 等待它。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-evil", NodeType: rings.NodeAsset, Title: "evil9.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-evil"}, Note: "加入资产"}},
	); err != nil {
		t.Fatal(err)
	}
	p451FreezeStep(t, store, task, "s1", "evil9.com", "")
	l := &Loop{TaskID: task, Store: store}

	// 对账:目标已入范围 → 唤醒回队队首(scope_head 标记进 detail)。
	woken, terminated := l.ResolveScopeWaits(ctx)
	if woken != 1 || terminated != 0 {
		t.Fatalf("应唤醒 1 终止 0: %d %d", woken, terminated)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "dispatched" {
		t.Fatalf("唤醒后步骤应回可领取: %+v", step)
	}
	if v, _ := step.Detail["scope_head"].(bool); !v {
		t.Fatalf("唤醒应带队首标记: %+v", step.Detail)
	}
	// 幂等:无等待步骤再对账零落笔。
	if woken, terminated := l.ResolveScopeWaits(ctx); woken != 0 || terminated != 0 {
		t.Fatalf("幂等对账应零变更: %d %d", woken, terminated)
	}

	// 反向:另一冻结步骤 s2 等待目标 evil9.com(意图锚定该资产——P4-69
	// 分族语义下挂等单位按意图覆盖收口,锚定保持"覆盖者终止"口径),
	// 随后该资产被排除(禁止测试/RoE 摘除同一机制)→ 对账收口中止。
	p451FreezeStep(t, store, task, "s2", "evil9.com", "8080")
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s2", ToNode: "a-evil"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-evil"}, Note: "禁止测试"}},
	); err != nil {
		t.Fatal(err)
	}
	woken, terminated = l.ResolveScopeWaits(ctx)
	if woken != 0 || terminated != 1 {
		t.Fatalf("应收口 1: %d %d", woken, terminated)
	}
	step2, _ := store.Node(ctx, task, "s2")
	if step2.Status != "aborted" {
		t.Fatalf("排除后等待步骤应中止: %+v", step2)
	}
}

// TestP451ResolveScopeWaitsUnrelatedTargetStays 完成标准②:挂等其他
// 目标的单位不受本次裁决影响(按目标各等各的)。
func TestP451ResolveScopeWaitsUnrelatedTargetStays(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p451-unrel"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x-target.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-x"}}},
	); err != nil {
		t.Fatal(err)
	}
	p451FreezeStep(t, store, task, "s1", "x-target.com", "")
	p451FreezeStep(t, store, task, "s2", "other-target.org", "")
	l := &Loop{TaskID: task, Store: store}
	woken, _ := l.ResolveScopeWaits(ctx)
	if woken != 1 {
		t.Fatalf("只应唤醒已入范围目标的步骤: %d", woken)
	}
	s2, _ := store.Node(ctx, task, "s2")
	if s2.Status != rings.StepScopeWait {
		t.Fatalf("无关目标应保持等待: %+v", s2)
	}
}

// TestP451RedteamVetoCardOnDiscovery 完成标准③④:红队范围外新资产入图
// 出可否决卡(同一目标一张;重复发现不堆叠;渗透任务不出发现卡——
// 拦截通道出卡);已裁过的目标不再出卡(防骚扰)。
func TestP451RedteamVetoCardOnDiscovery(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	// 红队任务:范围外新资产入图即出卡。
	p444Task(t, store, "t-red", rings.TaskTypeRedteam, "www.example.com")
	p444FactWithAsset(t, store, "t-red", "f-1", "sibling.example.com")
	if _, err := Harvest(ctx, store, "t-red", nil); err != nil {
		t.Fatal(err)
	}
	cards := p451ScopeCards(t, store, "t-red")
	if len(cards) != 1 || cards[0].RequestID != "sc-sibling.example.com" {
		t.Fatalf("红队范围外新资产应出一张可否决卡: %+v", cards)
	}
	if !strings.Contains(cards[0].Note, "红队任务") {
		t.Fatalf("红队卡说明应陈述先测可否决: %q", cards[0].Note)
	}

	// 同目标重复发现(第二事实入图锚定既有资产):不堆叠。
	p444FactWithAsset(t, store, "t-red", "f-2", "sibling.example.com")
	if _, err := Harvest(ctx, store, "t-red", nil); err != nil {
		t.Fatal(err)
	}
	if got := len(p451ScopeCards(t, store, "t-red")); got != 1 {
		t.Fatalf("同一目标重复发现不得堆叠卡: %d", got)
	}

	// 渗透任务:范围外新资产入图不出发现卡(锁定不测,拦截通道出卡)。
	p444Task(t, store, "t-pen", rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, "t-pen", "f-1", "sibling.example.com")
	if _, err := Harvest(ctx, store, "t-pen", nil); err != nil {
		t.Fatal(err)
	}
	if got := len(p451ScopeCards(t, store, "t-pen")); got != 0 {
		t.Fatalf("渗透任务发现入图不出卡(等拦截): %d", got)
	}
}

// p451ScopeCards 事件流里的范围待决卡。
func p451ScopeCards(t *testing.T, store *rings.Store, task string) []rings.ApprovalRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []rings.ApprovalRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var r rings.ApprovalRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.Type == rings.ApprovalRecScopeWait {
			out = append(out, r)
		}
	}
	return out
}

// TestP451ResolveClosesStaleCardViaOtherChannel 通道无关联动的卡面收口
// (P4-51 ⑥):目标经其他通道(资产页/手动资产)入范围后,对账唤醒挂等
// 单位的同时自动收口该目标的未决卡(批准语义,红点即消);显式卡两键
// 的裁决先落记录,对账零重复。
func TestP451ResolveClosesStaleCardViaOtherChannel(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p451-cardclose"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x-target.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// 冻结步骤 + 待决卡(拦截通道开的卡)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "探测 x", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: rings.StepScopeWait,
				Note: "scope_wait x-target.com "}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: "sc-x-target.com", StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash", Intent: "探测 x",
		}},
		// 其他通道解锁:资产页放行(scope_approved)。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-x"}, Note: "放行"}},
	); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	woken, _ := l.ResolveScopeWaits(ctx)
	if woken != 1 {
		t.Fatalf("应唤醒 1: %d", woken)
	}
	// 卡自动收口:同 RequestID 落批准记录(经其他通道),红点即消。
	rows, err := store.ApprovalByRequest(ctx, task, "sc-x-target.com")
	if err != nil || len(rows) != 2 {
		t.Fatalf("卡面应有恰 2 笔(卡+收口): %+v %v", rows, err)
	}
	if rows[1].Type != rings.ApprovalRecApproved || rows[1].DecidedBy != "creator" ||
		!strings.Contains(rows[1].Reason, "其他通道") {
		t.Fatalf("收口记录不符: %+v", rows[1])
	}
	// 幂等:再对账零重复收口。
	_, _ = l.ResolveScopeWaits(ctx)
	rows2, _ := store.ApprovalByRequest(ctx, task, "sc-x-target.com")
	if len(rows2) != 2 {
		t.Fatalf("重复对账不得重复收口: %d 笔", len(rows2))
	}
}
