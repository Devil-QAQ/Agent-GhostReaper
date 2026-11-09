package overmind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

// ---- P4-54(问题清单 #78＋#73⑧):牧者最高权限操作面的机器侧 ----
// 派发守卫(通配摘除/方向聚焦/已删目标)、停向引擎(停聚焦与目标删除
// 共用)、优先级落账、目标修改(完成条件重新冻结)、收割跳过(聚焦期
// 其他方向的声明不报错停在原地)。

// p454Goal 种一个目标(可选谓词)。
func p454Goal(t *testing.T, store *rings.Store, task, id, title string, predicate map[string]any) {
	t.Helper()
	detail := map[string]any{}
	if predicate != nil {
		detail["predicate"] = predicate
	}
	if err := SeedGoal(context.Background(), store, task, id, title, detail); err != nil {
		t.Fatal(err)
	}
}

// p454Step 种一个步骤(状态/归属目标/可选 detail)。
func p454Step(t *testing.T, store *rings.Store, task, id, title, status, goalID string, detail map[string]any) {
	t.Helper()
	d := map[string]any{"action_level": "只读探测"}
	if goalID != "" {
		d["goal_id"] = goalID
	}
	for k, v := range detail {
		d[k] = v
	}
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: ActorOvermind,
		Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeStep, Title: title, Status: status, Detail: d},
	}); err != nil {
		t.Fatal(err)
	}
}

func p454Task(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "created"},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestP454FocusDispatchGuard 方向聚焦派发守卫:非聚焦方向的探索与反证
// 拒收(ErrFocusOutside)、急活豁免;已删目标拒收(ErrGoalDiscarded);
// 解除聚焦后恢复。
func TestP454FocusDispatchGuard(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-guard"
	p454Task(t, store, task)
	p454Goal(t, store, task, "g1", "聚焦方向", nil)
	p454Goal(t, store, task, "g2", "其他方向", nil)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}},
	}); err != nil {
		t.Fatal(err)
	}

	mk := func(goal string, source dispatchSource) DispatchOptions {
		return DispatchOptions{GoalID: goal, ActionLevel: "只读探测", CoversAssets: []string{"a-x"}, source: source}
	}

	// 无聚焦:g2 探索照常可派。
	if _, err := stepEvents(ctx, store, task, "s-ok", "探测", mk("g2", sourceExplore)); err != nil {
		t.Fatalf("无聚焦时 g2 不应被拒: %v", err)
	}
	// 聚焦 g1。
	if _, err := FocusGoal(ctx, store, nil, task, "g1", "shepherd"); err != nil {
		t.Fatal(err)
	}
	if _, err := stepEvents(ctx, store, task, "s-bad", "探测", mk("g2", sourceExplore)); !errors.Is(err, ErrFocusOutside) {
		t.Fatalf("聚焦期间 g2 探索应被拒(ErrFocusOutside): %v", err)
	}
	if _, err := stepEvents(ctx, store, task, "s-fal", "反证", mk("g2", sourceFalsify)); !errors.Is(err, ErrFocusOutside) {
		t.Fatalf("聚焦期间 g2 反证应被拒: %v", err)
	}
	// 急活豁免:操作者指令高于聚焦纪律。
	if _, err := stepEvents(ctx, store, task, "s-urg", "急活", mk("g2", sourceUrgent)); err != nil {
		t.Fatalf("聚焦期间急活应豁免: %v", err)
	}
	// 聚焦方向自身照常。
	if _, err := stepEvents(ctx, store, task, "s-in", "聚焦方向探测", mk("g1", sourceExplore)); err != nil {
		t.Fatalf("聚焦方向 g1 不应被拒: %v", err)
	}
	// 解除聚焦后恢复。
	if _, err := UnfocusGoal(ctx, store, task, "shepherd"); err != nil {
		t.Fatal(err)
	}
	if _, err := stepEvents(ctx, store, task, "s-ok2", "探测", mk("g2", sourceExplore)); err != nil {
		t.Fatalf("解除聚焦后 g2 不应被拒: %v", err)
	}

	// 已删目标:g3 删除后绑定 g3 的派发拒收。
	p454Goal(t, store, task, "g3", "要删的方向", nil)
	if _, err := DeleteGoal(ctx, store, nil, task, "g3", "测试删除", "shepherd"); err != nil {
		t.Fatal(err)
	}
	if _, err := stepEvents(ctx, store, task, "s-del", "探测", mk("g3", sourceExplore)); !errors.Is(err, ErrGoalDiscarded) {
		t.Fatalf("已删目标应被拒(ErrGoalDiscarded): %v", err)
	}
}

// TestP454WildcardExclusionDispatchGuard 通配摘除派发守卫:`*.域` 摘除
// 覆盖其后新发现的子域资产(ID 精确命中不覆盖的缺口补确定性判定)。
func TestP454WildcardExclusionDispatchGuard(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-wild"
	p454Task(t, store, task)
	p454Goal(t, store, task, "g1", "目标", nil)
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-wild", NodeType: rings.NodeAsset, Title: "*.banned.org", Status: "active",
			AssetClass: rings.AssetDomain,
			Detail:     map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-sub", NodeType: rings.NodeAsset, Title: "sub.banned.org", Status: "active",
			AssetClass: rings.AssetSubdomain,
			Detail:     map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ok", NodeType: rings.NodeAsset, Title: "fine.example.com", Status: "active",
			AssetClass: rings.AssetDomain,
			Detail:     map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "shepherd",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-wild"}, Note: "摘除 *.banned.org"}},
	); err != nil {
		t.Fatal(err)
	}
	opt := DispatchOptions{GoalID: "g1", ActionLevel: "只读探测", CoversAssets: []string{"a-sub"}}
	if _, err := stepEvents(ctx, store, task, "s1", "探测子域", opt); err == nil ||
		!strings.Contains(err.Error(), "命中摘除覆盖写法") {
		t.Fatalf("通配摘除应拒派子域: %v", err)
	}
	opt2 := DispatchOptions{GoalID: "g1", ActionLevel: "只读探测", CoversAssets: []string{"a-ok"}}
	if _, err := stepEvents(ctx, store, task, "s2", "探测其他", opt2); err != nil {
		t.Fatalf("未命中通配的资产照常可派: %v", err)
	}
	// 精确 ID 摘除照旧(既有语义回归)。
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventRoeUpdated, Actor: "shepherd",
		Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-wild", "a-ok"}, Note: "再摘 a-ok"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stepEvents(ctx, store, task, "s3", "探测被摘", opt2); err == nil ||
		!strings.Contains(err.Error(), "已从授权范围摘除") {
		t.Fatalf("精确摘除应拒派: %v", err)
	}
}

// TestP454FocusGoalStopsOthers 停聚焦:非聚焦方向排队步骤取消留痕、
// 反证声明挂起(证据不足语义:pending+理由「方向被收窄」)、聚焦方向
// 照常;解除聚焦唤醒(经 reopen 派新一轮反证)。
func TestP454FocusGoalStopsOthers(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-focus-stop"
	p454Task(t, store, task)
	p454Goal(t, store, task, "g1", "只做这个", nil)
	p454Goal(t, store, task, "g2", "其他方向", nil)

	// g1 的排队步骤 + g2 的排队步骤 + g2 名下发现步骤/判断/反证步骤。
	p454Step(t, store, task, "s1", "聚焦方向探测", "dispatched", "g1", nil)
	p454Step(t, store, task, "s2", "其他方向探测", "dispatched", "g2", nil)
	p454Step(t, store, task, "s-disc2", "发现步骤", "done", "g2", nil)
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc1", NodeType: rings.NodeTestClaim, Title: "其他方向的判断", Status: "testing"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc1", ToNode: "s-disc2"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc1", ToNode: "a-x"}},
	); err != nil {
		t.Fatal(err)
	}
	p454Step(t, store, task, "s2f", "反证(第 1 轮)", "dispatched", "g2",
		map[string]any{"kind": "falsify", "testclaim_id": "tc1", "round": 1})

	res, err := FocusGoal(ctx, store, nil, task, "g1", "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AbortedSteps) != 2 {
		t.Fatalf("应取消 g2 的 2 条排队步骤(探测+反证): %+v", res.AbortedSteps)
	}
	if len(res.SuspendedClaims) != 1 || res.SuspendedClaims[0] != "tc1" {
		t.Fatalf("应挂起判断 tc1: %+v", res.SuspendedClaims)
	}
	if st, _ := store.Node(ctx, task, "s1"); st.Status != "dispatched" {
		t.Fatalf("聚焦方向的步骤不应被动: %+v", st)
	}
	if st, _ := store.Node(ctx, task, "s2"); st.Status != "cancelled" {
		t.Fatalf("其他方向排队步骤应取消留痕: %+v", st)
	}
	tc, _ := store.Node(ctx, task, "tc1")
	if tc.Status != rings.TestClaimPending {
		t.Fatalf("被取消反证的判断应挂起: %+v", tc)
	}
	if FocusGoalID(ctx, store, task) != "g1" {
		t.Fatal("聚焦标记应在 g1")
	}
	// 收割跳过:聚焦期间 HarvestClaims 对 g2 名下的合格候选不再派反证
	// ——按聚焦跳过不报错,候选停在原地。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc2", NodeType: rings.NodeTestClaim, Title: "候选判断", Status: "candidate",
			Detail: map[string]any{
				"unit_id":  "u1",
				"standard": map[string]any{"kind": "full_falsify", "require_impact": false},
			}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc2", ToNode: "s-disc2"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc2", ToNode: "a-x"}},
	); err != nil {
		t.Fatal(err)
	}
	hres, herr := HarvestClaims(ctx, store, task)
	if herr != nil {
		t.Fatalf("聚焦期间收割不应报错: %v", herr)
	}
	skipped := false
	for _, sk := range hres.Skipped {
		if sk.FactID == "tc2" && strings.Contains(sk.Reason, "方向聚焦中") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("g2 候选应按聚焦跳过: %+v", hres.Skipped)
	}
	if tc2, _ := store.Node(ctx, task, "tc2"); tc2.Status != "candidate" {
		t.Fatalf("跳过的候选应停在原地: %+v", tc2)
	}

	// 解除聚焦:挂起判断经 reopen 唤醒(回反证中,新一轮反证派发)。
	if _, err := UnfocusGoal(ctx, store, task, "shepherd"); err != nil {
		t.Fatal(err)
	}
	tc, _ = store.Node(ctx, task, "tc1")
	if tc.Status != rings.TestClaimTesting {
		t.Fatalf("解除聚焦后挂起判断应回反证中: %+v", tc)
	}
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	round2 := false
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id == "tc1" {
			if r, _ := s.Detail["round"].(float64); int(r) >= 2 {
				round2 = true
			}
		}
	}
	if !round2 {
		t.Fatal("reopen 应派新一轮反证")
	}
}

// TestP454DeleteGoalStopsDirection 目标删除:该方向排队步骤取消、目标
// 转已删除、影响预览计数、后续零新派;受影响判断程序不动状态(P4-55
// 审核迭代:交主宰判定,禁止连坐)——被取消的反证轮成待裁决中断,
// 声明留锤炼中,收割不重派,快照「待裁决判断」区可见。
func TestP454DeleteGoalStopsDirection(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-del"
	p454Task(t, store, task)
	p454Goal(t, store, task, "g1", "保留方向", nil)
	p454Goal(t, store, task, "g2", "要删的方向", nil)
	p454Step(t, store, task, "s1", "保留探测", "dispatched", "g1", nil)
	p454Step(t, store, task, "s2", "删除方向探测", "dispatched", "g2", nil)
	p454Step(t, store, task, "s-disc2", "发现步骤", "done", "g2", nil)
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc1", NodeType: rings.NodeTestClaim, Title: "判断", Status: "testing",
			Detail: map[string]any{
				"unit_id":  "u1",
				"standard": arbiter.DefaultClaimStandard(),
			}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc1", ToNode: "s-disc2"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc1", ToNode: "a-x"}},
	); err != nil {
		t.Fatal(err)
	}
	p454Step(t, store, task, "s2f", "反证(第 1 轮)", "dispatched", "g2",
		map[string]any{"kind": "falsify", "testclaim_id": "tc1", "round": 1})

	// 影响预览:1 排队探测 + 1 排队反证。
	imp, err := DirectionImpactOf(ctx, store, task, "g2")
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Queued) != 2 || len(imp.Units) != 0 {
		t.Fatalf("影响预览应计 2 条排队 0 个在跑: %+v", imp)
	}

	res, err := DeleteGoal(ctx, store, nil, task, "g2", "不要了", "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AbortedSteps) != 2 || len(res.SuspendedClaims) != 0 {
		t.Fatalf("删除应取消 2 条排队且程序不挂起判断(P4-55 审核迭代): %+v", res)
	}
	g2, _ := store.Node(ctx, task, "g2")
	if g2.Status != GoalStatusDiscarded {
		t.Fatalf("目标应转已删除: %+v", g2)
	}
	if st, _ := store.Node(ctx, task, "s1"); st.Status != "dispatched" {
		t.Fatalf("其他方向不受影响: %+v", st)
	}
	// 受影响判断零程序改态:留锤炼中;被取消的反证轮成待裁决中断——
	// 收割不重派、裁决不落终态,快照「待裁决判断」区可见。
	tc, _ := store.Node(ctx, task, "tc1")
	if tc.Status != rings.TestClaimTesting {
		t.Fatalf("删除后判断应留锤炼(交主宰判定,禁止连坐): %+v", tc)
	}
	hres, err := HarvestClaims(ctx, store, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(hres.Redispatched) != 0 {
		t.Fatalf("操作者取消的反证轮不应程序重派: %+v", hres.Redispatched)
	}
	l := &Loop{TaskID: task, Store: store}
	if n, err := l.judgeClaims(ctx); err != nil || n != 0 {
		t.Fatalf("待裁决中断不应产生裁决: n=%d err=%v", n, err)
	}
	goals, _ := store.NodesByType(ctx, task, rings.NodeGoal, "")
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	abouts, _ := store.Edges(ctx, task, rings.EdgeAbout)
	lines, err := l.pendingRulingBriefs(ctx, goals, steps, abouts)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "tc1") || !strings.Contains(lines[0], "已无进行中目标覆盖") {
		t.Fatalf("待裁决判断应入快照(资产无人需要事实行): %+v", lines)
	}
	// 聚焦别的方向再解除:判断仍留锤炼(不是聚焦挂起,不受聚焦唤醒影响)。
	if _, err := FocusGoal(ctx, store, nil, task, "g1", "shepherd"); err != nil {
		t.Fatal(err)
	}
	if _, err := UnfocusGoal(ctx, store, task, "shepherd"); err != nil {
		t.Fatal(err)
	}
	if tc, _ = store.Node(ctx, task, "tc1"); tc.Status != rings.TestClaimTesting {
		t.Fatalf("待裁决判断不受聚焦切换影响: %+v", tc)
	}
	// 重复删除:如实拒绝。
	if _, err := DeleteGoal(ctx, store, nil, task, "g2", "", "shepherd"); err == nil {
		t.Fatal("重复删除应拒绝")
	}
}

// TestP454SetStepsPriority 优先级直接落账:队列比较器立即按新优先级;
// 已领取(非 dispatched)步骤不动;非法词汇拒绝;空串回默认。
func TestP454SetStepsPriority(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-prio"
	p454Task(t, store, task)
	p454Goal(t, store, task, "g1", "目标", nil)
	p454Step(t, store, task, "s-early", "早排队", "dispatched", "g1", nil)
	p454Step(t, store, task, "s-late", "晚排队", "dispatched", "g1", nil)
	p454Step(t, store, task, "s-run", "在跑", "running", "g1", nil)

	// 初始:创建序早者先领。
	a, _ := store.Node(ctx, task, "s-early")
	b, _ := store.Node(ctx, task, "s-late")
	if !StepLaunchLess(*a, *b) {
		t.Fatal("初始应按创建序")
	}
	changed, err := SetStepsPriority(ctx, store, task, []string{"s-late", "s-run"}, "high", "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "s-late" {
		t.Fatalf("只应改排队步骤(在跑不动): %+v", changed)
	}
	b2, _ := store.Node(ctx, task, "s-late")
	a2, _ := store.Node(ctx, task, "s-early")
	if !StepLaunchLess(*b2, *a2) {
		t.Fatal("高优先级应立即排前")
	}
	if p, _ := a2.Detail["priority"].(string); p == "high" {
		t.Fatal("未调步骤不应变")
	}
	if _, err := SetStepsPriority(ctx, store, task, []string{"s-early"}, "urgent", "shepherd"); err == nil {
		t.Fatal("非法优先级应拒绝")
	}
	// 方向批量:goal_id 给全排队步骤。
	changed, err = SetStepsPriority(ctx, store, task, nil, "low", "shepherd")
	if err != nil || len(changed) != 0 {
		t.Fatalf("空清单应零变更: %+v %v", changed, err)
	}
}

// TestP454AmendGoalPredicateRefreezes 目标修改:完成条件重新冻结——
// 仲裁者按最后显式冻结值求值;未带谓词的修改不动谓词;已删目标拒改。
func TestP454AmendGoalPredicateRefreezes(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-amend"
	p454Task(t, store, task)
	predA := map[string]any{
		"op":         "all_of",
		"conditions": []map[string]any{{"kind": "fact_confirmed", "asset_id": "a1", "min": 1}},
	}
	p454Goal(t, store, task, "g1", "目标", predA)
	// 谓词 A:无已确认事实 → 不满足。
	v, err := arbiter.Evaluate(ctx, store, task, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Satisfied {
		t.Fatal("谓词 A 无事实应不满足")
	}
	// 改标题(不带谓词):谓词不动。
	title := "新标题"
	if err := AmendGoal(ctx, store, task, "g1", &title, nil, nil, "改标题", "creator"); err != nil {
		t.Fatal(err)
	}
	g, _ := store.Node(ctx, task, "g1")
	if g.Title != "新标题" {
		t.Fatalf("标题应已改: %q", g.Title)
	}
	// 改谓词:min=0(规范化为 1)且资产无事实 → 条件数不变;换成
	// 事实已确认计数(种 1 条已确认事实 + about 边)后应满足。
	predB := map[string]any{
		"op":         "all_of",
		"conditions": []map[string]any{{"kind": "fact_confirmed", "asset_id": "a2", "min": 1}},
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a2", NodeType: rings.NodeAsset, Title: "y.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "f1", NodeType: rings.NodeFact, Title: "事实", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f1", ToNode: "a2"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := AmendGoal(ctx, store, task, "g1", nil, predB, nil, "改完成条件", "creator"); err != nil {
		t.Fatal(err)
	}
	v, err = arbiter.Evaluate(ctx, store, task, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Satisfied {
		t.Fatalf("新谓词(a2 有 1 条已确认事实)应满足: %+v", v)
	}
	// 非法谓词:拒绝。
	if err := AmendGoal(ctx, store, task, "g1", nil, map[string]any{"op": "bad"}, nil, "", "creator"); err == nil {
		t.Fatal("非法谓词应拒绝")
	}
	// 已删目标拒改。
	p454Goal(t, store, task, "g9", "待删", nil)
	if _, err := DeleteGoal(ctx, store, nil, task, "g9", "", "creator"); err != nil {
		t.Fatal(err)
	}
	if err := AmendGoal(ctx, store, task, "g9", &title, nil, nil, "", "creator"); err == nil {
		t.Fatal("已删目标应拒改")
	}
}

// TestP454FocusGoalValidation 聚焦入参校验:非目标/非 active 拒绝;重复
// 聚焦幂等(切换解除旧聚焦)。
func TestP454FocusGoalValidation(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p454-focus-val"
	p454Task(t, store, task)
	p454Goal(t, store, task, "g1", "一", nil)
	p454Goal(t, store, task, "g2", "二", nil)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "arbiter",
		Payload: rings.NodeStateChanged{NodeID: "g2", Status: GoalStatusAchieved},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := FocusGoal(ctx, store, nil, task, "s-none", "shepherd"); err == nil {
		t.Fatal("非目标应拒绝")
	}
	if _, err := FocusGoal(ctx, store, nil, task, "g2", "shepherd"); err == nil {
		t.Fatal("已达成目标应拒绝聚焦")
	}
	if _, err := FocusGoal(ctx, store, nil, task, "g1", "shepherd"); err != nil {
		t.Fatal(err)
	}
	if _, err := FocusGoal(ctx, store, nil, task, "g1", "shepherd"); err != nil {
		t.Fatalf("重复聚焦同目标应幂等: %v", err)
	}
	if FocusGoalID(ctx, store, task) != "g1" {
		t.Fatal("聚焦应在 g1")
	}
}
