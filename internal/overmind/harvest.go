package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ActorOvermind 主宰在账本中的事件归属者。
const ActorOvermind = "overmind"

// Goal 状态(P1-06 起:active → achieved 由仲裁者机器判定迁移,
// actor=arbiter;LLM 无通道改写)。
const (
	GoalStatusActive   = "active"
	GoalStatusAchieved = "achieved"
)

// SeedGoal 种下目标节点(Goal)。P1-04 为主宰最简的"目标侧"入口:
// 目标从第一天起就是图上节点(待办必须绑定在图上,第四章 §2)。
// P1-06 起完成谓词随目标创建冻结(第三章 §6 契约②):detail.predicate
// 存在时必须是合法的受限结构谓词——非法谓词的目标在入口即被拒,
// 不得入账后再靠"解释"补救;账本只追加且无改 detail 的事件种类,
// 谓词就此物理冻结,变更判定标准须种新目标(新事件)。
func SeedGoal(ctx context.Context, store *rings.Store, taskID, goalID, title string, detail map[string]any) error {
	if raw, ok := detail["predicate"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("overmind: 目标 %s 谓词必须是受限结构对象: %T", goalID, raw)
		}
		if _, err := arbiter.ParsePredicate(m); err != nil {
			return fmt.Errorf("overmind: 目标 %s 谓词非法,创建被拒: %w", goalID, err)
		}
	}
	_, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeAdded, Actor: ActorOvermind,
		Payload: rings.NodeAdded{NodeID: goalID, NodeType: rings.NodeGoal, Title: title, Status: GoalStatusActive, Detail: detail},
	})
	return err
}

// DispatchOptions 派发一条意图(Step)的参数。
type DispatchOptions struct {
	source              dispatchSource
	GoalID              string // 归属目标(存入 step.detail.goal_id)
	ExpectedObservation string // 预期观察(验收参考而非产出上限)
	ActionLevel         string // 意图分级(必填:只读探测|主动测试|利用深入|修改与持久化)
	// HarmClass 拒绝线的危害声明(P2-06b,第四章 §3 v2):
	// data_destruction|denial_of_service;空 = 普通。落 step.detail.harm_class。
	HarmClass    string
	CoversAssets []string // 本步骤覆盖的资产节点
	// Priority 优先级(P2-06 派发策略):high|normal|low;空 = normal。
	// 落 step.detail.priority,调度侧按其排序领取并发名额。
	Priority string
	// DependsOn 前置步骤(P2-06 派发策略):落 depends_on 边(Step→Step),
	// 调度侧依赖闸——前置全部完成(done)才领取执行。
	DependsOn []string
	Detail    map[string]any
}

// DispatchStep 派发意图:Step 节点(status=dispatched,等待虫群领取)
// + covers 边(Step→Asset)。待办绑定在图上,不进主宰私有记忆(R8/R15 前提)。
// P1-07 起意图分级必填且必须是四级之一(第四章 §3:主宰派 Step 时标注
// 动作分类,分级决定审批强度——无分级即无审批依据,缺级/坏级拒绝派发;
// 细则见 swarm/actionlevel.go 与拆解提示词)。
func DispatchStep(ctx context.Context, store *rings.Store, taskID, stepID, intent string, opt DispatchOptions) error {
	for ctx.Err() == nil {
		rev, err := store.CurrentRevision(ctx, taskID)
		if err != nil {
			return err
		}
		events, err := stepEvents(ctx, store, taskID, stepID, intent, opt)
		if err != nil {
			return err
		}
		_, err = store.AppendAtRevision(ctx, taskID, rev, events...)
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		return err
	}
	return ctx.Err()
}

// stepEvents 构造派发一条意图的事件批(Step 节点 + covers 边 + depends_on
// 边),供 DispatchStep 与反证轮派发组批共用(后者须与声明状态迁移同事务
// 落账)。P2-06:优先级词汇受限(非法拒派);depends_on 端点必须是图上
// 既有 Step 且不得成环(环状依赖永不可执行,入口即拒)。
func stepEvents(ctx context.Context, store *rings.Store, taskID, stepID, intent string, opt DispatchOptions) ([]rings.Event, error) {
	if opt.source == sourceExplore {
		_, steps, edges, err := queueView(ctx, store, taskID)
		if err != nil {
			return nil, err
		}
		w := QueueWatermarkOf(steps, edges, QueueLimit(ctx, store))
		if w.Available == 0 {
			return nil, &QueueFullError{Watermark: w}
		}
	}
	if swarm.ActionLevelRank(rings.ActionLevel(opt.ActionLevel)) == 0 {
		return nil, fmt.Errorf("overmind: 派发 %s: 意图分级 %q 缺失或非法(须为 只读探测|主动测试|利用深入|修改与持久化 之一)",
			stepID, opt.ActionLevel)
	}
	if opt.HarmClass != "" && !rings.IsValidHarmClass(rings.HarmClass(opt.HarmClass)) {
		return nil, fmt.Errorf("overmind: 派发 %s: 危害声明 %q 非法(须为 data_destruction|denial_of_service 之一,普通意图留空)",
			stepID, opt.HarmClass)
	}
	if opt.Priority != "" && PriorityRank(opt.Priority) <= 0 {
		return nil, fmt.Errorf("overmind: 派发 %s: 优先级 %q 非法(须为 high|normal|low 之一,缺省 normal)",
			stepID, opt.Priority)
	}
	// 授权范围摘除守卫(P4-05):摘除的资产不再被派发——covers 命中
	// 即整批拒收,拒因回流进简报,主宰换方向不重试。双保险:快照资产
	// 行已标"范围外",RoE 约束块带清单。
	if len(opt.CoversAssets) > 0 {
		if roe, err := store.CurrentRoE(ctx, taskID); err == nil {
			if ex := roe.ExcludedAssetSet(); ex != nil {
				for _, a := range opt.CoversAssets {
					if ex[a] {
						return nil, fmt.Errorf("overmind: 派发 %s: 资产 %s 已从授权范围摘除(RoE),不派发覆盖它的意图", stepID, a)
					}
				}
			}
		}
	}
	// 待批守卫(P4-44 ③):渗透任务的范围外资产锁定不测——covers 命中
	// 待批资产即整批拒收(与摘除守卫同款拒因回流,主宰换方向);面板
	// 放行后转为范围内,派发自然通过。快照资产行已标"待批",单位侧
	// 还有命令级硬拦兜底(直接探测范围外主机的命令也拦)。
	if pending := coversPendingScope(ctx, store, taskID, opt.CoversAssets); len(pending) > 0 {
		return nil, fmt.Errorf("overmind: 派发 %s: 资产 %s 在任务范围外待批(渗透任务锁定不测,面板放行后才可派发)",
			stepID, strings.Join(pending, "、"))
	}
	if len(opt.DependsOn) > 0 {
		if err := validateDependencies(ctx, store, taskID, stepID, opt.DependsOn); err != nil {
			return nil, fmt.Errorf("overmind: 派发 %s: %w", stepID, err)
		}
	}
	detail := map[string]any{}
	for k, v := range opt.Detail {
		detail[k] = v
	}
	// 豁免标记只允许真实来源入口写入，普通探索不可自报。
	if opt.source == sourceExplore {
		delete(detail, "urgent")
		delete(detail, "kind")
	}
	if opt.GoalID != "" {
		detail["goal_id"] = opt.GoalID
	}
	if opt.ExpectedObservation != "" {
		detail["expected_observation"] = opt.ExpectedObservation
	}
	detail["action_level"] = opt.ActionLevel
	if h := strings.TrimSpace(opt.HarmClass); h != "" {
		detail["harm_class"] = h
	}
	if p := strings.TrimSpace(opt.Priority); p != "" && p != PriorityNormal {
		detail["priority"] = p
	}
	events := []rings.Event{{
		Kind: rings.EventNodeAdded, Actor: ActorOvermind,
		Payload: rings.NodeAdded{NodeID: stepID, NodeType: rings.NodeStep, Title: intent, Status: "dispatched", Detail: detail},
	}}
	for _, a := range opt.CoversAssets {
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: stepID, ToNode: a},
		})
	}
	for _, dep := range opt.DependsOn {
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: stepID, ToNode: dep},
		})
	}
	return events, nil
}

// validateDependencies depends_on 前置校验:非空、不得依赖自身、不得
// 重复,且每个前置必须是图上既有 Step。环在此入口不可构造:stepEvents
// 只为全新 stepID 组批(node.added 对已存在节点整批拒收),新节点在
// 既有图上无入边,新加的 depends_on 边(新→旧)不可能回到新节点——
// 依赖未来步骤则被账本边端点校验物理拒绝(端点必须已存在)。前置的
// 状态不限(可依赖 dispatched/running 的在途步骤,依赖闸等其完成)。
func validateDependencies(ctx context.Context, store *rings.Store, taskID, stepID string, deps []string) error {
	seen := map[string]bool{}
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			return fmt.Errorf("前置步骤 ID 为空")
		}
		if dep == stepID {
			return fmt.Errorf("不得依赖自身(%s)", stepID)
		}
		if seen[dep] {
			return fmt.Errorf("前置 %s 重复申报", dep)
		}
		seen[dep] = true
		n, err := store.Node(ctx, taskID, dep)
		if err != nil {
			return fmt.Errorf("前置 %s 不在图上(依赖未来步骤会永久等待,账本边端点校验亦拒): %w", dep, err)
		}
		if n.Type != rings.NodeStep {
			return fmt.Errorf("前置 %s 是 %s 不是 step", dep, n.Type)
		}
	}
	return nil
}

// Skipped 收割时被跳过的候选及原因(诚实呈现,不静默丢弃)。
type Skipped struct {
	FactID string
	Reason string
}

// HarvestResult 一次收割的结果。
type HarvestResult struct {
	Confirmed []string // 转正的候选 Fact ID
	Skipped   []Skipped
}

// Harvest 主宰收割最简(P1-04,确定性规则、零 LLM——R2:机器裁决):
// 扫描 candidate 状态的 Fact,凡携带证据(detail.evidence 或 blob 引用
// detail.evidence_ref)者转正 confirmed;正式化 = 追加 node.state_changed
// 事件(候选节点同一 ID,血缘与审计轨迹保留)。无证据候选跳过并记录原因。
// 证据真伪的核验升级(影响面、反证)归仲裁者 P1-06/P1-08。
// P4-12(问题清单 #21):带 new_asset 声明的候选转正时收割入图——发现型
// 对象点亮 source=discovered,出生血缘(发现步骤 produces→资产、发现
// 事实 about→资产)与发现单位/步骤/事实冻结进 detail,链路从成果一路
// 走通到"谁发现的它"。同标识资产查重:重复发现不重复入图,发现事实
// 锚定到既有对象(不编造第二次出生)。
func Harvest(ctx context.Context, store *rings.Store, taskID string) (*HarvestResult, error) {
	candidates, err := store.NodesByType(ctx, taskID, rings.NodeFact, "candidate")
	if err != nil {
		return nil, fmt.Errorf("overmind: 检查归纳扫描待确认事实: %w", err)
	}
	res := &HarvestResult{}
	for _, c := range candidates {
		hasEvidence := false
		if v, ok := c.Detail["evidence"].(string); ok && v != "" {
			hasEvidence = true
		}
		if v, ok := c.Detail["evidence_ref"].(string); ok && v != "" {
			hasEvidence = true
		}
		if !hasEvidence {
			res.Skipped = append(res.Skipped, Skipped{FactID: c.ID, Reason: "待确认事实无证据引用,不确认"})
			continue
		}
		events := []rings.Event{{
			Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
			Payload: rings.NodeStateChanged{NodeID: c.ID, Status: "confirmed"},
		}}
		if decl := factNewAsset(c.Detail); decl != nil {
			assetEvents, err := discoveredAssetEvents(ctx, store, taskID, c, decl)
			if err != nil {
				return res, fmt.Errorf("overmind: 检查归纳事实 %s 入图新对象: %w", c.ID, err)
			}
			events = append(events, assetEvents...)
		}
		if _, err := store.Append(ctx, taskID, events...); err != nil {
			return res, fmt.Errorf("overmind: 检查归纳事实 %s: %w", c.ID, err)
		}
		res.Confirmed = append(res.Confirmed, c.ID)
	}
	return res, nil
}

// factNewAsset 候选事实 detail 里的 new_asset 声明(P4-12);无声明或
// 声明缺标识返回 nil(入口已校验过词表,这里只取形状不重复校验)。
func factNewAsset(detail map[string]any) *swarm.NewAssetDecl {
	raw, ok := detail["new_asset"].(map[string]any)
	if !ok {
		return nil
	}
	decl := &swarm.NewAssetDecl{}
	decl.Class, _ = raw["class"].(string)
	decl.Title, _ = raw["title"].(string)
	decl.Parent, _ = raw["parent"].(string)
	if strings.TrimSpace(decl.Title) == "" {
		return nil
	}
	return decl
}

// discoveredAssetEvents 收割入图的事件批(P4-12,与事实转正同批一事务):
//   - 同标识资产已存在 → 只补 about 边(发现事实锚定既有对象,断链不编造);
//   - 新对象 → 资产节点(source=discovered,出生血缘冻结进 detail)+
//     produces 边(发现步骤 → 资产,对象出生边)+ about 边(发现事实 →
//     资产)+ 可选 sub_of 边(声明带父资产时挂层级)。
//
// P4-44 ①:新对象入图时对照种子闭包确定性判定范围归属并冻结进
// detail.scope(种子/手动/已放行资产构成闭包;给 IP 的闭包下域名经
// DNS 解析命中视为同机)。红队任务同样判定——"发现的其他资产"分组
// 靠它;锁定与否由消费侧按任务类型走(P4-44 ③④)。
func discoveredAssetEvents(ctx context.Context, store *rings.Store, taskID string, fact rings.NodeFull, decl *swarm.NewAssetDecl) ([]rings.Event, error) {
	title := strings.TrimSpace(decl.Title)
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return nil, fmt.Errorf("查资产查重: %w", err)
	}
	for _, a := range assets {
		if strings.TrimSpace(a.Title) == title {
			return []rings.Event{{Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
				Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: fact.ID, ToNode: a.ID}}}, nil
		}
	}
	// 出生步骤 = 事实的 produces 边来源(虫群提交时同批落的血缘)。
	stepID := ""
	produces, err := store.Edges(ctx, taskID, rings.EdgeProduces)
	if err != nil {
		return nil, fmt.Errorf("查事实血缘: %w", err)
	}
	for _, e := range produces {
		if e.To == fact.ID {
			stepID = e.From
			break
		}
	}
	unitID, _ := fact.Detail["unit_id"].(string)
	transcript, _ := fact.Detail["transcript_id"].(string)
	detail := map[string]any{
		"source":          rings.AssetSourceDiscovered,
		"unit_id":         unitID,
		"discovered_fact": fact.ID,
	}
	detail["scope"] = judgeDiscoveredScope(ctx, store, taskID, title, assets)
	if stepID != "" {
		detail["step_id"] = stepID
	}
	if transcript != "" {
		detail["transcript_id"] = transcript
	}
	id := "a-" + shortIDSuffix()
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: id, NodeType: rings.NodeAsset, Title: title, Status: "active",
			AssetClass: rings.AssetClass(strings.TrimSpace(decl.Class)), Detail: detail,
		}},
		{Kind: rings.EventEdgeAdded, Actor: ActorOvermind, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: fact.ID, ToNode: id,
		}},
	}
	if stepID != "" {
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: stepID, ToNode: id},
		})
	}
	if p := strings.TrimSpace(decl.Parent); p != "" {
		if _, err := store.Node(ctx, taskID, p); err == nil {
			events = append(events, rings.Event{
				Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
				Payload: rings.EdgeAdded{EdgeType: rings.EdgeSubOf, FromNode: id, ToNode: p},
			})
		}
	}
	return events, nil
}

// judgeDiscoveredScope 新发现资产的范围归属判定(P4-44 ①):种子/手动/
// 已放行资产构成闭包(给域名=域名级授权,给 IP=服务器级授权——解析到
// 该 IP 的域名视为同机),对照判 in/out。判定失败(类型/清单读不出)
// 按范围内兜底——冻结错误会把资产永久锁在待批;判定本身零 LLM。两种
// 任务类型都判(红队任务的"发现的其他资产"分组同靠此标记)。
func judgeDiscoveredScope(ctx context.Context, store *rings.Store, taskID, title string, assets []rings.NodeFull) string {
	approved := map[string]bool{}
	if roe, err := store.CurrentRoE(ctx, taskID); err == nil {
		approved = roe.ScopeApprovedSet()
	}
	if approved == nil {
		approved = map[string]bool{}
	}
	closure := rings.BuildScopeClosure(rings.ScopeSeedsOf(assets), approved)
	return rings.JudgeAssetScope(closure, title, rings.DefaultScopeResolver)
}

// coversPendingScope covers 清单里命中待批资产的条目(P4-44 ③ 派发
// 守卫的判定体;读不出任务类型/清单时返回 nil——判定失败不挡派发,
// 单位侧硬拦兜底)。
func coversPendingScope(ctx context.Context, store *rings.Store, taskID string, covers []string) []string {
	if len(covers) == 0 {
		return nil
	}
	detail, err := store.CreatedDetail(ctx, taskID)
	if err != nil || rings.TaskTypeOf(detail) != rings.TaskTypePentest {
		return nil
	}
	roe, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil
	}
	approved := roe.ScopeApprovedSet()
	excluded := roe.ExcludedAssetSet()
	var pending []string
	for _, id := range covers {
		a, err := store.Node(ctx, taskID, id)
		if err != nil || a.Type != rings.NodeAsset {
			continue
		}
		if rings.ScopeStateOf(rings.TaskTypePentest, rings.AssetSourceOf(a.Detail),
			rings.AssetScopeOf(a.Detail), approved[id], excluded[id]) == rings.ScopeStatePending {
			pending = append(pending, a.Title)
		}
	}
	return pending
}

// HarvestClaimsResult 一次声明收割(反证派发)的结果。
type HarvestClaimsResult struct {
	Dispatched []string // 派出反证 Step 的声明 ID
	Skipped    []Skipped
}

// HarvestClaims 声明收割(P1-08 + P2-02,第三章 §5:反证测试 = 普通意图
// Step,主宰派发、虫群单位执行、结果写回)。两类收割对象:
//   - candidate:完整性核验(冻结标准/发现单位/发现血缘齐全)后派发
//     第 1 轮反证 Step(冻结竞争解释全量清单进 step.detail)→ testing;
//   - awaiting_round(P2-02):轮末机器评估判"再反证"的声明,派发下一轮
//     (轮次续号,存活解释清单从账本算出)→ testing。
//
// R12 落地形态:一 Step 一单位,反证 Step 由领取的新单位执行,天然 ≠
// 发现单位;写回入口另有同单位拒收兜底,仲裁者求值再核验一道
// (verifier_independent)。反证 Step 继承发现步骤的意图分级(反证重放
// 同类动作,授权姿态与发现时一致;缺省只读探测)。完整性缺口的声明
// 跳过并留原因——不经机器终审的声明不得进入反证。
func HarvestClaims(ctx context.Context, store *rings.Store, taskID string) (*HarvestClaimsResult, error) {
	candidates, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimCandidate)
	if err != nil {
		return nil, fmt.Errorf("overmind: 检查归纳扫描待确认判断: %w", err)
	}
	awaiting, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimAwaitingRound)
	if err != nil {
		return nil, fmt.Errorf("overmind: 检查归纳扫描待下轮判断: %w", err)
	}
	res := &HarvestClaimsResult{}
	if len(candidates) == 0 && len(awaiting) == 0 {
		return res, nil
	}
	derived, err := store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
	if err != nil {
		return nil, fmt.Errorf("overmind: 检查归纳判断: 查血缘: %w", err)
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("overmind: 检查归纳判断: 查资产锚点: %w", err)
	}
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 检查归纳判断: 查步骤: %w", err)
	}

	for _, c := range candidates {
		rawStd, _ := c.Detail["standard"].(map[string]any)
		if _, err := arbiter.ParseClaimStandard(rawStd); err != nil {
			res.Skipped = append(res.Skipped, Skipped{FactID: c.ID,
				Reason: "判断无冻结判定标准,不进反证(仲裁者不可判): " + err.Error()})
			continue
		}
		if u, _ := c.Detail["unit_id"].(string); u == "" {
			res.Skipped = append(res.Skipped, Skipped{FactID: c.ID, Reason: "判断无发现单位记录,R12 无法核验,不进反证"})
			continue
		}
		combined, _ := c.Detail["combined"].(bool)
		var discoverStep *rings.NodeFull
		if combined {
			// 组合声明(P2-06):血缘核验改为环节 Fact——derived_from 指向
			// 的 Fact 即攻击链环节,无发现步骤语义(发现者=主宰)。
			links := 0
			for _, e := range derived {
				if e.From != c.ID {
					continue
				}
				if n, err := store.Node(ctx, taskID, e.To); err == nil && n.Type == rings.NodeFact {
					links++
				}
			}
			if links < MinChainLinks {
				res.Skipped = append(res.Skipped, Skipped{FactID: c.ID,
					Reason: fmt.Sprintf("组合判断血缘不全(derived_from→Fact 仅 %d 条,须 ≥%d),不进反证", links, MinChainLinks)})
				continue
			}
		} else {
			// 普通声明:发现步骤 = derived_from 指向的 Step(血缘即"这个发现从哪来")。
			var discoverStepID string
			for _, e := range derived {
				if e.From != c.ID {
					continue
				}
				if n, err := store.Node(ctx, taskID, e.To); err == nil && n.Type == rings.NodeStep {
					discoverStepID = e.To
					break
				}
			}
			if discoverStepID == "" {
				res.Skipped = append(res.Skipped, Skipped{FactID: c.ID, Reason: "判断无发现步骤血缘(derived_from),不进反证"})
				continue
			}
			step, err := store.Node(ctx, taskID, discoverStepID)
			if err != nil {
				return nil, fmt.Errorf("overmind: 检查归纳判断 %s: 查发现步骤: %w", c.ID, err)
			}
			discoverStep = step
		}
		var assets []string
		for _, e := range abouts {
			if e.From == c.ID {
				assets = append(assets, e.To)
			}
		}
		if err := dispatchFalsifyRound(ctx, store, taskID, c, 1, claimAlternatives(c), discoverStep, assets,
			"反证(第 1 轮): "+c.Title); err != nil {
			return res, fmt.Errorf("overmind: 派发判断 %s 反证: %w", c.ID, err)
		}
		res.Dispatched = append(res.Dispatched, c.ID)
	}

	// 待下轮声明:轮次续号,存活解释从账本算出(前几轮已排除的不重派)。
	for _, c := range awaiting {
		combined, _ := c.Detail["combined"].(bool)
		var discoverStep *rings.NodeFull
		if combined {
			links := 0
			for _, e := range derived {
				if e.From == c.ID {
					if n, err := store.Node(ctx, taskID, e.To); err == nil && n.Type == rings.NodeFact {
						links++
					}
				}
			}
			if links < MinChainLinks {
				res.Skipped = append(res.Skipped, Skipped{FactID: c.ID,
					Reason: fmt.Sprintf("组合判断血缘不全(derived_from→Fact 仅 %d 条),不派下一轮", links)})
				continue
			}
		} else {
			var discoverStepID string
			for _, e := range derived {
				if e.From != c.ID {
					continue
				}
				if n, err := store.Node(ctx, taskID, e.To); err == nil && n.Type == rings.NodeStep {
					discoverStepID = e.To
					break
				}
			}
			if discoverStepID == "" {
				res.Skipped = append(res.Skipped, Skipped{FactID: c.ID, Reason: "判断无发现步骤血缘(derived_from),无法继承意图分级,不派下一轮"})
				continue
			}
			step, err := store.Node(ctx, taskID, discoverStepID)
			if err != nil {
				return nil, fmt.Errorf("overmind: 检查归纳判断 %s: 查发现步骤: %w", c.ID, err)
			}
			discoverStep = step
		}
		var assets []string
		for _, e := range abouts {
			if e.From == c.ID {
				assets = append(assets, e.To)
			}
		}
		next := maxFalsifyRound(steps, c.ID) + 1
		alive, err := arbiter.AliveAlternatives(ctx, store, taskID, c.ID)
		if err != nil {
			return res, fmt.Errorf("overmind: 判断 %s 算存活解释: %w", c.ID, err)
		}
		if err := dispatchFalsifyRound(ctx, store, taskID, c, next, altToRaw(alive), discoverStep, assets,
			fmt.Sprintf("反证(第 %d 轮): %s", next, c.Title)); err != nil {
			return res, fmt.Errorf("overmind: 派发判断 %s 第 %d 轮反证: %w", c.ID, next, err)
		}
		res.Dispatched = append(res.Dispatched, c.ID)
	}
	return res, nil
}

// dispatchFalsifyRound 派发一轮反证:反证 Step(绑定声明 + 轮次 + 本轮
// 待检解释清单)+ 声明迁移 testing,同一批事务落账(派发与状态迁移
// 原子,不出现"步骤在跑而声明不在反证中"的中间态)。反证 Step 继承
// 发现步骤的意图分级与目标归属;组合声明(P2-06)无发现步骤——血缘
// 挂在环节 Fact 上,意图分级取只读探测(重放组合效果的默认授权姿态,
// 需更强动作时由反证单位按 RoE/审批门逐次过门)。P4-08 ①:优先级定
// high(反证少数有界、1~2 轮收敛;发现 → Confirmed 周期卡 R13 燃料,
// 探索稍让位是预期取舍,名额保障与解释限量不碰)。
func dispatchFalsifyRound(ctx context.Context, store *rings.Store, taskID string, c rings.NodeFull,
	round int, rawAlts []any, discoverStep *rings.NodeFull, assets []string, intent string) error {
	level := rings.ActionReadOnly
	harm := ""
	goalID := ""
	if discoverStep != nil {
		if al, _ := discoverStep.Detail["action_level"].(string); swarm.ActionLevelRank(rings.ActionLevel(al)) > 0 {
			level = rings.ActionLevel(al)
		}
		if h, _ := discoverStep.Detail["harm_class"].(string); rings.IsValidHarmClass(rings.HarmClass(h)) && h != "" {
			harm = h // 危害声明随分级同继承:反证重放同类动作,授权姿态一致
		}
		goalID, _ = discoverStep.Detail["goal_id"].(string)
	}
	stepID, err := nextStepID(ctx, store, taskID)
	if err != nil {
		return err
	}
	detail := map[string]any{"kind": "falsify", "testclaim_id": c.ID, "round": round}
	if rawAlts != nil {
		detail["alternatives"] = rawAlts
	}
	events, err := stepEvents(ctx, store, taskID, stepID, intent, DispatchOptions{
		source:              sourceFalsify,
		GoalID:              goalID,
		ExpectedObservation: "经 submit_falsify_result 写回结构化反证结论(逐条竞争解释按 ID 给 refuted)",
		ActionLevel:         string(level),
		HarmClass:           harm,
		Priority:            PriorityHigh,
		CoversAssets:        assets,
		Detail:              detail,
	})
	if err != nil {
		return err
	}
	events = append(events, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
		Payload: rings.NodeStateChanged{NodeID: c.ID, Status: rings.TestClaimTesting},
	})
	_, err = store.Append(ctx, taskID, events...)
	return err
}

// claimAlternatives 声明 detail 里冻结的解释清单原样转 step.detail 用的
// raw 形态(nil 保持 nil:无解释语义的声明不注入空清单)。
func claimAlternatives(c rings.NodeFull) []any {
	raw, _ := c.Detail["alternatives"].([]any)
	return raw
}

// altToRaw 解释清单转 detail 原始形态。
func altToRaw(alts []rings.Alternative) []any {
	if alts == nil {
		return nil
	}
	out := make([]any, 0, len(alts))
	for _, a := range alts {
		out = append(out, map[string]any{"id": a.ID, "label": a.Label})
	}
	return out
}

// maxFalsifyRound 声明名下反证步骤的最大轮次号(无步骤为 0)。
func maxFalsifyRound(steps []rings.NodeFull, claimID string) int {
	max := 0
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id != claimID {
			continue
		}
		if r, ok := s.Detail["round"].(float64); ok && int(r) > max {
			max = int(r)
		}
	}
	return max
}

// ClaimFalsifySteps 声明当前战役内的反证步骤(收割定稿组装复现步骤的
// 归因范围):绑定声明且创建于最近 reopen 之后的 Step,按轮次排序。
// P4-08 ② 起服务端发现详情的耗时拆解共用本口径(排队/执行/轮次只算
// 当前战役,重开前的旧战役不混计)。
func ClaimFalsifySteps(ctx context.Context, store *rings.Store, taskID, claimID string) ([]rings.NodeFull, error) {
	campaignRev := int64(0)
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿查事件流: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, fmt.Errorf("overmind: 定稿解码 testclaim.record: %w", err)
		}
		if r.ClaimID == claimID && r.Type == rings.TestClaimRecReopen {
			campaignRev = ev.Revision
		}
	}
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿查反证步骤: %w", err)
	}
	var out []rings.NodeFull
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id != claimID {
			continue
		}
		if campaignRev > 0 && s.CreatedRev <= campaignRev {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, _ := out[i].Detail["round"].(float64)
		rj, _ := out[j].Detail["round"].(float64)
		return ri < rj
	})
	return out, nil
}

// AssembleRepro 收割定稿组装(P2-02,第五章 §1 + R17 同源纪律):确认
// 裁决同批落库的复现步骤与严重级别。确定性组装、零 LLM:
//   - Severity = 末轮反证的影响面定级(级别只跟影响面证据走,发现者
//     无级别通道);
//   - 复现步骤 = 当前战役反证 Step 的镜留痕 HTTP(S)交换(P3-01 起
//     https 解密留痕同收;独立验证单位的真实重放,天然可复现),每步
//     携带操作方法(该 Step 的工具调用:工具名+参数,受控回流入口
//     不计;证据包收,面板/报告正文不显,P4-08 ⑤;P4-35 ④ 起总量
//     封顶——枚举型反证的成千上万条试错命令不再全量内嵌)、短名与
//     一句话描述(P4-08 ④/P4-35 ②:组装时先落确定性兜底,定稿落账
//     前由 Loop 用模型挑最小集并改写为三层人话冻结;失败回退确定性
//     兜底)与交换原文的 blob 引用(Burp 可验证数据包,完整原文经
//     内容寻址回放,R18);
//   - P3-05b ①:交换按目标流量过滤——请求 host 在任务授权范围
//     (种子资产标识)内才收,外网查证流量(web_fetch 等)不进步骤;
//     流量页不受影响(审计链全量保留);
//   - P4-08 ③ 组装侧去同型:同方法+同路径+同状态的重复交换只保留
//     构成复现流程的一条(确定性保留规则 = 该型首条;非成功状态自成
//     一型不合并,401→绕过→200 式流程叙事保留);反证变体流量(换
//     UA / 防缓存参数 / 近似 body)不进复现步骤;不同 URL / 步骤的
//     多步流程不受影响。
//   - P4-35 ①④ 最小集与存储瘦身(问题清单 #57):去同型结果只是
//     候选池(Steps 返回,模型据此挑必要步骤);战役范围内全部交换
//     另记 Process 引用清单(仅交换 ID 与 blob 编号,原文大块头一律
//     在仓库,数据库不内嵌)。Variants 不再填充——旧口径变体随步骤
//     全量内嵌曾把单条记录撑到百 MB 级;证据包导出按 Process 全量
//     收(审计不丢)。无步骤数上限、无异常报警(造物主拍板:最小集
//     天然几步,出现几十步属装配错误,不设闸)。
//
// 只读账本,不写;由主宰循环与确认裁决同批落账。
func AssembleRepro(ctx context.Context, store *rings.Store, taskID, claimID, severity string) (*rings.TestClaimRecord, error) {
	falsifySteps, err := ClaimFalsifySteps(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	byID := map[string]rings.NodeFull{}
	for _, s := range falsifySteps {
		byID[s.ID] = s
	}
	// 目标流量过滤口径(P3-05b ①):任务授权范围 = 种子资产标识解析出的
	// 目标源(host:port 精确;无端口的 host 级授权)。RoE 的资产引用与
	// 意图拦截均锚定同一批资产节点,口径同源。
	scope, err := loadTargetScope(ctx, store, taskID)
	if err != nil {
		return nil, err
	}
	// 每 Step 的操作方法:工具调用记录(工具名+参数截取;submit_* 回流
	// 入口不是对目标的操作,不计)。
	ops := map[string][]string{}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿读事件流: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, fmt.Errorf("overmind: 定稿解码 swarm.record: %w", err)
		}
		if r.Type != rings.SwarmRecToolCall || r.IsError {
			continue
		}
		if _, bound := byID[r.StepID]; !bound {
			continue
		}
		if strings.HasPrefix(r.ToolName, "submit_") {
			continue
		}
		op := r.ToolName + " " + clipArgs(string(r.ToolArgs), 200)
		ops[r.StepID] = append(ops[r.StepID], op)
	}
	// 账次序收全部范围内交换(候选池与过程清单共用一次收集;P4-35 ①:
	// 全量交换先进 Process 引用清单留档,再由去同型收出候选步骤)。
	var all []rings.ReproStep
	process := []rings.ReproProcessRef{}
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			return nil, fmt.Errorf("overmind: 定稿解码 mirror.record: %w", err)
		}
		s, ok := byID[m.StepID]
		if !ok || (m.Type != rings.MirrorRecHTTP && m.Type != rings.MirrorRecHTTPS) {
			continue // 只收可复现原文的交换(https 为 P3-01 解密留痕;隧道无原文,如实不收)
		}
		if !scope.allows(m.URL) {
			continue // 非目标流量(外网查证等)不进复现步骤;流量页全量保留
		}
		if m.BlobRef != "" {
			process = append(process, rings.ReproProcessRef{ExchangeID: m.ExchangeID, BlobRef: m.BlobRef})
		}
		round, _ := s.Detail["round"].(float64)
		step := rings.ReproStep{
			Round: int(round), StepID: m.StepID, UnitID: m.UnitID,
			Short:  reproStepShort(m),
			Desc:   reproStepDesc(m),
			Method: m.Method, URL: m.URL, Status: m.Status, BlobRef: m.BlobRef,
			ExchangeID: m.ExchangeID,
		}
		if m.Error != "" {
			step.Note = "该请求未完成" // 原始错误串在流量页悬停与交换详情,不进复现正文
		}
		if list := ops[m.StepID]; len(list) > 0 {
			step.Op = joinOps(list)
		}
		all = append(all, step)
	}
	// 去同型(P4-08 ③):型 = 方法+host+路径(不含查询串)+状态,
	// 保留规则 = 该型首条(型内同状态,"首条成功交换"退化为首条——
	// 非成功状态自成一型不合并,如 401→绕过→200 的流程叙事保留);
	// 查询串值差异(防缓存参数等)不另立型,不同路径的多步流程原样保留。
	// P4-35 ①:去同型结果是候选池——最小集由定稿时模型从中挑选
	// (Loop.genReproDescs),未选中的交换只留 Process 清单。
	seen := map[string]bool{}
	rec := &rings.TestClaimRecord{
		ClaimID: claimID, Type: rings.TestClaimRecRepro, Severity: severity,
		Process: process,
	}
	for _, st := range all {
		key := reproShapeKey(st.Method, st.URL, st.Status)
		if seen[key] {
			continue // 同型候选只留首条;后续交换在 Process 清单留档
		}
		seen[key] = true
		rec.Steps = append(rec.Steps, st)
	}
	for i := range rec.Steps {
		rec.Steps[i].Seq = i + 1
	}
	return rec, nil
}

// reproOpClip 单步操作方法(完整命令)的落账总量上限(P4-35 ④,问题
// 清单 #57):正常步骤数条命令不触顶;枚举型反证的成千上万条试错命令
// 在此封顶并如实标注省略——完整调用参数在 swarm.record 审计流,
// 过程流量原文在仓库与证据包,数据库不内嵌大块头(R18)。
const reproOpClip = 2000

// joinOps 步骤命令的落账拼接("; " 分隔,总量封顶,超长如实标注总条数)。
func joinOps(list []string) string {
	var b strings.Builder
	for i, op := range list {
		if i > 0 {
			b.WriteString("; ")
		}
		if b.Len()+len(op) > reproOpClip {
			fmt.Fprintf(&b, "…(共 %d 条命令,余略)", len(list))
			return b.String()
		}
		b.WriteString(op)
	}
	return b.String()
}

// reproShapeKey 交换的"型"(去同型分组键):方法 + host + 路径(不含
// 查询串)+ 状态。host 小写归一(DNS 语义);路径保持原样(大小写
// 敏感,不归一)。URL 解析失败按原串整体入键(如实不合并)。
func reproShapeKey(method, rawURL string, status int) string {
	host, path := rawURL, "/"
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host, path = strings.ToLower(u.Host), u.Path
		if path == "" {
			path = "/"
		}
	}
	return method + " " + host + " " + path + " " + strconv.Itoa(status)
}

// targetScope 任务授权的目标源集合(P3-05b ①)。种子资产标识两种形态:
// URL(scheme://host[:port]…)取 host:port;裸 host[:port](域名/主机类)
// 直接解析。带端口 = host:port 精确授权;不带端口 = host 级授权(任意
// 端口)。host 比较大小写不敏感(DNS 语义)。
type targetScope struct {
	exact map[string]bool // "host:port"
	hosts map[string]bool // "host"(host 级授权)
}

// loadTargetScope 从图上资产节点解析授权范围(资产默认全部来自操作者
// 划定的授权域:种子表单与面板手动补入;P4-05 起 RoE 摘除的资产除外)。
func loadTargetScope(ctx context.Context, store *rings.Store, taskID string) (*targetScope, error) {
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿查资产(目标范围): %w", err)
	}
	excluded := map[string]bool{}
	if roe, err := store.CurrentRoE(ctx, taskID); err == nil {
		excluded = roe.ExcludedAssetSet()
	}
	sc := &targetScope{exact: map[string]bool{}, hosts: map[string]bool{}}
	for _, a := range assets {
		if excluded[a.ID] {
			continue // 已从授权范围摘除,不再是授权目标
		}
		host, port, ok := parseTargetOrigin(a.Title)
		if !ok {
			continue // 非主机类标识(人员/组织等),不参与 host 匹配
		}
		if port == "" {
			sc.hosts[host] = true
			continue
		}
		sc.exact[host+":"+port] = true
	}
	return sc, nil
}

// parseTargetOrigin 资产标识 → (host, port)。URL 形态经 url.Parse;
// 裸 host[:port] 排除路径/空白后按 net.SplitHostPort 解析(兼容
// [IPv6]:port)。解析不出 host 返回 ok=false。
func parseTargetOrigin(title string) (host, port string, ok bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", "", false
	}
	if i := strings.Index(t, "://"); i >= 0 {
		u, err := url.Parse(t)
		if err != nil || u.Host == "" {
			return "", "", false
		}
		return strings.ToLower(u.Hostname()), u.Port(), true
	}
	if strings.ContainsAny(t, "/?# \t") {
		return "", "", false // 含路径或空白的标识不是主机类
	}
	if h, p, err := net.SplitHostPort(t); err == nil && h != "" {
		return strings.ToLower(h), p, true
	}
	return strings.ToLower(t), "", true
}

// allows 交换 URL 的目标是否在授权范围内(空 URL / 解析失败 = 范围外,
// 如实不收)。
func (s *targetScope) allows(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if s.hosts[host] {
		return true
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return s.exact[host+":"+port]
}

// reproStepDesc 复现步骤一句话描述(P3-05b ②):"这步在干什么"从交换
// 事实确定性生成——动作(按方法)+ 目标路径 + 请求体大小(有则)+
// 结果定性(状态类/响应字节数),陈述事实不解说(8.5 文风);同源落
// 定稿记录,面板与报告零复制。
func reproStepDesc(m rings.MirrorRecord) string {
	path := m.URL
	if u, err := url.Parse(m.URL); err == nil && u.Path != "" {
		path = u.Path
	}
	path = "/" + strings.TrimPrefix(clipLine(path, 90), "/")
	verb := "请求"
	switch m.Method {
	case "POST", "PUT", "PATCH":
		verb = "提交"
	case "DELETE":
		verb = "请求删除"
	}
	head := verb + " " + path
	if m.ReqBytes > 0 {
		head += fmt.Sprintf("(请求体 %d 字节)", m.ReqBytes)
	}
	var tail string
	switch {
	case m.Status == 0 && m.Error != "":
		tail = "未获响应(" + clipLine(m.Error, 60) + ")"
	case m.Status >= 300 && m.Status < 400:
		tail = fmt.Sprintf("目标 %d 跳转", m.Status)
	case m.Status >= 200 && m.Status < 300 && m.RespBytes > 0:
		tail = fmt.Sprintf("目标返回 %d(%d 字节)", m.Status, m.RespBytes)
	case m.Status > 0:
		tail = fmt.Sprintf("目标返回 %d", m.Status)
	default:
		tail = "未获响应"
	}
	return head + "," + tail
}

// reproStepShort 步骤短名的确定性兜底(P4-35 ②):方法+路径("GET
// /admin"式);定稿时模型改写为动宾短名("拿会话凭据"式),生成失败
// 保留本兜底,旧记录缺省时读侧退"步骤 N"。
func reproStepShort(m rings.MirrorRecord) string {
	path := m.URL
	if u, err := url.Parse(m.URL); err == nil && u.Host != "" {
		path = u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
	}
	if m.Method == "" {
		return clipLine(path, 32)
	}
	return clipLine(m.Method+" "+path, 32)
}

// clipArgs 参数截取(操作方法展示口径;完整参数在 swarm.record 审计流)。
func clipArgs(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// clipLine 单行文本截取(描述与错误文本的展示口径,超长以 … 截断)。
func clipLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ReopenClaim 重开(P2-02,第三章 §5"确认后出现新反证可撤销重开"/
// "待确认可被新事件唤醒重新进入反证"):Confirmed/待确认声明经 reopen
// 记录(触发词 + 理由,历史不删,R1)回到反证中——竞争解释全部复活
// (旧战役证据整体失效,战役边界见仲裁者),轮次全局续号,重开轮的
// 反证 Step 与状态迁移同批事务落账。面板(造物主反驳/过期)与未来
// 机器触发(新证据)共用本入口;结论的再裁决仍归仲裁者机器(R2)。
func ReopenClaim(ctx context.Context, store *rings.Store, taskID, claimID, trigger, reason, actor string) error {
	claim, err := store.Node(ctx, taskID, claimID)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: %w", claimID, err)
	}
	if claim.Type != rings.NodeTestClaim {
		return fmt.Errorf("overmind: 节点 %s 是 %s 不是 testclaim", claimID, claim.Type)
	}
	if claim.Status != rings.TestClaimConfirmed && claim.Status != rings.TestClaimPending {
		return fmt.Errorf("overmind: 判断 %s 当前状态 %s,只有已确认/待确认可重开", claimID, claim.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("overmind: 重开必须说明理由(可反驳的结论必有可审计的反驳依据)")
	}
	derived, err := store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: 查血缘: %w", claimID, err)
	}
	combined, _ := claim.Detail["combined"].(bool)
	var discoverStep *rings.NodeFull
	if !combined {
		var discoverStepID string
		for _, e := range derived {
			if e.From == claimID {
				if n, err := store.Node(ctx, taskID, e.To); err == nil && n.Type == rings.NodeStep {
					discoverStepID = e.To
					break
				}
			}
		}
		if discoverStepID == "" {
			return fmt.Errorf("overmind: 判断 %s 无发现步骤血缘,无法派重开反证", claimID)
		}
		discoverStep, err = store.Node(ctx, taskID, discoverStepID)
		if err != nil {
			return fmt.Errorf("overmind: 重开判断 %s: 查发现步骤: %w", claimID, err)
		}
	}
	var assets []string
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: 查资产锚点: %w", claimID, err)
	}
	for _, e := range abouts {
		if e.From == claimID {
			assets = append(assets, e.To)
		}
	}
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: 查步骤续号: %w", claimID, err)
	}
	next := maxFalsifyRound(steps, claimID) + 1
	stepID, err := nextStepID(ctx, store, taskID)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: %w", claimID, err)
	}
	level := rings.ActionReadOnly
	harm := ""
	goalID := ""
	if discoverStep != nil {
		if al, _ := discoverStep.Detail["action_level"].(string); swarm.ActionLevelRank(rings.ActionLevel(al)) > 0 {
			level = rings.ActionLevel(al)
		}
		if h, _ := discoverStep.Detail["harm_class"].(string); rings.IsValidHarmClass(rings.HarmClass(h)) && h != "" {
			harm = h
		}
		goalID, _ = discoverStep.Detail["goal_id"].(string)
	}
	// 落账次序(一批事务):reopen 记录 → 反证 Step → 状态迁移。reopen
	// 记录必须先于 Step 落账——战役边界取 reopen 的 revision,后创建的
	// Step 才归入新战役(仲裁者求值口径,见 arbiter.loadClaimOrigin)。
	events := []rings.Event{
		{Kind: rings.EventTestClaimRecord, Actor: actor, Payload: rings.TestClaimRecord{
			ClaimID: claimID, Type: rings.TestClaimRecReopen, Trigger: trigger, Reason: strings.TrimSpace(reason),
		}},
	}
	stepEvs, err := stepEvents(ctx, store, taskID, stepID, fmt.Sprintf("反证(重开第 %d 轮): %s", next, claim.Title), DispatchOptions{
		source:              sourceFalsify,
		GoalID:              goalID,
		ExpectedObservation: "经 submit_falsify_result 写回结构化反证结论(重开轮:全部竞争解释重新检验)",
		ActionLevel:         string(level),
		HarmClass:           harm,
		Priority:            PriorityHigh,
		CoversAssets:        assets,
		Detail: map[string]any{"kind": "falsify", "testclaim_id": claimID, "round": next,
			"alternatives": claimAlternatives(*claim)},
	})
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: %w", claimID, err)
	}
	events = append(events, stepEvs...)
	events = append(events, rings.Event{Kind: rings.EventNodeStateChanged, Actor: actor,
		Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimTesting}})
	if _, err := store.Append(ctx, taskID, events...); err != nil {
		return fmt.Errorf("overmind: 重开判断 %s 写入: %w", claimID, err)
	}
	return nil
}

// ---- 存量复核标记(P4-30 ③,问题清单 #51) ----

// reportExclusionPlan 拆解回复里的复核标记:主宰按立判危害门槛复核
// 图上既有判断,纯信息性的(纯前置条件/环境特性/无害观察)标记"不纳入
// 报告"。
type reportExclusionPlan struct {
	ClaimID string `json:"claim_id"`
	Reason  string `json:"reason"`
}

// applyReportExclusion 复核标记落账:程序校验形状——判断在本轮快照内、
// 复核理由非空;理由语义归模型(程序不代替判断,同 cancelQueued 纪律)。
// 落 testclaim.record(report_review)追加事件:账本不涂改、判断保留
// 可追溯,报告只收已确认且非信息性;读取侧按最新标记取舍(取代语义)。
func (l *Loop) applyReportExclusion(ctx context.Context, p reportExclusionPlan, evaluatedRev int64) error {
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("标记不纳入报告须注明理由(写明为何无直接危害)")
	}
	n, err := l.Store.Node(ctx, l.TaskID, p.ClaimID)
	if err != nil {
		return fmt.Errorf("判断 %s 不在图上", p.ClaimID)
	}
	if n.Type != rings.NodeTestClaim {
		return fmt.Errorf("节点 %s 是 %s 不是判断", p.ClaimID, n.Type)
	}
	if n.CreatedRev > evaluatedRev {
		return fmt.Errorf("判断 %s 不在本轮快照中", p.ClaimID)
	}
	_, err = l.Store.Append(ctx, l.TaskID, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: ActorOvermind,
		Payload: rings.TestClaimRecord{
			ClaimID: p.ClaimID, Type: rings.TestClaimRecReportReview,
			Reason: strings.TrimSpace(p.Reason),
		},
	})
	return err
}
