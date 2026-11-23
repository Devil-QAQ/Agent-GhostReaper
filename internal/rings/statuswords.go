package rings

const (
	claimWordCandidate = "待机器核验" // candidate(交卷到机器三查之间的秒级瞬态;词面用已记账术语"机器核验"组词,过文风闸门)

)

// ClaimStatusWord 发现(TestClaim)状态 → 界面显示词;未知状态原样退回
// (如实呈现,不编造)。被推翻(refuted,重验推翻终态)与已废弃
// (discarded,摘除连坐)机器态保留、账本留痕,显示层不进漏洞页
// (详情可追溯)。
func ClaimStatusWord(status string) string {
	switch status {
	case TestClaimCandidate:
		return claimWordCandidate
	case TestClaimConfirmed:
		return "已确认"
	case TestClaimInsufficient:
		return "证据不足"
	case TestClaimRefuted:
		return "被推翻"
	case TestClaimDiscarded:
		return "已废弃"
	case TestClaimFixed:
		return "已修复"
	}
	return status
}

// UnitFinishWord 单位会话收场状态(end_status,步骤状态同词汇)→ 界面
// 显示词(P5-09⑥ 全景图链路单位卡);挂等让位停等态一律"排队中"不标
// 原因(词表 v1.3.0);未知状态原样退回。
func UnitFinishWord(status string) string {
	switch status {
	case "done":
		return "已完成"
	case "exhausted":
		return "预算耗尽"
	case "failed":
		return "失败"
	case "aborted":
		return "已中止"
	case "cancelled":
		return "已取消"
	case "blocked":
		return "受阻"
	case "approval_denied":
		return "审批拒绝终止"
	case StepScopeWait, StepGateWait, StepPauseWait, StepModelWait:
		return "排队中"
	case "pause_yield":
		return "排队中"
	}
	return status
}
