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
		"审批门槛 主动测试 及以上弹门",
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
		"Confirmed 发现:**1** 条;待确认:1 条",
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

	// Confirmed 条目:声明、影响面、锚点(带标题)、发现/验证单位 R12。
	for _, want := range []string{
		"### C-1 `tc-1` · 信息暴露 · 级别 high",
		"存在性声明:/old-backup/ 备份目录可公开匿名列举",
		"影响面声明:敏感数据可被未授权访问",
		"资产锚点:`web1`(http://10.10.0.5:8080)",
		"发现单位 `u1` · 发现步骤 `s1`;验证单位 `u2`(验证者 ≠ 发现者)",
		"仲裁者机器判定 Confirmed",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺 Confirmed 片段 %q:\n%s", want, md)
		}
	}

	// P2-13 完整报告:级别分布 + 反证时间线 + 复现步骤(操作方法 +
	// Burp 可验证原文,与面板同源装配)。
	for _, want := range []string{
		"级别分布(Confirmed,跟影响面证据走):high 1",
		"反证记录(年轮版本刻度):",
		"候选声明入账(发现单位 `u1`)",
		"反证(单位 `u2`):存在性未推翻 · 影响面成立(high)",
		"机器裁决 confirm(Satisfied=true)",
		"竞争性替代解释(创建时冻结):",
		"复现步骤(收割定稿 · 版本",
		"操作方法:`bash curl -sS -m 5 http://10.10.0.5:8080/old-backup/`",
		"HTTP 原文(Burp 可验证):",
		"GET http://10.10.0.5:8080/old-backup/ HTTP/1.1",
		"HTTP/1.1 200 OK",
		"mock backup archive listing: db-2026-09-01.sql.gz config.bak",
		"原始数据 `sha256:",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺 P2-13 片段 %q:\n%s", want, md)
		}
	}

	// 待确认条目:搁置理由直陈(R4)。
	for _, want := range []string{
		"### P-1 `tc-2` · 信息暴露",
		"搁置理由:反证未推翻存在性,但影响面证据不足以支撑所声明的实际影响",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺待确认片段 %q:\n%s", want, md)
		}
	}

	// 审计摘要:审批统计、无熔断、事件构成。
	for _, want := range []string{
		"审批弹门:请求 1 · 批准 1 · 拒绝 0 · 研判后放行 0",
		"熔断记录:无",
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
		"本轮无经独立反证与机器裁决确认的发现。",
		"无待确认发现。",
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
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "被熔断"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{
			Action: "stopped", Detail: map[string]any{"operator": "creator", "reason": "范围变更"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	md2, err := Render(ctx, store, nil, fused, nil)
	if err != nil {
		t.Fatalf("熔断任务渲染失败: %v", err)
	}
	if !strings.Contains(md2, "任务状态:已熔断(stopped)") ||
		!strings.Contains(md2, "熔断记录:creator 于") || !strings.Contains(md2, "理由:范围变更") {
		t.Fatalf("熔断审计呈现不符:\n%s", md2)
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

// TestRenderHTTPSExchangeTLSLabel https 交换渲染(P3-01):首行如实标注
// 目标证书校验结论;http 交换不带该行;原文逐字(Burp 可核对)。
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
		"[目标证书: 自签]",
		"POST https://10.10.0.5:8443/login HTTP/1.1",
		"user=admin&pass=cracked",
		"HTTP/1.1 200 OK",
		"<html>admin</html>",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("https 交换渲染缺 %q:\n%s", want, raw)
		}
	}
	// 逐字保真的反面:校验结论 token 不进原文(http 无标注,https 的
	// 标注只在首行且为操作者语言,非实现细节词)。
	ex.Type = rings.MirrorRecHTTP
	ex.TLS = nil
	rawHTTP, _ := renderExchangeRaw(&ex)
	if strings.Contains(rawHTTP, "目标证书") {
		t.Fatalf("http 交换不应带证书标注:\n%s", rawHTTP)
	}
}

// TestExchangeRawSide 单侧原文装配(P3-02 审核迭代:请求包/返回包分开
// 展示):请求侧含证书标注与请求行、不含状态行;响应侧含状态行与响应体、
// 不含请求行与证书标注;无响应仅异常时响应侧只剩注记;单侧拼接 == 全量
// (同一分块装配,报告/证据包渲染不受影响)。
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
	for _, want := range []string{"[目标证书: 自签]", "POST https://mock/login HTTP/1.1", "Host: mock", "user=admin"} {
		if !strings.Contains(req, want) {
			t.Fatalf("请求侧缺 %q:\n%s", want, req)
		}
	}
	if strings.Contains(req, "HTTP/1.1 200 OK") || strings.Contains(req, "<html>") {
		t.Fatalf("请求侧不应含响应内容:\n%s", req)
	}
	for _, want := range []string{"HTTP/1.1 200 OK", "Content-Type: text/html", "<html>ok</html>"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("响应侧缺 %q:\n%s", want, resp)
		}
	}
	if strings.Contains(resp, "POST https") || strings.Contains(resp, "目标证书") {
		t.Fatalf("响应侧不应含请求内容/证书标注:\n%s", resp)
	}
	// 单侧拼接 == 全量(请求侧 + 空行分隔 + 响应侧)。
	full := renderExchangeFull(&ex)
	if full != req+"\n"+resp {
		t.Fatalf("单侧拼接须与全量逐字一致:\nfull=%q\nreq+resp=%q", full, req+"\n"+resp)
	}

	// 无响应仅异常:响应侧只剩注记(无前导空行),全量为请求+空行+注记。
	ex.Response, ex.Error = nil, "转发失败: connection refused"
	resp = ExchangeRawSide(&ex, "resp")
	if resp != "[镜留痕异常: 转发失败: connection refused]\n" {
		t.Fatalf("异常侧原文形态错误:%q", resp)
	}
	if full = renderExchangeFull(&ex); full != ExchangeRawSide(&ex, "req")+"\n"+resp {
		t.Fatalf("无响应形态的单侧拼接与全量不一致:%q", full)
	}
}
