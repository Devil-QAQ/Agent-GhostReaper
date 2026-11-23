// P5-05 挂等让位调度(五分钟宽限,设计稿第八节):虫群因任何原因干
// 不了活时给 5 分钟宽限——宽限内解除=原位继续(零开销,会话没动),
// 超时=冻结收场让位(复用 P4-33 冬眠收场件语义:会话保留、goroutine
// 退出、名额释放、计费停走),步骤落停等态(不可领取:不执行命令就
// 出不了卡/不会重复挂等);解除时唤醒回 dispatched+队首档(已批准
// 待上位,同 P5-02 补考档),重领的单位原会话 Resume 续跑+R14 复检。
//
// 五类挂等源与停等态/解除通道:
//
//	范围审批卡   → scope_wait(既有)→ ResolveScopeWaits 对账唤醒(P4-51)
//	弹门卡/攻击卡 → gate_wait(新)  → LedgerGate.Decide 裁决唤醒(票内同锁互斥)
//	人工暂停     → pause_wait(新)  → ResumeUnit 继续按钮唤醒
//	模型故障退避 → model_wait(新)  → 主宰循环轮探链路就绪唤醒(冷却自动
//	                                  恢复无配置事件,不走 blocked——等配置
//	                                  保存才重开的既有通道会卡死自动恢复)
//
// 显示纪律:停等条目一律"排队中"不标原因(2026-11-16 造物主定,太乱);
// 挂等宽限期内状态词照词表(等待审批/等待模型/已暂停);上位优先级为
// 内部调度不上界面。
package swarm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// DefaultGraceYield 挂等宽限定值(5 分钟,条目①定死不做配置项)。
// Executor.GraceYield 为测试旋钮(零值取本默认;缩小时长只影响时序不
// 影响语义,与 ModelRetryLadder 同纪律)。
const DefaultGraceYield = 5 * time.Minute

// graceYield 宽限值解析(测试旋钮 > 定值)。
func (e *Executor) graceYield() time.Duration {
	if e.GraceYield > 0 {
		return e.GraceYield
	}
	return DefaultGraceYield
}

// isYieldWaitStatus 挂等让位停等态判定(P5-05):终态写入的防覆盖保护
// 与让位事件入账共用——范围卡(scope_wait)/弹门攻击卡(gate_wait)/
// 人工暂停(pause_wait)/模型故障(model_wait)四态同族。
func isYieldWaitStatus(status string) bool {
	switch status {
	case rings.StepScopeWait, rings.StepGateWait, rings.StepPauseWait, rings.StepModelWait:
		return true
	}
	return false
}

// graceTick 挂等轮询节拍(范围卡宽限内对账裁决/RoE 现值;票等待是
// select 无需轮询)。100ms 对 SQLite 直查是零负载,宽限 5 分钟至多
// 三千次只读查询。
const graceTick = 100 * time.Millisecond

// ReleaseGateWaits 冬眠收场的让位步骤放行(P4-33 边界:挂等单位与卡
// 一并冻结——gate_wait 停等步骤的票随 DropTickets 清出待决表〔持有
// 单位已退出,唤醒通道没了〕,步骤须回可领取,唤醒后重领的单位重新
// 过门立新票,与弹门挂起中收场同纪律)。pause_wait/model_wait/scope_
// wait 的解除通道不经票表(继续按钮/循环轮对账/范围对账),冬眠冻结
// 原样保留、唤醒后各归其位。
func (e *Executor) ReleaseGateWaits(taskID string) int {
	ctx := ledgerCtx
	for {
		rev, err := e.Store.CurrentRevision(ctx, taskID)
		if err != nil {
			return 0
		}
		steps, err := e.Store.NodesByType(ctx, taskID, rings.NodeStep, "")
		if err != nil {
			return 0
		}
		assets, err := e.Store.NodesByType(ctx, taskID, rings.NodeAsset, "")
		if err != nil {
			return 0
		}
		roe, err := e.Store.CurrentRoE(ctx, taskID)
		if err != nil {
			return 0
		}
		members := rings.InventoryAssetIDs(assets, roe)
		covers, err := e.Store.Edges(ctx, taskID, rings.EdgeCovers)
		if err != nil {
			return 0
		}
		invalid := map[string]bool{}
		for _, edge := range covers {
			if !members[edge.To] {
				invalid[edge.From] = true
			}
		}
		events := []rings.Event{}
		woken := 0
		for _, step := range steps {
			if step.Status != rings.StepGateWait && step.Status != rings.StepScopeWait {
				continue
			}
			status, note := "dispatched", "scope_head"
			if invalid[step.ID] {
				status, note = "aborted", "关联对象未加入当前资产清单，不继续该意图"
			} else {
				woken++
			}
			events = append(events, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm", Payload: rings.NodeStateChanged{NodeID: step.ID, Status: status, Note: note}})
		}
		if len(events) == 0 {
			return 0
		}
		_, err = e.Store.AppendAtRevision(ctx, taskID, rev, events...)
		if errors.Is(err, rings.ErrStaleRevision) {
			continue
		}
		if err != nil {
			runlog.Errorf("冬眠放行让位步骤落账失败: %s: %v", taskID, err)
			return 0
		}
		return woken
	}
}

// yieldResumeInput 让位唤醒后续跑的首条指令(P5-05:步骤被裁决/继续/
// 模型恢复从停等态唤醒,重领单位原会话 Resume——指令按来源组装)。
// 审批类让位按 Yield 携带的票号回查裁决记录:批准带放行配额重发被拦
// 命令;动作类拒绝带换路反馈(只杀动作不杀单位);无裁决记录(冬眠
// 清票)如实告知重新发起会重新过门。返回 (输入指令, 批准时需武装的
// 放行请求,nil=非审批类让位)。
func (e *Executor) yieldResumeInput(ctx context.Context, pack *TaskPack, note *rings.YieldNote) (string, *ApprovalRequest) {
	switch note.Source {
	case "pause":
		return yieldPauseResumePrompt(pack), nil
	case "model":
		return modelRecoveredResumePrompt(pack), nil
	}
	req := ApprovalRequest{
		TaskID: pack.TaskID, StepID: pack.StepID, PolicyRevision: note.PolicyRevision,
		Intent: note.Intent, ToolName: note.ToolName, ToolArgs: note.ToolArgs,
	}
	rows, err := e.Store.ApprovalByRequest(ctx, pack.TaskID, note.Request)
	if err != nil || len(rows) == 0 {
		return yieldRedecideResumePrompt(pack, note), nil
	}
	latest := rows[len(rows)-1] // rev 升序,末位即最新
	switch latest.Type {
	case rings.ApprovalRecApproved:
		return approvalResumePrompt(req, GateDecision{Approved: true, By: latest.DecidedBy, Reason: latest.Reason}), &req
	case rings.ApprovalRecAccess:
		return yieldRedecideResumePrompt(pack, note), nil
	default:
		return gateDenyPromptOf(req, latest.Reason), nil
	}
}

// gateDenyPromptOf 动作类拒绝的换路反馈(弹门拒/攻击拒分流措辞,
// 与挂起中拒绝同源文案)。
func gateDenyPromptOf(req ApprovalRequest, reason string) string {

	return gateDenyResumePrompt(req, reason)
}

// yieldArmApproval 让位批准的放行配额武装(重领单位续跑首段前调用,
// 与挂起中批准的 armGateApproval 同一实现)。
func (env *unitEnv) yieldArmApproval(req *ApprovalRequest) {
	if req == nil {
		return
	}
	env.armGateApproval(*req)
}

// yieldPauseResumePrompt 人工暂停让位唤醒的续跑指令(操作者点继续)。
func yieldPauseResumePrompt(pack *TaskPack) string {
	return unitResumePrompt(true)
}

// yieldRedecideResumePrompt 审批让位唤醒但无裁决记录(冬眠清票等形态)
// 的续跑指令:被拦命令重新发起会重新过门(新卡),如实告知。
func yieldRedecideResumePrompt(pack *TaskPack, note *rings.YieldNote) string {
	var b strings.Builder
	b.WriteString("【继续 · 续跑】挂等让位后重新领取,本会话已从转录完整恢复(历史消息与已耗用量俱在)。")
	fmt.Fprintf(&b, "继续完成本步骤意图:%s。", pack.StepIntent)
	b.WriteString("此前等待审批的请求已随任务状态变化失效;被拦下的命令可重新发起,会重新提交审批。已执行过的动作与已写回的结论勿重复。")
	return b.String()
}

// ---- 范围卡宽限解除的原位续跑指令(P5-05:会话未收场,无"转录恢复"
// 措辞——段续跑与既有挂起续跑同族,等待期间预算未消耗照述) ----
