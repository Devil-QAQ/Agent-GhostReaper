package rings

import "testing"

// 状态显示词(P4-28 ⑥ 显示层合并 12→6)逐格断言:过程态合并"锤炼中",
// 终态与假设侧按六词;未知状态原样退回(如实呈现)。
func TestClaimStatusWord(t *testing.T) {
	cases := map[string]string{
		TestClaimCandidate:     "锤炼中",
		TestClaimTesting:       "锤炼中",
		TestClaimAwaitingRound: "锤炼中",
		TestClaimConfirmed:     "已确认",
		TestClaimPending:       "待确认",
		TestClaimFixed:         "已修复",
		"weird_old_status":     "weird_old_status",
	}
	for status, want := range cases {
		if got := ClaimStatusWord(status); got != want {
			t.Errorf("ClaimStatusWord(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestHypStatusWord(t *testing.T) {
	cases := map[string]string{
		HypothesisCandidate: "锤炼中",
		HypothesisTesting:   "锤炼中",
		HypothesisHeld:      "搁置",
		HypothesisConfirmed: "成立",
		HypothesisDiscarded: "已废弃",
		"weird_old_status":  "weird_old_status",
	}
	for status, want := range cases {
		if got := HypStatusWord(status); got != want {
			t.Errorf("HypStatusWord(%q) = %q, want %q", status, got, want)
		}
	}
}
