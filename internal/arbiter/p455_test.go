package arbiter

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-55(问题清单 #75):锤炼状态机补齐——推翻即不成立、中断必重派 ----
// 决策次序:被推翻 → 不成立(refuted,理由逐字);当前轮中断且重派预算
// 未尽 → ErrClaimInterrupted(收割侧自动重派);重派满 3 次仍无结论 →
// 待确认(理由逐字"反证多轮未取得结论");reopen 重开战役预算重置。

// p455Round 中断轮种步骤助手:按给定终态落一轮反证步骤(无结构化结论)。
func p455Round(t *testing.T, s *rings.Store, task, claimID, stepID, status string, round int) {
	t.Helper()
	stepDetail := map[string]any{"kind": "falsify", "testclaim_id": claimID, "action_level": "只读探测"}
	if round > 0 {
		stepDetail["round"] = round
	}
	mustAppend(t, s, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "反证", Status: status,
			Detail: stepDetail}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: claimID, Status: rings.TestClaimTesting}},
	)
}

// TestP455RefutedIsTerminal ②:存在性被推翻 → 不成立终态,理由逐字
// "被反证推翻";完整反证与单轮标准同判;前轮推翻而当前轮中断也判推翻
// (推翻即战役终局,先于中断判定);重开入口对不成立开放(ReopenClaim
// 的 overmind 侧另测,这里钉仲裁求值)。
func TestP455RefutedIsTerminal(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-refuted"

	// 完整反证:第 1 轮即推翻。
	claimFixture(t, s, task, "tc-full", nil)
	falsifyRound(t, s, task, "tc-full", "s1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-full", UnitID: "u2", Round: 1, Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: true, EvidenceRef: "sha256:aa",
	})
	j, err := JudgeClaim(ctx, s, task, "tc-full")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if j.Status != rings.TestClaimRefuted || j.Verdict.Decision != rings.VerdictRefuted {
		t.Fatalf("完整反证被推翻应判不成立: %+v", j.Verdict)
	}
	if j.Verdict.PendingReason != refutedReason || refutedReason != "被反证推翻" {
		t.Fatalf("推翻理由须逐字'被反证推翻': %q", j.Verdict.PendingReason)
	}

	// 单轮标准(P1-08 兼容):同判。
	claimFixture(t, s, task, "tc-single", func(d map[string]any) { d["standard"] = legacyStandard() })
	falsifyRound(t, s, task, "tc-single", "s2", "u3", &rings.TestClaimRecord{
		ClaimID: "tc-single", UnitID: "u3", Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: true, EvidenceRef: "sha256:bb",
	})
	j2, err := JudgeClaim(ctx, s, task, "tc-single")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if j2.Status != rings.TestClaimRefuted || j2.Verdict.PendingReason != "被反证推翻" {
		t.Fatalf("单轮标准被推翻应判不成立: %+v", j2.Verdict)
	}

	// 前轮推翻 + 当前轮中断(done 无结论):推翻先于中断,判不成立。
	claimFixture(t, s, task, "tc-early", nil)
	falsifyRound(t, s, task, "tc-early", "s3", "u4", &rings.TestClaimRecord{
		ClaimID: "tc-early", UnitID: "u4", Round: 1, Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: true, EvidenceRef: "sha256:cc",
	})
	p455Round(t, s, task, "tc-early", "s4", "done", 2)
	j3, err := JudgeClaim(ctx, s, task, "tc-early")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if j3.Status != rings.TestClaimRefuted {
		t.Fatalf("前轮推翻应即战役终局(先于中断判定): %+v", j3.Verdict)
	}
}

// TestP455InterruptedAwaitingRedispatch ①:各形态中断(审批拒绝终止/
// 完成未交结论/中止/blocked 终结)预算未尽 → ErrClaimInterrupted,不落
// 待确认;在跑/排队/scope_wait 等待批准不是中断(ErrClaimNotReady)。
func TestP455InterruptedAwaitingRedispatch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-interrupted"

	for _, tc := range []struct{ claim, step, status string }{
		{"tc-denied", "s1", "approval_denied"},
		{"tc-done", "s2", "done"},
		{"tc-aborted", "s3", "aborted"},
		{"tc-blocked", "s4", "blocked"},
	} {
		claimFixture(t, s, task, tc.claim, nil)
		p455Round(t, s, task, tc.claim, tc.step, tc.status, 1)
		if _, err := JudgeClaim(ctx, s, task, tc.claim); !errors.Is(err, ErrClaimInterrupted) {
			t.Fatalf("%s 中断(步骤 %s)预算未尽应 ErrClaimInterrupted, 得到 %v", tc.claim, tc.status, err)
		}
	}

	// 在跑/排队/等待批准不是中断。
	for _, tc := range []struct{ claim, step, status string }{
		{"tc-running", "s5", "running"},
		{"tc-queued", "s6", "dispatched"},
		{"tc-wait", "s7", "scope_wait"},
	} {
		claimFixture(t, s, task, tc.claim, nil)
		p455Round(t, s, task, tc.claim, tc.step, tc.status, 1)
		if _, err := JudgeClaim(ctx, s, task, tc.claim); !errors.Is(err, ErrClaimNotReady) {
			t.Fatalf("%s(步骤 %s)应 ErrClaimNotReady, 得到 %v", tc.claim, tc.status, err)
		}
	}
}

// TestP455RedispatchBudgetExhausted ③:同声明(当前战役)重派满 3 次仍无
// 结论 → 待确认,理由逐字"反证多轮未取得结论";reopen 重开后战役清空、
// 预算重置(中断重派从头计)。
func TestP455RedispatchBudgetExhausted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-budget"
	claimFixture(t, s, task, "tc-x", nil)

	// 原派 1 + 重派 3 = 4 次中断,预算递减:前 3 次中断(重派 0/1/2 次
	// 已用)交重派,第 4 次(重派 3 次已用)落待确认。
	for i := 0; i < 4; i++ {
		p455Round(t, s, task, "tc-x", "sx-"+strconv.Itoa(i), "approval_denied", 1)
		j, err := JudgeClaim(ctx, s, task, "tc-x")
		if i < MaxFalsifyRedispatch {
			if !errors.Is(err, ErrClaimInterrupted) {
				t.Fatalf("第 %d 次中断(已重派 %d)应交重派: %v", i+1, i, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("重派用尽应出裁决: %v", err)
		}
		if j.Status != rings.TestClaimPending || j.Verdict.PendingReason != "反证多轮未取得结论" {
			t.Fatalf("重派用尽应待确认且理由逐字: %+v", j.Verdict)
		}
	}

	// 重开:战役清空,预算重置——新一轮中断重新交重派。
	mustAppend(t, s, task, rings.Event{Kind: rings.EventTestClaimRecord, Actor: "creator",
		Payload: rings.TestClaimRecord{ClaimID: "tc-x", Type: rings.TestClaimRecReopen,
			Trigger: rings.ReopenNewEvidence, Reason: "新证据推翻旧结论"}})
	mustAppend(t, s, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator",
		Payload: rings.NodeStateChanged{NodeID: "tc-x", Status: rings.TestClaimTesting}})
	p455Round(t, s, task, "tc-x", "s-r9", "done", 9)
	if _, err := JudgeClaim(ctx, s, task, "tc-x"); !errors.Is(err, ErrClaimInterrupted) {
		t.Fatalf("重开后战役预算应重置(中断交重派): %v", err)
	}
}

// TestP455RedispatchBudgetPerRound 审核迭代(造物主 2026-11-08 定案):
// 重派额度按轮计——每轮锤炼 3 次;进下一轮即新额度(reopen 清零重算);
// 弹门拒绝(approval_denied)的换法重派计入同一额度(TestP455Redispatch
// BudgetExhausted 的 4 步全 approval_denied 已证)。
func TestP455RedispatchBudgetPerRound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-per-round"
	claimFixture(t, s, task, "tc-r", nil)
	// 第 1 轮:原派 + 3 次重派(4 步全中断,无结论)——重派 3 次后用尽。
	for i := 0; i < 4; i++ {
		p455Round(t, s, task, "tc-r", "sr-"+strconv.Itoa(i), "approval_denied", 1)
	}
	if j, err := JudgeClaim(ctx, s, task, "tc-r"); err != nil || j.Status != rings.TestClaimPending || j.Verdict.PendingReason != "反证多轮未取得结论" {
		t.Fatalf("第 1 轮重派 3 次用尽应落待确认: %v %+v", err, j)
	}
	// 轮末评估进第 2 轮:第 1 轮一条带存活解释的结论(冻结清单有 alt1
	// 未排除)→ 待下轮;随后第 2 轮中断,新轮额度从零计。
	claimFixture(t, s, task, "tc-r2", func(d map[string]any) {
		d["alternatives"] = []any{map[string]any{"id": "alt1", "label": "缓存假象"}}
	})
	falsifyRound(t, s, task, "tc-r2", "s2-a", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-r2", StepID: "s2-a", UnitID: "u2", Round: 1, Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "low", EvidenceRef: "sha256:11",
		Alternatives: []rings.AltResult{{ID: "alt1", Refuted: false}},
	})
	j2, err := JudgeClaim(ctx, s, task, "tc-r2")
	if err != nil || j2.Status != rings.TestClaimAwaitingRound {
		t.Fatalf("解释未全灭应待下轮: %v %+v", err, j2.Status)
	}
	// 第 2 轮中断(1 步无结论):新轮额度从零计——交重派,不落待确认。
	p455Round(t, s, task, "tc-r2", "s2-b", "done", 2)
	if _, err := JudgeClaim(ctx, s, task, "tc-r2"); !errors.Is(err, ErrClaimInterrupted) {
		t.Fatalf("第 2 轮中断应交重派(每轮额度独立): %v", err)
	}
}

// TestP455OperatorKilledAwaitingRuling 审核迭代:操作者终止(kill_work
// 送达后步骤 aborted;排队步骤 cancelled)——不占重派额度、不走程序
// 重派,声明留锤炼交主宰裁决:预算用尽也不落待确认;InterruptedClaim
// 带 OperatorKilled 标记。
func TestP455OperatorKilledAwaitingRuling(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-opkill"

	// 额度用尽形态(同轮 3 次换法重派 approval_denied 后,末步
	// kill_work):操作者终止不占额度、交裁决不落待确认。
	claimFixture(t, s, task, "tc-k", nil)
	for i := 0; i < 3; i++ {
		p455Round(t, s, task, "tc-k", "sk-"+strconv.Itoa(i), "approval_denied", 1)
	}
	p455Round(t, s, task, "tc-k", "sk-k", "aborted", 1)
	mustAppend(t, s, task, rings.Event{Kind: rings.EventSteerRecord, Actor: "creator",
		Payload: rings.SteerRecord{SteerID: "st-1", StepID: "sk-k", Command: rings.SteerCommandKill,
			State: rings.SteerStateDelivered, Reason: "停"}})
	info, err := InterruptedClaim(ctx, s, task, "tc-k")
	if err != nil || info == nil || !info.OperatorKilled {
		t.Fatalf("kill_work 终止应带 OperatorKilled: %v %+v", err, info)
	}
	if _, err := JudgeClaim(ctx, s, task, "tc-k"); !errors.Is(err, ErrClaimInterrupted) {
		t.Fatalf("操作者终止不占额度、预算用尽也不落待确认: %v", err)
	}

	// 排队步骤取消(cancelled):同裁决语义。
	claimFixture(t, s, task, "tc-c", nil)
	p455Round(t, s, task, "tc-c", "sc-1", "cancelled", 1)
	info2, err := InterruptedClaim(ctx, s, task, "tc-c")
	if err != nil || info2 == nil || !info2.OperatorKilled {
		t.Fatalf("取消步骤应带 OperatorKilled: %v %+v", err, info2)
	}
	if _, err := JudgeClaim(ctx, s, task, "tc-c"); !errors.Is(err, ErrClaimInterrupted) {
		t.Fatalf("取消步骤应交主宰裁决: %v", err)
	}

	// 系统 aborted(无 kill_work 记录):照常重派路径(额度内交收割)。
	claimFixture(t, s, task, "tc-s", nil)
	p455Round(t, s, task, "tc-s", "ss-1", "aborted", 1)
	info3, err := InterruptedClaim(ctx, s, task, "tc-s")
	if err != nil || info3 == nil || info3.OperatorKilled {
		t.Fatalf("系统中止不应标 OperatorKilled: %v %+v", err, info3)
	}
	if _, err := JudgeClaim(ctx, s, task, "tc-s"); !errors.Is(err, ErrClaimInterrupted) {
		t.Fatalf("系统中止预算内应交重派: %v", err)
	}
}

// TestP455InterruptedClaimExport InterruptedClaim(收割侧重派检测的
// arbiter 出口):testing 且当前轮终态无结论(或 blocked)→ 情报含步骤/
// 轮次/原因/已重派次数;非中断形态一律 nil;非 testing 声明 nil。
func TestP455InterruptedClaimExport(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-export"

	claimFixture(t, s, task, "tc-i", nil)
	p455Round(t, s, task, "tc-i", "s1", "approval_denied", 2)
	info, err := InterruptedClaim(ctx, s, task, "tc-i")
	if err != nil || info == nil {
		t.Fatalf("审批拒绝中断应有情报: %v %+v", err, info)
	}
	if info.StepID != "s1" || info.Round != 2 || info.RedispatchesUsed != 0 {
		t.Fatalf("中断情报不符: %+v", info)
	}
	if !strings.Contains(info.Reason, "审批拒绝终止") {
		t.Fatalf("中断原因须言明审批拒绝终止: %q", info.Reason)
	}
	// blocked 终结同样给情报。
	claimFixture(t, s, task, "tc-b", nil)
	p455Round(t, s, task, "tc-b", "s2", "blocked", 1)
	if info, err = InterruptedClaim(ctx, s, task, "tc-b"); err != nil || info == nil {
		t.Fatalf("blocked 终结应有情报: %v %+v", err, info)
	}
	// 在跑:无情报。
	claimFixture(t, s, task, "tc-r", nil)
	p455Round(t, s, task, "tc-r", "s3", "running", 1)
	if info, err = InterruptedClaim(ctx, s, task, "tc-r"); err != nil || info != nil {
		t.Fatalf("在跑反证不应有中断情报: %v %+v", err, info)
	}
	// 有结构化结论的终态:无情报。
	claimFixture(t, s, task, "tc-c", nil)
	falsifyRound(t, s, task, "tc-c", "s4", "u9", &rings.TestClaimRecord{
		ClaimID: "tc-c", UnitID: "u9", Round: 1, Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "low", EvidenceRef: "sha256:dd",
	})
	if info, err = InterruptedClaim(ctx, s, task, "tc-c"); err != nil || info != nil {
		t.Fatalf("有结论的终态不应有中断情报: %v %+v", err, info)
	}
}
