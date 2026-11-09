package rings

import "strings"

// 状态 → 界面显示词(P4-56 四页签口径,问题清单 #77):发现区四页签
// 漏斗——假设→事实→锤炼→成果;机器内部状态机一字不动(审计、判定、
// 事件流照旧用原状态常量)。本表服务"只有机器状态、无步骤现值"的消费
// 面(list_claims 与 read_findings 等工具回传摘要、报告导出状态词、
// 图页节点卡粗口径)——testing 一律粗显"反证中",精确暂停/重派细分
// 由显示键推导层(claimdisplay.go,发现页与详情消费)按步骤现值给出。
// 与面板词表(web/lib/api.js 的 CLAIM_STATUS/HYP_STATUS)同词同口径、
// 前后端同批对齐。
//
// P4-28 六词口径由本表修订(P4-56 ②状态词定稿):合并词"锤炼中"退役,
// 假设侧等待验证/验证中分立;"待确认"改"证据不足","不成立"并入
// "已废弃","挂账"改"搁置"(与面板同词)。
const (
	claimWordWaitFalsify = "等待反证" // candidate | awaiting_round(粗口径)
	claimWordFalsifying  = "反证中"  // testing(粗口径)
	hypWordWaitVerify    = "等待验证" // candidate
	hypWordVerifying     = "验证中"  // testing(粗口径)
)

// ClaimStatusWord 发现(TestClaim)状态 → 界面显示词;未知状态原样退回
// (如实呈现,不编造)。refuted 与 discarded(P4-55)在四页签体系合并
// 显示"已废弃"——锤炼页终了档,理由在详情(被反证推翻/目标已摘除)。
func ClaimStatusWord(status string) string {
	switch status {
	case TestClaimCandidate, TestClaimAwaitingRound:
		return claimWordWaitFalsify
	case TestClaimTesting:
		return claimWordFalsifying
	case TestClaimConfirmed:
		return "已确认"
	case TestClaimRefuted, TestClaimDiscarded:
		return "已废弃"
	case TestClaimPending:
		return "证据不足"
	case TestClaimFixed:
		return "已修复"
	}
	return status
}

// HypStatusWord 假设状态 → 界面显示词;未知状态原样退回。held 搁置
// (原"挂账"退役)、discarded 已废弃(三种出局合一:未验丢弃/验证被
// 推翻/证据不够,理由在详情);confirmed 成立——四页签下显示层迁入
// 事实页(带"由假设升级"来路),本页不留。
func HypStatusWord(status string) string {
	switch status {
	case HypothesisCandidate:
		return hypWordWaitVerify
	case HypothesisTesting:
		return hypWordVerifying
	case HypothesisHeld:
		return "搁置"
	case HypothesisConfirmed:
		return "成立"
	case HypothesisDiscarded:
		return "已废弃"
	}
	return status
}

// FactStatusWord 事实状态 → 界面显示词(P4-56 ②:候选改标"新"——
// 新进短暂标记,主宰收割后自动转已确认;被推翻为翻案历史的终了档,
// 灰色只可追溯——当前状态机无此迁移,词位为防御性映射,未知状态如实
// 退回)。
func FactStatusWord(status string) string {
	switch status {
	case "candidate":
		return "新"
	case "confirmed":
		return "已确认"
	case "refuted", "discarded":
		return "被推翻"
	}
	return status
}

// ClaimStatusWordOf 带步骤现值的发现显示词(claimdisplay 的词出口,
// 供报告等需要"状态+步骤"精确口径的消费面);hint 非空时附悬停细分
// 人话。
func ClaimStatusWordOf(status string, flight *ClaimFlight, pendingReason string) (word, hint string) {
	key, h := ClaimDisplay(status, flight, pendingReason)
	word = claimDisplayWords[key]
	if word == "" {
		word = status
	}
	switch h {
	case ClaimHintRedispatch:
		hint = "已重派,换新单位继续反证"
	case ClaimHintRedispatchAlt:
		hint = "已重派换法:原动作方法被拒,换新方法重验"
	case ClaimHintRulingWait:
		hint = "等待主宰判定去留"
	case ClaimHintTaskDone:
		hint = "任务完成时验证中断"
	}
	return word, hint
}

// claimDisplayWords 显示键 → 词(与面板 CLAIM_DISPLAY 同词)。
var claimDisplayWords = map[string]string{
	ClaimDisplayWaitFalsify:   claimWordWaitFalsify,
	ClaimDisplayFalsifying:    claimWordFalsifying,
	ClaimDisplayFalsifyPaused: "反证暂停",
	ClaimDisplayInsufficient:  "证据不足",
	ClaimDisplayDiscarded:     "已废弃",
	ClaimDisplayConfirmed:     "已确认",
	ClaimDisplayFixed:         "已修复",
}

// ClaimDisplayWord 显示键 → 词;未知键原样退回(如实呈现)。
func ClaimDisplayWord(key string) string {
	if w, ok := claimDisplayWords[key]; ok {
		return w
	}
	return key
}

// HypStatusWordOf 带验证步骤现值的假设显示词。
func HypStatusWordOf(status string, flight *HypFlight) string {
	switch HypDisplay(status, flight) {
	case HypDisplayWaitVerify:
		return hypWordWaitVerify
	case HypDisplayVerifying:
		return hypWordVerifying
	case HypDisplayVerifyPaused:
		return "验证暂停"
	case HypDisplayHeld:
		return "搁置"
	case HypDisplayConfirmed:
		return "成立"
	case HypDisplayDiscarded:
		return "已废弃"
	}
	return status
}

// PendingReasonWord 挂起理由的显示口径(P4-56:pending_reason 是机器
// 拼装冻结文案,详情与报告如实呈现原文;本函数只用于计数行等聚合面
// 的归类词,不改账本)。
func PendingReasonWord(reason string) string {
	if strings.Contains(reason, "中断反证") {
		return "任务完成时验证中断"
	}
	return "证据不足"
}
