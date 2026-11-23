package manager

import (
	"context"
	"fmt"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 任务终局清点(P4-43,问题清单 #63;P5-02⑤ 状态机适配):任务落终态
// (stopped|completed)后紧邻执行的一道确定性收场。终局此前只收场了
// "有人干的"(在跑单位随任务 ctx 中止记账),无人收场"没人领的"与
// 在途判断——无主排队步骤永远 dispatched、候选判断永远"候选"。
// 清点把两者如实收场:
//   - 无主排队步骤(全部 dispatched,含等前置等待中)批量记 aborted,
//     原因"任务收场"随事件留痕(取消留痕不删节点,R1);
//   - 机器三查未完成的候选判断(含补考窗口)批量落证据不足终态并带
//     理由(R4 精神:理由必填、可被新证据重开唤醒),诚实呈现
//     "没验证完"。
//
// 幂等:已终态步骤/非候选判断不动,无在途对象零落笔。只挂终态
// (挂点在 recordWith 的 stopped|completed 分支)——冬眠、暂停等非终态
// 一律不触发(P4-33 后冬眠任务同样无活 goroutine,在途状态保留待唤醒
// 续跑,勿误清)。清点失败如实报错,不阻塞终态本身(终态事件已落账)。
// 存量旧账本的 testing/awaiting_round/pending 判断不再清点(停原状态,
// 升级删除路线 P5-03④)。

// sweepStepNote 无主排队步骤的收场原因(事件 Note 留痕;何时何因收场
// 走事件流审计,投影不携带)。
const sweepStepNote = "任务收场"

// sweepPendingReason 候选判断落证据不足的机器理由:按终态如实措辞
// (终止/完成各自陈述;reopen 唤醒路径不受影响,证据不足可被重开)。
func sweepPendingReason(terminal string) string {
	if terminal == StatusCompleted {
		return "任务完成时中断，证据未立，可被唤醒"
	}
	return "任务终止时中断，证据未立，可被唤醒"
}

// sweepAtTerminal 终局清点本体:单批事务落账(步骤中止 + 判断落证据
// 不足),无在途对象零落笔(幂等)。事件序紧邻终态生命周期事件(挂点
// 保证)。P4-51:范围等待中(scope_wait)的步骤一并收场——任务终局无
// 裁决通道(待决卡冻结),等待单位如实落中止(与无主排队同款"任务
// 收场");遗留的未决范围卡同步按拒绝结案(与弹门票 ExpireTickets 同
// 纪律,红点不留僵尸)。P5-05:挂等让位停等态(gate_wait/pause_wait/
// model_wait)同族收场——挂等单位已收场让位,终局无解除通道。
func (m *Manager) sweepAtTerminal(taskID, terminal string) error {
	ctx := context.Background()
	dispatched, err := m.cfg.Store.NodesByType(ctx, taskID, rings.NodeStep, "dispatched")
	if err != nil {
		return fmt.Errorf("manager: 终局清点查无主排队步骤: %w", err)
	}
	waiting, err := m.cfg.Store.NodesByType(ctx, taskID, rings.NodeStep, rings.StepScopeWait)
	if err != nil {
		return fmt.Errorf("manager: 终局清点查等待批准步骤: %w", err)
	}
	// P5-05 挂等让位停等态(弹门/攻击卡/人工暂停/模型故障)。
	for _, st := range []string{rings.StepGateWait, rings.StepPauseWait, rings.StepModelWait} {
		ws, werr := m.cfg.Store.NodesByType(ctx, taskID, rings.NodeStep, st)
		if werr != nil {
			return fmt.Errorf("manager: 终局清点查挂等让位步骤(%s): %w", st, werr)
		}
		waiting = append(waiting, ws...)
	}
	candidates, err := m.cfg.Store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimCandidate)
	if err != nil {
		return fmt.Errorf("manager: 终局清点查在途判断: %w", err)
	}
	// 未决范围卡(approval_records 投影按 RequestID 取最新,恰为
	// scope_wait 即未决)。
	scopeCards := 0
	events := make([]rings.Event, 0, len(dispatched)+2*len(candidates)+4)
	if rows, err := m.cfg.Store.ApprovalRows(ctx, taskID); err == nil {
		latestType := map[string]string{} // RequestID → 最新记录类型
		for _, r := range rows {
			latestType[r.RequestID] = r.Type // rev 升序,后见覆盖前见
		}
		for _, r := range rows {
			if r.Type != rings.ApprovalRecScopeWait || latestType[r.RequestID] != rings.ApprovalRecScopeWait {
				continue
			}
			delete(latestType, r.RequestID) // 同卡多笔卡面只结一张
			card := sweepScopeCardDeny(r)
			events = append(events, rings.Event{
				Kind: rings.EventApprovalRecord, Actor: "manager", Payload: card,
			})
			scopeCards++
		}
	}
	dispatched = append(dispatched, waiting...)
	if len(dispatched) == 0 && len(candidates) == 0 && scopeCards == 0 {
		return nil
	}
	for _, s := range dispatched {
		events = append(events, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: "manager",
			Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "aborted", Note: sweepStepNote},
		})
	}
	rev, err := m.cfg.Store.CurrentRevision(ctx, taskID)
	if err != nil {
		return fmt.Errorf("manager: 终局清点查图版本: %w", err)
	}
	reason := sweepPendingReason(terminal)
	for _, c := range candidates {
		// 收场不是裁决:条件行只陈述"证据成立未取得"(三查未完成),
		// 不对三查各项做真假断言。
		events = append(events, rings.Event{
			Kind: rings.EventTestClaimRecord, Actor: "manager",
			Payload: rings.TestClaimRecord{
				ClaimID: c.ID, Type: rings.TestClaimRecVerdict,
				Decision: rings.VerdictInsufficient, EvaluatedRev: rev,
				PendingReason: reason,
				Conditions: []rings.ArbiterConditionResult{{
					Kind: arbiter.CondEvidenceTraceable, Required: 1, Actual: 0, Satisfied: false,
				}},
			},
		})
		events = append(events, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: "manager",
			Payload: rings.NodeStateChanged{NodeID: c.ID, Status: rings.TestClaimInsufficient},
		})
	}
	if _, err := m.cfg.Store.Append(ctx, taskID, events...); err != nil {
		return fmt.Errorf("manager: 终局清点记账: %w", err)
	}
	runlog.Infof("任务终局清点: %s 无主排队步骤中止 %d 个,在途判断落证据不足 %d 个,未决范围卡结案 %d 张(原因: %s)",
		taskID, len(dispatched), len(candidates), scopeCards, sweepStepNote)
	return nil
}

// sweepScopeCardDeny 终局遗留范围卡的结案记录(按拒绝结案,与弹门票
// closeout 同语义:任务级中断不是审批裁决)。
func sweepScopeCardDeny(row rings.ApprovalRow) rings.ApprovalRecord {
	return rings.ApprovalRecord{
		RequestID: row.RequestID, StepID: row.StepID, UnitID: row.UnitID,
		Type: rings.ApprovalRecDenied, Intent: row.Intent,
		ToolName: row.ToolName, ToolArgs: []byte(row.ToolArgs), Note: row.Note,
		DenyLine: rings.DenyLineCloseout, DecidedBy: "manager",
		Reason: "任务收场,等待批准的范围卡按拒绝结案",
	}
}
