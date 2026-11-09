package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func openTestStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(t.TempDir(), "arbiter.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAppend(t *testing.T, s *rings.Store, task string, events ...rings.Event) {
	t.Helper()
	if _, err := s.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
}

func na(nt rings.NodeType, id, title, status string) rings.Event {
	return rings.Event{Kind: rings.EventNodeAdded, Actor: "test",
		Payload: rings.NodeAdded{NodeID: id, NodeType: nt, Title: title, Status: status}}
}

func pred(op string, conds ...map[string]any) map[string]any {
	return map[string]any{"op": op, "conditions": conds}
}

func cond(kind, asset string, extra ...map[string]any) map[string]any {
	c := map[string]any{"kind": kind, "asset_id": asset}
	for _, m := range extra {
		for k, v := range m {
			c[k] = v
		}
	}
	return c
}

func goalEvent(id string, predicate map[string]any) rings.Event {
	detail := map[string]any{}
	if predicate != nil {
		detail["predicate"] = predicate
	}
	return rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeGoal, Title: "目标 " + id, Status: "active", Detail: detail}}
}

// --- 契约①:判定标准用受限结构表达,结构外一律拒 ---

func TestParsePredicate(t *testing.T) {
	// 合法:最小谓词 + min 规范化(0 → 1)。
	p, err := ParsePredicate(pred(OpAllOf, cond(CondFactConfirmed, "a1")))
	if err != nil || p.Op != OpAllOf || len(p.Conditions) != 1 || p.Conditions[0].Min != 1 {
		t.Fatalf("最小合法谓词应通过并规范化 min: err=%v p=%+v", err, p)
	}
	// 合法:tool_observed 全字段。
	if _, err := ParsePredicate(pred(OpAnyOf,
		cond(CondToolObserved, "a1", map[string]any{"tool": "port_probe", "result_contains": "state=open", "min": 2})),
	); err != nil {
		t.Fatalf("tool_observed 合法谓词应通过: %v", err)
	}

	bad := []struct {
		name string
		raw  map[string]any
	}{
		{"空谓词", nil},
		{"缺 op", map[string]any{"conditions": []any{cond(CondFactConfirmed, "a1")}}},
		{"未知组合子", pred("satisfied_if_llm_says_so", cond(CondFactConfirmed, "a1"))},
		{"自然语言判定标准", pred(OpAllOf, map[string]any{"kind": "free_text", "text": "目标已满足,LLM 说了算"})},
		{"零条件", pred(OpAllOf)},
		{"未知条件种类", pred(OpAllOf, cond("goal_satisfied", "a1"))},
		{"缺 asset_id", pred(OpAllOf, map[string]any{"kind": CondFactConfirmed, "min": 1})},
		{"min 为负", pred(OpAllOf, cond(CondFactConfirmed, "a1", map[string]any{"min": -1}))},
		{"tool_observed 缺 tool", pred(OpAllOf, cond(CondToolObserved, "a1", map[string]any{"result_contains": "x"}))},
		{"tool_observed 缺 result_contains", pred(OpAllOf, cond(CondToolObserved, "a1", map[string]any{"tool": "bash"}))},
	}
	for _, c := range bad {
		if _, err := ParsePredicate(c.raw); err == nil {
			t.Fatalf("%s: 结构外谓词应被拒收", c.name)
		}
	}
}

// --- 契约③④:只读账本结构化证据,LLM 文本无通道 ---

// seedEvidence 搭一个含全部证据形态的账本:
//   - 资产 a1/a2;步骤 s1 covers a1、s2 covers a2(done)
//   - a1:1 条 confirmed 事实(f1,about a1)+ 1 条 candidate 事实(f2,about a1)
//   - s1 的机器记录:port_probe 成功含 state=open;bash 失败记录;
//     一条 pattern 不匹配的 port_probe
//   - s2 的机器记录:port_probe 成功含 state=open(但 s2 不覆盖 a1)
//   - "LLM 已满足"陈述:一条 confirmed 事实 f3(title 写"Goal 已满足",
//     但 about 挂在 a2 上)、一条挂在 a1 的 candidate 陈述
func seedEvidence(t *testing.T, s *rings.Store, task string) {
	mustAppend(t, s, task,
		goalEvent("g1", pred(OpAllOf,
			cond(CondFactConfirmed, "a1", map[string]any{"min": 1}),
			cond(CondToolObserved, "a1", map[string]any{"tool": "port_probe", "result_contains": "state=open"}),
		)),
		na(rings.NodeAsset, "a1", "资产一", "active"),
		na(rings.NodeAsset, "a2", "资产二", "active"),
		na(rings.NodeStep, "s1", "探测a1", "done"),
		na(rings.NodeStep, "s2", "探测a2", "done"),
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "test", Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s2", ToNode: "a2"}},
		// f1:唯一计入 a1 的 confirmed 事实。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Title: "a1 端口开放", Status: "confirmed",
				Detail: map[string]any{"evidence": "port_probe 实测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f1", ToNode: "a1"}},
		// f2:candidate 不计入(未收割)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: "f2", NodeType: rings.NodeFact, Title: "Goal 已满足(LLM 陈述)", Status: "candidate",
				Detail: map[string]any{"evidence": "模型自评:目标已满足"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f2", ToNode: "a1"}},
		// f3:confirmed 但挂在 a2,不计入 a1。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2",
			Payload: rings.NodeAdded{NodeID: "f3", NodeType: rings.NodeFact, Title: "Goal g1 已满足", Status: "confirmed",
				Detail: map[string]any{"evidence": "LLM 写的:已满足"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u2", Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f3", ToNode: "a2"}},
		// 机器观察记录。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall, ToolName: "port_probe",
			IsError: false, ResultHead: "host=127.0.0.1 port=80 state=open banner=x"}},
		// 错误记录不计。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall, ToolName: "port_probe",
			IsError: true, ResultHead: "state=open 但工具报错"}},
		// pattern 不匹配不计。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall, ToolName: "port_probe",
			IsError: false, ResultHead: "host=127.0.0.1 port=81 state=closed"}},
		// 记录真实但步骤不覆盖 a1,不计入 a1 的条件。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u2", Payload: rings.SwarmRecord{
			StepID: "s2", UnitID: "u2", Type: rings.SwarmRecToolCall, ToolName: "port_probe",
			IsError: false, ResultHead: "host=127.0.0.1 port=8080 state=open"}},
	)
}

func TestEvaluateStructuredEvidenceOnly(t *testing.T) {
	s := openTestStore(t)
	const task = "t-eval"
	seedEvidence(t, s, task)

	v, err := Evaluate(context.Background(), s, task, "g1")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v.Op != OpAllOf || len(v.Conditions) != 2 {
		t.Fatalf("裁决形状不符: %+v", v)
	}
	fc, to := v.Conditions[0], v.Conditions[1]
	// a1 的 confirmed 事实恰 1 条:f1。candidate 的"已满足"陈述(f2)与
	// 挂在 a2 的 confirmed"已满足"陈述(f3)都不计入——LLM 文本无通道。
	if fc.Kind != CondFactConfirmed || fc.Actual != 1 || !fc.Satisfied {
		t.Fatalf("fact_confirmed 计数应恰 1(f1),得到 %+v", fc)
	}
	// a1 的机器观察恰 1 条:错误记录、pattern 不匹配、他资产步骤记录均不计。
	if to.Kind != CondToolObserved || to.Actual != 1 || !to.Satisfied {
		t.Fatalf("tool_observed 计数应恰 1,得到 %+v", to)
	}
	if !v.Satisfied || v.EvaluatedRev == 0 {
		t.Fatalf("all_of 两条件均满足应判达成: %+v", v)
	}
}

func TestEvaluateUnsatisfiedAndAnyOf(t *testing.T) {
	s := openTestStore(t)
	const task = "t-eval2"
	seedEvidence(t, s, task)

	// 换一个更严的谓词(min=2):f1 只有 1 条 confirmed,应不满足。
	mustAppend(t, s, task, goalEvent("g2", pred(OpAllOf,
		cond(CondFactConfirmed, "a1", map[string]any{"min": 2}))))
	v, err := Evaluate(context.Background(), s, task, "g2")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v.Satisfied || v.Conditions[0].Actual != 1 || v.Conditions[0].Satisfied {
		t.Fatalf("min=2 下应不满足: %+v", v)
	}

	// any_of:机器观察渠道满足(a1 的 port_probe state=open 记录)、
	// 事实渠道不满足(min=99)——任一达成即达成。
	mustAppend(t, s, task, goalEvent("g3", pred(OpAnyOf,
		cond(CondToolObserved, "a1", map[string]any{"tool": "port_probe", "result_contains": "state=open"}),
		cond(CondFactConfirmed, "a1", map[string]any{"min": 99}),
	)))
	v, err = Evaluate(context.Background(), s, task, "g3")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if !v.Satisfied {
		t.Fatalf("any_of 应因机器观察渠道达成: %+v", v)
	}

	// a2 的机器观察(curl)不存在:该渠道单独求证为 0。
	mustAppend(t, s, task, goalEvent("g4", pred(OpAllOf,
		cond(CondToolObserved, "a2", map[string]any{"tool": "curl", "result_contains": "200"}))))
	v, err = Evaluate(context.Background(), s, task, "g4")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v.Satisfied || v.Conditions[0].Actual != 0 {
		t.Fatalf("无 curl 机器记录应不满足: %+v", v)
	}
}

// --- 契约②:谓词创建时冻结,变更须走新事件 ---

func TestPredicateFrozenAtCreation(t *testing.T) {
	s := openTestStore(t)
	const task = "t-freeze"
	strict := pred(OpAllOf, cond(CondFactConfirmed, "a1", map[string]any{"min": 5}))
	mustAppend(t, s, task, goalEvent("g1", strict))

	// 物理冻结:同 ID 目标再入账(哪怕带宽松谓词)被账本拒绝;
	// rings 不存在"改 detail"的事件种类,谓词无第二条写路径。
	loose := pred(OpAnyOf, cond(CondFactConfirmed, "a1"))
	if _, err := s.Append(context.Background(), task, goalEvent("g1", loose)); !errors.Is(err, rings.ErrNodeExists) {
		t.Fatalf("重复种目标应被拒(谓词冻结的物理兜底),得到 %v", err)
	}

	// 求值读到的仍是创建时的严格谓词:无事实时 min=5 不满足。
	v, err := Evaluate(context.Background(), s, task, "g1")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v.Satisfied || v.Conditions[0].Required != 5 {
		t.Fatalf("谓词应为创建时冻结的 min=5: %+v", v)
	}

	// 要换判定标准,唯一路径是种新目标(新事件):新旧谓词并存可审计。
	mustAppend(t, s, task, goalEvent("g2", loose))
	v2, err := Evaluate(context.Background(), s, task, "g2")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v2.Op != OpAnyOf || v2.Satisfied {
		t.Fatalf("新目标应按新谓词求值: %+v", v2)
	}
}

// --- 目标缺失与无谓词 ---

func TestEvaluateGoalNotFoundAndNoPredicate(t *testing.T) {
	s := openTestStore(t)
	const task = "t-nopred"
	ctx := context.Background()
	mustAppend(t, s, task, goalEvent("g1", nil)) // 无谓词目标:LLM 拆解治理

	if _, err := Evaluate(ctx, s, task, "g1"); !errors.Is(err, ErrNoPredicate) {
		t.Fatalf("无谓词应回 ErrNoPredicate,得到 %v", err)
	}
	if _, err := Evaluate(ctx, s, task, "ghost"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("不存在目标应回 ErrGoalNotFound,得到 %v", err)
	}
	// task 隔离:B 任务里的 g1 对 A 任务不可见。
	if _, err := Evaluate(ctx, s, "other-task", "g1"); !errors.Is(err, ErrGoalNotFound) {
		t.Fatalf("跨任务求值应回 ErrGoalNotFound,得到 %v", err)
	}
}

// --- 裁决负载可直接落账(与 rings 事件负载结构一致) ---

func TestVerdictPayloadAppendable(t *testing.T) {
	s := openTestStore(t)
	const task = "t-payload"
	mustAppend(t, s, task,
		goalEvent("g1", pred(OpAllOf, cond(CondFactConfirmed, "a1"))),
		na(rings.NodeAsset, "a1", "资产", "active"),
	)
	v, err := Evaluate(context.Background(), s, task, "g1")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if v.Satisfied {
		t.Fatalf("无事实不应达成: %+v", v)
	}
	// 不满足的裁决同样可落账(审计"何时求值过、为何未达成")。
	if _, err := s.Append(context.Background(), task, rings.Event{
		Kind: rings.EventArbiterVerdict, Actor: ActorArbiter, Payload: *v,
	}); err != nil {
		t.Fatalf("裁决负载落账失败: %v", err)
	}
	evs, _ := s.Events(context.Background(), task, 0, 0)
	var got rings.ArbiterVerdict
	if err := json.Unmarshal(evs[len(evs)-1].Payload, &got); err != nil {
		t.Fatalf("解码裁决负载失败: %v", err)
	}
	if got.Satisfied || got.GoalID != "g1" || len(got.Conditions) != 1 {
		t.Fatalf("裁决负载往返不符: %+v", got)
	}
}

// --- P1-08: TestClaim 确认裁决(JudgeClaim,四点契约声明侧) ---
// P2-02 起 DefaultClaimStandard 为 full_falsify;P1-08 语义用例显式挂
// single_round_falsify 标准保持原口径(旧账本兼容路径),完整版行为见
// TestJudgeClaimFullFalsify*。

// legacyStandard P1-08 单轮标准。
func legacyStandard() map[string]any {
	return map[string]any{"kind": ClaimStandardSingleRoundFalsify, "require_impact": true}
}

// claimFixture 铺垫一张最小声明图:发现 Step(发现单位 u1)+ 声明
// (detail 带冻结标准)+ 资产锚点 + 血缘;同任务多声明互不撞 ID。
// 返回声明当前状态可被覆写的辅助函数。
func claimFixture(t *testing.T, s *rings.Store, task, claimID string, mutateDetail func(map[string]any)) {
	t.Helper()
	discoverStep := "sd-" + claimID
	assetID := "as-" + claimID
	detail := map[string]any{
		"unit_id":  "u1",
		"step_id":  discoverStep,
		"impact":   "备份文件可被未授权下载",
		"standard": DefaultClaimStandard(),
	}
	if mutateDetail != nil {
		mutateDetail(detail)
	}
	mustAppend(t, s, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: discoverStep, NodeType: rings.NodeStep, Title: "审计主站", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: assetID, NodeType: rings.NodeAsset, Title: "资产", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: "备份目录可公开列举",
			Status: rings.TestClaimCandidate, Detail: detail}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: assetID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: discoverStep}},
	)
}

// falsifyRound 进入反证中:反证 Step(绑定 testclaim_id)+ 声明→testing
// + 可选结构化反证结论(验证单位 unit)。
func falsifyRound(t *testing.T, s *rings.Store, task, claimID, stepID, unit string, result *rings.TestClaimRecord) {
	t.Helper()
	round := 0
	if result != nil {
		round = result.Round
	}
	stepDetail := map[string]any{"kind": "falsify", "testclaim_id": claimID, "action_level": "只读探测"}
	if round > 0 {
		stepDetail["round"] = round
	}
	mustAppend(t, s, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "反证: 声明", Status: "done",
			Detail: stepDetail}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: claimID, Status: rings.TestClaimTesting}},
	)
	if result != nil {
		if result.StepID == "" {
			result.StepID = stepID
		}
		mustAppend(t, s, task, rings.Event{Kind: rings.EventTestClaimRecord, Actor: "swarm:" + unit, Payload: *result})
	}
}

func TestParseClaimStandard(t *testing.T) {
	// 合法:默认标准(P2-02 full_falsify,轮上限缺省 3);require_impact
	// 缺省从严为 true。
	st, err := ParseClaimStandard(map[string]any{"kind": ClaimStandardSingleRoundFalsify})
	if err != nil || st.Kind != ClaimStandardSingleRoundFalsify || !st.RequireImpact {
		t.Fatalf("合法单轮标准应通过且缺省从严: err=%v st=%+v", err, st)
	}
	if st, err := ParseClaimStandard(DefaultClaimStandard()); err != nil ||
		st.Kind != ClaimStandardFullFalsify || !st.RequireImpact || st.MaxRounds != DefaultMaxFalsifyRounds {
		t.Fatalf("默认标准应为 full_falsify + 默认轮上限: err=%v st=%+v", err, st)
	}
	if st, err := ParseClaimStandard(map[string]any{"kind": ClaimStandardFullFalsify, "max_rounds": 2}); err != nil || st.MaxRounds != 2 {
		t.Fatalf("full 标准应接受显式轮上限: err=%v st=%+v", err, st)
	}
	// 结构外一律拒(契约①)。
	for _, raw := range []map[string]any{
		nil,
		{"kind": "llm_says_confirmed"},
		{"kind": ClaimStandardFullFalsify, "require_impact": "看情况"},
		{"kind": ClaimStandardFullFalsify, "max_rounds": -1},
		{"require_impact": true},
	} {
		if _, err := ParseClaimStandard(raw); err == nil {
			t.Fatalf("非法判定标准 %v 应被拒", raw)
		}
	}
}

func TestJudgeClaimVerdicts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-judge"
	legacy := func(d map[string]any) { d["standard"] = legacyStandard() }

	// 声明 A:反证未推翻 + 影响面充分 + 独立单位 → Confirmed。
	claimFixture(t, s, task, "tc-a", legacy)
	falsifyRound(t, s, task, "tc-a", "s2", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-a", UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high", EvidenceRef: "sha256:ab",
		Note: "重放观察确认目录列举仍可匿名访问",
	})
	j, err := JudgeClaim(ctx, s, task, "tc-a")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if j.Status != rings.TestClaimConfirmed || !j.Verdict.Satisfied || j.Verdict.Decision != rings.VerdictConfirm {
		t.Fatalf("独立反证未推翻且影响面充分应判 Confirmed: %+v", j.Verdict)
	}
	if j.Severity != "high" {
		t.Fatalf("确认裁决应携带末轮影响面定级(收割定稿来源): %q", j.Severity)
	}
	kinds := map[string]bool{}
	for _, c := range j.Verdict.Conditions {
		kinds[c.Kind] = c.Satisfied
		if c.Required != 1 || c.Actual != 1 {
			t.Fatalf("Confirmed 裁决条件须逐条满足: %+v", c)
		}
	}
	for _, k := range []string{CondFalsifyCompleted, CondExistenceNotRefuted, CondImpactSupported, CondVerifierIndependent} {
		if !kinds[k] {
			t.Fatalf("裁决条件缺 %s: %+v", k, j.Verdict.Conditions)
		}
	}
	if j.Verdict.PendingReason != "" || j.Verdict.EvaluatedRev <= 0 || j.Verdict.UnitID != "u2" {
		t.Fatalf("Confirmed 裁决不应带搁置理由: %+v", j.Verdict)
	}

	// 声明 B:反证未推翻存在性,但影响面证据不足 → 待确认,理由含"影响面"。
	claimFixture(t, s, task, "tc-b", legacy)
	falsifyRound(t, s, task, "tc-b", "s3", "u3", &rings.TestClaimRecord{
		ClaimID: "tc-b", UnitID: "u3", Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: false, ImpactSupported: false, EvidenceRef: "sha256:cd",
	})
	jb, err := JudgeClaim(ctx, s, task, "tc-b")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if jb.Status != rings.TestClaimPending || jb.Verdict.Satisfied {
		t.Fatalf("影响面证据不足应判待确认: %+v", jb.Verdict)
	}
	if !strings.Contains(jb.Verdict.PendingReason, "证据不足以支撑申报的影响") {
		t.Fatalf("搁置理由须言明证据不足: %q", jb.Verdict.PendingReason)
	}

	// 声明 C:存在性被推翻 → 不成立(P4-55 ②:推翻即终态,理由"被反证推翻")。
	claimFixture(t, s, task, "tc-c", legacy)
	falsifyRound(t, s, task, "tc-c", "s4", "u4", &rings.TestClaimRecord{
		ClaimID: "tc-c", UnitID: "u4", Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: true, ImpactSupported: false, EvidenceRef: "sha256:ef",
	})
	jc, err := JudgeClaim(ctx, s, task, "tc-c")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if jc.Status != rings.TestClaimRefuted || jc.Verdict.Decision != rings.VerdictRefuted || jc.Verdict.PendingReason != "被反证推翻" {
		t.Fatalf("存在性被推翻应判不成立(理由逐字'被反证推翻'): %+v", jc.Verdict)
	}

	// 声明 D:反证单位 = 发现单位(R12 违例)→ 结果不被采信,待确认。
	claimFixture(t, s, task, "tc-d", legacy)
	falsifyRound(t, s, task, "tc-d", "s5", "u1", &rings.TestClaimRecord{
		ClaimID: "tc-d", UnitID: "u1", Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: false, ImpactSupported: true, ImpactLevel: "high", EvidenceRef: "sha256:aa",
	})
	jd, err := JudgeClaim(ctx, s, task, "tc-d")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if jd.Status != rings.TestClaimPending || !strings.Contains(jd.Verdict.PendingReason, "R12") {
		t.Fatalf("发现者自证不得确认: %+v", jd.Verdict)
	}

	// 声明 E:反证轮完成但未写回结构化结论 → 中断(P4-55 ①:预算未尽
	// 交收割自动重派,不落待确认装死)。
	claimFixture(t, s, task, "tc-e", legacy)
	falsifyRound(t, s, task, "tc-e", "s6", "u5", nil)
	if _, err := JudgeClaim(ctx, s, task, "tc-e"); !errors.Is(err, ErrClaimInterrupted) {
		t.Fatalf("无结构化结论且预算未尽应 ErrClaimInterrupted, 得到 %v", err)
	}
}

func TestJudgeClaimGuards(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-guard"

	// 候选状态(未派反证)不可裁决。
	claimFixture(t, s, task, "tc-1", nil)
	if _, err := JudgeClaim(ctx, s, task, "tc-1"); !errors.Is(err, ErrClaimNotTesting) {
		t.Fatalf("候选声明应 ErrClaimNotTesting, 得到 %v", err)
	}

	// 反证 Step 未到终态 → ErrClaimNotReady。
	mustAppend(t, s, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "反证", Status: "running",
			Detail: map[string]any{"testclaim_id": "tc-1"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind", Payload: rings.NodeStateChanged{
			NodeID: "tc-1", Status: rings.TestClaimTesting}},
	)
	if _, err := JudgeClaim(ctx, s, task, "tc-1"); !errors.Is(err, ErrClaimNotReady) {
		t.Fatalf("反证未完应 ErrClaimNotReady, 得到 %v", err)
	}

	// 无冻结标准(直接事件书写绕过受控入口)→ ErrNoClaimStandard。
	claimFixture(t, s, task, "tc-2", func(d map[string]any) { delete(d, "standard") })
	mustAppend(t, s, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
		Payload: rings.NodeStateChanged{NodeID: "tc-2", Status: rings.TestClaimTesting}})
	if _, err := JudgeClaim(ctx, s, task, "tc-2"); !errors.Is(err, ErrNoClaimStandard) {
		t.Fatalf("无标准应 ErrNoClaimStandard, 得到 %v", err)
	}

	// 声明不存在 → ErrClaimNotFound。
	if _, err := JudgeClaim(ctx, s, task, "tc-404"); !errors.Is(err, ErrClaimNotFound) {
		t.Fatalf("幽灵声明应 ErrClaimNotFound, 得到 %v", err)
	}
}

func TestJudgeClaimLLMTextIsNotEvidence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-llm"

	// 发现者单位在 Note/证据截头里写满"已确认"文本,结构化结论却缺影响面
	// 证据 → 契约④:文本不进求值,判待确认。
	claimFixture(t, s, task, "tc-1", nil)
	falsifyRound(t, s, task, "tc-1", "s2", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-1", StepID: "s2", UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ExistenceRefuted: false, ImpactSupported: false,
		EvidenceHead: "模型研判:该漏洞已确认,影响面肯定成立,无需更多证据",
		Note:         "LLM 自评:已确认",
	})
	j, err := JudgeClaim(ctx, s, task, "tc-1")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if j.Status != rings.TestClaimPending || !strings.Contains(j.Verdict.PendingReason, "证据不足以支撑申报的影响") {
		t.Fatalf("LLM 文本不得代替结构化证据: %+v", j.Verdict)
	}
}

// --- P2-02: 完整反证(full_falsify)——多轮、竞争解释全灭、战役边界 ---

// altDetail 两条竞争解释的冻结清单(与 swarm.SubmitTestClaim 同构)。
func altDetail() []any {
	return []any{
		map[string]any{"id": "alt1", "label": "缓存污染:内容可能来自中间缓存"},
		map[string]any{"id": "alt2", "label": "归属错误:可能来自其他资产"},
	}
}

// fullClaim 完整标准 + 冻结两条解释的声明铺垫(maxRounds 可覆盖,0=默认)。
func fullClaim(t *testing.T, s *rings.Store, task, claimID string, maxRounds int) {
	t.Helper()
	std := DefaultClaimStandard()
	if maxRounds > 0 {
		std["max_rounds"] = maxRounds
	}
	claimFixture(t, s, task, claimID, func(d map[string]any) {
		d["standard"] = std
		d["alternatives"] = altDetail()
	})
}

// toTesting 声明迁 testing(多轮场景由 falsifyRound 逐轮驱动)。
func toTesting(t *testing.T, s *rings.Store, task, claimID string) {
	t.Helper()
	mustAppend(t, s, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
		Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimTesting}})
}

func TestJudgeClaimFullFalsifyRounds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-full"

	// 多轮正例:R1 排除 alt2(归属错误)、影响面成立(high)但 alt1 未除
	// → 待下轮反证;R2 缓存穿透重放排除 alt1、影响面成立(medium)
	// → Confirmed,定级跟末轮影响面证据(medium,不取 R1 的 high)。
	fullClaim(t, s, task, "tc-multi", 0)
	toTesting(t, s, task, "tc-multi")
	falsifyRound(t, s, task, "tc-multi", "sf1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-multi", Round: 1, UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high",
		Alternatives: []rings.AltResult{{ID: "alt2", Refuted: true}},
		EvidenceRef:  "sha256:r1",
	})
	j1, err := JudgeClaim(ctx, s, task, "tc-multi")
	if err != nil {
		t.Fatalf("R1 求值失败: %v", err)
	}
	if j1.Status != rings.TestClaimAwaitingRound || j1.Verdict.Decision != rings.VerdictAdvanceRound {
		t.Fatalf("解释未全灭且轮次未尽应判待下轮: %+v", j1.Verdict)
	}
	if len(j1.AliveAlt) != 1 || j1.AliveAlt[0].ID != "alt1" || j1.Round != 1 {
		t.Fatalf("应携带存活解释与轮次供收割派发: %+v round=%d", j1.AliveAlt, j1.Round)
	}
	if j1.Verdict.Satisfied || j1.Verdict.PendingReason == "" {
		t.Fatalf("advance 裁决必须落理由(R4 同源纪律): %+v", j1.Verdict)
	}
	// conditions:alternatives_killed 应为 1/2。
	var altCond *rings.ArbiterConditionResult
	for i := range j1.Verdict.Conditions {
		if j1.Verdict.Conditions[i].Kind == CondAlternativesKilled {
			altCond = &j1.Verdict.Conditions[i]
		}
	}
	if altCond == nil || altCond.Required != 2 || altCond.Actual != 1 || altCond.Satisfied {
		t.Fatalf("alternatives_killed 应为 1/2: %+v", altCond)
	}

	// 收割侧把声明迁回 testing 后,第 2 轮排除 alt1 → 确认。
	toTesting(t, s, task, "tc-multi")
	falsifyRound(t, s, task, "tc-multi", "sf2", "u3", &rings.TestClaimRecord{
		ClaimID: "tc-multi", Round: 2, UnitID: "u3", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "medium",
		Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}},
		EvidenceRef:  "sha256:r2",
	})
	j2, err := JudgeClaim(ctx, s, task, "tc-multi")
	if err != nil {
		t.Fatalf("R2 求值失败: %v", err)
	}
	if j2.Status != rings.TestClaimConfirmed || j2.Verdict.Decision != rings.VerdictConfirm || !j2.Verdict.Satisfied {
		t.Fatalf("解释全灭 + 末轮影响面成立应确认: %+v", j2.Verdict)
	}
	if j2.Severity != "medium" || j2.Round != 2 {
		t.Fatalf("定级应跟末轮影响面证据(medium)、轮次=2: sev=%q round=%d", j2.Severity, j2.Round)
	}

	// 轮次耗尽:max_rounds=2,R1/R2 均未排除 alt1 → R2 后待确认(理由言明轮数)。
	fullClaim(t, s, task, "tc-exhaust", 2)
	toTesting(t, s, task, "tc-exhaust")
	falsifyRound(t, s, task, "tc-exhaust", "se1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-exhaust", Round: 1, UnitID: "u2", Type: rings.TestClaimRecFalsify,
		Alternatives: []rings.AltResult{{ID: "alt2", Refuted: true}}, EvidenceRef: "sha256:e1"})
	toTesting(t, s, task, "tc-exhaust")
	falsifyRound(t, s, task, "tc-exhaust", "se2", "u3", &rings.TestClaimRecord{
		ClaimID: "tc-exhaust", Round: 2, UnitID: "u3", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "low", EvidenceRef: "sha256:e2"})
	je, err := JudgeClaim(ctx, s, task, "tc-exhaust")
	if err != nil {
		t.Fatalf("轮耗尽求值失败: %v", err)
	}
	if je.Status != rings.TestClaimPending || !strings.Contains(je.Verdict.PendingReason, "还有 1 个疑点没排除") {
		t.Fatalf("轮耗尽应诚实待确认且理由言明: %+v", je.Verdict)
	}

	// 末轮影响面口径:R1 影响面成立,R2 影响面不成立(解释在 R1 已全灭)
	// → 不确认,待确认理由言明影响面。
	fullClaim(t, s, task, "tc-impact", 0)
	toTesting(t, s, task, "tc-impact")
	falsifyRound(t, s, task, "tc-impact", "si1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-impact", Round: 1, UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high",
		Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}},
		EvidenceRef:  "sha256:i1"})
	toTesting(t, s, task, "tc-impact")
	falsifyRound(t, s, task, "tc-impact", "si2", "u3", &rings.TestClaimRecord{
		ClaimID: "tc-impact", Round: 2, UnitID: "u3", Type: rings.TestClaimRecFalsify,
		ImpactSupported: false, EvidenceRef: "sha256:i2"})
	ji, err := JudgeClaim(ctx, s, task, "tc-impact")
	if err != nil {
		t.Fatalf("末轮影响面求值失败: %v", err)
	}
	if ji.Status != rings.TestClaimPending || !strings.Contains(ji.Verdict.PendingReason, "证据不足以支撑申报的影响") {
		t.Fatalf("末轮影响面不成立不得确认: %+v", ji.Verdict)
	}

	// 战役内任一轮 R12 违例:R2 由发现者自证 → 整个战役不采信。
	fullClaim(t, s, task, "tc-r12", 0)
	toTesting(t, s, task, "tc-r12")
	falsifyRound(t, s, task, "tc-r12", "sr1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-r12", Round: 1, UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high",
		Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}},
		EvidenceRef:  "sha256:x1"})
	toTesting(t, s, task, "tc-r12")
	falsifyRound(t, s, task, "tc-r12", "sr2", "u1", &rings.TestClaimRecord{
		ClaimID: "tc-r12", Round: 2, UnitID: "u1", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high", EvidenceRef: "sha256:x2"})
	jr, err := JudgeClaim(ctx, s, task, "tc-r12")
	if err != nil {
		t.Fatalf("R12 求值失败: %v", err)
	}
	if jr.Status != rings.TestClaimPending || !strings.Contains(jr.Verdict.PendingReason, "R12") {
		t.Fatalf("战役内发现者自证应整体不采信: %+v", jr.Verdict)
	}
}

// TestJudgeClaimReopenCampaign 重开战役边界:确认后的旧排除不带入新战役
// ——重开轮未重新排除的解释仍然存活(advance 而非 confirm)。
func TestJudgeClaimReopenCampaign(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-reopen"

	fullClaim(t, s, task, "tc-r", 0)
	toTesting(t, s, task, "tc-r")
	falsifyRound(t, s, task, "tc-r", "sr1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-r", Round: 1, UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high",
		Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}},
		EvidenceRef:  "sha256:rr1"})
	j, err := JudgeClaim(ctx, s, task, "tc-r")
	if err != nil || j.Status != rings.TestClaimConfirmed {
		t.Fatalf("重开前应先确认: err=%v status=%s", err, j.Status)
	}

	// 重开(结论可反驳):reopen 记录落账 + 回 testing。
	mustAppend(t, s, task,
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "creator", Payload: rings.TestClaimRecord{
			ClaimID: "tc-r", Type: rings.TestClaimRecReopen,
			Trigger: rings.ReopenNewEvidence, Reason: "新证据表明可能是缓存污染"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator",
			Payload: rings.NodeStateChanged{NodeID: "tc-r", Status: rings.TestClaimTesting}},
	)
	// 重开轮(round 2):一条解释都没重新排除 → 不得沿用旧战役的排除
	// 结论直接确认,应判待下轮(战役边界生效)。
	falsifyRound(t, s, task, "tc-r", "sr2", "u3", &rings.TestClaimRecord{
		ClaimID: "tc-r", Round: 2, UnitID: "u3", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high", EvidenceRef: "sha256:rr2"})
	j2, err := JudgeClaim(ctx, s, task, "tc-r")
	if err != nil {
		t.Fatalf("重开轮求值失败: %v", err)
	}
	if j2.Status != rings.TestClaimAwaitingRound || len(j2.AliveAlt) != 2 {
		t.Fatalf("重开后旧排除应整体失效(全部复活): %+v alive=%+v", j2.Verdict, j2.AliveAlt)
	}

	// 重开轮全部重新排除 → 再确认(新裁决取代旧裁决,历史不删)。
	toTesting(t, s, task, "tc-r")
	falsifyRound(t, s, task, "tc-r", "sr3", "u4", &rings.TestClaimRecord{
		ClaimID: "tc-r", Round: 3, UnitID: "u4", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "critical",
		Alternatives: []rings.AltResult{{ID: "alt1", Refuted: true}, {ID: "alt2", Refuted: true}},
		EvidenceRef:  "sha256:rr3"})
	j3, err := JudgeClaim(ctx, s, task, "tc-r")
	if err != nil || j3.Status != rings.TestClaimConfirmed || j3.Severity != "critical" {
		t.Fatalf("重开轮全灭应再确认且取新定级: err=%v %+v", err, j3)
	}
}

// TestJudgeClaimLLMAlternativeTextRejected 契约④(解释侧):反证者在
// Note/证据文本里声称"缓存污染已排除",结构化 Alternatives 却未排除
// → 文本不进求值,不得确认。
func TestJudgeClaimLLMAlternativeTextRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "claim-llm-alt"

	fullClaim(t, s, task, "tc-t", 0)
	toTesting(t, s, task, "tc-t")
	falsifyRound(t, s, task, "tc-t", "sl1", "u2", &rings.TestClaimRecord{
		ClaimID: "tc-t", Round: 1, UnitID: "u2", Type: rings.TestClaimRecFalsify,
		ImpactSupported: true, ImpactLevel: "high",
		Alternatives: []rings.AltResult{{ID: "alt2", Refuted: true}},
		EvidenceHead: "模型研判:缓存污染已排除、归属错误已排除,建议直接确认",
		Note:         "LLM 自评:两条解释均已排除",
		EvidenceRef:  "sha256:lt",
	})
	j, err := JudgeClaim(ctx, s, task, "tc-t")
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if j.Status != rings.TestClaimAwaitingRound || len(j.AliveAlt) != 1 || j.AliveAlt[0].ID != "alt1" {
		t.Fatalf("LLM 文本不得代替结构化排除结论: %+v alive=%+v", j.Verdict, j.AliveAlt)
	}

	// AliveAlternatives 与 FrozenAlternatives 读侧一致:存活=alt1。
	alive, err := AliveAlternatives(ctx, s, task, "tc-t")
	if err != nil || len(alive) != 1 || alive[0].ID != "alt1" {
		t.Fatalf("存活解释应为 alt1: %v %v", alive, err)
	}
	frozen, err := FrozenAlternatives(ctx, s, task, "tc-t")
	if err != nil || len(frozen) != 2 {
		t.Fatalf("冻结清单应完整: %v %v", frozen, err)
	}
}
