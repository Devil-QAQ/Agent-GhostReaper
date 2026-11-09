package rings

import "strings"

// P4-56 发现区四页签(问题清单 #77):发现区重构为四页签漏斗——假设→
// 事实→锤炼→成果,条目往右迁、左边不留。本文件是锤炼/假设对象的显示态
// 推导层:机器状态机一字不动(账本/审计/判定照旧原状态),显示键按
// "机器状态 + 当前反证/验证步骤现值"推导,前后端同键同词。
//
// 锤炼(发现/TestClaim)显示键:
//   - wait_falsify    等待反证(过程色)——待派或排队;悬停细分三种:
//     重派后排队(已重派第 N 轮/已重派换法)、操作者终止交主宰判定
//     (等待主宰判定去留)、普通等待
//   - falsifying      反证中(过程色动画)——独立单位在跑;重派在跑时
//     悬停带重派细分
//   - falsify_paused  反证暂停(过程色静止+标等待审批)——反证单位
//     等范围批准(P4-51 scope_wait)
//   - insufficient    证据不足(暗黄)——锤炼完仍证不实挂起,可复活
//   - discarded       已废弃(灰)——refuted(被推翻)与 discarded
//     (摘除连坐/主宰放弃)合并的终了档,只可追溯
//   - confirmed/fixed 归成果页(已确认/已修复)
//
// 假设(Hypothesis)显示键:wait_verify 等待验证 / verifying 验证中 /
// verify_paused 验证暂停(等范围批准) / held 搁置(暗黄) / discarded
// 已废弃(灰) / confirmed 迁入事实页(已确认+由假设升级,本页不留)。
//
// 颜色三档全站统一:过程色动画=在动 / 过程色静止=暂停 / 暗黄=挂起可
// 复活(搁置、证据不足) / 灰=终了只可追溯(已废弃、被推翻);面板词表
// (web/lib/api.js CLAIM_DISPLAY/HYP_DISPLAY)与本层同键同词。

// 锤炼(发现)显示键。
const (
	ClaimDisplayWaitFalsify   = "wait_falsify"   // 等待反证
	ClaimDisplayFalsifying    = "falsifying"     // 反证中
	ClaimDisplayFalsifyPaused = "falsify_paused" // 反证暂停(等待审批)
	ClaimDisplayInsufficient  = "insufficient"   // 证据不足
	ClaimDisplayDiscarded     = "discarded"      // 已废弃(refuted+discarded 合并)
	ClaimDisplayConfirmed     = "confirmed"      // 已确认(成果页)
	ClaimDisplayFixed         = "fixed"          // 已修复(成果页修复标记)
)

// 假设显示键。
const (
	HypDisplayWaitVerify   = "wait_verify"   // 等待验证
	HypDisplayVerifying    = "verifying"     // 验证中
	HypDisplayVerifyPaused = "verify_paused" // 验证暂停(等待审批)
	HypDisplayHeld         = "held"          // 搁置
	HypDisplayDiscarded    = "discarded"     // 已废弃(三种出局合一)
	HypDisplayConfirmed    = "confirmed"     // 成立→迁入事实页,假设页不留
)

// 显示悬停细分键(附在显示键之后的悬停人话;面板按 key 取词)。
const (
	ClaimHintRedispatch    = "redispatch"     // 已重派第 N 轮
	ClaimHintRedispatchAlt = "redispatch_alt" // 已重派换法(弹门拒绝后换法重验)
	ClaimHintRulingWait    = "ruling_wait"    // 等待主宰判定去留(操作者终止)
	ClaimHintTaskDone      = "task_done"      // 任务完成时验证中断(终局清点兜底)
)

// ClaimFlight 当前反证轮的飞行情报(锤炼显示态的推导输入):最新反证
// 步骤的现值 + 操作者终止判定(与 arbiter.operatorKilled 同口径:
// cancelled 或舵杆 kill 送达)。nil = 无反证步骤(候选/终态)。
type ClaimFlight struct {
	StepID            string
	Status            string // 反证步骤状态
	Round             int    // 步骤轮次
	Redispatch        int    // 该步骤重派序号(0=原派,detail.redispatch)
	RedispatchReason  string // detail.redispatch_reason
	OperatorCancelled bool   // 步骤 cancelled(操作者取消)
	SteerKilled       bool   // 步骤被舵杆 kill_work 送达
}

// interrupted 当前轮步骤终态无在跑后继(中断窗口:收割侧重派/主宰判定
// 之前的瞬时或挂起形态)。
func (f *ClaimFlight) interrupted() bool {
	switch f.Status {
	case "done", "exhausted", "failed", "aborted", "approval_denied", "blocked", "cancelled":
		return true
	}
	return false
}

// operatorKilled 操作者终止(与 arbiter.operatorKilled 同口径)。
func (f *ClaimFlight) operatorKilled() bool {
	return f.OperatorCancelled || f.SteerKilled
}

// ClaimDisplay 锤炼对象显示键推导(P4-56 ②③):status=机器状态;
// flight=当前反证轮现值(testing 时调用方给,其余可 nil);
// pendingReason=pending 投影理由(终局清点兜底悬停判定)。返回显示键与
// 悬停细分键(空=无细分)。
func ClaimDisplay(status string, flight *ClaimFlight, pendingReason string) (key, hint string) {
	switch status {
	case TestClaimCandidate:
		return ClaimDisplayWaitFalsify, ""
	case TestClaimTesting:
		if flight == nil {
			return ClaimDisplayWaitFalsify, ""
		}
		switch {
		case flight.Status == "running":
			return ClaimDisplayFalsifying, redispatchHint(flight)
		case flight.Status == StepScopeWait:
			return ClaimDisplayFalsifyPaused, ""
		case flight.Status == "dispatched" || flight.Status == "pending":
			return ClaimDisplayWaitFalsify, redispatchHint(flight)
		case flight.interrupted() && flight.operatorKilled():
			// 杀单位后:无反证在跑,去留交主宰(P4-55 判定杠杆)。
			return ClaimDisplayWaitFalsify, ClaimHintRulingWait
		default:
			// 中断窗口(重派前的瞬态)等其余未知步骤态:如实按等待
			// 反证呈现,下一拍收割重派/判定自然修正。
			return ClaimDisplayWaitFalsify, ""
		}
	case TestClaimAwaitingRound:
		return ClaimDisplayWaitFalsify, ""
	case TestClaimPending:
		if containsInterruptedCloseout(pendingReason) {
			return ClaimDisplayInsufficient, ClaimHintTaskDone
		}
		return ClaimDisplayInsufficient, ""
	case TestClaimRefuted, TestClaimDiscarded:
		return ClaimDisplayDiscarded, ""
	case TestClaimConfirmed:
		return ClaimDisplayConfirmed, ""
	case TestClaimFixed:
		return ClaimDisplayFixed, ""
	}
	return status, "" // 未知状态原样退回(如实呈现,不编造)
}

// redispatchHint 在跑/排队的反证步骤若为重派产物,给悬停细分:
// 弹门拒绝后的换法重验(原因含"审批拒绝")→ 已重派换法;其余 →
// 已重派第 N 轮(N=重派序号)。
func redispatchHint(f *ClaimFlight) string {
	if f.Redispatch <= 0 {
		return ""
	}
	if containsApprovalDenied(f.RedispatchReason) {
		return ClaimHintRedispatchAlt
	}
	return ClaimHintRedispatch
}

// HypFlight 假设验证步骤的飞行情报(假设显示态推导输入)。
type HypFlight struct {
	Status            string // 验证步骤状态
	OperatorCancelled bool
	SteerKilled       bool
}

// HypDisplay 假设显示键推导:status=机器状态;flight=testing 时的验证
// 步骤现值(其余可 nil)。
func HypDisplay(status string, flight *HypFlight) string {
	switch status {
	case HypothesisCandidate:
		return HypDisplayWaitVerify
	case HypothesisTesting:
		if flight == nil {
			return HypDisplayWaitVerify
		}
		switch {
		case flight.Status == "running":
			return HypDisplayVerifying
		case flight.Status == StepScopeWait:
			return HypDisplayVerifyPaused
		default:
			return HypDisplayWaitVerify
		}
	case HypothesisHeld:
		return HypDisplayHeld
	case HypothesisConfirmed:
		return HypDisplayConfirmed
	case HypothesisDiscarded:
		return HypDisplayDiscarded
	}
	return status
}

// ClaimFlightOfSteps 按声明的全部反证步骤推导当前轮飞行情报(与
// arbiter.currentRound 同口径:轮次号最大,并列取创建序最晚);无步骤
// 返回 nil。server 显示层与报告导出共用(同词同拍)。
func ClaimFlightOfSteps(steps []NodeFull, steerKills map[string]bool) *ClaimFlight {
	var cur NodeFull
	found := false
	for _, st := range steps {
		r := detailRound(st.Detail)
		if !found || r > detailRound(cur.Detail) ||
			(r == detailRound(cur.Detail) && st.CreatedRev > cur.CreatedRev) {
			cur, found = st, true
		}
	}
	if !found {
		return nil
	}
	reason, _ := cur.Detail["redispatch_reason"].(string)
	return &ClaimFlight{
		StepID:            cur.ID,
		Status:            cur.Status,
		Round:             detailRound(cur.Detail),
		Redispatch:        detailRedispatch(cur.Detail),
		RedispatchReason:  reason,
		OperatorCancelled: cur.Status == "cancelled",
		SteerKilled:       steerKills != nil && steerKills[cur.ID],
	}
}

// detailRound detail.round(缺省 0,P1-08 旧步骤无轮次)。
func detailRound(d map[string]any) int {
	if r, ok := d["round"].(float64); ok {
		return int(r)
	}
	return 0
}

// detailRedispatch detail.redispatch(缺省 0=原派)。
func detailRedispatch(d map[string]any) int {
	if r, ok := d["redispatch"].(float64); ok {
		return int(r)
	}
	return 0
}

// containsInterruptedCloseout 终局清点兜底的挂起理由(P4-43:任务终止/
// 完成中断反证)——P4-56 ③ 选 B 定案的显示悬停判定面。
func containsInterruptedCloseout(reason string) bool {
	return strings.Contains(reason, "中断反证")
}

// containsApprovalDenied 重派原因里的弹门拒绝特征(P4-55 中断原因拼装
// "审批拒绝终止";其余拒绝线不经反证步骤,不在此列)。
func containsApprovalDenied(reason string) bool {
	return strings.Contains(reason, "审批拒绝")
}
