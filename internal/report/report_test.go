package report

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func openStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// openBlobs 测试用内容寻址存储(证据原文)。
func openBlobs(t *testing.T) *swarm.BlobStore {
	t.Helper()
	return swarm.NewBlobStore(filepath.Join(t.TempDir(), "blobs"))
}

// seedRepro 给 tc-1 补收割定稿:repro 记录(级别 high + 一步复现)+
// 镜交换原文入 blob(P2-13 报告正文与导出的数据面)。
func seedRepro(t *testing.T, store *rings.Store, blobs *swarm.BlobStore, task string) {
	t.Helper()
	ex := mirror.Exchange{
		ID: "ms-1", Type: "http", TaskID: task, StepID: "s2", UnitID: "u2",
		Request: &mirror.Request{
			Method: "GET", URL: "http://10.10.0.5:8080/old-backup/", Proto: "HTTP/1.1",
			Host: "10.10.0.5:8080",
			Headers: []mirror.Header{
				{Name: "User-Agent", Value: "curl/8.4.0"},
				{Name: "Accept", Value: "*/*"},
			},
		},
		Response: &mirror.Response{
			Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "text/html"}},
			Body:    []byte("mock backup archive listing: db-2026-09-01.sql.gz config.bak"),
		},
	}
	doc, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("序列化交换失败: %v", err)
	}
	ref, err := blobs.Write(doc)
	if err != nil {
		t.Fatalf("写入 blob 失败: %v", err)
	}
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high",
			Narrative: "单位 u1 执行「审计主站敏感路径」时,在 /old-backup/ 的 200 响应正文里发现备份列举;u2 独立反证 1 轮排除缓存污染与归属错误,仲裁者判定成立。",
			Steps: []rings.ReproStep{{
				Seq: 1, Round: 1, StepID: "s2", UnitID: "u2",
				Op:     "bash curl -sS -m 5 http://10.10.0.5:8080/old-backup/",
				Method: "GET", URL: "http://10.10.0.5:8080/old-backup/", Status: 200,
				BlobRef: ref,
			}},
		}},
	); err != nil {
		t.Fatalf("铺定稿失败: %v", err)
	}
}

// seedReportScenario 铺一份"跑完一轮"的任务账本:完成终态 + 双资产
// (一覆盖一未覆盖)+ 已确认事实 + 一条 Confirmed 发现与一条待确认发现
// (各自带反证与裁决记录)+ 审批留痕 + RoE 边界。
func seedReportScenario(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	must := func(events ...rings.Event) {
		t.Helper()
		if _, err := store.Append(ctx, task, events...); err != nil {
			t.Fatalf("铺账本失败: %v", err)
		}
	}

	must(
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "授权靶场演练"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{DeniedTools: []string{"nmap"}, ApprovalThreshold: rings.ActionActiveTest, Note: "仅白天测试"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "摸清靶场暴露面", Status: "active",
			Detail: map[string]any{"predicate": map[string]any{
				"op": "all_of", "conditions": []any{
					map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1}}}}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://10.10.0.5:8080", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "host2", NodeType: rings.NodeAsset, Title: "10.10.0.9", Status: "active", AssetClass: rings.AssetHost}},
	)
	// 覆盖链:意图 Step covers 资产,产出候选事实,收割转正。
	must(
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "审计主站敏感路径", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": rings.ActionReadOnly}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "web1"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f1", NodeType: rings.NodeFact, Title: "主站服务在线,HTTP 指纹确认", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f1", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f1"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{NodeID: "f1", Status: "confirmed"}},
	)
	// 两条发现:一 Confirmed 一待确认(单轮反证 + 机器裁决,含搁置理由)。
	claim := func(id, statement string) rings.NodeAdded {
		return rings.NodeAdded{NodeID: id, NodeType: rings.NodeTestClaim, Title: statement,
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "vulnclass": "信息暴露",
				"impact": "敏感数据可被未授权访问", "standard": arbiter.DefaultClaimStandard(),
				"alternatives": []any{
					map[string]any{"id": "alt1", "label": "缓存污染:列举内容可能来自中间缓存"},
					map[string]any{"id": "alt2", "label": "归属错误:列举可能来自其他资产"}}}}
	}
	must(
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: claim("tc-1", "/old-backup/ 备份目录可公开匿名列举")},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-1", ToNode: "web1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-1", ToNode: "s1"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{NodeID: "tc-1", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "反证: 备份目录列举", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "kind": "falsify", "testclaim_id": "tc-1", "action_level": rings.ActionReadOnly}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", Payload: rings.NodeStateChanged{NodeID: "s2", Status: "done"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u2", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecFalsify,
			ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high", EvidenceRef: "sha256:aa", EvidenceHead: "独立重放仍返回列举"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecVerdict, Satisfied: true,
			EvaluatedRev: 17, Conditions: []rings.ArbiterConditionResult{
				{Kind: "falsify_completed", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "existence_not_refuted", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "impact_supported", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "verifier_independent", Required: 1, Actual: 1, Satisfied: true},
			}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{NodeID: "tc-1", Status: rings.TestClaimConfirmed}},
	)
	must(
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: claim("tc-2", "/api/version 公开暴露服务版本号")},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-2", ToNode: "web1"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{NodeID: "tc-2", Status: rings.TestClaimTesting}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "反证: 版本暴露", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "kind": "falsify", "testclaim_id": "tc-2", "action_level": rings.ActionReadOnly}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3", Payload: rings.NodeStateChanged{NodeID: "s3", Status: "done"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:u3", Payload: rings.TestClaimRecord{
			ClaimID: "tc-2", StepID: "s3", UnitID: "u3", Type: rings.TestClaimRecFalsify,
			ExistenceRefuted: false, ImpactSupported: false, EvidenceHead: "重放仅返回版本号"}},
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "arbiter", Payload: rings.TestClaimRecord{
			ClaimID: "tc-2", StepID: "s3", UnitID: "u3", Type: rings.TestClaimRecVerdict, Satisfied: false,
			PendingReason: "反证未推翻存在性,但影响面证据不足以支撑所声明的实际影响",
			EvaluatedRev:  24, Conditions: []rings.ArbiterConditionResult{
				{Kind: "falsify_completed", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "existence_not_refuted", Required: 1, Actual: 1, Satisfied: true},
				{Kind: "impact_supported", Required: 1, Actual: 0, Satisfied: false},
				{Kind: "verifier_independent", Required: 1, Actual: 1, Satisfied: true},
			}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{NodeID: "tc-2", Status: rings.TestClaimPending}},
	)
	// 审批留痕 + 目标达成 + 完成终态。
	must(
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: "ap-1", StepID: "s1", UnitID: "u1", Type: rings.ApprovalRecRequested,
			ActionLevel: rings.ActionActiveTest, Intent: "主动测试", ToolName: "bash", ToolArgs: []byte(`{"command":"curl"}`)}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator", Payload: rings.ApprovalRecord{
			RequestID: "ap-1", StepID: "s1", UnitID: "u1", Type: rings.ApprovalRecApproved,
			ActionLevel: rings.ActionActiveTest, Intent: "主动测试", ToolName: "bash", DecidedBy: "creator", Reason: "允许"}},
		rings.Event{Kind: rings.EventArbiterVerdict, Actor: "arbiter", Payload: rings.ArbiterVerdict{
			GoalID: "g1", Op: "all_of", Satisfied: true, EvaluatedRev: 27, Conditions: []rings.ArbiterConditionResult{
				{Kind: "fact_confirmed", AssetID: "web1", Required: 1, Actual: 1, Satisfied: true}}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", Payload: rings.NodeStateChanged{NodeID: "g1", Status: "achieved"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "completed", Detail: map[string]any{"reason": "全部目标经仲裁者判定达成"}}},
	)
}

// TestRenderNarrativeFallback P4-13:旧定稿(P4-13 前的 repro 记录无
// Narrative 字段)如实标注不编造;无定稿的声明(待确认)不来龙去脉行。
func TestRenderNarrativeFallback(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-nar"
	seedReportScenario(t, store, task)
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "overmind", Payload: rings.TestClaimRecord{
			ClaimID: "tc-1", Type: rings.TestClaimRecRepro, Severity: "high"}}); err != nil {
		t.Fatalf("铺旧定稿失败: %v", err)
	}
	md, err := Render(context.Background(), store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}
	if !strings.Contains(md, "来龙去脉:定稿时未生成叙述") {
		t.Fatalf("旧定稿应如实标注未生成叙述:\n%s", md)
	}
	if n := strings.Count(md, "来龙去脉:"); n != 1 {
		t.Fatalf("无定稿的声明(待确认)不得有来龙去脉行(共 %d 行):\n%s", n, md)
	}
}

func TestRenderTaskReport(t *testing.T) {
	store := openStore(t)
	blobs := openBlobs(t)
	const task = "rep-t"
	seedReportScenario(t, store, task)
	seedRepro(t, store, blobs, task)
	ctx := context.Background()

	md, err := Render(ctx, store, blobs, task, nil)
	if err != nil {
		t.Fatalf("渲染报告失败: %v", err)
	}

	// 任务概要:标题、完成终态、目标、RoE 范围、确定性声明。
	for _, want := range []string{
		"# 渗透测试报告 · 授权靶场演练",
		"任务 ID:`rep-t`",
		"任务状态:已完成(completed)",
		"g1 摸清靶场暴露面[已达成]",
		"审批门槛 主动测试 及以上需批准",
		"禁止工具 nmap",
		"说明:仅白天测试",
		"零 LLM",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺概要片段 %q:\n%s", want, md)
		}
	}

	// 聚合摘要:1 Confirmed / 1 待确认 / 覆盖 1/2 / 事实 1。
	for _, want := range []string{
		"已确认发现:**1** 条;证据不足:1 条",
		"资产覆盖:1 / 2(覆盖率 50%)",
		"已确认事实:1 条",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺聚合片段 %q:\n%s", want, md)
		}
	}

	// 资产覆盖统计:服务 1/1、主机 0/1、合计。
	for _, want := range []string{
		"| 资产类 | 总数 | 已覆盖 |",
		"| 服务 | 1 | 1 |",
		"| 主机 | 1 | 0 |",
		"| **合计** | **2** | **1** |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺覆盖统计 %q:\n%s", want, md)
		}
	}

	// Confirmed 条目:声明、影响面、锚点(带标题)、发现/验证单位 R12、
	// 来龙去脉(P4-13:定稿冻结的模型叙述,读定稿零 LLM)。
	for _, want := range []string{
		"### C-1 /old-backup/ 备份目录可公开匿名列举 · 信息暴露 · 级别 high",
		"存在性判断:/old-backup/ 备份目录可公开匿名列举",
		"影响面判断:敏感数据可被未授权访问",
		"资产锚点:`web1`(http://10.10.0.5:8080)",
		"发现单位 `u1` · 发现步骤 `s1`;验证单位 `u2`(验证者 ≠ 发现者)",
		"仲裁者机器判定已确认",
		"来龙去脉:单位 u1 执行「审计主站敏感路径」时,在 /old-backup/ 的 200 响应正文里发现备份列举",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺 Confirmed 片段 %q:\n%s", want, md)
		}
	}

	// P2-13 完整报告:级别分布 + 反证时间线 + 复现步骤(Burp 可验证
	// 原文,与面板同源装配)。P4-08 ⑤:操作方法撤下报告正文(证据包
	// 收);⑥:请求行 origin-form(Host 头承载)。
	for _, want := range []string{
		"级别分布(已确认,跟影响面证据走):high 1",
		// P4-56:零在途不输出"判断在途"行;退役词零出现由闸门保证。
		"机器判定:已确认",
		"反证记录:",
		"判断已记录，等待安排反证(发现单位 `u1`)",
		"反证(单位 `u2`):存在性未推翻 · 影响面:证据充分(high)",
		"机器判定:已确认",
		"其他可能原因(创建时冻结):",
		"复现步骤(检查归纳定稿",
		"HTTP 原文(Burp 可验证):",
		"GET /old-backup/ HTTP/1.1",
		"Host: 10.10.0.5:8080",
		"HTTP/1.1 200 OK",
		"mock backup archive listing: db-2026-09-01.sql.gz config.bak",
		"原始数据 `sha256:",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺 P2-13 片段 %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "操作方法") {
		t.Fatalf("P4-08 ⑤:操作方法(op 行)不得进报告正文(证据包收):\n%s", md)
	}
	if strings.Contains(md, "GET http://10.10.0.5:8080/ HTTP/1.1") {
		t.Fatalf("P4-08 ⑥:请求行须 origin-form(绝对 URI 不得出现):\n%s", md)
	}

	// 证据不足条目(P4-56 改词):理由直陈(R4)。
	for _, want := range []string{
		"### P-1 /api/version 公开暴露服务版本号 · 信息暴露",
		"证据不足:反证未推翻存在性,但影响面证据不足以支撑所声明的实际影响",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺待确认片段 %q:\n%s", want, md)
		}
	}

	// 审计摘要:审批统计、无熔断、事件构成(P4-51 ⑤:规划后放行废除,
	// 零 forced 不占行)。
	for _, want := range []string{
		"需批准:请求 1 · 批准 1 · 拒绝 0\n",
		"终止记录:无",
		"testclaim.record ×5",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺审计片段 %q:\n%s", want, md)
		}
	}
}

// TestRenderDeterministic 字节级可重现:同账本两次导出一致;他任务追加
// 事件不串扰(task_id 隔离)。
func TestRenderDeterministic(t *testing.T) {
	store := openStore(t)
	const task = "rep-det"
	seedReportScenario(t, store, task)
	ctx := context.Background()

	a, err := Render(ctx, store, nil, task, nil)
	if err != nil {
		t.Fatalf("首次渲染失败: %v", err)
	}
	b, err := Render(ctx, store, nil, task, nil)
	if err != nil {
		t.Fatalf("二次渲染失败: %v", err)
	}
	if a != b {
		t.Fatalf("相同账本状态两次导出必须字节级一致")
	}
	// 隔离:另一任务追加事件后,本任务报告不变。
	other := "rep-other"
	if _, err := store.Append(ctx, other,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "他任务"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "x1", NodeType: rings.NodeAsset, Title: "http://other", Status: "active", AssetClass: rings.AssetService}},
	); err != nil {
		t.Fatalf("铺他任务失败: %v", err)
	}
	c, err := Render(ctx, store, nil, task, nil)
	if err != nil {
		t.Fatalf("三次渲染失败: %v", err)
	}
	if c != a {
		t.Fatalf("他任务事件不得影响本任务报告(task_id 隔离被破坏)")
	}
	if !strings.Contains(c, "任务状态:已完成(completed)") {
		t.Fatalf("报告应仍为完成态")
	}
}

// TestRenderEdgeStates 空账本边角:零资产零发现的运行中任务照常出报告
// (占位诚实、不造数据);不存在任务报错。
func TestRenderEdgeStates(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "rep-empty"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "空任务"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "started"}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	md, err := Render(ctx, store, nil, task, nil)
	if err != nil {
		t.Fatalf("空任务渲染失败: %v", err)
	}
	for _, want := range []string{
		"任务状态:运行中(started)",
		"本轮无经独立反证与机器判定确认的发现。",
		"无证据不足发现。",
		"资产覆盖:0 / 0",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("空任务报告缺 %q:\n%s", want, md)
		}
	}
	if _, err := Render(ctx, store, nil, "no-such", nil); err == nil {
		t.Fatalf("不存在任务应报错")
	}
	// 熔断留痕场景:stopped 带操作者与理由。
	const fused = "rep-fused"
	if _, err := store.Append(ctx, fused,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "被终止"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{
			Action: "stopped", Detail: map[string]any{"operator": "creator", "reason": "范围变更"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	md2, err := Render(ctx, store, nil, fused, nil)
	if err != nil {
		t.Fatalf("终止任务渲染失败: %v", err)
	}
	if !strings.Contains(md2, "任务状态:已停止(手动终止)") ||
		!strings.Contains(md2, "终止记录:creator 于") || !strings.Contains(md2, "理由:范围变更") {
		t.Fatalf("终止审计呈现不符:\n%s", md2)
	}
	// 已停止统称的结束方式括注(P4-25 审核迭代):整体超时与收口结束
	// 两种有序收尾各验一例,与面板悬停同口径。
	for _, tc := range []struct{ id, by, want string }{
		{"rep-settled-gate", rings.SettleByTaskGate, "任务状态:已停止(整体超时)"},
		{"rep-settled-closure", rings.SettleByClosurePrompt, "任务状态:已停止(收口结束)"},
	} {
		if _, err := store.Append(ctx, tc.id,
			rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "收尾终态"}},
			rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{
				Action: "stopped", Detail: map[string]any{"settled_by": tc.by}}},
		); err != nil {
			t.Fatalf("铺账本失败: %v", err)
		}
		md, err := Render(ctx, store, nil, tc.id, nil)
		if err != nil {
			t.Fatalf("收尾终态渲染失败: %v", err)
		}
		if !strings.Contains(md, tc.want) {
			t.Fatalf("%s 应呈 %q:\n%s", tc.id, tc.want, md)
		}
	}
}

// TestRenderBlockedOverlay 受阻单显覆盖层(P4-27 ② 报告同口径):
// 活动任务存在模型故障归因的 blocked 步骤 → 状态词单显"受阻";
// 确定性归因不触发(⑦:程序缺陷不计故障);冻结态单显冬眠中不叠加。
func TestRenderBlockedOverlay(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	seedBlockedTask := func(task, note string) {
		if _, err := store.Append(ctx, task,
			rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "受阻任务"}},
			rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "started"}},
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: "s1", NodeType: rings.NodeStep, Title: "探测意图", Status: "dispatched"}},
			rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
				Payload: rings.NodeStateChanged{NodeID: "s1", Status: "blocked", Note: note}},
		); err != nil {
			t.Fatalf("铺账本失败: %v", err)
		}
	}
	seedBlockedTask("rep-blocked-model", "model_fault")
	md, err := Render(ctx, store, nil, "rep-blocked-model", nil)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(md, "任务状态:受阻(started)") {
		t.Fatalf("模型故障 blocked 应单显受阻:\n%s", md)
	}
	// 确定性归因:不触发受阻覆盖层。
	seedBlockedTask("rep-blocked-det", "determinant")
	md, err = Render(ctx, store, nil, "rep-blocked-det", nil)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(md, "任务状态:运行中(started)") {
		t.Fatalf("确定性归因不应触发受阻覆盖层:\n%s", md)
	}
}

// TestRenderTimesFromLedger 时刻全部取自账本(非墙钟):发生在已知时刻的
// 事件决定"数据截至"。
func TestRenderTimesFromLedger(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "rep-time"
	known := time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager", OccurredAt: known,
		Payload: rings.TaskLifecycle{Action: "created", Title: "定时任务"},
	}); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	md, err := Render(ctx, store, nil, task, nil)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(md, "2026-09-20 08:30:00 UTC") {
		t.Fatalf("时刻应取自账本事件(确定性):\n%s", md)
	}
}

// TestRenderCountsMirrorRecords P2-01 面板可见性:镜留痕事件自动进入
// 报告"年轮审计摘要 · 事件构成"(kindCount 机制 kind 无关,流量页 UI
// 属 [P3],P2 内以报告为直观验收面)。
func TestRenderCountsMirrorRecords(t *testing.T) {
	store := openStore(t)
	const task = "t-report-mirror"
	seedReportScenario(t, store, task)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror",
			Payload: rings.MirrorRecord{
				ExchangeID: "ms-1.1", Type: rings.MirrorRecHTTP, StepID: "s1", UnitID: "u1",
				Method: "GET", URL: "http://10.10.0.5:8080/api/version", Status: 200,
				BlobRef: "sha256:" + strings.Repeat("ab", 32), RespBytes: 44,
			}},
		rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror",
			Payload: rings.MirrorRecord{
				ExchangeID: "ms-1.2", Type: rings.MirrorRecTunnel, StepID: "s1", UnitID: "u1",
				URL: "secret.example.com:443", ReqBytes: 517, RespBytes: 8192,
			}},
	); err != nil {
		t.Fatalf("铺镜留痕失败: %v", err)
	}
	out, err := Render(ctx, store, nil, task, nil)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(out, "mirror.record ×2") {
		t.Fatalf("事件构成应含 mirror.record ×2:\n%s", out)
	}
}

// TestRenderHTTPSExchangeTLSLabel https 交换渲染(P3-01 起;P4-08 ⑥ 新
// 口径):请求行 origin-form(Host 头承载,复制即贴 Burp Repeater 可发),
// 目标证书结论不进包文(包外信息行,审计事实在 blob);http 交换同形;
// 原文逐字(Burp 可核对)。
func TestRenderHTTPSExchangeTLSLabel(t *testing.T) {
	ex := mirror.Exchange{
		ID: "ms-hs", Type: rings.MirrorRecHTTPS, TaskID: "t", StepID: "s", UnitID: "u",
		Request: &mirror.Request{
			Method: "POST", URL: "https://10.10.0.5:8443/login", Proto: "HTTP/1.1",
			Host:    "10.10.0.5:8443",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "application/x-www-form-urlencoded"}},
			Body:    []byte("user=admin&pass=cracked"),
		},
		Response: &mirror.Response{
			Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "text/html"}},
			Body:    []byte("<html>admin</html>"),
		},
		TLS: &mirror.TLSInfo{
			Host: "10.10.0.5", Validity: mirror.TLSValidSelfSigned,
			PeerSubject: "CN=mock-https", PeerIssuer: "CN=mock-https",
			VerifyError: "x509: certificate signed by unknown authority",
		},
	}
	raw, _ := renderExchangeRaw(&ex)
	for _, want := range []string{
		"POST /login HTTP/1.1",
		"Host: 10.10.0.5:8443",
		"user=admin&pass=cracked",
		"HTTP/1.1 200 OK",
		"<html>admin</html>",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("https 交换渲染缺 %q:\n%s", want, raw)
		}
	}
	// 逐字保真的反面:证书结论不进包文(P4-08 ⑥:结论是包外信息行),
	// 绝对 URI 不出现在请求行(origin-form)。
	if strings.Contains(raw, "目标证书") {
		t.Fatalf("证书结论不得进包文:\n%s", raw)
	}
	if strings.Contains(raw, "https://10.10.0.5:8443/login HTTP") {
		t.Fatalf("请求行须 origin-form:\n%s", raw)
	}
	ex.Type = rings.MirrorRecHTTP
	ex.TLS = nil
	rawHTTP, _ := renderExchangeRaw(&ex)
	if strings.Contains(rawHTTP, "目标证书") {
		t.Fatalf("http 交换不应带证书标注:\n%s", rawHTTP)
	}
}

// TestExchangeRawSide 单侧原文装配(P3-02 审核迭代:请求包/返回包分开
// 展示):请求侧 Burp 就绪(P4-08 ⑥:origin-form 请求行 + Host 头,
// 证书结论不进包文)、不含状态行;响应侧含状态行与响应体、不含请求行;
// 无响应仅异常时响应侧只剩注记;单侧拼接 == 全量(同一分块装配,报告/
// 证据包渲染不受影响)。
func TestExchangeRawSide(t *testing.T) {
	ex := mirror.Exchange{
		ID: "ms-x.1", Type: rings.MirrorRecHTTPS, StepID: "s1", UnitID: "u1",
		Request: &mirror.Request{
			Method: "POST", URL: "https://mock/login", Proto: "HTTP/1.1", Host: "mock",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "application/x-www-form-urlencoded"}},
			Body:    []byte("user=admin"),
		},
		Response: &mirror.Response{
			Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "text/html"}},
			Body:    []byte("<html>ok</html>"),
		},
		TLS: &mirror.TLSInfo{Validity: mirror.TLSValidSelfSigned},
	}
	req := ExchangeRawSide(&ex, "req")
	resp := ExchangeRawSide(&ex, "resp")
	for _, want := range []string{"POST /login HTTP/1.1", "Host: mock", "user=admin"} {
		if !strings.Contains(req, want) {
			t.Fatalf("请求侧缺 %q:\n%s", want, req)
		}
	}
	if strings.Contains(req, "目标证书") || strings.Contains(req, "https://mock") {
		t.Fatalf("请求侧须 Burp 就绪(无证书行、无绝对 URI):\n%s", req)
	}
	if strings.Contains(req, "HTTP/1.1 200 OK") || strings.Contains(req, "<html>") {
		t.Fatalf("请求侧不应含响应内容:\n%s", req)
	}
	for _, want := range []string{"HTTP/1.1 200 OK", "Content-Type: text/html", "<html>ok</html>"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("响应侧缺 %q:\n%s", want, resp)
		}
	}
	if strings.Contains(resp, "POST /login") || strings.Contains(resp, "目标证书") {
		t.Fatalf("响应侧不应含请求内容/证书标注:\n%s", resp)
	}
	// 单侧拼接 == 全量(请求侧 + CRLF 分隔 + 响应侧,线上行尾)。
	full := renderExchangeFull(&ex)
	if full != req+"\r\n"+resp {
		t.Fatalf("单侧拼接须与全量逐字一致:\nfull=%q\nreq+resp=%q", full, req+"\r\n"+resp)
	}

	// 无响应仅异常:响应侧只剩注记(无前导空行),全量为请求+注记
	//(注记自带前导 \r\n 兼作分隔)。
	ex.Response, ex.Error = nil, "转发失败: connection refused"
	resp = ExchangeRawSide(&ex, "resp")
	if resp != "[镜留痕异常: 转发失败: connection refused]\r\n" {
		t.Fatalf("异常侧原文形态错误:%q", resp)
	}
	if full = renderExchangeFull(&ex); full != ExchangeRawSide(&ex, "req")+"\r\n"+resp {
		t.Fatalf("无响应形态的单侧拼接与全量不一致:%q", full)
	}
}

// TestP446StepStatusWordsAligned 报告步骤状态词与面板口径一致(P4-46 ②
// 状态总表):dispatched=排队中、aborted=已中止、approval_denied=审批拒绝
// 终止——报告导出的状态词与 web/lib/api.js STEP_STATUS 同词,旧词零残留。
func TestP446StepStatusWordsAligned(t *testing.T) {
	for status, want := range map[string]string{
		"dispatched": "排队中", "running": "执行中", "done": "已完成",
		"exhausted": "预算耗尽", "blocked": "受阻", "approval_denied": "审批拒绝终止",
		"cancelled": "已取消", "aborted": "已中止", "failed": "失败",
	} {
		if stepStatusWordsMap[status] != want {
			t.Fatalf("报告步骤词 %s = %q,应为 %q", status, stepStatusWordsMap[status], want)
		}
	}
	for _, old := range []string{"已派发", "已终止"} {
		for status, word := range stepStatusWordsMap {
			if strings.Contains(word, old) {
				t.Fatalf("报告步骤词 %s 仍含旧词 %q: %q", status, old, word)
			}
		}
	}
	// 计数固定序清单必须覆盖词表全部键(P4-46 ②:approval_denied 曾漏列,
	// 计数入总数而词被吞)。
	if got := stepStatusWords(map[string]int{
		"done": 1, "exhausted": 1, "failed": 1, "blocked": 1, "approval_denied": 1,
		"aborted": 1, "cancelled": 1, "running": 1, "dispatched": 1,
	}); !strings.Contains(got, "受阻 1") || !strings.Contains(got, "审批拒绝终止 1") ||
		!strings.Contains(got, "排队中 1") || !strings.Contains(got, "已中止 1") {
		t.Fatalf("步骤计数词应含全部九态词(受阻/审批拒绝终止/排队中/已中止),实际: %q", got)
	}
}
