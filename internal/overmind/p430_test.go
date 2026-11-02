package overmind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// P4-30(问题清单 #51):立判危害门槛、定级校准锚与存量复核标记。

// TestOvermindP430PromptClauses 主宰纪律与输出契约携带门槛/锚/复核字段:
// 虫群侧共用句(swarm.ClaimHarmGate)逐字进主宰纪律,组合立判与存量
// 复核同守此门;契约带 report_exclusions 可选字段。
func TestOvermindP430PromptClauses(t *testing.T) {
	if !strings.Contains(overmindDisciplineRules, swarm.ClaimHarmGate) {
		t.Fatalf("主宰纪律段须逐字携带立判危害门槛(P4-30 ①)")
	}
	for _, want := range []string{
		"report_exclusions",
		"不纳入报告",
		"报告只收已确认且非信息性判断",
		"已标记的不重复标记",
	} {
		if !strings.Contains(overmindDisciplineRules, want) {
			t.Fatalf("主宰纪律段缺存量复核条款 %q", want)
		}
	}
	if !strings.Contains(overmindOutputContract, "report_exclusions") {
		t.Fatalf("输出契约须声明 report_exclusions 可选字段")
	}
}

// TestApplyReportExclusion 存量复核标记落账:程序校验形状(判断在快照
// 内、理由非空、锚点确为判断),落 testclaim.record(report_review)
// 追加事件;拒收路径逐条。
func TestApplyReportExclusion(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "om-p430"
	must := func(events ...rings.Event) {
		t.Helper()
		if _, err := store.Append(ctx, task, events...); err != nil {
			t.Fatalf("铺账本失败: %v", err)
		}
	}
	must(
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://10.10.0.5:8080", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "反证: 滑块验证码", Status: "done",
			Detail: map[string]any{"kind": "falsify", "action_level": "只读探测"}}},
	)
	must(rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
		NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "登录滑块验证码可被脚本解算",
		Status: rings.TestClaimConfirmed, Detail: map[string]any{"unit_id": "u1", "impact": "可自动完成滑块验证"}}})
	rev, err := store.CurrentRevision(ctx, task)
	if err != nil {
		t.Fatalf("查版本失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}

	// 理由为空:拒收。
	if err := l.applyReportExclusion(ctx, reportExclusionPlan{ClaimID: "tc-1"}, rev); err == nil {
		t.Fatalf("无理由的标记须被拒收")
	}
	// 锚点不是判断:拒收。
	if err := l.applyReportExclusion(ctx, reportExclusionPlan{ClaimID: "s1", Reason: "信息性"}, rev); err == nil {
		t.Fatalf("锚点非判断须被拒收")
	}
	// 不在本轮快照内(判断创建晚于评估版本):拒收。
	if err := l.applyReportExclusion(ctx, reportExclusionPlan{ClaimID: "tc-1", Reason: "信息性"}, rev-1); err == nil {
		t.Fatalf("快照外判断须被拒收")
	}
	// 合规标记:落 report_review 追加事件,判断节点与状态不动。
	if err := l.applyReportExclusion(ctx, reportExclusionPlan{
		ClaimID: "tc-1", Reason: "滑块可解只是登录前置条件,无未授权访问或边界突破"}, rev); err != nil {
		t.Fatalf("合规标记失败: %v", err)
	}
	evs, err := store.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查事件失败: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil {
			continue
		}
		if r.ClaimID == "tc-1" && r.Type == rings.TestClaimRecReportReview &&
			r.Reason == "滑块可解只是登录前置条件,无未授权访问或边界突破" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未找到 report_review 标记记录")
	}
	n, err := store.Node(ctx, task, "tc-1")
	if err != nil {
		t.Fatalf("查判断失败: %v", err)
	}
	if n.Status != rings.TestClaimConfirmed {
		t.Fatalf("账本不涂改:判断状态须保持 confirmed,实际 %s", n.Status)
	}
}

// TestSnapshotCarriesReportState 快照发现区带影响面、定稿级别与已标记
// 标注(存量复核的模型输入;已标记的不重复标记)。
func TestSnapshotCarriesReportState(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "om-p430-snap"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "登录滑块验证码可被脚本解算",
			Status: rings.TestClaimConfirmed, Detail: map[string]any{"unit_id": "u1", "impact": "可自动完成滑块验证"}}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "medium"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecReportReview, Reason: "纯前置条件,无直接危害"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-2", NodeType: rings.NodeTestClaim, Title: "/api/users 无需登录返回用户名单",
			Status: rings.TestClaimConfirmed, Detail: map[string]any{"unit_id": "u1", "impact": "未授权获取全部用户敏感信息"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	l := &Loop{TaskID: task, Store: store}
	text, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatalf("构建快照失败: %v", err)
	}
	for _, want := range []string{
		"tc-1",
		"[级别 medium]",
		"[已标记不纳入报告]",
		"(影响面: 可自动完成滑块验证)",
		"tc-2",
		"(影响面: 未授权获取全部用户敏感信息)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("快照发现区缺 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tc-2 [已标记") || strings.Contains(text, "tc-2 (影响面: 未授权获取全部用户敏感信息) [已标记") {
		t.Fatalf("未标记的判断不得带已标记标注:\n%s", text)
	}
}

// TestParseDecomposeReportExclusions 拆解回复解析 report_exclusions。
func TestParseDecomposeReportExclusions(t *testing.T) {
	reply := `{"assessment":{"q_access":"无"},"new_intents":[],
		"report_exclusions":[{"claim_id":"tc-1","reason":"滑块可解只是前置条件,无直接危害"}]}`
	r, err := parseIntents(reply)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(r.ReportExclusions) != 1 || r.ReportExclusions[0].ClaimID != "tc-1" ||
		r.ReportExclusions[0].Reason != "滑块可解只是前置条件,无直接危害" {
		t.Fatalf("report_exclusions 解析不符: %+v", r.ReportExclusions)
	}
}
