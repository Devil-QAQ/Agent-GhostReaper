package rings

// P4-36 面板按需取数(问题清单 #57 ②)的投影测试:增量维护与全量重放
// 同源(重建后逐项一致)、升级旧库自动回填、摘要字段与事件扫描语义
// 对账(latest-wins / reopen 清战役 / 计数)。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// p436Seed 构造一个覆盖全部新投影面的任务:单位会话(含续跑链 billable
// 累加)、会话标记、审批记录、判断卷宗(创建/反证/裁决/定稿/复测/重放/
// 复核/重开/证据绑定)、假设结论、任务摘要、收口状态机、成果追问、
// 镜留痕摘要、节点时刻列。
func p436Seed(t *testing.T, s *Store, task string) (claimID string, reproRev int64) {
	t.Helper()
	ctx := context.Background()
	must := func(revs []int64, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed 追加失败: %v", err)
		}
	}
	// created(带描述)+ 种子资产。
	must(s.Append(ctx, task,
		Event{Kind: EventTaskLifecycle, Actor: "manager", Payload: TaskLifecycle{
			Action: "created", Title: "p436", Detail: map[string]any{"description": "测试描述"}}},
		Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{
			NodeID: "a-1", NodeType: NodeAsset, Title: "seed.example.com", Status: "active",
			AssetClass: AssetDomain, Detail: map[string]any{"source": AssetSourceSeed}}},
	))
	// 手动资产(不入目标域,但算收口新发现锚点)。
	must(s.Append(ctx, task, Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{
		NodeID: "a-2", NodeType: NodeAsset, Title: "manual.example.com", Status: "active",
		AssetClass: AssetDomain, Detail: map[string]any{"source": AssetSourceManual}}}))
	// 单位会话:u1 起飞 → 收场;原会话续跑第二段(billable 累加)。
	must(s.Append(ctx, task,
		Event{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{
			StepID: "s-1", UnitID: "u1", TranscriptID: "swarm-u1", Type: SwarmRecSessionStarted}},
		Event{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{
			StepID: "s-1", UnitID: "u1", TranscriptID: "swarm-u1", Type: SwarmRecSessionEnd,
			Note: "reason=模型连续失败:xxx turns=3 status=blocked billable=12.5s gate_wait=0s"}},
		Event{Kind: EventSwarmRecord, Actor: "swarm:u1", Payload: SwarmRecord{
			StepID: "s-1", UnitID: "u1", TranscriptID: "swarm-u1", Type: SwarmRecSessionEnd,
			Note: "reason=completed turns=9 status=done billable=30s gate_wait=2s"}},
	))
	// 审批:requested → denied(拒绝线)→ forced。
	must(s.Append(ctx, task,
		Event{Kind: EventApprovalRecord, Actor: "gate", Payload: ApprovalRecord{
			RequestID: "ap-1", StepID: "s-9", UnitID: "u9", Type: ApprovalRecRequested,
			ActionLevel: "主动测试", ToolName: "bash", Intent: "探测"}},
		Event{Kind: EventApprovalRecord, Actor: "gate", Payload: ApprovalRecord{
			RequestID: "ap-1", StepID: "s-9", UnitID: "u9", Type: ApprovalRecDenied,
			ActionLevel: "主动测试", ToolName: "bash", Intent: "探测",
			DenyLine: DenyLineHardRule, Reason: "内置硬规则命中:系统破坏命令 · 删库"}},
		Event{Kind: EventApprovalRecord, Actor: "creator", Payload: ApprovalRecord{
			RequestID: "ap-1", StepID: "s-9", UnitID: "u9", Type: ApprovalRecForced,
			ToolName: "bash", Reason: "复核可执行"}},
	))
	// 判断:创建(冻结解释 + 发现单位锚)→ 反证步骤入战役 → falsify →
	// 裁决 pending → reopen 清战役 → 新 falsify → 裁决 confirm → 定稿。
	claimID = "tc-p436"
	must(s.Append(ctx, task, Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
		NodeID: claimID, NodeType: NodeTestClaim, Title: "存在未授权读取", Status: "candidate",
		Detail: map[string]any{
			"unit_id": "u1", "step_id": "s-1", "transcript_id": "swarm-u1",
			"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存污染"}, map[string]any{"id": "alt2", "label": "归属错误"}},
		}}}))
	must(s.Append(ctx, task, Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
		NodeID: "s-f1", NodeType: NodeStep, Title: "反证第 1 轮", Status: "dispatched",
		Detail: map[string]any{"testclaim_id": claimID, "round": 1}}}))
	must(s.Append(ctx, task, Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{
		NodeID: "s-f1", Status: "running"}}))
	must(s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "swarm:u2", Payload: TestClaimRecord{
		ClaimID: claimID, Type: TestClaimRecFalsify, StepID: "s-f1", UnitID: "u2", Round: 1,
		Alternatives: []AltResult{{ID: "alt1", Refuted: true}}}}))
	must(s.Append(ctx, task,
		Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "s-f1", Status: "done"}},
		Event{Kind: EventTestClaimRecord, Actor: "arbiter", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecVerdict, Decision: VerdictPending,
			PendingReason: "证据不足", Conditions: []ArbiterConditionResult{{Kind: "falsify_completed", Satisfied: true}}}},
		Event{Kind: EventNodeStateChanged, Actor: "arbiter", Payload: NodeStateChanged{NodeID: claimID, Status: TestClaimPending}},
		Event{Kind: EventTestClaimRecord, Actor: "creator", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecReopen, Trigger: ReopenManual, Reason: "复核"}},
		Event{Kind: EventNodeStateChanged, Actor: "arbiter", Payload: NodeStateChanged{NodeID: claimID, Status: TestClaimTesting}},
	))
	// 新战役步骤(reopen 后)与反证。
	must(s.Append(ctx, task, Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
		NodeID: "s-f2", NodeType: NodeStep, Title: "反证第 2 轮", Status: "dispatched",
		Detail: map[string]any{"testclaim_id": claimID, "round": 2}}}))
	must(s.Append(ctx, task,
		Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "s-f2", Status: "running"}},
		Event{Kind: EventSwarmRecord, Actor: "swarm:u2", Payload: SwarmRecord{
			StepID: "s-f2", UnitID: "u2", TranscriptID: "swarm-u2", Type: SwarmRecSessionStarted}},
		Event{Kind: EventTestClaimRecord, Actor: "swarm:u2", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecFalsify, StepID: "s-f2", UnitID: "u2", Round: 2,
			ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high",
			Alternatives: []AltResult{{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}}}},
		Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "s-f2", Status: "done"}},
		Event{Kind: EventTestClaimRecord, Actor: "arbiter", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecVerdict, Decision: VerdictConfirm, Satisfied: true,
			Conditions: []ArbiterConditionResult{{Kind: "falsify_completed", Satisfied: true}}}},
		Event{Kind: EventNodeStateChanged, Actor: "arbiter", Payload: NodeStateChanged{NodeID: claimID, Status: TestClaimConfirmed}},
	))
	// 定稿(新旧混合:Steps 带三层小字段,Process 引用清单;Variants 旧口径)。
	revs, err := s.Append(ctx, task, Event{Kind: EventTestClaimRecord, Actor: "overmind", Payload: TestClaimRecord{
		ClaimID: claimID, Type: TestClaimRecRepro, Severity: "high",
		Narrative: "在首页 JS 里发现硬编码凭据。",
		Steps: []ReproStep{{Seq: 1, StepID: "s-f2", UnitID: "u2", Method: "GET",
			URL: "http://t/api", Status: 200, Desc: "直接请求该接口", Short: "拿数据", Purpose: "触发未授权读取",
			BlobRef: "sha256:step1", ExchangeID: "ex-1"}},
		Process:  []ReproProcessRef{{ExchangeID: "ex-1", BlobRef: "sha256:step1"}, {ExchangeID: "ex-2", BlobRef: "sha256:p2"}},
		Variants: []ReproStep{{Seq: 2, StepID: "s-f2", UnitID: "u2", Op: "curl -sS http://t/api?x=1", Method: "GET", Status: 200}},
	}})
	if err != nil {
		t.Fatalf("定稿追加失败: %v", err)
	}
	reproRev = revs[0]
	// 证据绑定 + 复测 + 重放 + 复核标记。
	must(s.Append(ctx, task,
		Event{Kind: EventMirrorRecord, Actor: "mirror", Payload: MirrorRecord{
			ExchangeID: "ex-1", Type: MirrorRecHTTP, StepID: "s-f2", UnitID: "u2", BlobRef: "sha256:step1",
			Method: "GET", URL: "http://t/api", Status: 200}},
		Event{Kind: EventMirrorRecord, Actor: "mirror", Payload: MirrorRecord{
			ExchangeID: "ex-2", Type: MirrorRecHTTPS, StepID: "s-f2", UnitID: "u2", BlobRef: "sha256:p2",
			Method: "GET", URL: "https://t/x", Status: 200, TLS: "trusted"}},
		Event{Kind: EventTestClaimRecord, Actor: "creator", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecEvidence, Exchanges: []string{"ex-1", "ex-2"}, By: "creator"}},
		Event{Kind: EventTestClaimRecord, Actor: "swarm:rt-1", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecRetest, StepID: "rt-1", UnitID: "rt-1",
			RetestVerdict: "unfixed", Round: 1, EvidenceHead: "200 仍在"}},
		Event{Kind: EventTestClaimRecord, Actor: "swarm:rp-1", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecReplay, StepID: "rp-1", ReplayVerdict: "reproduced",
			ReplayResults: []ReplayResult{{ExchangeID: "ex-1", NewExchangeID: "ex-3", Method: "GET", URL: "http://t/api", OldStatus: 200, NewStatus: 200, BodySame: true}}}},
		Event{Kind: EventTestClaimRecord, Actor: "overmind", Payload: TestClaimRecord{
			ClaimID: claimID, Type: TestClaimRecReportReview, Reason: "纯信息性观察"}},
	))
	// 假设评估与结论。
	must(s.Append(ctx, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "h-1", NodeType: NodeHypothesis, Title: "猜测:存在旧备份", Status: "candidate"}},
		Event{Kind: EventHypothesisRecord, Actor: "overmind", Payload: HypothesisRecord{
			HypothesisID: "h-1", Type: HypothesisRecEvaluation, Decision: HypothesisVerify, Reason: "值得跟"}},
		Event{Kind: EventHypothesisRecord, Actor: "swarm:u3", Payload: HypothesisRecord{
			HypothesisID: "h-1", Type: HypothesisRecResult, StepID: "s-h", UnitID: "u3", Verdict: HypothesisSupported}},
	))
	// 事实(确认时刻列;收口锚点先于研判轮)。
	must(s.Append(ctx, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "f-1", NodeType: NodeFact, Title: "接口返回全员数据", Status: "candidate"}},
		Event{Kind: EventNodeStateChanged, Actor: "overmind", Payload: NodeStateChanged{NodeID: "f-1", Status: "confirmed"}},
	))
	// 会话标记(重命名 + 置顶)。
	title := "主会话"
	must(s.Append(ctx, task, Event{Kind: EventSessionMeta, Actor: "creator", Payload: SessionMeta{
		SessionID: "swarm-u1", Title: &title, Pinned: boolPtr(true)}}))
	// 收口:研判轮 → prompt → dismiss。
	must(s.Append(ctx, task,
		Event{Kind: EventOvermindRecord, Actor: "overmind", Payload: OvermindRecord{Type: OvermindRecAssessment, QAccess: "无变化"}},
		Event{Kind: EventClosureRecord, Actor: "overmind", Payload: ClosureRecord{
			Type: ClosureRecPrompt, Rounds: 1, Minutes: 60, RoundsThreshold: 20, MinutesThreshold: 50,
			AnchorAt: time.Now().UTC().Format(time.RFC3339), AnchorKind: ClosureAnchorCreated}},
		Event{Kind: EventClosureRecord, Actor: "creator", Payload: ClosureRecord{Type: ClosureRecDismiss}},
	))
	// 成果追问(两轮)。
	must(s.Append(ctx, task,
		Event{Kind: EventShepherdRecord, Actor: "creator", Payload: ShepherdRecord{
			Type: ShepherdRecUserMessage, MessageID: "m-1", Text: "怎么利用?", ClaimID: claimID, Round: 1}},
		Event{Kind: EventShepherdRecord, Actor: "creator", Payload: ShepherdRecord{
			Type: ShepherdRecUserMessage, MessageID: "m-2", Text: "再问", ClaimID: claimID, Round: 1}},
		Event{Kind: EventShepherdRecord, Actor: "creator", Payload: ShepherdRecord{
			Type: ShepherdRecUserMessage, MessageID: "m-3", Text: "新开一轮", ClaimID: claimID, Round: 2}},
	))
	return claimID, reproRev
}

func boolPtr(b bool) *bool { return &b }

// p436Snapshot 抓全部新投影的读侧快照(重建一致性对账用)。
type p436Snapshot struct {
	tasks     map[string]TaskExtrasRow
	closure   ClosureStateRow
	units     []UnitSessionRow
	labels    map[string]SessionLabel
	approvals []ApprovalRow
	claims    map[string]ClaimDigest
	caseJSON  map[string]string
	hyps      map[string]HypStateRow
	qa        []ClaimQARow
	briefs    map[string]MirrorBriefRow
}

func p436Snap(t *testing.T, s *Store, task, claimID string) p436Snapshot {
	t.Helper()
	ctx := context.Background()
	snap := p436Snapshot{tasks: map[string]TaskExtrasRow{}, labels: map[string]SessionLabel{},
		claims: map[string]ClaimDigest{}, caseJSON: map[string]string{},
		hyps: map[string]HypStateRow{}, briefs: map[string]MirrorBriefRow{}}
	var err error
	if snap.tasks[task], _, err = s.TaskExtras(ctx, task); err != nil {
		t.Fatalf("读任务摘要: %v", err)
	}
	if snap.closure, err = s.ClosureState(ctx, task); err != nil {
		t.Fatalf("读收口状态: %v", err)
	}
	if snap.units, err = s.UnitSessions(ctx, task); err != nil {
		t.Fatalf("读单位会话: %v", err)
	}
	if snap.labels, err = s.SessionLabels(ctx, task); err != nil {
		t.Fatalf("读会话标记: %v", err)
	}
	if snap.approvals, err = s.ApprovalRows(ctx, task); err != nil {
		t.Fatalf("读审批记录: %v", err)
	}
	if snap.claims, err = s.ClaimDigests(ctx, task); err != nil {
		t.Fatalf("读判断摘要: %v", err)
	}
	if proj, ok, err := s.ClaimCaseOf(ctx, task, claimID); err != nil || !ok {
		t.Fatalf("读判断卷宗: %v %v", err, ok)
	} else {
		b, _ := json.Marshal(proj)
		snap.caseJSON[claimID] = string(b)
	}
	if snap.hyps, err = s.HypStates(ctx, task); err != nil {
		t.Fatalf("读假设结论: %v", err)
	}
	if snap.qa, err = s.ClaimQAs(ctx, task); err != nil {
		t.Fatalf("读成果追问: %v", err)
	}
	if snap.briefs, err = s.MirrorBriefs(ctx, task, []string{"ex-1", "ex-2"}); err != nil {
		t.Fatalf("读镜留痕摘要: %v", err)
	}
	return snap
}

// TestP436RebuildParity 增量维护与全量重放同源:同一事件序列,Append 后
// 与 RebuildProjections 后的全部读侧投影逐项一致。
func TestP436RebuildParity(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer s.Close()
	task := "t-p436"
	claimID, _ := p436Seed(t, s, task)
	before := p436Snap(t, s, task, claimID)
	if err := s.RebuildProjections(context.Background()); err != nil {
		t.Fatalf("重建: %v", err)
	}
	after := p436Snap(t, s, task, claimID)
	if bs, as := mustJSON(before.tasks), mustJSON(after.tasks); bs != as {
		t.Fatalf("任务摘要重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.closure), mustJSON(after.closure); bs != as {
		t.Fatalf("收口状态重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.units), mustJSON(after.units); bs != as {
		t.Fatalf("单位会话重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.labels), mustJSON(after.labels); bs != as {
		t.Fatalf("会话标记重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.approvals), mustJSON(after.approvals); bs != as {
		t.Fatalf("审批记录重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.claims), mustJSON(after.claims); bs != as {
		t.Fatalf("判断摘要重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.caseJSON), mustJSON(after.caseJSON); bs != as {
		t.Fatalf("判断卷宗重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.hyps), mustJSON(after.hyps); bs != as {
		t.Fatalf("假设结论重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.qa), mustJSON(after.qa); bs != as {
		t.Fatalf("成果追问重建不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.briefs), mustJSON(after.briefs); bs != as {
		t.Fatalf("镜留痕摘要重建不一致:\n%s\n%s", bs, as)
	}
}

// TestP436ProjectionSemantics 摘要字段与事件扫描语义对账:latest-wins、
// reopen 清战役(解释排除与耗时)、计费累加、目标域只收种子、收口状态机
// 待决搁置、节点时刻列。
func TestP436ProjectionSemantics(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer s.Close()
	task := "t-p436"
	claimID, reproRev := p436Seed(t, s, task)
	ctx := context.Background()

	// 单位会话:续跑链 billable 累加(12.5 + 30 = 42.5),末段终态覆盖。
	units, err := s.UnitSessions(ctx, task)
	if err != nil || len(units) != 2 {
		t.Fatalf("应两个单位会话: %v %+v", err, units)
	}
	u1 := units[0]
	if u1.UnitID != "u1" || u1.EndStatus != "done" || u1.Turns != 9 || u1.BillableSec != 42.5 {
		t.Fatalf("续跑链终态/计费不符: %+v", u1)
	}
	if u1.Reason != "completed" {
		t.Fatalf("终因应取末段: %+v", u1)
	}

	// 会话标记:标题 + 置顶。
	labels, err := s.SessionLabels(ctx, task)
	if err != nil {
		t.Fatalf("读会话标记: %v", err)
	}
	if l := labels["swarm-u1"]; !l.Pinned || l.Title != "主会话" {
		t.Fatalf("会话标记不符: %+v", l)
	}

	// 审批记录:3 条,forced 存在 → denied 不可再放行。
	approvals, err := s.ApprovalRows(ctx, task)
	if err != nil || len(approvals) != 3 {
		t.Fatalf("应 3 条审批记录: %v %+v", err, approvals)
	}
	denies, err := s.StepDenyLatest(ctx, task)
	if err != nil {
		t.Fatalf("读步骤拒绝: %v", err)
	}
	if d := denies["s-9"]; d.DenyLine != DenyLineHardRule {
		t.Fatalf("步骤拒绝来源不符: %+v", d)
	}

	// 判断摘要:最新 verdict(confirm)、复核标记、复测/重放、证据在场。
	digests, err := s.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("读判断摘要: %v", err)
	}
	d := digests[claimID]
	if d.Severity != "high" || d.ReproSteps != 1 || !d.ReportExcluded || d.ReportExcludedReason != "纯信息性观察" {
		t.Fatalf("定稿/复核摘要不符: %+v", d)
	}
	if d.RetestVerdict != "unfixed" || d.RetestRound != 1 || d.ReplayVerdict != "reproduced" || !d.HasEvidence {
		t.Fatalf("复测/重放/证据摘要不符: %+v", d)
	}
	if d.EvidenceBinds != 1 || d.ReproEvents != 1 {
		t.Fatalf("证据版本计数不符: %+v", d)
	}

	// 卷宗:reopen 后旧战役步骤不计轮次;定稿计数;证据首绑次序。
	proj, ok, err := s.ClaimCaseOf(ctx, task, claimID)
	if err != nil || !ok {
		t.Fatalf("读卷宗: %v %v", err, ok)
	}
	if proj.Timing.Rounds != 1 {
		t.Fatalf("reopen 后应只计新战役 1 轮: %+v", proj.Timing)
	}
	if proj.Repro == nil || proj.Repro.Rev != reproRev || proj.Repro.ProcessCount != 2 || proj.Repro.VariantsCount != 1 {
		t.Fatalf("定稿计数不符: %+v", proj.Repro)
	}
	if len(proj.Repro.Steps) != 1 || proj.Repro.Steps[0].Op != "" || proj.Repro.Steps[0].Short != "拿数据" {
		t.Fatalf("定稿步骤不符(Op 不进投影): %+v", proj.Repro.Steps)
	}
	if len(proj.Evidence) != 2 || proj.Evidence[0].ExchangeID != "ex-1" {
		t.Fatalf("证据首绑次序不符: %+v", proj.Evidence)
	}
	// 溯源:发现单位锚(detail 转录锚优先)。
	if proj.Discoverer != "u1" || proj.DiscoverSess != "swarm-u1" {
		t.Fatalf("溯源锚不符: %+v", proj)
	}
	// 时间线:created/state/falsify/verdict/reopen/repro 均在。
	kinds := map[string]int{}
	for _, e := range proj.Timeline {
		kinds[e.K]++
	}
	if kinds["created"] != 1 || kinds["state"] < 3 || kinds["falsify"] != 2 || kinds["verdict"] != 2 ||
		kinds["reopen"] != 1 || kinds["repro"] != 1 || kinds["retest"] != 1 || kinds["replay"] != 1 {
		t.Fatalf("时间线种类计数不符: %+v", kinds)
	}

	// 任务摘要:描述 + 目标域只收种子(手动资产不入)。
	extras, ok, err := s.TaskExtras(ctx, task)
	if err != nil || !ok {
		t.Fatalf("读任务摘要: %v %v", err, ok)
	}
	if extras.Description != "测试描述" || len(extras.Targets) != 1 || extras.Targets[0] != "seed.example.com" {
		t.Fatalf("任务摘要不符: %+v", extras)
	}

	// 收口状态:prompt 待决 + dismiss 搁置。
	closure, err := s.ClosureState(ctx, task)
	if err != nil {
		t.Fatalf("读收口状态: %v", err)
	}
	if closure.Pending == nil || !closure.Parked || closure.Rounds != 1 {
		t.Fatalf("收口状态不符: %+v", closure)
	}

	// 假设结论。
	hyps, err := s.HypStates(ctx, task)
	if err != nil {
		t.Fatalf("读假设结论: %v", err)
	}
	if h := hyps["h-1"]; h.Decision != HypothesisVerify || h.Verifier != "u3" || h.Verdict != HypothesisSupported || h.VerifyStep != "s-h" {
		t.Fatalf("假设结论不符: %+v", h)
	}

	// 成果追问:两轮,第 1 轮 2 问。
	qas, err := s.ClaimQAs(ctx, task)
	if err != nil || len(qas) != 2 {
		t.Fatalf("应两轮追问: %v %+v", err, qas)
	}
	if qas[0].Round != 1 || qas[0].Questions != 2 || qas[1].Round != 2 {
		t.Fatalf("追问计数不符: %+v", qas)
	}

	// 节点时刻列:事实的入账/确认时刻在案。
	fact, err := s.Node(ctx, task, "f-1")
	if err != nil {
		t.Fatalf("读事实: %v", err)
	}
	if fact.AddedAt == "" || fact.ConfirmedAt == "" || fact.ConfirmedRev == 0 {
		t.Fatalf("事实时刻列不符: %+v", fact)
	}

	// 证据版本投影口径与卷宗计数一致。
	perClaim, total, err := s.EvidenceVersions(ctx, task)
	if err != nil || perClaim[claimID] != 2 || total != 2 {
		t.Fatalf("证据版本不符: %v %+v", err, perClaim)
	}
}

// TestP436UpgradeBackfill 模拟 v9 旧库升级:去掉新投影表与节点新列、
// 版本号降回 9,重新 Open 应自动全量重放回填(列表不呈现空数据)。
func TestP436UpgradeBackfill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rings.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	task := "t-up"
	claimID, _ := p436Seed(t, s, task)
	before := p436Snap(t, s, task, claimID)
	if err := s.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}
	// 模拟 v9 库:清掉全部新投影(表保留空壳,等价旧库建表前状态由迁移
	// 补齐——此处直接把 user_version 降回 9 并清空新表,Open 后迁移补
	// DDL 幂等失败前先核对:user_version=9 时 v10 的 CREATE TABLE 因已
	// 存在而报错?迁移语句不带 IF NOT EXISTS——因此按"表已存在但为空"
	// 的等价形态验证回填(升级路径的重建触发才是被测对象)。
	if err := downgradeForP436Test(path); err != nil {
		t.Fatalf("降级模拟: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重开库: %v", err)
	}
	defer s2.Close()
	after := p436Snap(t, s2, task, claimID)
	if bs, as := mustJSON(before.claims), mustJSON(after.claims); bs != as {
		t.Fatalf("升级回填判断摘要不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.units), mustJSON(after.units); bs != as {
		t.Fatalf("升级回填单位会话不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.caseJSON), mustJSON(after.caseJSON); bs != as {
		t.Fatalf("升级回填卷宗不一致:\n%s\n%s", bs, as)
	}
	if bs, as := mustJSON(before.approvals), mustJSON(after.approvals); bs != as {
		t.Fatalf("升级回填审批记录不一致:\n%s\n%s", bs, as)
	}
}

// downgradeForP436Test 完整模拟 v9 旧库:删全部新投影表、剥 nodes 新列、
// user_version 回 9——重开时 v10 迁移补 DDL 并触发全量重放回填。
func downgradeForP436Test(path string) error {
	s, err := Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	for _, stmt := range []string{
		`DROP TABLE unit_sessions`,
		`DROP TABLE session_labels`,
		`DROP TABLE approval_records`,
		`DROP TABLE claim_cases`,
		`DROP TABLE hyp_states`,
		`DROP TABLE task_extras`,
		`DROP TABLE closure_states`,
		`DROP TABLE claim_qa`,
		`DROP TABLE mirror_briefs`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE nodes DROP COLUMN added_at`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`ALTER TABLE nodes DROP COLUMN confirmed_at`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`ALTER TABLE nodes DROP COLUMN confirmed_rev`); err != nil {
		return err
	}
	// v11(P4-44)列同样剥除,模拟真正的 v9 形态(task_extras 整表已
	// DROP,roe_state 的放行清单列在此剥)。
	if _, err := s.db.Exec(`ALTER TABLE roe_state DROP COLUMN scope_approved`); err != nil {
		return err
	}
	// v12(P4-49)注意事项列同款剥除(升级重放路径随新增迁移同步)。
	if _, err := s.db.Exec(`ALTER TABLE roe_state DROP COLUMN creator_notes`); err != nil {
		return err
	}
	// v15(P4-64)node_polish 整表剥除(claim_cases 的 v15 两列随表 DROP
	// 重建消失);升级重放路径随新增迁移同步。
	if _, err := s.db.Exec(`DROP TABLE node_polish`); err != nil {
		return err
	}
	_, err = s.db.Exec(`PRAGMA user_version = 9`)
	return err
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
