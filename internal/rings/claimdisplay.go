package rings

// 声明(发现)显示键(P5-02⑤ 三态)。
const (
	ClaimDisplayCandidate    = "candidate"    // 候选(交卷到机器三查之间,秒级)
	ClaimDisplayInsufficient = "insufficient" // 证据不足(暗黄;漏洞页灰区)
	ClaimDisplayRefuted      = "refuted"      // 被推翻(重验推翻终态;账本留痕,漏洞页不展示)
	ClaimDisplayDiscarded    = "discarded"    // 已废弃(摘除连坐;账本留痕)
	ClaimDisplayConfirmed    = "confirmed"    // 已确认
	ClaimDisplayFixed        = "fixed"        // 已修复(已确认之上的修复标记)
)

// ClaimDisplay 声明对象显示键推导:status=机器状态,直接映射;旧状态
// 词(testing/awaiting_round/pending 等)原样退回(如实呈现,不编造)。
func ClaimDisplay(status string) (key, hint string) {
	switch status {
	case TestClaimCandidate:
		return ClaimDisplayCandidate, ""
	case TestClaimInsufficient:
		return ClaimDisplayInsufficient, ""
	case TestClaimRefuted:
		return ClaimDisplayRefuted, ""
	case TestClaimDiscarded:
		return ClaimDisplayDiscarded, ""
	case TestClaimConfirmed:
		return ClaimDisplayConfirmed, ""
	case TestClaimFixed:
		return ClaimDisplayFixed, ""
	}
	return status, "" // 未知状态(含旧账本过程态)原样退回
}
