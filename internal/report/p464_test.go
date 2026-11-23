package report

import (
	"context"
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
			EvidenceHead: "旧格式结论(结构化字段已删)"}},
		{Kind: rings.EventTestClaimRecord, Actor: "swarm:u3", Payload: rings.TestClaimRecord{
			ClaimID: "tc-x", StepID: "s2", UnitID: "u3", Type: rings.TestClaimRecFalsify, Round: 2,
			EvidenceHead: "旧格式结论"}},
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

// (TestP464ReportRoundHumanPrecedence 已随逐轮人话链路退役删除,P5-03③。)
// (TestP464ReportLegacyRoundsStillRead 已随逐轮人话链路退役删除,
// P5-03③——旧定稿 rounds 按未知字段忽略。)
