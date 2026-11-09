package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

// ---- P4-55(问题清单 #75)收割与循环侧:中断必重派、重派有终点、
// 操作者取消免除重派(P4-54 衔接)、不成立可重开 ----

// p455Claim 铺一张 testing 声明的最小图:发现步骤(done)+ 资产 + 声明
// (完整反证标准)+ 血缘 + 锚点;falsifyStep 按(状态,轮次)另种。
func p455Claim(t *testing.T, store *rings.Store, task, claimID string) {
	t.Helper()
	ctx := context.Background()
	p454Task(t, store, task)
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "sd-" + claimID, NodeType: rings.NodeStep, Title: "发现步骤", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-" + claimID, NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: "判断 " + claimID,
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{
				"unit_id":      "u1",
				"standard":     arbiter.DefaultClaimStandard(),
				"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存假象"}},
			}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: "a-" + claimID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: "sd-" + claimID}},
	); err != nil {
		t.Fatal(err)
	}
}

// p455FalsifyStep 种一轮反证步骤(状态/轮次/可选目标归属)并置声明
// testing。
func p455FalsifyStep(t *testing.T, store *rings.Store, task, claimID, stepID, status string, round int, goalID ...string) {
	t.Helper()
	g := ""
	if len(goalID) > 0 {
		g = goalID[0]
	}
	p454Step(t, store, task, stepID, "反证(第 1 轮)", status, g,
		map[string]any{"kind": "falsify", "testclaim_id": claimID, "round": round,
			"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存假象"}}})
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
		Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimTesting},
	}); err != nil {
		t.Fatal(err)
	}
}

// p455StepsOf 取声明名下反证步骤。
func p455StepsOf(t *testing.T, store *rings.Store, task, claimID string) []rings.NodeFull {
	t.Helper()
	steps, err := store.NodesByType(context.Background(), task, rings.NodeStep, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []rings.NodeFull
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id == claimID {
			out = append(out, s)
		}
	}
	return out
}

// TestP455InterruptedRedispatched ①:反证步骤各形态中断(审批拒绝终止/
// 完成未交结论/blocked 终结)→ HarvestClaims 确定性检测自动重派同轮次
// 反证步骤(新步骤、优先级 high、重派轮次与原因入 detail),声明留
// testing 不落待确认;blocked 中断步骤一并收场(防配置恢复后同轮双跑)。
func TestP455InterruptedRedispatched(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	for _, tc := range []struct{ claim, step, status string }{
		{"p455-rd-a", "sf-a", "approval_denied"},
		{"p455-rd-b", "sf-b", "done"},
		{"p455-rd-c", "sf-c", "blocked"},
	} {
		p455Claim(t, store, tc.claim, tc.claim)
		p455FalsifyStep(t, store, tc.claim, tc.claim, tc.step, "approval_denied", 1)
		// 首步按用例真实终态修正(approval_denied 直接种;done/blocked
		// 覆写状态)。
		if tc.status != "approval_denied" {
			if _, err := store.Append(ctx, tc.claim, rings.Event{
				Kind: rings.EventNodeStateChanged, Actor: "test",
				Payload: rings.NodeStateChanged{NodeID: tc.step, Status: tc.status},
			}); err != nil {
				t.Fatal(err)
			}
		}
		res, err := HarvestClaims(ctx, store, tc.claim)
		if err != nil {
			t.Fatalf("%s 收割不应报错: %v", tc.claim, err)
		}
		if len(res.Redispatched) != 1 || res.Redispatched[0] != tc.claim {
			t.Fatalf("%s 应自动重派: %+v", tc.claim, res.Redispatched)
		}
		claim, _ := store.Node(ctx, tc.claim, tc.claim)
		if claim.Status != rings.TestClaimTesting {
			t.Fatalf("%s 中断后应留在锤炼(testing),不落待确认: %s", tc.claim, claim.Status)
		}
		// 重派步骤:同轮次、优先级 high、原因与被替步骤入 detail。
		var redis rings.NodeFull
		for _, s := range p455StepsOf(t, store, tc.claim, tc.claim) {
			if s.ID != tc.step {
				redis = s
			}
		}
		if redis.ID == "" {
			t.Fatalf("%s 应有重派新步骤: %+v", tc.claim, p455StepsOf(t, store, tc.claim, tc.claim))
		}
		if redis.Status != "dispatched" {
			t.Fatalf("重派步骤应待领取: %+v", redis)
		}
		if r, _ := redis.Detail["round"].(float64); int(r) != 1 {
			t.Fatalf("重派应沿用同轮次 1: %+v", redis.Detail)
		}
		if p, _ := redis.Detail["priority"].(string); p != PriorityHigh {
			t.Fatalf("重派优先级应 high: %+v", redis.Detail)
		}
		if of, _ := redis.Detail["redispatch_of"].(string); of != tc.step {
			t.Fatalf("重派应记录被替步骤: %+v", redis.Detail)
		}
		if reason, _ := redis.Detail["redispatch_reason"].(string); reason == "" {
			t.Fatalf("重派应记录中断原因: %+v", redis.Detail)
		}
		if n, _ := redis.Detail["redispatch"].(float64); int(n) != 1 {
			t.Fatalf("重派序号应为 1: %+v", redis.Detail)
		}
		// blocked 中断步骤收场。
		if old, _ := store.Node(ctx, tc.claim, tc.step); tc.status == "blocked" && old.Status != "cancelled" {
			t.Fatalf("blocked 中断步骤应随重派收场: %+v", old)
		}
		// 二次收割:重派步骤在队(未终态),不再重派。
		res2, err := HarvestClaims(ctx, store, tc.claim)
		if err != nil || len(res2.Redispatched) != 0 {
			t.Fatalf("%s 重派在队期间不应重复重派: %v %+v", tc.claim, err, res2.Redispatched)
		}
	}
}

// TestP455RedispatchExhaustedFallsPending ③+循环侧:重派满 3 次仍无结论
// → 收割不再派,judgeClaims 判待确认且理由逐字"反证多轮未取得结论";
// blocked 终结的中断步骤随裁决收场。
func TestP455RedispatchExhaustedFallsPending(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-exhausted"
	p455Claim(t, store, task, "tc-x")
	// 原派 + 3 次重派(4 步同轮)全部审批拒绝中断,无结构化结论。
	for _, id := range []string{"sf-1", "sf-2", "sf-3", "sf-4"} {
		p455FalsifyStep(t, store, task, "tc-x", id, "approval_denied", 1)
	}
	// 最后一步改为 blocked 终结(裁决时应收场的形态)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "test",
		Payload: rings.NodeStateChanged{NodeID: "sf-4", Status: "blocked"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := HarvestClaims(ctx, store, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redispatched) != 0 {
		t.Fatalf("重派用尽不应再派: %+v", res.Redispatched)
	}
	l := &Loop{TaskID: task, Store: store}
	if _, err := l.judgeClaims(ctx); err != nil {
		t.Fatal(err)
	}
	claim, _ := store.Node(ctx, task, "tc-x")
	if claim.Status != rings.TestClaimPending {
		t.Fatalf("重派用尽应落待确认: %s", claim.Status)
	}
	digests, _ := store.ClaimDigests(ctx, task)
	if digests["tc-x"].PendingReason != "反证多轮未取得结论" {
		t.Fatalf("理由须逐字'反证多轮未取得结论': %q", digests["tc-x"].PendingReason)
	}
	if st, _ := store.Node(ctx, task, "sf-4"); st.Status != "cancelled" {
		t.Fatalf("blocked 中断步骤应随裁决收场: %+v", st)
	}
}

// TestP455JudgeSkipsInterrupted ①循环侧:预算未尽的中断声明 judgeClaims
// 静默跳过(ErrClaimInterrupted),零裁决记录、声明留锤炼。
func TestP455JudgeSkipsInterrupted(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-skip"
	p455Claim(t, store, task, "tc-s")
	p455FalsifyStep(t, store, task, "tc-s", "sf-1", "done", 1)
	l := &Loop{TaskID: task, Store: store}
	n, err := l.judgeClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("中断声明不应产生裁决: %d", n)
	}
	claim, _ := store.Node(ctx, task, "tc-s")
	if claim.Status != rings.TestClaimTesting {
		t.Fatalf("中断声明应留锤炼: %s", claim.Status)
	}
}

// TestP455RefutedReopenable ②+重开:不成立终态可经 ReopenClaim 重开回
// 反证中(新反证步骤全局续号,战役清空)。
func TestP455RefutedReopenable(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-reopen"
	p455Claim(t, store, task, "tc-r")
	p455FalsifyStep(t, store, task, "tc-r", "sf-1", "done", 1)
	// 反证写回存在性推翻 → 仲裁判不成立。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-r", StepID: "sf-1", UnitID: "u2", Round: 1,
			Type: rings.TestClaimRecFalsify, ExistenceRefuted: true, EvidenceRef: "sha256:aa"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
			Payload: rings.NodeStateChanged{NodeID: "tc-r", Status: rings.TestClaimRefuted}},
	); err != nil {
		t.Fatal(err)
	}
	if err := ReopenClaim(ctx, store, task, "tc-r", rings.ReopenNewEvidence, "新证据:推翻依据的样本已失效", "creator"); err != nil {
		t.Fatalf("不成立应可重开: %v", err)
	}
	claim, _ := store.Node(ctx, task, "tc-r")
	if claim.Status != rings.TestClaimTesting {
		t.Fatalf("重开应回反证中: %s", claim.Status)
	}
	steps := p455StepsOf(t, store, task, "tc-r")
	if len(steps) != 2 {
		t.Fatalf("重开应派新一轮反证: %+v", steps)
	}
	maxRound := 0
	for _, s := range steps {
		if r, _ := s.Detail["round"].(float64); int(r) > maxRound {
			maxRound = int(r)
		}
	}
	if maxRound != 2 {
		t.Fatalf("重开轮应全局续号(2): %+v", steps)
	}
}

// TestP455UnitKillZeroStateChangeAwaitRuling 审核迭代:杀单位→声明零
// 程序改态——收割不重派(留痕 Skipped)、裁决不落终态、声明留锤炼中、
// 快照「待裁决判断」区可见;主宰 claim_rulings 裁决两向:continue=程序
// 重派反证(计入该轮额度)、suspend=挂起(理由入账);挂起(目标已
// 删除)的 continue 经 reopen 重开反证。
func TestP455UnitKillZeroStateChangeAwaitRuling(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-kill-ruling"
	p455Claim(t, store, task, "tc-k")
	p455FalsifyStep(t, store, task, "tc-k", "sf-1", "running", 1)
	// 单位被舵杆终止:kill_work 送达回执 + 步骤落 aborted。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventSteerRecord, Actor: "creator",
			Payload: rings.SteerRecord{SteerID: "st-9", StepID: "sf-1", Command: rings.SteerCommandKill,
				State: rings.SteerStateDelivered, Reason: "杀掉这个单位"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator",
			Payload: rings.NodeStateChanged{NodeID: "sf-1", Status: "aborted"}},
	); err != nil {
		t.Fatal(err)
	}
	// 零程序改态:收割不重派(如实留痕)、裁决零笔、声明留锤炼中。
	res, err := HarvestClaims(ctx, store, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Redispatched) != 0 {
		t.Fatalf("操作者终止不应程序重派: %+v", res.Redispatched)
	}
	skipped := false
	for _, sk := range res.Skipped {
		if sk.FactID == "tc-k" && strings.Contains(sk.Reason, "交主宰判定") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("收割应留痕交主宰: %+v", res.Skipped)
	}
	l := &Loop{TaskID: task, Store: store}
	if n, err := l.judgeClaims(ctx); err != nil || n != 0 {
		t.Fatalf("待裁决中断不应产生裁决: n=%d err=%v", n, err)
	}
	claim, _ := store.Node(ctx, task, "tc-k")
	if claim.Status != rings.TestClaimTesting {
		t.Fatalf("声明应零程序改态留锤炼中: %s", claim.Status)
	}
	// 快照「待裁决判断」区可见。
	goals, _ := store.NodesByType(ctx, task, rings.NodeGoal, "")
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	abouts, _ := store.Edges(ctx, task, rings.EdgeAbout)
	lines, err := l.pendingRulingBriefs(ctx, goals, steps, abouts)
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0], "tc-k") || !strings.Contains(lines[0], "反证被操作者终止") {
		t.Fatalf("待裁决判断应入快照: %v %+v", err, lines)
	}
	// 主宰裁决 continue:程序重派同轮反证(计入额度),声明留锤炼。
	rev, _ := store.CurrentRevision(ctx, task)
	if err := l.applyClaimRuling(ctx, claimRulingPlan{ClaimID: "tc-k", Action: "continue"}, rev); err != nil {
		t.Fatalf("continue 裁决应通过: %v", err)
	}
	falsifies := p455StepsOf(t, store, task, "tc-k")
	if len(falsifies) != 2 {
		t.Fatalf("continue 应重派新反证步骤: %+v", falsifies)
	}
	if r, _ := falsifies[1].Detail["round"].(float64); int(r) != 1 {
		t.Fatalf("裁决重派应沿用同轮次: %+v", falsifies[1].Detail)
	}
	if reason, _ := falsifies[1].Detail["redispatch_reason"].(string); !strings.Contains(reason, "主宰判定恢复锤炼") {
		t.Fatalf("判定重派原因应入账: %q", reason)
	}
	if claim, _ = store.Node(ctx, task, "tc-k"); claim.Status != rings.TestClaimTesting {
		t.Fatalf("continue 后声明应留锤炼: %s", claim.Status)
	}
	// 再造一次操作者终止(重派步骤中止),裁决 suspend:挂起(理由入账)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventSteerRecord, Actor: "creator",
			Payload: rings.SteerRecord{SteerID: "st-8", StepID: falsifies[1].ID, Command: rings.SteerCommandKill,
				State: rings.SteerStateDelivered, Reason: "再杀"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator",
			Payload: rings.NodeStateChanged{NodeID: falsifies[1].ID, Status: "aborted"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := l.applyClaimRuling(ctx, claimRulingPlan{ClaimID: "tc-k", Action: "suspend", Reason: SuspendReasonGoalDeleted}, rev); err != nil {
		t.Fatalf("suspend 裁决应通过: %v", err)
	}
	claim, _ = store.Node(ctx, task, "tc-k")
	if claim.Status != rings.TestClaimPending {
		t.Fatalf("suspend 应挂起: %s", claim.Status)
	}
	digests, _ := store.ClaimDigests(ctx, task)
	if digests["tc-k"].PendingReason != SuspendReasonGoalDeleted {
		t.Fatalf("挂起理由应入账: %q", digests["tc-k"].PendingReason)
	}
	// 挂起(目标已删除)进快照待裁决区;continue 经 reopen 重开。
	lines, err = l.pendingRulingBriefs(ctx, goals, steps, abouts)
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0], "挂起(目标已删除)") {
		t.Fatalf("目标已删除挂起应入快照待裁决区: %v %+v", err, lines)
	}
	if err := l.applyClaimRuling(ctx, claimRulingPlan{ClaimID: "tc-k", Action: "continue", Reason: "新建了同资产目标"}, rev); err != nil {
		t.Fatalf("挂起声明的 continue 应经重开通过: %v", err)
	}
	if claim, _ = store.Node(ctx, task, "tc-k"); claim.Status != rings.TestClaimTesting {
		t.Fatalf("重开应回反证中: %s", claim.Status)
	}
	if got := len(p455StepsOf(t, store, task, "tc-k")); got != 3 {
		t.Fatalf("重开应派新一轮反证: %d", got)
	}
}

// TestP455DeleteGoalStillNeededContinues 审核迭代删目标分支 A:资产仍被
// 其他目标需要——删除后声明留锤炼,快照事实行"仍被目标覆盖",主宰
// continue → 程序重派反证继续锤炼。
func TestP455DeleteGoalStillNeededContinues(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-del-a"
	p455Claim(t, store, task, "tc-a")
	p454Goal(t, store, task, "g1", "要删的方向", nil)
	p454Goal(t, store, task, "g2", "仍要的方向", nil)
	// g2 的步骤覆盖同资产(需要事实的来源)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "sg2", NodeType: rings.NodeStep, Title: "g2 的探测", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g2", "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "sg2", ToNode: "a-tc-a"}},
	); err != nil {
		t.Fatal(err)
	}
	p455FalsifyStep(t, store, task, "tc-a", "sf-1", "dispatched", 1, "g1")
	if _, err := DeleteGoal(ctx, store, nil, task, "g1", "不要了", "creator"); err != nil {
		t.Fatal(err)
	}
	claim, _ := store.Node(ctx, task, "tc-a")
	if claim.Status != rings.TestClaimTesting {
		t.Fatalf("删目标后声明应留锤炼(交主宰): %s", claim.Status)
	}
	l := &Loop{TaskID: task, Store: store}
	goals, _ := store.NodesByType(ctx, task, rings.NodeGoal, "")
	steps, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	abouts, _ := store.Edges(ctx, task, rings.EdgeAbout)
	lines, err := l.pendingRulingBriefs(ctx, goals, steps, abouts)
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0], "仍被目标 g2") {
		t.Fatalf("事实行应言明仍被 g2 覆盖: %v %+v", err, lines)
	}
	rev, _ := store.CurrentRevision(ctx, task)
	if err := l.applyClaimRuling(ctx, claimRulingPlan{ClaimID: "tc-a", Action: "continue"}, rev); err != nil {
		t.Fatalf("仍需要分支应 continue: %v", err)
	}
	if got := len(p455StepsOf(t, store, task, "tc-a")); got != 2 {
		t.Fatalf("continue 应重派反证: %d", got)
	}
}

// TestP455StopDirectionRunningFalsifySuspends 衔接(P4-54 补齐):停向/
// 聚焦杀掉在跑反证单位时,其声明同排队取消一样转挂起(理由"方向被收窄"),
// 免除中断自动重派——重新聚焦经 reopen 唤醒(P4-54 语义,不再被重派
// 绕过)。
func TestP455StopDirectionRunningFalsifySuspends(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-focus-kill"
	p455Claim(t, store, task, "tc-k")
	p454Goal(t, store, task, "g-focus", "聚焦方向", nil)
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded,
		Actor: ActorOvermind, Payload: rings.NodeAdded{NodeID: "s-focus", NodeType: rings.NodeStep,
			Title: "聚焦方向探测", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测", "goal_id": "g-focus"}}}); err != nil {
		t.Fatal(err)
	}
	// 在跑反证步骤(声明锚定,与聚焦方向不同);声明在反证中。
	p454Step(t, store, task, "sf-run", "反证(第 1 轮)", "running", "",
		map[string]any{"kind": "falsify", "testclaim_id": "tc-k", "round": 1})
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
		Payload: rings.NodeStateChanged{NodeID: "tc-k", Status: rings.TestClaimTesting},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := FocusGoal(ctx, store, nil, task, "g-focus", "shepherd")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SuspendedClaims) != 1 || res.SuspendedClaims[0] != "tc-k" {
		t.Fatalf("在跑反证被停应挂起声明: %+v", res.SuspendedClaims)
	}
	claim, _ := store.Node(ctx, task, "tc-k")
	if claim.Status != rings.TestClaimPending {
		t.Fatalf("声明应转挂起: %s", claim.Status)
	}
	digests, _ := store.ClaimDigests(ctx, task)
	if !strings.Contains(digests["tc-k"].PendingReason, "方向被收窄") {
		t.Fatalf("挂起理由应为方向被收窄: %q", digests["tc-k"].PendingReason)
	}
	// 挂起声明不在锤炼,收割侧零重派(免除自动重派)。
	hres, err := HarvestClaims(ctx, store, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(hres.Redispatched) != 0 {
		t.Fatalf("挂起声明免除自动重派: %+v", hres.Redispatched)
	}
	// 解除聚焦:挂起声明经 reopen 唤醒回反证中(新反证步骤)。
	if _, err := UnfocusGoal(ctx, store, task, "shepherd"); err != nil {
		t.Fatal(err)
	}
	claim, _ = store.Node(ctx, task, "tc-k")
	if claim.Status != rings.TestClaimTesting {
		t.Fatalf("解除聚焦应唤醒挂起声明: %s", claim.Status)
	}
}
