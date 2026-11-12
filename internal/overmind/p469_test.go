package overmind

// P4-69(问题清单 #86 D,摘除联动收口与状态一致性)对账层:
// 挂等冻结单位被排除时的分族收口(意图覆盖者终止留痕、不覆盖者唤醒
// 换路)、未决卡全量收口(红队可否决卡无挂等步骤同受治——僵尸卡根治,
// 实锤 sc-example.com:80 形态类级复现)。

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// p469WaiterEnv 渗透任务 + 范围外目标资产 a-ex(被排除)+ 一张该目标的
// 待决卡 + 一个冻结在 scope_wait 的挂等步骤(锚定资产由 anchor 决定:
// "ex"=意图覆盖该目标,"seed"=锚定种子顺带触碰,"verify"=验证类锚定)。
func p469WaiterEnv(t *testing.T, task, anchor string) *rings.Store {
	t.Helper()
	store := openStore(t)
	ctx := context.Background()
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "sc-target.com:80",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	p451FreezeStep(t, store, task, "s1", "sc-target.com", "80")
	var anchorEvs []rings.Event
	switch anchor {
	case "ex":
		anchorEvs = []rings.Event{{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a-ex"}}}
	case "seed":
		seed, err := store.NodesByType(ctx, task, rings.NodeAsset, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range seed {
			if a.Title == "www.example.com" {
				anchorEvs = []rings.Event{{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
					EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: a.ID}}}
			}
		}
	case "verify":
		anchorEvs = []rings.Event{
			{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: "h1", NodeType: rings.NodeHypothesis, Title: "猜 sc-target 有洞", Status: "candidate"}},
			{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeAbout, FromNode: "h1", ToNode: "a-ex"}},
			{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeVerifies, FromNode: "s1", ToNode: "h1"}},
		}
	case "none":
		// 无锚定(命令顺带触碰形态:意图线在别处,挂等只因一条命令)。
	}
	if len(anchorEvs) > 0 {
		if _, err := store.Append(ctx, task, anchorEvs...); err != nil {
			t.Fatal(err)
		}
	}
	// 待决卡(渗透命令拦截形态;先冻结后出卡,带命令上下文)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("sc-target.com", "80"), StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, Intent: "探测目标", ToolName: "bash",
			Note: "渗透任务:命令目标 sc-target.com:80 在范围外,拦下等待批准"},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestP469ExcludedCoverTerminates 完成标准①·覆盖者:挂等步骤意图覆盖被
// 排除目标(covers 锚定)→ 对账终止留痕(aborted·范围禁止测试);卡按
// 目标类拒绝收口(denied·roe_scope)——卡消红点消(ScopePendingCount
// 归零);幂等(再对账零变更)。
func TestP469ExcludedCoverTerminates(t *testing.T) {
	store := p469WaiterEnv(t, "p469-cover", "ex")
	ctx := context.Background()
	const task = "p469-cover"
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}, Note: "摘除"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	woken, terminated := l.ResolveScopeWaits(ctx)
	if woken != 0 || terminated != 1 {
		t.Fatalf("覆盖者应收口终止: woken=%d terminated=%d", woken, terminated)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "aborted" {
		t.Fatalf("覆盖者应 aborted: %+v", step.Status)
	}
	// 卡按目标类拒绝收口留痕(卡消红点消)。
	rows, _ := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("sc-target.com", "80"))
	last := rows[len(rows)-1]
	if last.Type != rings.ApprovalRecDenied || last.DenyLine != rings.DenyLineRoeScope {
		t.Fatalf("卡应按拒绝语义收口: type=%s deny=%s", last.Type, last.DenyLine)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("红点计数应归零: %d", n)
	}
	// 幂等。
	if woken, terminated := l.ResolveScopeWaits(ctx); woken != 0 || terminated != 0 {
		t.Fatalf("幂等对账零变更: %d %d", woken, terminated)
	}
}

// TestP469ExcludedTouchWakes 完成标准①·不覆盖者:挂等步骤意图不覆盖被
// 排除目标(锚定种子,命令顺带触碰)→ 对账唤醒回可领取(队首)换路,
// 不终止——卡仍按拒绝语义收口(目标的卡使命已终,挂等单位与卡各归各)。
func TestP469ExcludedTouchWakes(t *testing.T) {
	store := p469WaiterEnv(t, "p469-touch", "seed")
	ctx := context.Background()
	const task = "p469-touch"
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}, Note: "摘除"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	woken, terminated := l.ResolveScopeWaits(ctx)
	if woken != 1 || terminated != 0 {
		t.Fatalf("顺带触碰者应唤醒换路: woken=%d terminated=%d", woken, terminated)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "dispatched" {
		t.Fatalf("唤醒后步骤应回可领取: %+v", step.Status)
	}
	if v, _ := step.Detail["scope_head"].(bool); !v {
		t.Fatalf("唤醒应带队首标记: %+v", step.Detail)
	}
	// 续跑语义:目标现值被排除,重领时续跑指令走"目标已排除"档
	//(swarm 侧断言措辞,此处断言判定输入——scope_wait_host 在、目标被
	// 排除覆盖)。
	if host, _ := step.Detail["scope_wait_host"].(string); host != "sc-target.com" {
		t.Fatalf("续跑指令的目标标记应保留: %+v", step.Detail)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if !rings.ExcludedCoversTarget(assets, roe.ExcludedAssetSet(), "sc-target.com", "80") {
		t.Fatal("目标现值应被排除覆盖(续跑指令据此走目标已排除档)")
	}
	// 卡同样收口(无僵尸卡)。
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("卡应随排除收口: %d", n)
	}
}

// TestP469VerifyAnchorTerminates 分族单源的验证形态:挂等步骤为验证类
// (verifies→假设→about 锚定被排除资产)→ 意图覆盖成立,终止留痕
// (与 P4-68 ② 命令路同口径,验证工作线就是它)。
func TestP469VerifyAnchorTerminates(t *testing.T) {
	store := p469WaiterEnv(t, "p469-verify", "verify")
	ctx := context.Background()
	const task = "p469-verify"
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}, Note: "摘除"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	woken, terminated := l.ResolveScopeWaits(ctx)
	if woken != 0 || terminated != 1 {
		t.Fatalf("验证类锚定覆盖者应终止: woken=%d terminated=%d", woken, terminated)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "aborted" {
		t.Fatalf("验证类覆盖者应 aborted: %+v", step.Status)
	}
}

// TestP469ZombieRedteamCardCloses 完成标准①·僵尸卡类级(实锤
// sc-example.com:80 形态):红队可否决卡的目标没有挂等步骤(红队直接
// 测,卡只是提醒开关)——摘除该目标后对账,卡按拒绝语义收口(旧机制
// 卡收口依赖"该目标还有挂等步骤",红队卡永等不到 → 挂死;全量重判
// 不依赖步骤)。步骤零变更(本来就没有)。
func TestP469ZombieRedteamCardCloses(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p469-zombie"
	p444Task(t, store, task, rings.TaskTypeRedteam, "www.example.com")
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		// 红队发现路:范围外资产入图直接可测 + 可否决卡(无挂等步骤)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-rt", NodeType: rings.NodeAsset, Title: "sc-example.com:80",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: ActorOvermind, Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("sc-example.com", "80"),
			Type:      rings.ApprovalRecScopeWait,
			Note:      "红队任务:发现范围外资产 sc-example.com:80,已直接开始测试;可随时禁止"}},
	); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 1 {
		t.Fatalf("摘除前卡在待决: %d", n)
	}
	// RoE 摘除该目标(排除断链前红队卡从未被收口的形态)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-rt"}, Note: "摘除"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	if _, terminated := l.ResolveScopeWaits(ctx); terminated != 0 {
		t.Fatalf("无挂等步骤零终止: %d", terminated)
	}
	rows, _ := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("sc-example.com", "80"))
	last := rows[len(rows)-1]
	if last.Type != rings.ApprovalRecDenied || last.DenyLine != rings.DenyLineRoeScope {
		t.Fatalf("红队卡应按拒绝语义收口: type=%s deny=%s", last.Type, last.DenyLine)
	}
	if !strings.Contains(last.Reason, "排除") {
		t.Fatalf("收口理由应注明排除: %s", last.Reason)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("卡消红点消: %d", n)
	}
	// 卡收口后状态机为终局:同目标不再出卡(scopeguard 侧既有语义,
	// 此处断言未决面为空即可)。
	if cards, _ := store.PendingScopeCards(ctx, task); len(cards) != 0 {
		t.Fatalf("未决卡应清空: %+v", cards)
	}
}

// TestP469UnrelatedWaiterUntouched 邻避断言:同任务挂等其他目标的冻结
// 单位不受本次排除影响(各等各的,P4-51 ② 既有语义随分族重构不破)。
func TestP469UnrelatedWaiterUntouched(t *testing.T) {
	store := p469WaiterEnv(t, "p469-unrel", "seed")
	ctx := context.Background()
	const task = "p469-unrel"
	// 另一冻结步骤等待别的目标(无卡)。
	p451FreezeStep(t, store, task, "s2", "elsewhere.org", "443")
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}, Note: "摘除"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	woken, terminated := l.ResolveScopeWaits(ctx)
	if woken != 1 || terminated != 0 {
		t.Fatalf("仅顺带触碰者唤醒: woken=%d terminated=%d", woken, terminated)
	}
	s2, _ := store.Node(ctx, task, "s2")
	if s2.Status != rings.StepScopeWait {
		t.Fatalf("挂等其他目标的单位不受影响: %+v", s2.Status)
	}
}
