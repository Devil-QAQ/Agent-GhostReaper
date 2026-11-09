package manager

import (
	"context"
	"fmt"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 任务终局清点(P4-43,问题清单 #63):任务落终态(stopped|completed)后
// 紧邻执行的一道确定性收场。终局此前只收场了"有人干的"(在跑单位随
// 任务 ctx 中止记账),无人收场"没人领的"与在途判断——无主排队步骤
// 永远 dispatched、testing 判断永远"反证中",工作台水位与虫群排队条目
// 终身不清零。清点把两者如实收场:
//   - 无主排队步骤(全部 dispatched,含等前置等待中)批量记 aborted,
//     原因"任务收场"随事件留痕(取消留痕不删节点,R1);
//   - testing 判断批量回 pending 并带理由(R4:待确认是一等状态,搁置
//     理由必填、可被新证据重开唤醒),诚实呈现"没锤炼完"。
//
// 幂等:已终态步骤/非 testing 判断不动,无在途对象零落笔。只挂终态
// (挂点在 recordWith 的 stopped|completed 分支)——冬眠、暂停等非终态
// 一律不触发(P4-33 后冬眠任务同样无活 goroutine,在途状态保留待唤醒
// 续跑,勿误清)。清点失败如实报错,不阻塞终态本身(终态事件已落账)。

// sweepStepNote 无主排队步骤的收场原因(事件 Note 留痕;何时何因收场
// 走事件流审计,投影不携带)。
const sweepStepNote = "任务收场"

// sweepPendingReason testing 判断回 pending 的搁置理由:按终态如实措辞
// (终止/完成各自陈述;重开的唤醒路径不受影响,reopen 机制照旧)。
func sweepPendingReason(terminal string) string {
	if terminal == StatusCompleted {
		return "任务完成中断反证，可被唤醒"
	}
	return "任务终止中断反证，可被唤醒"
}

// sweepAtTerminal 终局清点本体:单批事务落账(步骤中止 + 判断回挂起),
// 无在途对象零落笔(幂等)。事件序紧邻终态生命周期事件(挂点保证)。
// P4-51:范围等待中(scope_wait)的步骤一并收场——任务终局无裁决通道
// (待决卡冻结),等待单位如实落中止(与无主排队同款"任务收场");
// 遗留的未决范围卡同步按拒绝结案(与弹门票 ExpireTickets 同纪律,
// 红点不留僵尸)。
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
	testing, err := m.cfg.Store.NodesByType(ctx, taskID, rings.NodeTestClaim, rings.TestClaimTesting)
	if err != nil {
		return fmt.Errorf("manager: 终局清点查在途判断: %w", err)
	}
	// 未决范围卡(approval_records 投影按 RequestID 取最新,恰为
	// scope_wait 即未决)。
	scopeCards := 0
	events := make([]rings.Event, 0, len(dispatched)+2*len(testing)+4)
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
	if len(dispatched) == 0 && len(testing) == 0 && scopeCards == 0 {
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
	for _, c := range testing {
		events = append(events, rings.Event{
			Kind: rings.EventTestClaimRecord, Actor: "manager",
			Payload: rings.TestClaimRecord{
				ClaimID: c.ID, Type: rings.TestClaimRecVerdict,
				Decision: rings.VerdictPending, EvaluatedRev: rev,
				PendingReason: reason,
				// 依据条件(R2:verdict 无论结论必须逐条列出依据):任务终局
				// 收场,当前反证轮未写回结构化结论。
				Conditions: []rings.ArbiterConditionResult{{
					Kind: arbiter.CondFalsifyCompleted, Required: 1, Actual: 0, Satisfied: false,
				}},
			},
		})
		events = append(events, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: "manager",
			Payload: rings.NodeStateChanged{NodeID: c.ID, Status: rings.TestClaimPending},
		})
	}
	if _, err := m.cfg.Store.Append(ctx, taskID, events...); err != nil {
		return fmt.Errorf("manager: 终局清点记账: %w", err)
	}
	runlog.Infof("任务终局清点: %s 无主排队步骤中止 %d 个,在途判断回挂起 %d 个,未决范围卡结案 %d 张(原因: %s)",
		taskID, len(dispatched), len(testing), scopeCards, sweepStepNote)
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
