// overmind 目标操作面(P4-54,问题清单 #78＋#73⑧牧者部分;造物主定
// 宗旨:牧者是最高权限的,可以改变整个任务的任何动作、资产、目标等)。
// 目标登记(SeedGoal)与标记达成之外补齐修改/删除;方向优先级直接落账
// (node.amended detail.priority,队列比较器读投影立即生效);停方向
// 聚焦与目标删除共用同一停向引擎(取消排队＋终止在跑＋反证声明挂起)。
// 全部走账本受控入口,面板与牧者两通道同源(R7 无旁路)。
package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
	"ghostreaper/internal/swarm"
)

// GoalStatusDiscarded 目标删除态(P4-54):该方向不要了——非终局"完成",
// 是操作者撤销方向。图上保留节点与历史(R1 只追加),呈现"已删除";
// active 查询自然排除,拆解不再归属、仲裁不再判定、新意图不再绑定。
const GoalStatusDiscarded = "discarded"

// SuspendReasonGoalDeleted 目标删除且资产无人需要时,主宰裁决挂起的
// 理由(P4-55 审核迭代,逐字;新建目标或操作者深入可再唤醒)。
const SuspendReasonGoalDeleted = "目标已删除"

// SuspendReasonNarrowed 停方向取消反证的挂起理由(逐字入账;重新聚焦
// 经 reopen 唤醒,与 P4-55 免疫规则衔接——操作者显式取消的步骤免除
// 自动重派)。
const SuspendReasonNarrowed = "方向被收窄"

var (
	// ErrFocusOutside 方向聚焦中的派发拒收(P4-54 停聚焦的派发守卫):
	// 聚焦目标之外的意图不再派发。收割侧(HarvestClaims)据此跳过而非
	// 报错——聚焦期间其他方向的声明停在原地,重新聚焦自然续上。
	ErrFocusOutside = errors.New("overmind: 方向聚焦中,只派发聚焦方向的意图")
	// ErrGoalDiscarded 已删除目标不再派发(P4-54 目标删除:该方向后续
	// 零新派)。反证步骤若绑定已删除目标,同样拒收。
	ErrGoalDiscarded = errors.New("overmind: 目标已删除,该方向不再派发")
)

// FocusGoalID 当前聚焦方向(P4-54):active 目标里 detail.focus=true 的
// 目标 ID;无聚焦返回空。读投影小查询(goal 数量级小),派发守卫与
// 拆解快照共用;非 active(已达成/已删除)目标不构成聚焦。
func FocusGoalID(ctx context.Context, store *rings.Store, taskID string) string {
	goals, err := store.NodesByType(ctx, taskID, rings.NodeGoal, GoalStatusActive)
	if err != nil {
		return ""
	}
	for _, g := range goals {
		if f, _ := g.Detail["focus"].(bool); f {
			return g.ID
		}
	}
	return ""
}

// AmendGoal 修改目标(P4-54,面板与牧者同源):标题整体覆盖(非空时)、
// 完成条件重新冻结(仲裁契约②判定标准变更走新事件——受限结构入口
// 校验,node.amended 落账,仲裁者读最后显式冻结值)、聚焦标记读写。
// predicate 非nil 时校验受限结构;已删除目标不可改(方向已撤销)。
// 唤醒主宰由调用方执行(修改通知主宰并可复活任务,宪法既有)。
func AmendGoal(ctx context.Context, store *rings.Store, taskID, goalID string, title *string, predicate map[string]any, focus *bool, note, actor string) error {
	g, err := store.Node(ctx, taskID, goalID)
	if err != nil {
		return fmt.Errorf("overmind: 改目标 %s: %w", goalID, err)
	}
	if g.Type != rings.NodeGoal {
		return fmt.Errorf("overmind: 节点 %s 是 %s 不是目标", goalID, g.Type)
	}
	if g.Status == GoalStatusDiscarded {
		return fmt.Errorf("overmind: 目标 %s 已删除,不可修改", goalID)
	}
	detail := map[string]any{}
	if predicate != nil {
		if _, err := arbiter.ParsePredicate(predicate); err != nil {
			return fmt.Errorf("overmind: 目标 %s 完成条件非法,修改被拒: %w", goalID, err)
		}
		detail["predicate"] = predicate
	}
	if focus != nil {
		if g.Status != GoalStatusActive {
			return fmt.Errorf("overmind: 目标 %s 状态 %s,聚焦标记只对进行中目标生效", goalID, g.Status)
		}
		detail["focus"] = *focus
	}
	if len(detail) == 0 && (title == nil || strings.TrimSpace(*title) == g.Title) {
		return fmt.Errorf("overmind: 修改目标 %s 未携带任何变更", goalID)
	}
	_, err = store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeAmended, Actor: actor,
		Payload: rings.NodeAmended{NodeID: goalID, Title: title, Detail: detail, Note: note},
	})
	return err
}

// GoalDirectionImpact 方向停向影响预览(纯读):该目标名下在跑单位与
// 排队步骤(含等前置)——"将终止 N 个在途"的数据源,删除确认弹窗与
// 牧者执行前告知共用。
type GoalDirectionImpact struct {
	Units  []swarm.ScopeHit `json:"units"`
	Queued []swarm.ScopeHit `json:"queued"`
}

// DirectionImpactOf 方向影响预览实现(GoalDirectionImpact 的求值)。
func DirectionImpactOf(ctx context.Context, store *rings.Store, taskID, goalID string) (*GoalDirectionImpact, error) {
	steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
	if err != nil {
		return nil, fmt.Errorf("overmind: 方向影响查步骤: %w", err)
	}
	out := &GoalDirectionImpact{Units: []swarm.ScopeHit{}, Queued: []swarm.ScopeHit{}}
	for _, s := range steps {
		if gid, _ := s.Detail["goal_id"].(string); gid != goalID {
			continue
		}
		hit := swarm.ScopeHit{StepID: s.ID, Title: s.Title}
		switch s.Status {
		case "running":
			out.Units = append(out.Units, hit)
		case "dispatched":
			out.Queued = append(out.Queued, hit)
		}
	}
	sort.Slice(out.Units, func(i, j int) bool { return out.Units[i].StepID < out.Units[j].StepID })
	sort.Slice(out.Queued, func(i, j int) bool { return out.Queued[i].StepID < out.Queued[j].StepID })
	return out, nil
}

// DeleteGoal 删除目标(P4-54,面板与牧者对等;P4-55 审核迭代修订):该
// 方向不要了——同停聚焦力度,未领取排队步骤取消留痕、在跑单位立即终止
// (调用方执行前告知"将终止 N 个在途");目标转已删除态(active 查询
// 自然排除,后续零新派:拆解归属与反证绑定的派发守卫同拒)。受影响
// 的反证声明程序不再自动挂起(禁止连坐):被杀/被取消的反证轮成
// "待裁决中断",声明留锤炼中,目标删除事件与资产需要情况进态势快照,
// 去留由主宰下轮裁决(仍需要→continue 继续锤炼;无人需要→suspend
// 挂起,理由"目标已删除";新建目标或深入可再唤醒)。账本只追加,
// 节点与历史保留(R1)。
func DeleteGoal(ctx context.Context, store *rings.Store, helm *swarm.Helm, taskID, goalID, reason, actor string) (*DirectionStopResult, error) {
	g, err := store.Node(ctx, taskID, goalID)
	if err != nil {
		return nil, fmt.Errorf("overmind: 删目标 %s: %w", goalID, err)
	}
	if g.Type != rings.NodeGoal {
		return nil, fmt.Errorf("overmind: 节点 %s 是 %s 不是目标", goalID, g.Type)
	}
	if g.Status == GoalStatusDiscarded {
		return nil, fmt.Errorf("overmind: 目标 %s 已删除,无需再删", goalID)
	}
	note := strings.TrimSpace(reason)
	if note == "" {
		note = "删除目标 " + g.Title
	}
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: actor,
		Payload: rings.NodeStateChanged{NodeID: goalID, Status: GoalStatusDiscarded, Note: note},
	}); err != nil {
		return nil, err
	}
	res, err := stopDirection(ctx, store, helm, taskID, func(s rings.NodeFull) bool {
		gid, _ := s.Detail["goal_id"].(string)
		return gid == goalID
	}, actor, "目标已删除:"+g.Title, false)
	if err != nil {
		return res, err
	}
	runlog.Infof("目标删除: %s %s(终止在途单位 %d 个,取消排队步骤 %d 个;受影响判断交主宰下轮判定)",
		taskID, goalID, len(res.KilledUnits), len(res.AbortedSteps))
	return res, nil
}

// FocusGoal 停方向聚焦(P4-54,"其他都停掉只做 X"):X 转 focus 态(原
// 聚焦目标如有则解除)、非 X 方向未领取排队步骤取消留痕、在跑单位立即
// 终止、被取消反证的声明转挂起;派发守卫此后零新派其他方向(急活豁免
// ——操作者指令高于聚焦纪律)。执行前告知"将终止 N 个在途"由调用方
// 用 GoalDirectionImpact 全向预览承担(聚焦停的是全部非 X 方向)。
func FocusGoal(ctx context.Context, store *rings.Store, helm *swarm.Helm, taskID, goalID, actor string) (*DirectionStopResult, error) {
	g, err := store.Node(ctx, taskID, goalID)
	if err != nil {
		return nil, fmt.Errorf("overmind: 聚焦 %s: %w", goalID, err)
	}
	if g.Type != rings.NodeGoal {
		return nil, fmt.Errorf("overmind: 节点 %s 是 %s 不是目标", goalID, g.Type)
	}
	if g.Status != GoalStatusActive {
		return nil, fmt.Errorf("overmind: 目标 %s 状态 %s,聚焦只对进行中目标生效", goalID, g.Status)
	}
	var batch []rings.Event
	if old := FocusGoalID(ctx, store, taskID); old != "" && old != goalID {
		f := false
		batch = append(batch, rings.Event{Kind: rings.EventNodeAmended, Actor: actor,
			Payload: rings.NodeAmended{NodeID: old, Detail: map[string]any{"focus": f}, Note: "方向聚焦切换,解除原聚焦"}})
	}
	t := true
	batch = append(batch, rings.Event{Kind: rings.EventNodeAmended, Actor: actor,
		Payload: rings.NodeAmended{NodeID: goalID, Detail: map[string]any{"focus": t}, Note: "停方向聚焦:只做该方向"}})
	if _, err := store.Append(ctx, taskID, batch...); err != nil {
		return nil, err
	}
	res, err := stopDirection(ctx, store, helm, taskID, func(s rings.NodeFull) bool {
		gid, _ := s.Detail["goal_id"].(string)
		return gid != goalID
	}, actor, "停方向聚焦:只做 "+g.Title, true)
	if err != nil {
		return res, err
	}
	// 重新聚焦唤醒(P4-54:挂起声明"重新聚焦时唤醒再派"):此前因方向
	// 收窄挂起、且属新聚焦方向的声明,经 reopen 回反证(与新聚焦方向
	// 无关或方向已删的保持挂起)。
	woken, err := resumeSuspendedClaims(ctx, store, taskID, goalID, actor)
	if err != nil {
		runlog.Warnf("聚焦唤醒挂起判断失败: %s %v", taskID, err)
	}
	res.ResumedClaims = woken
	runlog.Infof("停方向聚焦: %s 只做 %s(终止在途单位 %d 个,取消排队步骤 %d 个,挂起判断 %d 个,唤醒判断 %d 个)",
		taskID, goalID, len(res.KilledUnits), len(res.AbortedSteps), len(res.SuspendedClaims), woken)
	return res, nil
}

// UnfocusGoal 解除聚焦(P4-54,"恢复各方向"):清掉聚焦标记;因方向收窄
// 挂起的声明(其方向目标仍在)全部经 reopen 唤醒;不动在途(解除聚焦
// 不构成止损)。无聚焦时如实零变更。
func UnfocusGoal(ctx context.Context, store *rings.Store, taskID, actor string) (resumed int, err error) {
	cur := FocusGoalID(ctx, store, taskID)
	if cur == "" {
		return 0, nil
	}
	f := false
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventNodeAmended, Actor: actor,
		Payload: rings.NodeAmended{NodeID: cur, Detail: map[string]any{"focus": f}, Note: "解除方向聚焦"},
	}); err != nil {
		return 0, err
	}
	resumed, err = resumeSuspendedClaims(ctx, store, taskID, "", actor)
	if err != nil {
		return resumed, err
	}
	runlog.Infof("解除方向聚焦: %s(唤醒挂起判断 %d 个)", taskID, resumed)
	return resumed, nil
}

// SetStepsPriority 方向优先级调整(P4-54,面板与牧者同源):步骤优先级
// 直接落账(node.amended detail.priority)——队列比较器(StepLaunchLess)
// 读投影,领取次序立即按新优先级。只调未领取的排队步骤(在跑单位已领
// 名额,调了不生效,如实不计入);priority 空串=清除回缺省 normal。
func SetStepsPriority(ctx context.Context, store *rings.Store, taskID string, stepIDs []string, priority, actor string) (changed []string, err error) {
	if priority != "" && PriorityRank(priority) <= 0 {
		return nil, fmt.Errorf("overmind: 优先级 %q 非法(须为 high|normal|low 之一,空=回默认)", priority)
	}
	var batch []rings.Event
	for _, id := range stepIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		s, nerr := store.Node(ctx, taskID, id)
		if nerr != nil {
			return nil, fmt.Errorf("overmind: 调优先级: %w", nerr)
		}
		if s.Type != rings.NodeStep {
			return nil, fmt.Errorf("overmind: 节点 %s 是 %s 不是步骤", id, s.Type)
		}
		if s.Status != "dispatched" {
			continue // 已领取/已终态:调了不生效,如实不计入
		}
		detail := map[string]any{}
		if priority == "" {
			detail["priority"] = nil // 显式 null = 移除键,回缺省 normal
		} else {
			detail["priority"] = priority
		}
		batch = append(batch, rings.Event{
			Kind: rings.EventNodeAmended, Actor: actor,
			Payload: rings.NodeAmended{NodeID: id, Detail: detail, Note: "优先级调整为 " + defaultPriorityWord(priority)},
		})
		changed = append(changed, id)
	}
	if len(batch) == 0 {
		return []string{}, nil
	}
	if _, err := store.Append(ctx, taskID, batch...); err != nil {
		return nil, err
	}
	return changed, nil
}

func defaultPriorityWord(p string) string {
	if p == "" {
		return "默认"
	}
	return p
}

// DirectionStopResult 一次停向的结果(回执与账本对账的共用数据面)。
type DirectionStopResult struct {
	KilledUnits     []swarm.ScopeHit `json:"killed_units"`     // 终止的在跑单位
	AbortedSteps    []swarm.ScopeHit `json:"aborted_steps"`    // 取消的排队步骤
	SuspendedClaims []string         `json:"suspended_claims"` // 因取消反证而挂起的声明 ID
	ResumedClaims   int              `json:"resumed_claims"`   // 重新聚焦时唤醒的声明数
}

// stopDirection 停向引擎(停聚焦与目标删除共用,与摘除止损同力度):
// victims 判定的步骤——dispatched 整步取消留痕(Note 带停向理由,交主宰
// 重排时派发守卫已拒,重排只会换方向);running 走舵杆 kill(与熔断同款
// 真中断)。被取消/被杀的反证步骤(testclaim_id)其声明的处置按调用方
// 分道(P4-55 审核迭代,造物主 2026-11-08 定案:动"意图"删目标→交主宰
// 裁决,程序禁止连坐;临时性动作聚焦→挂起保留):
//   - suspendClaims=true(停方向聚焦):声明转挂起(pending + suspend
//     记录,理由"方向被收窄",不自动重派——重新聚焦经 reopen 唤醒);
//   - suspendClaims=false(删除目标):程序不动任何声明状态——被杀/
//     被取消的反证轮成为"待裁决中断",声明留锤炼中,目标删除事件与
//     资产需要情况进态势快照,去留由主宰下轮裁决(仍需要→continue
//     继续锤炼;无人需要→suspend 挂起,理由"目标已删除")。
//
// 竞态防护与 StopLossForAssets 同款:批量取消走 AppendAtRevision、
// 在跑 kill 由 KillWork 现读校验、≤3 轮收敛复查。
func stopDirection(ctx context.Context, store *rings.Store, helm *swarm.Helm, taskID string,
	victims func(rings.NodeFull) bool, actor, reason string, suspendClaims bool) (*DirectionStopResult, error) {
	res := &DirectionStopResult{
		KilledUnits: []swarm.ScopeHit{}, AbortedSteps: []swarm.ScopeHit{}, SuspendedClaims: []string{},
	}
	killedSet := map[string]bool{}
	claimSeen := map[string]bool{}
	for pass := 0; pass < 3; pass++ {
		if pass > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		rev, err := store.CurrentRevision(ctx, taskID)
		if err != nil {
			return res, fmt.Errorf("overmind: 停向查图版本: %w", err)
		}
		steps, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
		if err != nil {
			return res, fmt.Errorf("overmind: 停向查步骤: %w", err)
		}
		var cancelEvs []rings.Event
		var cancelHits []swarm.ScopeHit
		var running []swarm.ScopeHit
		for _, s := range steps {
			if !victims(s) {
				continue
			}
			switch s.Status {
			case "dispatched":
				cancelEvs = append(cancelEvs, rings.Event{
					Kind: rings.EventNodeStateChanged, Actor: actor,
					Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "cancelled", Note: reason},
				})
				cancelHits = append(cancelHits, swarm.ScopeHit{StepID: s.ID, Title: s.Title})
				// 声明挂起仅聚焦语义(suspendClaims):删除目标不动声明——
				// 被取消的反证轮成"待裁决中断"交主宰(P4-55 审核迭代)。
				if suspendClaims {
					if cid, _ := s.Detail["testclaim_id"].(string); cid != "" && !claimSeen[cid] {
						if claim, cerr := store.Node(ctx, taskID, cid); cerr == nil &&
							claim.Type == rings.NodeTestClaim && claim.Status == rings.TestClaimTesting {
							cancelEvs = append(cancelEvs,
								rings.Event{Kind: rings.EventNodeStateChanged, Actor: actor,
									Payload: rings.NodeStateChanged{NodeID: cid, Status: rings.TestClaimPending, Note: SuspendReasonNarrowed}},
								rings.Event{Kind: rings.EventTestClaimRecord, Actor: actor,
									Payload: rings.TestClaimRecord{ClaimID: cid, Type: rings.TestClaimRecSuspend, Reason: SuspendReasonNarrowed}},
							)
							res.SuspendedClaims = append(res.SuspendedClaims, cid)
						}
						claimSeen[cid] = true
					}
				}
			case "running":
				if !killedSet[s.ID] {
					running = append(running, swarm.ScopeHit{StepID: s.ID, Title: s.Title})
				}
				// 在跑反证被杀的声明处置同上分道(聚焦挂起/删除交主宰)。
				if suspendClaims {
					if cid, _ := s.Detail["testclaim_id"].(string); cid != "" && !claimSeen[cid] {
						if claim, cerr := store.Node(ctx, taskID, cid); cerr == nil &&
							claim.Type == rings.NodeTestClaim && claim.Status == rings.TestClaimTesting {
							cancelEvs = append(cancelEvs,
								rings.Event{Kind: rings.EventNodeStateChanged, Actor: actor,
									Payload: rings.NodeStateChanged{NodeID: cid, Status: rings.TestClaimPending, Note: SuspendReasonNarrowed}},
								rings.Event{Kind: rings.EventTestClaimRecord, Actor: actor,
									Payload: rings.TestClaimRecord{ClaimID: cid, Type: rings.TestClaimRecSuspend, Reason: SuspendReasonNarrowed}},
							)
							res.SuspendedClaims = append(res.SuspendedClaims, cid)
						}
						claimSeen[cid] = true
					}
				}
			}
		}
		if len(cancelEvs) > 0 {
			if _, err := store.AppendAtRevision(ctx, taskID, rev, cancelEvs...); err != nil {
				if errors.Is(err, rings.ErrStaleRevision) {
					continue // 领取竞态:整批重读(该步骤已成在跑,转 kill 分支)
				}
				return res, fmt.Errorf("overmind: 停向取消排队步骤: %w", err)
			}
			res.AbortedSteps = append(res.AbortedSteps, cancelHits...)
		}
		if helm != nil {
			for _, h := range running {
				if _, kerr := helm.KillWork(ctx, taskID, h.StepID, reason, actor); kerr != nil {
					runlog.Infof("停向: 步骤 %s 无需终止(%v)", h.StepID, kerr)
					continue
				}
				killedSet[h.StepID] = true
				res.KilledUnits = append(res.KilledUnits, h)
			}
		}
		// 收敛复查:无遗留(在跑未杀/排队未取消)即完成。
		steps2, err := store.NodesByType(ctx, taskID, rings.NodeStep, "")
		if err != nil {
			return res, err
		}
		left := false
		for _, s := range steps2 {
			if !victims(s) {
				continue
			}
			if s.Status == "dispatched" || (s.Status == "running" && !killedSet[s.ID]) {
				left = true
				break
			}
		}
		if !left {
			break
		}
	}
	return res, nil
}

// resumeSuspendedClaims 重新聚焦唤醒(P4-54):当前挂起(pending)且最新
// 挂起理由为"方向被收窄"的声明经 reopen 回反证。goalID 非空 = 聚焦
// 切换场景,只唤醒属该方向的声明;空 = 解除聚焦,唤醒全部(方向目标
// 已删除的除外——该方向不存在了,保持挂起)。唤醒经 ReopenClaim 正常
// 派反证(派发守卫此刻已放行)。
func resumeSuspendedClaims(ctx context.Context, store *rings.Store, taskID, goalID, actor string) (int, error) {
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0, err
	}
	latestSuspend := map[string]string{}
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.Type != rings.TestClaimRecSuspend {
			continue
		}
		latestSuspend[r.ClaimID] = r.Reason
	}
	if len(latestSuspend) == 0 {
		return 0, nil
	}
	claims, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimPending)
	if err != nil {
		return 0, err
	}
	derived, err := store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
	if err != nil {
		return 0, err
	}
	discoverGoal := map[string]string{}
	for _, e := range derived {
		if discoverGoal[e.From] == "" {
			if n, nerr := store.Node(ctx, taskID, e.To); nerr == nil && n.Type == rings.NodeStep {
				if gid, _ := n.Detail["goal_id"].(string); gid != "" {
					discoverGoal[e.From] = gid
				}
			}
		}
	}
	n := 0
	for _, c := range claims {
		if latestSuspend[c.ID] != SuspendReasonNarrowed {
			continue
		}
		g := discoverGoal[c.ID]
		if g == "" && goalID != "" {
			// 无方向归属的声明按不属于任何聚焦方向处理:切换聚焦时保持
			// 挂起,解除聚焦(全向恢复)时唤醒。
			continue
		}
		if g != "" {
			gn, gerr := store.Node(ctx, taskID, g)
			if gerr == nil && gn.Type == rings.NodeGoal && gn.Status == GoalStatusDiscarded {
				continue // 方向已删除:保持挂起
			}
			if goalID != "" && g != goalID {
				continue // 聚焦切换:只唤醒新聚焦方向
			}
		}
		if err := ReopenClaim(ctx, store, taskID, c.ID, rings.ReopenManual, "方向恢复,重新反证", actor); err != nil {
			runlog.Infof("聚焦唤醒判断 %s 未成(%v)", c.ID, err)
			continue
		}
		n++
	}
	return n, nil
}
