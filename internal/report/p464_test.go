package report

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// P4-64(问题清单 #82)报告侧:反证时间线人话优先逐轮事件投影(新),
// 存量旧定稿的 repro.Rounds 合读(同轮号新事件优先);导出零 LLM、
// 同账本重导字节一致(R17)。

func p464SeedCase(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	events := []rings.Event{
		{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{
			Action: "created", Title: "P464 报告侧验证"}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-x", NodeType: rings.NodeTestClaim, Title: "原始存在性判断原文",
			Status: rings.TestClaimConfirmed, Detail: map[string]any{"unit_id": "u1"}}},
		{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", StepID: "s1", UnitID: "u2", Type: rings.TestClaimRecFalsify, Round: 1,
			ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high"}},
		{Kind: rings.EventTestClaimRecord, Actor: "swarm:u3", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", StepID: "s2", UnitID: "u3", Type: rings.TestClaimRecFalsify, Round: 2,
			ExistenceRefuted: false}},
	}
	if _, err := store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("铺链失败: %v", err)
	}
}

func p464Render(t *testing.T, store *rings.Store, task string) string {
	t.Helper()
	ctx := context.Background()
	md, err := Render(ctx, store, openBlobs(t), task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	return md
}

func TestP464ReportRoundHumanPrecedence(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p464-rep"
	p464SeedCase(t, store, task)
	// 存量旧定稿(带第 1 轮人话)+ 逐轮新事件(第 1 轮覆盖、第 2 轮新增)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", Type: rings.TestClaimRecRepro, Severity: "high",
			Rounds: []rings.FalsifyRoundHuman{{Round: 1, Doubt: "存量旧稿的疑点", How: "存量旧稿的验证", Conclusion: "已排除"}},
			Steps: []rings.ReproStep{{Seq: 1, StepID: "s1", UnitID: "u2", Method: "GET",
				URL: "http://k3.local/x", Status: 200, BlobRef: "b1"}}}},
		rings.Event{Kind: rings.EventClaimRoundHuman, Actor: "overmind", Payload: rings.ClaimRoundHuman{
			ClaimID: "tc-x", Round: 1, Doubt: "逐轮新稿的疑点", How: "逐轮新稿的验证", Conclusion: "已排除"}},
		rings.Event{Kind: rings.EventClaimRoundHuman, Actor: "overmind", Payload: rings.ClaimRoundHuman{
			ClaimID: "tc-x", Round: 2, Doubt: "第二轮的疑点", How: "第二轮的验证", Conclusion: "未排除"}},
	); err != nil {
		t.Fatalf("铺包装失败: %v", err)
	}
	md1 := p464Render(t, store, task)
	if !strings.Contains(md1, "逐轮新稿的疑点") || strings.Contains(md1, "存量旧稿的疑点") {
		t.Fatalf("同轮号新事件应优先存量 repro.Rounds")
	}
	if !strings.Contains(md1, "第二轮的疑点") {
		t.Fatalf("逐轮新增轮次的人话应呈现(锤炼逐轮语义)")
	}
	// 重导字节一致(R17:导出零 LLM)。
	if md2 := p464Render(t, store, task); md2 != md1 {
		t.Fatalf("同账本重导报告必须字节级一致")
	}
}

func TestP464ReportLegacyRoundsStillRead(t *testing.T) {
	// 存量兼容:无逐轮事件的旧定稿,人话照读 repro.Rounds。
	store := openStore(t)
	const task = "p464-rep-legacy"
	p464SeedCase(t, store, task)
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", Type: rings.TestClaimRecRepro, Severity: "high",
			Rounds: []rings.FalsifyRoundHuman{{Round: 1, Doubt: "存量旧稿的疑点", How: "存量旧稿的验证", Conclusion: "已排除"}},
			Steps: []rings.ReproStep{{Seq: 1, StepID: "s1", UnitID: "u2", Method: "GET",
				URL: "http://k3.local/x", Status: 200, BlobRef: "b1"}}}}); err != nil {
		t.Fatalf("铺存量定稿失败: %v", err)
	}
	md := p464Render(t, store, task)
	if !strings.Contains(md, "存量旧稿的疑点") {
		t.Fatalf("存量旧定稿的人话应照读(不回填不丢失)")
	}
}
