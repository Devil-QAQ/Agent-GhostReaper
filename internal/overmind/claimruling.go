package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

// 待判定判断(P4-55 审核迭代,造物主 2026-11-08 定案):动"意图"删目标
// 或动"人"杀单位 → 交主宰裁决,程序禁止连坐。本文件承载两端:
//   - 态势快照的「待判定判断」区(主宰的裁决输入:中断声明 + 资产需要
//     情况的确定性事实);
//   - 拆解回复 claim_rulings 的程序应用(continue=恢复锤炼/suspend=挂起)。

// pendingRulingClaims 待判定判断清单:两类——testing 且当前反证轮被
// 操作者终止(kill_work 送达/步骤取消,arbiter 确定性检测);pending 且
// 最新挂起记录为"目标已删除"(待新建目标或深入唤醒的裁决)。返回
// (声明,中断情报或 nil,资产锚点行)。
type pendingRuling struct {
	claim rings.NodeFull
	info  *arbiter.ClaimInterruption // testing 类必非 nil;pending 类为 nil
}

func pendingRulingClaims(ctx context.Context, store *rings.Store, taskID string) ([]pendingRuling, error) {
	// 挂起记录扫描:最新 suspend 理由 per claim(latest-wins,事件序)。
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("overmind: 待判定判断读事件流: %w", err)
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
	var out []pendingRuling
	testing, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimTesting)
	if err != nil {
		return nil, fmt.Errorf("overmind: 待判定判断查反证中: %w", err)
	}
	for _, c := range testing {
		info, err := arbiter.InterruptedClaim(ctx, store, taskID, c.ID)
		if err != nil {
			return nil, err
		}
		if info != nil && info.OperatorKilled {
			out = append(out, pendingRuling{claim: c, info: info})
		}
	}
	pending, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimPending)
	if err != nil {
		return nil, fmt.Errorf("overmind: 待判定判断查挂起: %w", err)
	}
	for _, c := range pending {
		if latestSuspend[c.ID] == SuspendReasonGoalDeleted {
			out = append(out, pendingRuling{claim: c, info: nil})
		}
	}
	return out, nil
}

// assetNeedFacts 资产需要情况的确定性事实行(裁决依据):资产仍被哪些
// 进行中目标覆盖(step.goal_id 归属 + covers 边)。返回 assetID → 事实行。
func assetNeedFacts(goals, steps []rings.NodeFull, covers []rings.EdgeView) map[string]string {
	activeGoals := map[string]rings.NodeFull{}
	for _, g := range goals {
		if g.Status == GoalStatusActive {
			activeGoals[g.ID] = g
		}
	}
	stepGoal := map[string]string{}
	for _, s := range steps {
		if gid, _ := s.Detail["goal_id"].(string); gid != "" {
			stepGoal[s.ID] = gid
		}
	}
	facts := map[string]string{}
	for _, e := range covers {
		gid := stepGoal[e.From]
		if gid == "" {
			continue
		}
		g, ok := activeGoals[gid]
		if !ok {
			continue
		}
		line := fmt.Sprintf("%s(%s)", g.ID, clipLine(g.Title, 40))
		if facts[e.To] == "" {
			facts[e.To] = line
		} else if !strings.Contains(facts[e.To], g.ID) {
			facts[e.To] += "、" + line
		}
	}
	return facts
}

// pendingRulingBriefs 态势快照「待判定判断」区的内容行(确定性事实,
// 零 LLM):testing 且反证被操作者终止的声明(中断原因 + 资产需要
// 情况 + 判定指引);pending 且挂起理由为"目标已删除"的声明(资产
// 需要情况 + 唤醒指引)。covers 由调用方传入(快照已取则复用)。
func (l *Loop) pendingRulingBriefs(ctx context.Context, goals, steps []rings.NodeFull, abouts []rings.EdgeView) ([]string, error) {
	rulings, err := pendingRulingClaims(ctx, l.Store, l.TaskID)
	if err != nil || len(rulings) == 0 {
		return nil, err
	}
	covers, err := l.Store.Edges(ctx, l.TaskID, rings.EdgeCovers)
	if err != nil {
		return nil, fmt.Errorf("overmind: 待判定判断查覆盖: %w", err)
	}
	facts := assetNeedFacts(goals, steps, covers)
	assetsOf := map[string][]string{}
	for _, e := range abouts {
		assetsOf[e.From] = append(assetsOf[e.From], e.To)
	}
	var lines []string
	for _, r := range rulings {
		var b strings.Builder
		fmt.Fprintf(&b, "  - %s [%s] 「%s」", r.claim.ID, r.claim.Status, clipLine(r.claim.Title, 60))
		if r.info != nil {
			fmt.Fprintf(&b, " 反证被操作者终止(%s)", r.info.Reason)
		} else {
			b.WriteString(" 挂起(目标已删除)")
		}
		if as := assetsOf[r.claim.ID]; len(as) > 0 {
			for _, a := range as {
				if facts[a] != "" {
					fmt.Fprintf(&b, ";资产 %s 仍被目标 %s 覆盖", a, facts[a])
				} else {
					fmt.Fprintf(&b, ";资产 %s 已无进行中目标覆盖", a)
				}
			}
		}
		if r.info != nil {
			b.WriteString(";判定:continue=恢复反证(程序重派)或 suspend=挂起(理由写明依据)")
		} else {
			b.WriteString(";判定:continue=重开反证(须写依据),或维持挂起(不输出即可)")
		}
		lines = append(lines, b.String())
	}
	return lines, nil
}

// applyClaimRuling 应用一条待判定判断的裁决(拆解回复 claim_rulings)。
// 程序校验形状与状态,语义归主宰(理由语义不代替判断);单条被拒不
// 拖垮其余,拒因回流简报(同 cancelQueued/applyReportExclusion 纪律)。
func (l *Loop) applyClaimRuling(ctx context.Context, p claimRulingPlan, evaluatedRev int64) error {
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
	reason := strings.TrimSpace(p.Reason)
	switch p.Action {
	case "continue":
		if n.Status == rings.TestClaimTesting {
			info, err := arbiter.InterruptedClaim(ctx, l.Store, l.TaskID, p.ClaimID)
			if err != nil {
				return err
			}
			if info == nil || !info.OperatorKilled {
				return fmt.Errorf("判断 %s 当前反证轮不是操作者终止形态,无可恢复的中断", p.ClaimID)
			}
			if info.RedispatchesUsed >= arbiter.MaxFalsifyRedispatch {
				return fmt.Errorf("判断 %s 第 %d 轮重派额度已用尽(%d 次),继续锤炼应改走重开", p.ClaimID, roundNumberOf(info.Round), info.RedispatchesUsed)
			}
			return redispatchFalsifyForRuling(ctx, l.Store, l.TaskID, *n, info, reason)
		}
		if n.Status == rings.TestClaimPending {
			// 挂起唤醒(目标已删除类;新建目标/深入触发本裁决):经 reopen
			// 重开反证,竞争解释全部复活、轮次全局续号。
			if reason == "" {
				return fmt.Errorf("判断 %s 恢复锤炼须说明依据(新目标/深入指令/新证据)", p.ClaimID)
			}
			if err := ReopenClaim(ctx, l.Store, l.TaskID, p.ClaimID, rings.ReopenManual,
				"主宰判定恢复锤炼:"+reason, ActorOvermind); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("判断 %s 当前状态 %s,continue 只适用于反证中被操作者终止或挂起的判断", p.ClaimID, n.Status)
	case "suspend":
		if reason == "" {
			return fmt.Errorf("判断 %s 挂起须说明理由(目标已删除且资产无人需要时用「目标已删除」)", p.ClaimID)
		}
		if n.Status != rings.TestClaimTesting {
			return fmt.Errorf("判断 %s 当前状态 %s,只有锤炼中的判断可挂起", p.ClaimID, n.Status)
		}
		_, err := l.Store.Append(ctx, l.TaskID,
			rings.Event{Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
				Payload: rings.NodeStateChanged{NodeID: p.ClaimID, Status: rings.TestClaimPending, Note: reason}},
			rings.Event{Kind: rings.EventTestClaimRecord, Actor: ActorOvermind,
				Payload: rings.TestClaimRecord{ClaimID: p.ClaimID, Type: rings.TestClaimRecSuspend, Reason: reason}},
		)
		return err
	default:
		return fmt.Errorf("判断 %s 判定动作 %q 不在词表(continue|suspend)", p.ClaimID, p.Action)
	}
}

// redispatchFalsifyForRuling 主宰裁决 continue 后的重派:与系统中断自动
// 重派同一构造器与轮次口径,重派计入该轮额度;原因入账带裁决依据。
func redispatchFalsifyForRuling(ctx context.Context, store *rings.Store, taskID string, c rings.NodeFull, info *arbiter.ClaimInterruption, reason string) error {
	derived, err := store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
	if err != nil {
		return fmt.Errorf("overmind: 判定重派查血缘: %w", err)
	}
	abouts, err := store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		return fmt.Errorf("overmind: 判定重派查锚点: %w", err)
	}
	discoverStep, assets, ok := claimDispatchContext(ctx, store, taskID, c, derived, abouts)
	if !ok {
		return fmt.Errorf("判断 %s 无发现步骤血缘,无法继承意图分级", c.ID)
	}
	rawAlts := []any(nil)
	if interrupted, nerr := store.Node(ctx, taskID, info.StepID); nerr == nil {
		rawAlts, _ = interrupted.Detail["alternatives"].([]any)
	}
	rd := "主宰判定恢复锤炼"
	if reason != "" {
		rd += ":" + clipLine(reason, 80)
	}
	events, err := falsifyRoundEvents(ctx, store, taskID, c, info.Round, rawAlts, discoverStep, assets,
		fmt.Sprintf("反证(第 %d 轮·重派): %s", roundNumberOf(info.Round), c.Title),
		map[string]any{
			"redispatch":        info.RedispatchesUsed + 1,
			"redispatch_of":     info.StepID,
			"redispatch_reason": info.Reason + ";" + rd,
		})
	if err != nil {
		return err
	}
	// 声明已在锤炼中(testing),无状态迁移——与系统中断自动重派同形。
	_, err = store.Append(ctx, taskID, events...)
	return err
}
