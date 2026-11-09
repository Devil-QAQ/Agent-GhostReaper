package rings

import (
	"context"
	"strings"
	"testing"
)

// ---- P4-55(问题清单 #75)+ 审核迭代(造物主 2026-11-08 定案):refuted
// 与 discarded 终态的地基层——状态词汇、verdict 校验、显示词、摘除
// 连坐废弃。 ----

// TestP455RefutedStatusWord refuted 与 discarded 终态显示词:P4-55 过渡
// 词"不成立"经 P4-56 四页签归位——两态合并"已废弃"(终了档,灰),
// 过程态显示"等待反证/反证中"(粗口径)。
func TestP455RefutedStatusWord(t *testing.T) {
	if w := ClaimStatusWord(TestClaimRefuted); w != "已废弃" {
		t.Fatalf("refuted 应显示'已废弃'(P4-56 归位): %q", w)
	}
	if w := ClaimStatusWord(TestClaimDiscarded); w != "已废弃" {
		t.Fatalf("discarded 应显示'已废弃': %q", w)
	}
	for _, s := range []string{TestClaimCandidate, TestClaimAwaitingRound} {
		if w := ClaimStatusWord(s); w != "等待反证" {
			t.Fatalf("%s 应显示'等待反证': %q", s, w)
		}
	}
	if w := ClaimStatusWord(TestClaimTesting); w != "反证中" {
		t.Fatalf("testing 应显示'反证中': %q", w)
	}
}

// TestP455RefutedVerdictValidation verdict decision=refuted 的受控入口:
// 合法(satisfied=false + 理由必填 + 条件必列);缺理由/确认自洽破坏拒收。
func TestP455RefutedVerdictValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-rings"
	mustAppend(t, s, task,
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{Action: "created"}},
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "tc-1", NodeType: NodeTestClaim, Title: "判断", Status: TestClaimTesting}},
	)
	// 合法:refuted + 理由 + 条件 + satisfied=false。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "arbiter",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
			Decision: VerdictRefuted, PendingReason: "被反证推翻",
			Conditions: []ArbiterConditionResult{{Kind: "existence_not_refuted", Required: 1}}}}); err != nil {
		t.Fatalf("refuted 裁决应通过校验: %v", err)
	}
	// 缺理由拒收(非 confirm 裁决理由必填,R4)。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "arbiter",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
			Decision:   VerdictRefuted,
			Conditions: []ArbiterConditionResult{{Kind: "existence_not_refuted", Required: 1}}}}); err == nil {
		t.Fatal("refuted 裁决缺理由应拒收")
	}
	// decision/satisfied 不自洽(refuted 不得 satisfied=true)拒收。
	if _, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "arbiter",
		Payload: TestClaimRecord{ClaimID: "tc-1", Type: TestClaimRecVerdict,
			Decision: VerdictRefuted, Satisfied: true, PendingReason: "被反证推翻",
			Conditions: []ArbiterConditionResult{{Kind: "existence_not_refuted", Required: 1}}}}); err == nil {
		t.Fatal("refuted 裁决 satisfied=true 应拒收(不自洽)")
	}
	// refuted 状态迁移落账照常。
	if _, err := s.Append(ctx, task, Event{Kind: EventNodeStateChanged, Actor: "arbiter",
		Payload: NodeStateChanged{NodeID: "tc-1", Status: TestClaimRefuted}}); err != nil {
		t.Fatalf("refuted 状态迁移应通过: %v", err)
	}
}

// TestP455ExclusionDiscardsAnchored 摘除连坐废弃(审核迭代定案:动"对象"
// 摘资产→锚定声明/假设程序确定性全废弃):非终态声明(candidate/
// testing/awaiting_round)与假设转已废弃、理由逐字"目标已摘除"、记录
// 与状态同批落账;已确认声明不动;通配摘除覆盖子域资产的声明同废;
// 幂等;勾回资产不自动复活。
func TestP455ExclusionDiscardsAnchored(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p455-discard"
	mustAppend(t, s, task, Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{Action: "created"}})
	mustAppend(t, s, task,
		na(NodeAsset, "a-x", "x.example.com", "active", AssetDomain, map[string]any{"source": AssetSourceSeed}),
		na(NodeAsset, "a-wild", "*.banned.org", "active", AssetDomain, map[string]any{"source": AssetSourceDiscovered}),
		na(NodeAsset, "a-sub", "sub.banned.org", "active", AssetSubdomain, map[string]any{"source": AssetSourceDiscovered}),
		na(NodeTestClaim, "tc-cand", "候选判断", TestClaimCandidate, "", nil),
		na(NodeTestClaim, "tc-test", "反证中判断", TestClaimTesting, "", nil),
		na(NodeTestClaim, "tc-wait", "待下轮判断", TestClaimAwaitingRound, "", nil),
		na(NodeTestClaim, "tc-ok", "已确认判断", TestClaimConfirmed, "", nil),
		na(NodeTestClaim, "tc-sub", "子域判断", TestClaimTesting, "", nil),
		na(NodeHypothesis, "h-1", "同资产猜测", HypothesisCandidate, "", nil),
		na(NodeHypothesis, "h-2", "他资产猜测", HypothesisTesting, "", nil),
		ea(EdgeAbout, "tc-cand", "a-x"), ea(EdgeAbout, "tc-test", "a-x"),
		ea(EdgeAbout, "tc-wait", "a-x"), ea(EdgeAbout, "tc-ok", "a-x"),
		ea(EdgeAbout, "tc-sub", "a-sub"), ea(EdgeAbout, "h-1", "a-x"),
		ea(EdgeAbout, "h-2", "a-sub"),
		Event{Kind: EventRoeUpdated, Actor: "creator",
			Payload: RoeUpdated{ExcludedAssets: []string{"a-x"}, Note: "摘除 x.example.com"}},
	)
	claims, hypos, err := DiscardAnchoredFindings(ctx, s, task, DiscardReasonExcluded, "creator")
	if err != nil {
		t.Fatalf("连坐废弃不应报错: %v", err)
	}
	if len(claims) != 3 || len(hypos) != 1 {
		t.Fatalf("应废弃 3 声明 1 假设(候选/反证中/待下轮+同资产猜测): %+v %+v", claims, hypos)
	}
	for _, id := range []string{"tc-cand", "tc-test", "tc-wait"} {
		n, _ := s.Node(ctx, task, id)
		if n.Status != TestClaimDiscarded {
			t.Fatalf("%s 应转已废弃: %s", id, n.Status)
		}
	}
	if n, _ := s.Node(ctx, task, "tc-ok"); n.Status != TestClaimConfirmed {
		t.Fatalf("已确认声明不动: %s", n.Status)
	}
	if n, _ := s.Node(ctx, task, "h-1"); n.Status != HypothesisDiscarded {
		t.Fatalf("同资产假设应转已废弃: %s", n.Status)
	}
	// 废弃记录理由逐字,时间线可溯。
	evs, _ := s.Events(ctx, task, 0, 0)
	discardN := 0
	for _, ev := range evs {
		if ev.Kind == EventTestClaimRecord && strings.Contains(string(ev.Payload), `"type":"discard"`) {
			if !strings.Contains(string(ev.Payload), DiscardReasonExcluded) {
				t.Fatalf("废弃理由须逐字'目标已摘除': %s", ev.Payload)
			}
			discardN++
		}
	}
	if discardN != 3 {
		t.Fatalf("应有 3 条废弃记录: %d", discardN)
	}
	// 幂等:重复调用零废弃。
	if c2, h2, err := DiscardAnchoredFindings(ctx, s, task, DiscardReasonExcluded, "creator"); err != nil || len(c2) != 0 || len(h2) != 0 {
		t.Fatalf("连坐废弃应幂等: %v %+v %+v", err, c2, h2)
	}
	// 通配摘除:子域资产(a-sub)的声明与假设随模式资产入清单而废弃。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ExcludedAssets: []string{"a-x", "a-wild"}, Note: "再摘 *.banned.org"}})
	if c3, h3, err := DiscardAnchoredFindings(ctx, s, task, DiscardReasonExcluded, "creator"); err != nil || len(c3) != 1 || len(h3) != 1 {
		t.Fatalf("通配摘除应废弃子域声明与假设: %v %+v %+v", err, c3, h3)
	}
	if n, _ := s.Node(ctx, task, "tc-sub"); n.Status != TestClaimDiscarded {
		t.Fatalf("子域判断应随通配废弃: %s", n.Status)
	}
	// 勾回不复活:清单清空后声明保持已废弃(再验走操作者深入)。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{Note: "勾回全部"}})
	if n, _ := s.Node(ctx, task, "tc-test"); n.Status != TestClaimDiscarded {
		t.Fatalf("勾回不自动复活: %s", n.Status)
	}
}
