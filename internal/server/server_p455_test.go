package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
)

// ---- P4-55 ⑤(问题清单 #75):存量一次归位——被推翻类 pending 转不成立、
// "待重派"类回锤炼触发重派;标记键一次性,历史不涂改。 ----

// p455MigStore 开库并按给定生命周期状态建两任务(活任务 started /
// 终态任务 stopped)。
func p455MigStore(t *testing.T) *rings.Store {
	t.Helper()
	store, err := rings.Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, tk := range []struct{ id, action string }{
		{"p455-live", "created"}, {"p455-live", "started"},
		{"p455-dead", "created"}, {"p455-dead", "stopped"},
	} {
		if _, err := store.Append(ctx, tk.id, rings.Event{
			Kind: rings.EventTaskLifecycle, Actor: "test",
			Payload: rings.TaskLifecycle{Action: tk.action}}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// p455MigPendingClaim 种一张 pending 声明:创建(candidate)+ verdict
// (pending,理由 reason;进 pending_reason 投影)+ 状态迁移。extras 允许
// 追加血缘/反证步骤等周边事件(供归位后的重派验收)。
func p455MigPendingClaim(t *testing.T, store *rings.Store, task, claimID, reason string, extras ...rings.Event) {
	t.Helper()
	ctx := context.Background()
	base := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: "判断 " + claimID,
			Status: rings.TestClaimCandidate, Detail: map[string]any{
				"unit_id":  "u1",
				"standard": map[string]any{"kind": "full_falsify", "require_impact": false, "max_rounds": 3},
			}}},
		{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: claimID, Type: rings.TestClaimRecVerdict, Decision: rings.VerdictPending,
			PendingReason: reason, EvaluatedRev: 2,
			Conditions: []rings.ArbiterConditionResult{{
				Kind: "falsify_completed", Required: 1, Actual: 0, Satisfied: false}}}},
		{Kind: rings.EventNodeStateChanged, Actor: "arbiter",
			Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimPending}},
	}
	if _, err := store.Append(ctx, task, append(base, extras...)...); err != nil {
		t.Fatal(err)
	}
}

// TestP455MigratePendingClaims 存量归位:① 被推翻类(两种历史文案)→
// 不成立(verdict refuted + 状态迁移,追加事件可溯);② "待重派"类在
// 活任务回锤炼,收割侧随即自动重派(中断检测走起新步骤);③ 终态任务
// 的"待重派"类如实保持待确认;④ 操作者挂起(方向被收窄)与终局清点
// (任务终止中断反证)两类不动;⑤ 标记键一次性——第二次跑不再触碰。
func TestP455MigratePendingClaims(t *testing.T) {
	store := p455MigStore(t)
	ctx := context.Background()

	// ① 被推翻类:完整反证与单轮两套历史文案。
	p455MigPendingClaim(t, store, "p455-live", "tc-refu-a", "存在性被反证推翻,判断不成立,待复核(重开可再证)")
	p455MigPendingClaim(t, store, "p455-live", "tc-refu-b", "存在性被反证推翻,判断不成立,待复核")

	// ② "待重派"类(活任务):带血缘与中断反证步骤,归位后收割可重派。
	p455MigPendingClaim(t, store, "p455-live", "tc-redisp",
		"第 1 轮反证未写回结构化结论(反证轮被审批拒绝终止(该单位已终止)),待重派反证后复核",
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sd-x", NodeType: rings.NodeStep, Title: "发现步骤", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-redisp", ToNode: "a-x"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-redisp", ToNode: "sd-x"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sf-x", NodeType: rings.NodeStep, Title: "反证(第 1 轮)", Status: "approval_denied",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-redisp", "round": 1}}})

	// ③ 终态任务的"待重派"类:保持待确认;被推翻类照转不成立(终态
	// 任务同样转,审核迭代定案的不对称处理)。
	p455MigPendingClaim(t, store, "p455-dead", "tc-dead",
		"反证轮未写回结构化结论(反证轮被中止),待重派反证后复核")
	p455MigPendingClaim(t, store, "p455-dead", "tc-dead-refu",
		"存在性被反证推翻,判断不成立,待复核(重开可再证)")

	// ④ 不动的两类:操作者挂起、终局清点。
	p455MigPendingClaim(t, store, "p455-live", "tc-susp", "方向被收窄")
	p455MigPendingClaim(t, store, "p455-dead", "tc-sweep", "任务终止中断反证,可被唤醒")

	if err := migratePendingClaimsOnce(store); err != nil {
		t.Fatalf("存量归位不应报错: %v", err)
	}

	// ① 被推翻类 → 不成立(两种文案一致)。
	for _, id := range []string{"tc-refu-a", "tc-refu-b"} {
		n, _ := store.Node(ctx, "p455-live", id)
		if n.Status != rings.TestClaimRefuted {
			t.Fatalf("%s 应转不成立: %s", id, n.Status)
		}
		digests, _ := store.ClaimDigests(ctx, "p455-live")
		if digests[id].PendingReason != "被反证推翻" {
			t.Fatalf("%s 归位后最新裁决理由应为被反证推翻: %q", id, digests[id].PendingReason)
		}
	}
	// 追加事件可溯:verdict(refuted) 与状态迁移都在事件流。
	evs, _ := store.Events(ctx, "p455-live", 0, 0)
	refutedVerdict, refutedState := false, false
	for _, ev := range evs {
		if ev.Actor != "system" {
			continue
		}
		if ev.Kind == rings.EventTestClaimRecord && strings.Contains(string(ev.Payload), `"refuted"`) {
			refutedVerdict = true
		}
		if ev.Kind == rings.EventNodeStateChanged && strings.Contains(string(ev.Payload), `"refuted"`) {
			refutedState = true
		}
	}
	if !refutedVerdict || !refutedState {
		t.Fatalf("归位应追加 refuted 裁决与状态事件: verdict=%v state=%v", refutedVerdict, refutedState)
	}

	// ② "待重派"类(活任务)→ 回锤炼,收割随即自动重派。
	if n, _ := store.Node(ctx, "p455-live", "tc-redisp"); n.Status != rings.TestClaimTesting {
		t.Fatalf("活任务待重派类应回锤炼: %s", n.Status)
	}
	res, err := overmind.HarvestClaims(ctx, store, "p455-live")
	if err != nil {
		t.Fatalf("归位后收割不应报错: %v", err)
	}
	if len(res.Redispatched) != 1 || res.Redispatched[0] != "tc-redisp" {
		t.Fatalf("归位后应自动重派 tc-redisp: %+v", res.Redispatched)
	}

	// ③ 终态任务:保持待确认;被推翻类转不成立。
	if n, _ := store.Node(ctx, "p455-dead", "tc-dead"); n.Status != rings.TestClaimPending {
		t.Fatalf("终态任务待重派类应保持待确认: %s", n.Status)
	}
	if n, _ := store.Node(ctx, "p455-dead", "tc-dead-refu"); n.Status != rings.TestClaimRefuted {
		t.Fatalf("终态任务被推翻类应转不成立: %s", n.Status)
	}
	// ④ 两类不动。
	if n, _ := store.Node(ctx, "p455-live", "tc-susp"); n.Status != rings.TestClaimPending {
		t.Fatalf("操作者挂起不应被动: %s", n.Status)
	}
	if n, _ := store.Node(ctx, "p455-dead", "tc-sweep"); n.Status != rings.TestClaimPending {
		t.Fatalf("终局清点待确认不应被动: %s", n.Status)
	}

	// ⑤ 标记键一次性:新种的"待重派"类不再归位。
	p455MigPendingClaim(t, store, "p455-live", "tc-late",
		"第 1 轮反证未写回结构化结论(反证轮执行失败),待重派反证后复核")
	if err := migratePendingClaimsOnce(store); err != nil {
		t.Fatalf("二次归位不应报错: %v", err)
	}
	if n, _ := store.Node(ctx, "p455-live", "tc-late"); n.Status != rings.TestClaimPending {
		t.Fatalf("标记键已写,二次归位不应再动: %s", n.Status)
	}
}
