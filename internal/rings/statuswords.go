package rings

// 状态 → 界面显示词(P4-28 ⑥,显示层合并 12→6):"待验证/反证中/待下轮
// 反证"三类过程态在显示层合并为"锤炼中",出结果才显示"已确认/待确认"
// 与假设侧"成立/不成立/未能定论";搁置/已废弃/已修复为假设管理与修复
// 轴的既有界面词,不在合并范围。机器内部状态机一字不动——审计、判定、
// 事件流照旧用原状态常量;本表只服务显示层(报告导出状态词、list_claims
// 与 read_findings 等工具回传摘要),与面板词表(web/lib/api.js 的
// CLAIM_STATUS/HYP_STATUS)同词同口径、前后端同批对齐。
const (
	claimWordForging = "锤炼中" // candidate | testing | awaiting_round
	hypWordForging   = "锤炼中" // candidate | testing
)

// ClaimStatusWord 发现(TestClaim)状态 → 界面显示词;未知状态原样退回
// (如实呈现,不编造)。
func ClaimStatusWord(status string) string {
	switch status {
	case TestClaimCandidate, TestClaimTesting, TestClaimAwaitingRound:
		return claimWordForging
	case TestClaimConfirmed:
		return "已确认"
	case TestClaimPending:
		return "待确认"
	case TestClaimFixed:
		return "已修复"
	}
	return status
}

// HypStatusWord 假设状态 → 界面显示词;未知状态原样退回。
func HypStatusWord(status string) string {
	switch status {
	case HypothesisCandidate, HypothesisTesting:
		return hypWordForging
	case HypothesisHeld:
		return "搁置"
	case HypothesisConfirmed:
		return "成立"
	case HypothesisDiscarded:
		return "已废弃"
	}
	return status
}
