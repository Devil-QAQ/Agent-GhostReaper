package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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

// claimFixture 铺垫一张最小声明图:发现 Step(发现单位 u1)+ 声明
// (detail 带冻结标准)+ 资产锚点 + 血缘;同任务多声明互不撞 ID。
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

// --- P5-02: 机器三查判定(ThreeChecks)见 p502_check_test.go ---
// 旧收割侧反证轮判定(JudgeClaim 族与 falsifyRound/legacyStandard 辅助)
// 已随 P5-02 判定重构删除:确认判定收口为交卷即查的机器三查,存量
// testing 声明的判定不再可达(升级删除路线 P5-03④)。
