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
	goalMustBeActive    bool
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
	DependsOn  []string
	SourceRefs []string // 可核验的来源节点；独立于调度前置条件

	Detail map[string]any
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
	// P4-54:摘除粒度升级为写法粒度——`*.域` 通配摘除覆盖其后新发现的
	// 子域资产(新发现子域是独立节点、ID 精确命中不覆盖的派发面缺口,
	// 此前只有提示词软约束;现按通配排除模式确定性判定,命中不派)。
	if len(opt.CoversAssets) > 0 {
		if roe, err := store.CurrentRoE(ctx, taskID); err == nil {
			if ex := roe.ExcludedAssetSet(); len(ex) > 0 {
				assets, aerr := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
				if aerr != nil {
					return nil, fmt.Errorf("overmind: 派发 %s: 摘除通配判定查资产: %w", stepID, aerr)
				}
				// 覆盖面单源判定(rings.ExcludedCoveredAssetIDs):精确命中 +
				// 通配/端口写法覆盖 + 无锚点资产的排除标识子串回退——派发
				// 守卫与摘除连坐废弃(P4-55 审核迭代)同一覆盖面,防两处漂移。
				covered := rings.ExcludedCoveredAssetIDs(assets, ex)
				for _, a := range opt.CoversAssets {
					if ex[a] {
						return nil, fmt.Errorf("overmind: 派发 %s: 资产 %s 已从授权范围摘除(RoE),不派发覆盖它的意图", stepID, a)
					}
					if !covered[a] {
						continue
					}
					node, err := store.Node(ctx, taskID, a)
					if err != nil {
						continue
					}
					return nil, fmt.Errorf("overmind: 派发 %s: 资产 %s(%s) 命中摘除覆盖写法,不派发覆盖它的意图", stepID, a, node.Title)
				}
			}
		}
	}
	// 方向聚焦守卫(P4-54 停聚焦):聚焦期间只派发聚焦方向的意图——
	// 非聚焦方向的探索拒收;操作者急活豁免(造物主指令高于聚焦纪律)。
	if opt.source != sourceUrgent {
		if fg := FocusGoalID(ctx, store, taskID); fg != "" && opt.GoalID != fg {
			return nil, fmt.Errorf("overmind: 派发 %s「%s」: %w", stepID, intent, ErrFocusOutside)
		}
	}
	// 已删除目标守卫(P4-54 目标删除):该方向后续零新派——绑定已删除
	// 目标的意图一律拒收。
	if opt.GoalID != "" {
		goal, err := store.Node(ctx, taskID, opt.GoalID)
		if err != nil || goal.Type != rings.NodeGoal {
			return nil, fmt.Errorf("overmind: 派发 %s: 目标编号 %s 不对应本任务目标", stepID, opt.GoalID)
		}
		if goal.Status == GoalStatusDiscarded {
			return nil, fmt.Errorf("overmind: 派发 %s: %w", stepID, ErrGoalDiscarded)
		}
		if opt.goalMustBeActive && goal.Status != GoalStatusActive {
			return nil, fmt.Errorf("overmind: 派发 %s: 目标 %s 已结束，本轮旧规划不再派发", stepID, opt.GoalID)
		}
	}

	// A covers edge may only target an actual current inventory member.
	if len(opt.CoversAssets) > 0 {
		assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
		if err != nil {
			return nil, err
		}
		roe, err := store.CurrentRoE(ctx, taskID)
		if err != nil {
			return nil, err
		}
		members := rings.InventoryAssetIDs(assets, roe)
		for _, id := range opt.CoversAssets {
			if !members[id] {
				return nil, fmt.Errorf("overmind: 派发 %s: 资产 %s 未加入当前资产清单", stepID, id)
			}
		}
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
	sources, err := store.SourceEvents(ctx, taskID, stepID, ActorOvermind, opt.SourceRefs)
	if err != nil {
		return nil, err
	}
	events = append(events, sources...)
	return events, nil
}

// 无锚点资产的摘除子串回退判定已收编 rings.ExcludedCoveredAssetIDs
// (派发守卫与摘除连坐废弃共用单源覆盖判定,P4-55 审核迭代)。

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
		return nil, fmt.Errorf("overmind: 检查归纳扫描新进事实: %w", err)
	}
	res := &HarvestResult{}
	for _, candidate := range candidates {
		for ctx.Err() == nil {
			rev, err := store.CurrentRevision(ctx, taskID)
			if err != nil {
				return res, err
			}
			c, err := store.Node(ctx, taskID, candidate.ID)
			if err != nil {
				return res, err
			}
			if c.Status != "candidate" {
				break
			}
			hasEvidence := false
			if v, ok := c.Detail["evidence"].(string); ok && v != "" {
				hasEvidence = true
			}
			if v, ok := c.Detail["evidence_ref"].(string); ok && v != "" {
				hasEvidence = true
			}
			if !hasEvidence {
				res.Skipped = append(res.Skipped, Skipped{FactID: c.ID, Reason: "新进事实无证据引用,不确认"})
				break
			}
			events := []rings.Event{{
				Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
				Payload: rings.NodeStateChanged{NodeID: c.ID, Status: "confirmed"},
			}}
			if decl := factNewAsset(c.Detail); decl != nil {
				assetEvents, err := proposedAssetEvents(ctx, store, taskID, *c, decl)
				if err != nil {
					return res, fmt.Errorf("overmind: 检查归纳事实 %s 资产加入请求: %w", c.ID, err)
				}
				events = append(events, assetEvents...)
			}
			if _, err := store.AppendAtRevision(ctx, taskID, rev, events...); err != nil {
				if errors.Is(err, rings.ErrStaleRevision) {
					continue
				}
				return res, fmt.Errorf("overmind: 检查归纳事实 %s: %w", c.ID, err)
			}
			res.Confirmed = append(res.Confirmed, c.ID)
			break
		}
	}
	return res, nil
}

// factUnitOf 发现事实的发现单位(detail 血缘,可能缺)。
func factUnitOf(fact rings.NodeFull) string {
	u, _ := fact.Detail["unit_id"].(string)
	return u
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

// discoveredAssetEvents is used only after adoption or for an already joined
// identity. Original observations retain their own about/observed bindings.
func discoveredAssetEvents(ctx context.Context, store *rings.Store, taskID string, fact rings.NodeFull, decl *swarm.NewAssetDecl) ([]rings.Event, error) {
	class, title, key, err := rings.AssetIdentity(rings.AssetClass(decl.Class), decl.Title)
	if err != nil {
		return nil, err
	}
	assets, err := store.NodesByType(ctx, taskID, rings.NodeAsset, "")
	if err != nil {
		return nil, fmt.Errorf("查资产查重: %w", err)
	}
	for _, a := range assets {
		_, _, existing, e := rings.AssetIdentity(a.AssetClass, a.Title)
		if e == nil && existing == key && a.Status != rings.AssetStatusRetired {
			return store.BindObservedAssets(ctx, taskID, []rings.NodeAdded{{NodeID: a.ID, AssetClass: class, Title: title}}, []rings.NodeFull{fact}, ActorOvermind)
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
	detail["scope"] = rings.AssetScopeIn
	detail["adopted"] = true
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
			AssetClass: class, Detail: detail,
		}},
	}
	if stepID != "" {
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: stepID, ToNode: id},
		})
	}
	if p := strings.TrimSpace(decl.Parent); p != "" {
		if parent, err := store.Node(ctx, taskID, p); err == nil && parent.Type == rings.NodeAsset && parent.Status != rings.AssetStatusRetired && rings.ValidateAssetParent(class, title, parent.AssetClass, parent.Title) == nil {
			events = append(events, rings.Event{
				Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
				Payload: rings.EdgeAdded{EdgeType: rings.EdgeSubOf, FromNode: id, ToNode: p},
			})
		}
	}
	bindings, err := store.BindObservedAssets(ctx, taskID, []rings.NodeAdded{{NodeID: id, AssetClass: class, Title: title}}, []rings.NodeFull{fact}, ActorOvermind)
	if err != nil {
		return nil, err
	}
	return append(events, bindings...), nil
}

// claimCampaignRev 声明当前战役起点(最近一次 reopen 的 revision;0=从未
// 重开)——定稿组装(复现步骤归因范围)与证据种子解析(P4-73)共用,
// 单源防漂移。
func claimCampaignRev(ctx context.Context, store *rings.Store, taskID, claimID string) (int64, error) {
	campaignRev := int64(0)
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("overmind: 定稿查事件流: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return 0, fmt.Errorf("overmind: 定稿解码 testclaim.record: %w", err)
		}
		if r.ClaimID == claimID && r.Type == rings.TestClaimRecReopen {
			campaignRev = ev.Revision
		}
	}
	return campaignRev, nil
}

// ClaimFalsifySteps 声明当前战役内的归因步骤(收割定稿组装复现步骤
// 的归因范围):绑定声明且创建于最近 reopen 之后的 Step(旧反证步骤,
// detail.testclaim_id)+ 战役内交换单的执行步骤(submit 记录的
// StepID——新流程证据出自交卷单位的意图步骤,P5-02③ 种子/复现池
// 归因范围),按创建序排列。
// P4-08 ② 起服务端发现详情的耗时拆解共用本口径(排队/执行/轮次只算
// 当前战役,重开前的旧战役不混计)。P4-73 起证据种子解析同用战役口径。
func ClaimFalsifySteps(ctx context.Context, store *rings.Store, taskID, claimID string) ([]rings.NodeFull, error) {
	campaignRev, err := claimCampaignRev(ctx, store, taskID, claimID)
	if err != nil {
		return nil, err
	}
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿查反证步骤: %w", err)
	}
	// 战役内交换单的执行步骤 ID(P5-02③:交卷步骤流量进归因范围)。
	submitSteps := map[string]bool{}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿查事件流: %w", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord || (campaignRev > 0 && ev.Revision <= campaignRev) {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != claimID || r.Type != rings.TestClaimRecSubmit {
			continue
		}
		if r.StepID != "" {
			submitSteps[r.StepID] = true
		}
	}
	var out []rings.NodeFull
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id == claimID {
			// 旧反证步骤(存量账本)。
		} else if !submitSteps[s.ID] {
			continue
		}
		if campaignRev > 0 && s.CreatedRev <= campaignRev {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedRev < out[j].CreatedRev
	})
	return out, nil
}

// AssembleRepro 收割定稿组装(P2-02,第五章 §1 + R17 同源纪律):确认
// 裁决同批落库的复现步骤与严重级别。确定性组装、零 LLM:
//   - Severity = 末轮反证的影响面定级(级别只跟影响面证据走,发现者
//     无级别通道);
//   - 复现步骤 = 候选池(去同型后),P4-35 ① 起最小集由定稿时模型从中
//     挑选(P4-62 A:挑选失败/空选不再倒池——见 reprodesc.go);组装侧
//     只建池与过程清单,直出渲染路径已废;
//   - P3-05b ①:交换按目标流量过滤(任务授权范围,种子资产标识);
//   - P4-08 ③ 组装侧去同型:同方法+同路径+同状态的重复交换只保留构成
//     复现流程的一条(该型首条;非成功状态自成一型不合并);反证变体
//     流量不进复现步骤;不同 URL/步骤的多步流程不受影响;
//   - P4-35 ①④:战役范围内全部交换记 Process 引用清单(原文大块头一律
//     在仓库,R18);证据包导出按 Process 全量收(审计不丢);
//   - P4-62 B(问题清单 #83)池入口两道过滤:error 交换(未获响应)与
//     交换目标 host 不在声明锚定资产(含组合链上资产与种子解析 IP,
//     P4-50 ② 口径)内的成功交换只进过程清单,不进候选池——失败请求
//     证明不了任何东西,旁支探测(如顺手 GET 无关主机)不构成复现;
//   - P4-62 C(b):直连旁路流量从工具调用记录确定性组包入池(http_probe
//     参数结构化直接组;bash curl 仅可静态解析的简单形态,复杂的跳过
//     并如实计入缺料——不编造),组包文档与镜留痕同构入仓库;
//   - P4-73 ②(问题清单 #89)种子化:seeds(确认依据解析出的证明物,
//     见 ResolveEvidenceSeeds)直进复现区——不经候选池、不受去同型/
//     池裁剪/模型挑选任何影响;与池候选(含模型补选)按账本次序合并,
//     种子对应的池内重复条目让位;返回 seedSeqs(候选 Seq 中属种子的
//     编号,模型挑选契约的"已固定步骤"输入);
//   - P4-35 ①:无步骤数上限、无异常报警(最小集天然几步)。
//
// 返回候选池记录、入池诊断(过滤/缺料计数,组原因句用,不落账)与
// 种子步骤的候选 Seq 集。只读账本(种子为工具记录时组包文档入仓库);
// 由主宰循环与确认裁决同批落账。
func AssembleRepro(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore,
	resolve rings.ScopeResolver, taskID, claimID, severity string, seeds []EvidenceSeed) (*rings.TestClaimRecord, ReproPoolDiag, []int, error) {
	diag := ReproPoolDiag{}
	byID, err := claimEvidenceSteps(ctx, store, taskID, claimID)
	if err != nil {
		return nil, diag, nil, err
	}
	// 目标流量过滤口径(P3-05b ①):任务授权范围 = 资产标识解析出的
	// 目标源(host:port 精确;无端口的 host 级授权)。RoE 的资产引用与
	// 意图拦截均锚定同一批资产节点,口径同源。
	scope, err := loadTargetScope(ctx, store, taskID)
	if err != nil {
		return nil, diag, nil, err
	}
	// 声明锚定资产闭包(P4-62 B ②):声明 about 资产(组合声明含链上
	// 事实的资产),P4-50 ② 写法粒度与种子解析 IP 口径;锚点全无主机
	// 形态时退 nil(仅按任务范围过滤,不过度收窄)。
	anchors, err := claimAnchorScope(ctx, store, taskID, claimID)
	if err != nil {
		return nil, diag, nil, err
	}
	// 每 Step 的操作方法:工具调用记录(工具名+参数截取;submit_* 回流
	// 入口不是对目标的操作,不计)。
	ops := map[string][]string{}
	// 账次序收全部范围内交换(候选池与过程清单共用一次收集;P4-35 ①:
	// 全量交换先进 Process 引用清单留档,再由去同型收出候选步骤)。
	var all []rings.ReproStep
	allOrigin := []int{} // 与 all 平行:账本次序(镜=事件序,工具=事件序+事件总数)
	allToolKey := []string{}
	process := []rings.ReproProcessRef{}
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, diag, nil, fmt.Errorf("overmind: 定稿读事件流: %w", err)
	}
	toolOriginOffset := len(evs) // 工具组包排在全部镜交换之后(与池构造次序一致)
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			return nil, diag, nil, fmt.Errorf("overmind: 定稿解码 swarm.record: %w", err)
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
	for i, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			return nil, diag, nil, fmt.Errorf("overmind: 定稿解码 mirror.record: %w", err)
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
		// B ①:error 交换(未获响应的任意形态)只进过程清单——失败请求
		// 证明不了任何东西,不进候选池(P4-62,问题清单 #83)。
		if m.Error != "" {
			diag.ErrExchanges++
			continue
		}
		// B ②:交换目标 host 不在声明锚定资产内只进过程清单(旁支探测
		// 不构成复现;锚定口径含种子解析 IP,P4-50 ②)。
		if anchors != nil && !anchorsCoverURL(anchors, resolve, m.URL) {
			diag.OffAnchor++
			continue
		}
		round, _ := s.Detail["round"].(float64)
		all = append(all, rings.ReproStep{
			Round: int(round), StepID: m.StepID, UnitID: m.UnitID,
			Short: reproStepShort(m), Desc: reproStepDesc(m),
			Method: m.Method, URL: m.URL, Status: m.Status, BlobRef: m.BlobRef,
			ExchangeID: m.ExchangeID,
		})
		allOrigin = append(allOrigin, i)
		allToolKey = append(allToolKey, "")
	}
	// C(b):直连旁路流量从工具调用记录确定性组包入候选池(镜交换优先:
	// 同型既有镜留痕时组包条目让位——镜是逐字节的捕获,组包是重构)。
	if blobs != nil {
		for i, ev := range evs {
			if ev.Kind != rings.EventSwarmRecord {
				continue
			}
			var r rings.SwarmRecord
			if err := json.Unmarshal(ev.Payload, &r); err != nil || r.Type != rings.SwarmRecToolCall || r.IsError {
				continue
			}
			s, bound := byID[r.StepID]
			if !bound || (r.ToolName != "http_probe" && r.ToolName != "bash") {
				continue
			}
			result := toolCallResult(blobs, r)
			var pkt *toolPacket
			if r.ToolName == "http_probe" {
				pkt = assembleProbePacket(taskID, r.StepID, r.UnitID, r.ToolArgs, result)
			} else {
				pkt = assembleBashPacket(taskID, r.StepID, r.UnitID, r.ToolArgs, result)
			}
			if pkt.Skipped {
				diag.ToolSkipped++ // 流量命令不可静态还原:如实缺料,不编造
				continue
			}
			if pkt.Ex == nil {
				continue // 非流量命令:不是复现原料
			}
			// 直连流量同按声明锚定口径过滤(B ②):非锚定目标的组包不进
			// 候选池(旁支探测无关于声明,镜交换与组包一视同仁)。
			if anchors != nil && !anchorsCoverURL(anchors, resolve, pkt.Ex.Request.URL) {
				continue
			}
			round, _ := s.Detail["round"].(float64)
			st, ok := writeToolPacket(blobs, taskID, pkt.Ex, int(round), r.StepID, r.UnitID)
			if !ok {
				continue
			}
			diag.ToolBuilt++
			all = append(all, st)
			allOrigin = append(allOrigin, toolOriginOffset+i)
			allToolKey = append(allToolKey, r.StepID+"|"+r.ToolName+"|"+string(r.ToolArgs))
		}
	}
	// 去同型(P4-08 ③):型 = 方法+host+路径(不含查询串)+状态,
	// 保留规则 = 该型首条(型内同状态,"首条成功交换"退化为首条——
	// 非成功状态自成一型不合并,如 401→绕过→200 的流程叙事保留);
	// 查询串值差异(防缓存参数等)不另立型,不同路径的多步流程原样保留。
	// P4-35 ①:去同型结果是候选池——最小集由定稿时模型从中挑选
	// (pickReproSteps),未选中的交换只留 Process 清单。同型竞争时镜
	// 留痕优先于工具组包(捕获优于重构,P4-62 C(b))。
	seen := map[string]int{} // 型 → all 内下标
	toolOnly := map[string]bool{}
	rec := &rings.TestClaimRecord{
		ClaimID: claimID, Type: rings.TestClaimRecRepro, Severity: severity,
		Process: process,
	}
	poolOrigin := []int{}
	poolToolKey := []string{}
	for idx, st := range all {
		key := reproShapeKey(st.Method, st.URL, st.Status)
		if pos, dup := seen[key]; dup {
			if toolOnly[key] && st.Source == "" {
				// 镜留痕到位:顶替同型的组包条目(位次保持账次序)。
				rec.Steps[pos] = st
				poolOrigin[pos] = allOrigin[idx]
				poolToolKey[pos] = ""
				delete(toolOnly, key)
			}
			continue // 同型候选只留首条;后续交换在 Process 清单留档
		}
		seen[key] = len(rec.Steps)
		if st.Source != "" {
			toolOnly[key] = true
		}
		rec.Steps = append(rec.Steps, st)
		poolOrigin = append(poolOrigin, allOrigin[idx])
		poolToolKey = append(poolToolKey, allToolKey[idx])
	}
	// P4-73 ②:种子直进——证明物不经候选池(池入口过滤与去同型都不
	// 适用),种子对应的池内同源条目让位(镜按交换 ID、组包按工具记录
	// 键),种子与池候选按账本次序合并。
	seedSteps, seedOrigin, serr := seedStepsOf(seeds, byID, blobs, taskID)
	if serr != nil {
		return nil, diag, nil, serr
	}
	seedExIDs := map[string]bool{}
	seedToolKeys := map[string]bool{}
	for _, sd := range seeds {
		if sd.Mirror != nil {
			seedExIDs[sd.Mirror.ExchangeID] = true
		}
		if sd.ToolRec != nil {
			seedToolKeys[sd.ToolRec.StepID+"|"+sd.ToolRec.ToolName+"|"+string(sd.ToolRec.ToolArgs)] = true
		}
	}
	type mergedStep struct {
		step   rings.ReproStep
		origin int
		isSeed bool
	}
	merged := make([]mergedStep, 0, len(rec.Steps)+len(seedSteps))
	for i, st := range rec.Steps {
		if seedExIDs[st.ExchangeID] || (st.Source != "" && seedToolKeys[poolToolKey[i]]) {
			continue // 种子已覆盖同源池内条目,不重复进包
		}
		merged = append(merged, mergedStep{step: st, origin: poolOrigin[i]})
	}
	for i, st := range seedSteps {
		merged = append(merged, mergedStep{step: st, origin: seedOrigin[i], isSeed: true})
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].origin < merged[j].origin })
	rec.Steps = nil
	seedSeqs := []int{}
	for _, m := range merged {
		m.step.Seq = len(rec.Steps) + 1
		if m.isSeed {
			seedSeqs = append(seedSeqs, m.step.Seq)
		}
		rec.Steps = append(rec.Steps, m.step)
	}
	// 操作方法按步骤归并(P4-08 ⑤:证据包收,面板/报告正文不显)。
	for i := range rec.Steps {
		if list := ops[rec.Steps[i].StepID]; len(list) > 0 {
			rec.Steps[i].Op = joinOps(list)
		}
	}
	return rec, diag, seedSeqs, nil
}

// seedStepsOf 种子 → 复现步骤(镜留痕按池构造同款字段;直连工具记录
// 经 P4-62 C(b) 通道组包入仓库)。返回步骤与账本次序。
func seedStepsOf(seeds []EvidenceSeed, byID map[string]rings.NodeFull, blobs *swarm.BlobStore,
	taskID string) ([]rings.ReproStep, []int, error) {
	var steps []rings.ReproStep
	var origins []int
	for _, sd := range seeds {
		round := 0
		if sd.Mirror != nil {
			if s, ok := byID[sd.Mirror.StepID]; ok {
				r, _ := s.Detail["round"].(float64)
				round = int(r)
			}
			m := *sd.Mirror
			steps = append(steps, rings.ReproStep{
				Round: round, StepID: m.StepID, UnitID: m.UnitID,
				Short: reproStepShort(m), Desc: reproStepDesc(m),
				Method: m.Method, URL: m.URL, Status: m.Status, BlobRef: m.BlobRef,
				ExchangeID: m.ExchangeID,
			})
			origins = append(origins, sd.Origin)
			continue
		}
		if sd.ToolRec == nil || blobs == nil {
			continue
		}
		r := *sd.ToolRec
		if s, ok := byID[r.StepID]; ok {
			rr, _ := s.Detail["round"].(float64)
			round = int(rr)
		}
		result := toolCallResult(blobs, r)
		var pkt *toolPacket
		if r.ToolName == "http_probe" {
			pkt = assembleProbePacket(taskID, r.StepID, r.UnitID, r.ToolArgs, result)
		} else {
			pkt = assembleBashPacket(taskID, r.StepID, r.UnitID, r.ToolArgs, result)
		}
		if pkt.Skipped || pkt.Ex == nil {
			continue // 防御:种子解析已验证可组包,组不出如实让位(不编造)
		}
		st, ok := writeToolPacket(blobs, taskID, pkt.Ex, round, r.StepID, r.UnitID)
		if !ok {
			continue
		}
		steps = append(steps, st)
		origins = append(origins, sd.Origin)
	}
	return steps, origins, nil
}

// ReproPoolDiag 候选池入池诊断(P4-62:过滤与缺料计数,组"无可用复现
// 包"原因句用;确定性事实,不落账)。
type ReproPoolDiag struct {
	ErrExchanges int // error 交换条数(只进过程清单,B ①)
	OffAnchor    int // 目标不在声明锚定资产内的成功交换条数(只进过程清单,B ②)
	ToolSkipped  int // 流量命令不可静态还原的直连调用条数(缺料,C(b))
	ToolBuilt    int // 由工具调用记录组包的候选条数(C(b))
}

// Empty 各计数是否全零。
func (d ReproPoolDiag) Empty() bool {
	return d.ErrExchanges == 0 && d.OffAnchor == 0 && d.ToolSkipped == 0 && d.ToolBuilt == 0
}

// toolCallResult 工具调用的结果文本(blob 优先,读不到退截头)。
func toolCallResult(blobs *swarm.BlobStore, r rings.SwarmRecord) string {
	if r.ResultRef != "" {
		if doc, err := blobs.Read(r.ResultRef); err == nil {
			return string(doc)
		}
	}
	return r.ResultHead
}

// claimAnchorScope 声明锚定资产的闭包(P4-62 B ②):声明 about 资产,
// 组合声明并集链上事实(derived_from→Fact)的 about 资产;P4-50 ③ 写法
// 粒度(裸域名=全端口、host:port=仅此端口、URL 等同裸域名)与 ② 种子
// 域名当前解析 IP 并入(CoversTarget 判定时解析、闭包级缓存)。RoE 摘除
// 的资产不贡献锚点。锚点全无主机形态(或声明节点缺失)返回 nil——
// 调用方仅按任务范围过滤,不过度收窄。
func claimAnchorScope(ctx context.Context, store *rings.Store, taskID, claimID string) (*rings.ScopeClosure, error) {
	claim, err := store.Node(ctx, taskID, claimID)
	if err != nil || claim.Type != rings.NodeTestClaim {
		return nil, nil
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return nil, fmt.Errorf("overmind: 定稿查判断的资产锚点: %w", err)
	}
	anchorIDs := map[string]bool{}
	collect := func(from string) {
		for _, e := range abouts {
			if e.From == from {
				anchorIDs[e.To] = true
			}
		}
	}
	collect(claimID)
	// 组合声明:链上事实的资产同入锚定集(声明由链构成,链环节的目标
	// 即声明的目标)。
	if combined, _ := claim.Detail["combined"].(bool); combined {
		derived, err := store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
		if err != nil {
			return nil, fmt.Errorf("overmind: 定稿查组合链: %w", err)
		}
		for _, e := range derived {
			if e.From != claimID {
				continue
			}
			n, err := store.Node(ctx, taskID, e.To)
			if err == nil && n.Type == rings.NodeFact {
				collect(n.ID)
			}
		}
	}
	if len(anchorIDs) == 0 {
		return nil, nil
	}
	var seeds []rings.ScopeSeed
	approved := map[string]bool{}
	for id := range anchorIDs {
		a, err := store.Node(ctx, taskID, id)
		if err != nil || a.Type != rings.NodeAsset {
			continue
		}
		// 声明锚点自身即判定的"关于哪些资产":发现的资产按已放行对待
		// (锚定语义先于来源;RoE 摘除在下方 excluded 统一去权)。
		approved[id] = true
		seeds = append(seeds, rings.ScopeSeed{ID: a.ID, Title: a.Title, Source: rings.AssetSourceOf(a.Detail)})
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	excluded := map[string]bool{}
	if roe, err := store.CurrentRoE(ctx, taskID); err == nil {
		excluded = roe.ExcludedAssetSet()
	}
	closure := rings.BuildScopeClosure(seeds, approved, excluded)
	if closure.Empty() {
		return nil, nil
	}
	return closure, nil
}

// anchorsCoverURL 交换 URL 的目标是否在锚定闭包内(解析失败/无 host =
// 不在,如实不收;端口缺省按 scheme 取默认)。
func anchorsCoverURL(c *rings.ScopeClosure, resolve rings.ScopeResolver, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if resolve == nil {
		resolve = rings.DefaultScopeResolver
	}
	return c.CoversTarget(u.Hostname(), port, resolve)
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

// ReopenClaim 重开(P2-02 起;P5-02⑦ v1.3.0 收口):已确认/被推翻/证据
// 不足 + 新反证 → 重开——reopen 记录(触发词 + 理由,历史不删,R1)+
// 状态迁移候选(回交卷待查态;旧战役证据整体失效,战役边界=reopen 的
// revision)+ 派重验步骤拉起原会话(单位带着新反证重跑证明链:坐实则
// 重交卷[submit_claim 带 claim_id],立不住则如实收尾不交)。重验步骤
// 到终态而无重交 → 收割兜底记推翻终态(checkPendingClaims,P5-02⑤
// "重验推翻")。本入口不派反证步骤(P5-01⑤ 起);面板(造物主反驳/
// 过期)与未来机器触发(新证据)共用;结论的再裁决仍归机器(R2)。
func ReopenClaim(ctx context.Context, store *rings.Store, taskID, claimID, trigger, reason, actor string) error {
	claim, err := store.Node(ctx, taskID, claimID)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s: %w", claimID, err)
	}
	if claim.Type != rings.NodeTestClaim {
		return fmt.Errorf("overmind: 节点 %s 是 %s 不是 testclaim", claimID, claim.Type)
	}
	if claim.Status != rings.TestClaimConfirmed && claim.Status != rings.TestClaimRefuted &&
		claim.Status != rings.TestClaimInsufficient {
		return fmt.Errorf("overmind: 判断 %s 当前状态 %s,只有已确认/被推翻/证据不足可重开", claimID, claim.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("overmind: 重开必须说明理由(可反驳的结论必有可审计的反驳依据)")
	}
	reason = strings.TrimSpace(reason)
	// 落账(一批事务):reopen 记录 → 状态迁移候选 → 重验步骤(队首档,
	// 原会话续跑注入新反证)。重验步骤派发失败不吞(reopen 语义含重验
	// 拉起,缺它声明悬死候选)。
	rev, err := store.CurrentRevision(ctx, taskID)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s 查图版本: %w", claimID, err)
	}
	events := []rings.Event{
		{Kind: rings.EventTestClaimRecord, Actor: actor, Payload: rings.TestClaimRecord{
			ClaimID: claimID, Type: rings.TestClaimRecReopen, Trigger: trigger, Reason: reason,
		}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: actor,
			Payload: rings.NodeStateChanged{NodeID: claimID, Status: rings.TestClaimCandidate}},
	}
	stepEvents, err := retakeStepEvents(ctx, store, taskID, *claim, "重验: "+reason, rev, true)
	if err != nil {
		return fmt.Errorf("overmind: 重开判断 %s 派重验步骤: %w", claimID, err)
	}
	events = append(events, stepEvents...)
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
