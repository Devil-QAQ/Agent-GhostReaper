package arbiter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ghostreaper/internal/rings"
)

// ActorArbiter 仲裁者在账本中的事件归属者(裁决事件的 actor)。
const ActorArbiter = "arbiter"

var (
	// ErrGoalNotFound 目标节点在账本事件流中无创建事件。
	ErrGoalNotFound = errors.New("arbiter: goal not found")
	// ErrNoPredicate 目标创建时未携带完成谓词——无谓词的目标不由机器
	// 终审(其"何时算完"交 LLM 拆解治理),这是合法状态而非错误。
	ErrNoPredicate = errors.New("arbiter: goal has no completion predicate")
)

// 四点最小契约(第三章 §6,P1-06 落地):
//  ① 判定标准用受限结构表达(非自然语言):谓词只能是下方常量的
//     组合子与叶子条件,ParsePredicate 对结构外的一切拒收;
//  ② 创建时冻结:谓词存于 Goal 创建事件负载(detail.predicate),账本
//     只追加且无"改 detail"的事件种类 ⇒ 物理冻结;Evaluate 只从创建
//     事件读谓词,变更判定标准须种新目标(新事件);
//  ③ 只读账本内结构化证据:求值只查投影与事件流(节点状态/类型化边/
//     虫群机器记录),不读任何外部输入;
//  ④ LLM 陈述不构成证据:求值输入里没有任何自然语言文本通道——
//     fact_confirmed 数的是图结构(节点+边),tool_observed 数的是
//     执行器机器写入的 swarm.record;LLM 写的"已满足"无论出现在
//     哪个文本通道(最终回复/候选陈述/证据摘录)都进不了求值。

// 谓词组合子(P1-06 最小集)。
const (
	OpAllOf = "all_of" // 全部条件满足
	OpAnyOf = "any_of" // 任一条件满足
)

// 叶子条件种类(P1-06 最小集;新声明类型如"资产已测透"后续扩入)。
const (
	// CondFactConfirmed:资产上挂有 ≥min 条已确认事实(about 边,
	// status=confirmed 的 Fact 节点)——业务结论通道,经主宰收割把关。
	CondFactConfirmed = "fact_confirmed"
	// CondToolObserved:覆盖该资产的 Step 留有 ≥min 条机器工具观察
	// 记录(swarm.record tool_call,非错误、结果含指定串)——物理
	// 观察通道,由执行器机器写入,LLM 无法伪造。
	CondToolObserved = "tool_observed"
)

var conditionKinds = map[string]bool{CondFactConfirmed: true, CondToolObserved: true}

// Condition 一条叶子条件。字段按 Kind 取用:fact_confirmed 用
// AssetID+Min;tool_observed 用 AssetID+Tool+ResultContains+Min。
type Condition struct {
	Kind           string `json:"kind"`
	AssetID        string `json:"asset_id"`
	Tool           string `json:"tool,omitempty"`
	ResultContains string `json:"result_contains,omitempty"`
	Min            int    `json:"min"` // 达标阈值;0 规范化为 1,负数拒收
}

// Predicate Goal 完成谓词的受限结构:单层组合子 + 叶子条件(不支持
// 嵌套,P1 最小版刻意收紧表达力;发散型谓词留后续分期扩种类)。
type Predicate struct {
	Op         string      `json:"op"`
	Conditions []Condition `json:"conditions"`
}

// ParsePredicate 解析并校验受限结构谓词(契约①的入口:结构外一律拒)。
// 输入是 Goal detail["predicate"] 的原始值;经 JSON 规范化后逐项校验。
func ParsePredicate(raw map[string]any) (*Predicate, error) {
	if raw == nil {
		return nil, fmt.Errorf("arbiter: 谓词不能为空")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 谓词序列化: %w", err)
	}
	var p Predicate
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("arbiter: 谓词不是合法的受限结构: %w", err)
	}
	if p.Op != OpAllOf && p.Op != OpAnyOf {
		return nil, fmt.Errorf("arbiter: 谓词组合子只允许 %s/%s,得到 %q", OpAllOf, OpAnyOf, p.Op)
	}
	if len(p.Conditions) == 0 {
		return nil, fmt.Errorf("arbiter: 谓词至少需要一条条件")
	}
	for i := range p.Conditions {
		c := &p.Conditions[i]
		if !conditionKinds[c.Kind] {
			return nil, fmt.Errorf("arbiter: 未知条件种类 %q(允许: %s/%s)", c.Kind, CondFactConfirmed, CondToolObserved)
		}
		if strings.TrimSpace(c.AssetID) == "" {
			return nil, fmt.Errorf("arbiter: 条件#%d 缺 asset_id", i+1)
		}
		if c.Min < 0 {
			return nil, fmt.Errorf("arbiter: 条件#%d 的 min 不得为负", i+1)
		}
		if c.Min == 0 {
			c.Min = 1
		}
		if c.Kind == CondToolObserved {
			if strings.TrimSpace(c.Tool) == "" {
				return nil, fmt.Errorf("arbiter: tool_observed 条件#%d 缺 tool", i+1)
			}
			if strings.TrimSpace(c.ResultContains) == "" {
				return nil, fmt.Errorf("arbiter: tool_observed 条件#%d 缺 result_contains", i+1)
			}
		}
	}
	return &p, nil
}

// Evaluate Goal 完成判定(纯函数,契约②③④):对目标创建时冻结的谓词,
// 只读账本内结构化证据求值。求值不写账本、不调 LLM、不读账本以外
// 的任何输入;同一账本状态必得同一裁决。返回值即裁决事件负载结构,
// 记账时由调用方(主宰循环)以 actor=arbiter 落账。
//
// 谓词取自目标创建事件(node.added)的原始负载——不从投影当前值读,
// 把"创建时冻结"落成字面语义(契约②)。
func Evaluate(ctx context.Context, store *rings.Store, taskID, goalID string) (*rings.ArbiterVerdict, error) {
	pred, err := predicateOfGoal(ctx, store, taskID, goalID)
	if err != nil {
		return nil, err
	}
	rev, err := store.CurrentRevision(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 查图版本: %w", err)
	}

	v := &rings.ArbiterVerdict{GoalID: goalID, Op: pred.Op, EvaluatedRev: rev}
	for _, c := range pred.Conditions {
		var actual int
		var err error
		switch c.Kind {
		case CondFactConfirmed:
			actual, err = countConfirmedFacts(ctx, store, taskID, c.AssetID)
		case CondToolObserved:
			actual, err = countToolObservations(ctx, store, taskID, c.AssetID, c.Tool, c.ResultContains)
		}
		if err != nil {
			return nil, err
		}
		v.Conditions = append(v.Conditions, rings.ArbiterConditionResult{
			Kind: c.Kind, AssetID: c.AssetID, Tool: c.Tool, ResultContains: c.ResultContains,
			Required: c.Min, Actual: actual, Satisfied: actual >= c.Min,
		})
	}

	satisfied := v.Op == OpAllOf
	for _, r := range v.Conditions {
		if v.Op == OpAllOf && !r.Satisfied {
			satisfied = false
			break
		}
		if v.Op == OpAnyOf && r.Satisfied {
			satisfied = true
			break
		}
	}
	v.Satisfied = satisfied
	return v, nil
}

// predicateOfGoal 从事件流取目标当前生效的完成谓词(契约②)。P4-54 起
// 目标可经 node.amended 变更(面板/牧者改完成条件):创建事件先立、
// 后续携带 predicate 键的变更事件整体覆盖(未携带的变更不动谓词),
// "创建时冻结"落成"最后一次显式冻结生效"——变更本身就是新事件,
// 每次冻结的时刻与内容都在账本可审计。事件按 revision 升序遍历,
// 最后写入者胜。
func predicateOfGoal(ctx context.Context, store *rings.Store, taskID, goalID string) (*Predicate, error) {
	if goalID == "" {
		return nil, fmt.Errorf("arbiter: goal id 为空")
	}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("arbiter: 读事件流: %w", err)
	}
	found := false
	created := false
	var raw map[string]any
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return nil, fmt.Errorf("arbiter: 解码 node.added: %w", err)
			}
			if p.NodeID != goalID || p.NodeType != rings.NodeGoal {
				continue
			}
			created = true
			if v, ok := p.Detail["predicate"].(map[string]any); ok {
				raw, found = v, true
			}
		case rings.EventNodeAmended:
			var p rings.NodeAmended
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return nil, fmt.Errorf("arbiter: 解码 node.amended: %w", err)
			}
			if p.NodeID != goalID {
				continue
			}
			if v, ok := p.Detail["predicate"].(map[string]any); ok {
				raw, found = v, true
			}
		}
	}
	if !created {
		return nil, fmt.Errorf("%w: task %s goal %s", ErrGoalNotFound, taskID, goalID)
	}
	if !found {
		return nil, fmt.Errorf("%w: goal %s", ErrNoPredicate, goalID)
	}
	return ParsePredicate(raw)
}

// countConfirmedFacts 数资产上挂的已确认事实(契约③:只读结构——
// status=confirmed 的 Fact 节点 + about 类型化边;陈述文本不参与)。
func countConfirmedFacts(ctx context.Context, store *rings.Store, taskID, assetID string) (int, error) {
	facts, err := store.NodesByType(ctx, taskID, rings.NodeFact, "confirmed")
	if err != nil {
		return 0, fmt.Errorf("arbiter: 查已确认事实: %w", err)
	}
	confirmed := make(map[string]bool, len(facts))
	for _, f := range facts {
		confirmed[f.ID] = true
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return 0, fmt.Errorf("arbiter: 查 about 边: %w", err)
	}
	n := 0
	for _, e := range abouts {
		if e.To == assetID && confirmed[e.From] {
			n++
		}
	}
	return n, nil
}

// countToolObservations 数资产的机器工具观察记录(契约③④:证据是
// 执行器机器写入的 swarm.record tool_call——非错误、结果含指定串、
// 且记录归属的 Step 经 covers 边锚定该资产;LLM 文本无通道进入)。
func countToolObservations(ctx context.Context, store *rings.Store, taskID, assetID, tool, pattern string) (int, error) {
	covers, err := store.Edges(ctx, taskID, rings.EdgeCovers)
	if err != nil {
		return 0, fmt.Errorf("arbiter: 查 covers 边: %w", err)
	}
	coveringSteps := map[string]bool{}
	for _, e := range covers {
		if e.To == assetID {
			coveringSteps[e.From] = true
		}
	}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("arbiter: 读事件流: %w", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return 0, fmt.Errorf("arbiter: 解码 swarm.record: %w", err)
		}
		if r.Type != rings.SwarmRecToolCall || r.ToolName != tool || r.IsError {
			continue
		}
		if !coveringSteps[r.StepID] || !strings.Contains(r.ResultHead, pattern) {
			continue
		}
		n++
	}
	return n, nil
}
