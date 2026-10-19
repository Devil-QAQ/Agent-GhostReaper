package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// seedChainFixture 铺组合声明测试底:资产 + 两条已确认 Fact + 一条候选。
func seedChainFixture(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-aaa", NodeType: rings.NodeFact, Title: "备份目录可未授权读取", Status: "confirmed",
			Detail: map[string]any{"evidence": "curl 200"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "f-bbb", NodeType: rings.NodeFact, Title: "备份含明文凭据", Status: "confirmed",
			Detail: map[string]any{"evidence": "curl 200"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u3", Payload: rings.NodeAdded{
			NodeID: "f-ccc", NodeType: rings.NodeFact, Title: "未证实的线索", Status: "candidate"}},
	); err != nil {
		t.Fatalf("铺底失败: %v", err)
	}
}

// TestSubmitCombinedClaimGuards 组合声明受控入口守卫:两维声明齐、链条
// ≥2 且逐条已确认 Fact(R13 未验证线索不得作为组合前提)、资产锚点
// 存在;合法提交落 TestClaim 节点 + derived_from×N + about,冻结判定
// 标准与竞争解释清单,发现者=主宰。
func TestSubmitCombinedClaimGuards(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "t-combined"
	seedChainFixture(t, store, task)

	chain := []ChainLink{{FactID: "f-aaa", Role: "入口"}, {FactID: "f-bbb", Role: "凭据"}}
	bad := []struct {
		name   string
		stmt   string
		impact string
		chain  []ChainLink
		asset  string
	}{
		{"缺影响面声明", "存在性", "", chain, "web1"},
		{"单环节", "存在性", "影响", chain[:1], "web1"},
		{"含未确认环节", "存在性", "影响", append(append([]ChainLink{}, chain...), ChainLink{FactID: "f-ccc"}), "web1"},
		{"幽灵环节", "存在性", "影响", append(append([]ChainLink{}, chain...), ChainLink{FactID: "f-nope"}), "web1"},
		{"幽灵资产", "存在性", "影响", chain, "web9"},
	}
	for _, c := range bad {
		if _, err := SubmitCombinedClaim(ctx, store, task, c.stmt, c.impact, "理由", c.chain, c.asset, "", nil); err == nil {
			t.Fatalf("%s 应被拒", c.name)
		}
	}

	id, err := SubmitCombinedClaim(ctx, store, task, "攻击链:备份可读+凭据泄露→未授权访问", "未授权取得访问", "三环相扣",
		chain, "web1", "攻击链", nil)
	if err != nil {
		t.Fatalf("合法组合声明应可提交: %v", err)
	}
	if !strings.HasPrefix(id, "tc-chain-") {
		t.Fatalf("组合声明 ID 形态: %s", id)
	}
	node, err := store.Node(ctx, task, id)
	if err != nil {
		t.Fatalf("查组合声明: %v", err)
	}
	if node.Status != rings.TestClaimCandidate {
		t.Fatalf("组合声明初始状态应 candidate: %s", node.Status)
	}
	if combined, _ := node.Detail["combined"].(bool); !combined {
		t.Fatalf("detail.combined 应为 true")
	}
	if u, _ := node.Detail["unit_id"].(string); u != ActorOvermind {
		t.Fatalf("组合声明发现者应记主宰: %q", u)
	}
	if raw, _ := node.Detail["chain"].([]any); len(raw) != 2 {
		t.Fatalf("detail.chain 应 2 环节: %+v", node.Detail["chain"])
	}
	if std, _ := node.Detail["standard"].(map[string]any); std == nil {
		t.Fatalf("判定标准应同批冻结")
	}
	// 未列竞争解释 → 兜底四条基线(与虫群 submit_test_claim 同一编译语义)。
	if alts, _ := node.Detail["alternatives"].([]any); len(alts) != 4 {
		t.Fatalf("竞争解释应兜底四条: %+v", node.Detail["alternatives"])
	}
	// 血缘:derived_from×2 → Fact + about → 资产。
	derived, _ := store.Edges(ctx, task, rings.EdgeDerivedFrom)
	gotFacts := map[string]bool{}
	for _, e := range derived {
		if e.From == id {
			gotFacts[e.To] = true
		}
	}
	if !gotFacts["f-aaa"] || !gotFacts["f-bbb"] {
		t.Fatalf("血缘应挂两条环节 Fact: %v", gotFacts)
	}
	abouts, _ := store.Edges(ctx, task, rings.EdgeAbout)
	hasAnchor := false
	for _, e := range abouts {
		if e.From == id && e.To == "web1" {
			hasAnchor = true
		}
	}
	if !hasAnchor {
		t.Fatalf("组合声明应锚定资产 web1")
	}
}

// TestDispatchPriorityAndDepends 派发策略落账:优先级进 step.detail
// (词汇受限,非法拒派;normal 缺省不落);depends_on 边(端点校验:
// 依赖自身/幽灵步骤/非 Step 节点拒绝)。
func TestDispatchPriorityAndDepends(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "t-dispatch"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "前置意图", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "active"}},
	); err != nil {
		t.Fatalf("铺底失败: %v", err)
	}

	// 非法优先级拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", Priority: "urgent"}); err == nil {
		t.Fatalf("非法优先级应拒派")
	}
	// 依赖自身拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", DependsOn: []string{"sx"}}); err == nil {
		t.Fatalf("依赖自身应拒派")
	}
	// 幽灵前置拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", DependsOn: []string{"s9"}}); err == nil {
		t.Fatalf("幽灵前置应拒派")
	}
	// 依赖非 Step 节点拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", DependsOn: []string{"web1"}}); err == nil {
		t.Fatalf("依赖非 Step 节点应拒派")
	}

	// 合法派发:高优先级 + 依赖既有步骤。
	if err := DispatchStep(ctx, store, task, "s2", "后续意图", DispatchOptions{
		GoalID: "g1", ActionLevel: "只读探测", Priority: "high", DependsOn: []string{"s1"}}); err != nil {
		t.Fatalf("合法派发失败: %v", err)
	}
	node, err := store.Node(ctx, task, "s2")
	if err != nil {
		t.Fatalf("查步骤: %v", err)
	}
	if p, _ := node.Detail["priority"].(string); p != "high" {
		t.Fatalf("优先级应落 detail: %+v", node.Detail)
	}
	deps, _ := store.Edges(ctx, task, rings.EdgeDependsOn)
	found := false
	for _, e := range deps {
		if e.From == "s2" && e.To == "s1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("depends_on 边应落账: %v", deps)
	}

	// normal 缺省不落(快照与 detail 的降噪口径:normal 是常态)。
	if err := DispatchStep(ctx, store, task, "s3", "常规意图", DispatchOptions{
		ActionLevel: "只读探测"}); err != nil {
		t.Fatalf("常规派发失败: %v", err)
	}
	n3, _ := store.Node(ctx, task, "s3")
	if _, ok := n3.Detail["priority"]; ok {
		t.Fatalf("normal 优先级不应落 detail: %+v", n3.Detail)
	}
}

// TestSelectRunnable 派发策略的确定性选步:依赖闸(前置 done 才可执行,
// 前置 running/failed/不存在均等待)+ 优先级排序(high > normal > low,
// 同级创建序)。
func TestSelectRunnable(t *testing.T) {
	node := func(id, status string, pri string, rev int64) rings.NodeFull {
		detail := map[string]any{}
		if pri != "" {
			detail["priority"] = pri
		}
		return rings.NodeFull{NodeView: rings.NodeView{ID: id, Status: status, CreatedRev: rev}, Detail: detail}
	}
	dispatched := []rings.NodeFull{
		node("slow", "dispatched", "low", 1),
		node("norm", "dispatched", "", 2),
		node("quick", "dispatched", "high", 3),
		node("waiter", "dispatched", "high", 4), // 依赖 running 前置 → 等待
		node("follow", "dispatched", "low", 5),  // 依赖 done 前置 → 可执行
	}
	all := append(append([]rings.NodeFull{}, dispatched...),
		node("front-run", "running", "", 6),
		node("front-done", "done", "", 7),
	)
	deps := []rings.EdgeView{
		{Type: rings.EdgeDependsOn, From: "waiter", To: "front-run"},
		{Type: rings.EdgeDependsOn, From: "follow", To: "front-done"},
		{Type: rings.EdgeDependsOn, From: "follow", To: "ghost"}, // 前置不存在 → 等待
	}
	got := selectRunnable(dispatched, all, deps)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	// quick(high) 先于 norm;slow(low) 最后;waiter/follow 等待不选。
	if len(ids) != 3 || ids[0] != "quick" || ids[1] != "norm" || ids[2] != "slow" {
		t.Fatalf("选步不符: %v", ids)
	}
}
