package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-68 ①(问题清单 #86 B):范围卡第三键"允许访问"的对账唤醒 ----
// 卡上手授＝分诊自动授＝牧者标记(信息源清单同一状态):挂等单位按信息
// 源分支回队队首(目标不在闭包内也唤醒),原会话续跑;卡按允许访问语
// 义自动收口(access 记录,红点即消不留僵尸卡)。

// TestP468ResolveWakesInfoSource 完成标准①:目标获访问授权(host 入
// 信息源清单,不在范围闭包内)→ 挂等单位回可领取＋队首标记;待决卡
// 按允许访问语义收口。
func TestP468ResolveWakesInfoSource(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p468-resolve"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")

	// 冻结步骤 s1 等待范围外目标 gitee.com(候选出卡形态)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("gitee.com", "443"), StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash",
			Note: "渗透任务:命令目标 gitee.com:443 在范围外,拦下等待批准"}},
	); err != nil {
		t.Fatal(err)
	}
	p451FreezeStep(t, store, task, "s1", "gitee.com", "443")

	// 第三键核心(卡上手授;host 入信息源清单,目标不入范围闭包)。
	if err := swarm.AllowScopeAccess(ctx, store, task, "gitee.com:443", "操作者手授(允许访问)", "creator"); err != nil {
		t.Fatal(err)
	}

	l := &Loop{TaskID: task, Store: store}
	woken, terminated := l.ResolveScopeWaits(ctx)
	if woken != 1 || terminated != 0 {
		t.Fatalf("信息源授权后应唤醒 1 终止 0: %d %d", woken, terminated)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "dispatched" {
		t.Fatalf("唤醒后步骤应回可领取(原会话续跑): %+v", step)
	}
	if v, _ := step.Detail["scope_head"].(bool); !v {
		t.Fatalf("唤醒应带队首标记: %+v", step.Detail)
	}
	// 目标不在范围闭包(允许访问≠加入资产):资产图无该目标。
	ag, _ := store.AssetGraph(ctx, task)
	for _, a := range ag.Assets {
		if strings.Contains(a.Title, "gitee.com") {
			t.Fatalf("允许访问不得入资产图: %+v", a)
		}
	}
	// 卡收口:同请求号 access 记录(待决卡使命已终)。
	rows, err := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("gitee.com", "443"))
	if err != nil || len(rows) == 0 {
		t.Fatalf("应可查卡记录: %v", err)
	}
	last := rows[len(rows)-1]
	if last.Type != rings.ApprovalRecAccess || !strings.Contains(last.Reason, "访问授权") {
		t.Fatalf("卡应按允许访问语义收口: type=%s reason=%s", last.Type, last.Reason)
	}
	// 幂等:再对账零落笔。
	if woken, terminated := l.ResolveScopeWaits(ctx); woken != 0 || terminated != 0 {
		t.Fatalf("幂等对账应零变更: %d %d", woken, terminated)
	}
}

// TestP468OtherChannelAutoClosesAccess 其他通道(牧者标记/分诊自动授)
// 授访问权后,未决卡自动按允许访问语义收口(通道无关,卡消红点消,
// 不留僵尸卡)。
func TestP468OtherChannelAutoClosesAccess(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p468-autoclose"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")

	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "g", Status: "active"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("pypi.org", ""), StepID: "s1", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash",
			Note: "渗透任务:命令目标 pypi.org 在范围外,拦下等待批准"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "查包文档", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: rings.StepScopeWait,
				Note: "scope_wait pypi.org "}},
		// 牧者标记(另一通道):host 入清单。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "shepherd",
			Payload: rings.RoeUpdated{InfoSources: &[]rings.RoeInfoSource{{
				Host: "pypi.org", Basis: "操作者标记", At: "2026-11-11 00:00:00",
			}}, Note: "信息源入清单: pypi.org"}},
	); err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: store}
	if woken, _ := l.ResolveScopeWaits(ctx); woken != 1 {
		t.Fatalf("清单命中应唤醒挂等单位: %d", woken)
	}
	rows, _ := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("pypi.org", ""))
	last := rows[len(rows)-1]
	if last.Type != rings.ApprovalRecAccess {
		t.Fatalf("未决卡应自动按允许访问收口(其他通道): type=%s", last.Type)
	}
}
