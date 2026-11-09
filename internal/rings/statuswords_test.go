package rings

import "testing"

// 状态显示词(P4-56 四页签口径)逐格断言:发现侧 candidate/awaiting_round
// 显示"等待反证"、testing 粗显"反证中"(精确暂停/重派细分由 ClaimDisplay
// 按步骤现值推导,见 claimdisplay_test);pending 显示"证据不足";refuted
// 与 discarded 合并"已废弃"。未知状态原样退回(如实呈现)。
func TestClaimStatusWord(t *testing.T) {
	cases := map[string]string{
		TestClaimCandidate:     "等待反证",
		TestClaimTesting:       "反证中",
		TestClaimAwaitingRound: "等待反证",
		TestClaimConfirmed:     "已确认",
		TestClaimRefuted:       "已废弃",
		TestClaimDiscarded:     "已废弃",
		TestClaimPending:       "证据不足",
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
		HypothesisCandidate: "等待验证",
		HypothesisTesting:   "验证中",
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

// TestFactStatusWord 事实侧显示词(P4-56 ②:候选改标"新";被推翻为
// 防御性词位——当前状态机无此迁移,词位留给翻案历史)。
func TestFactStatusWord(t *testing.T) {
	cases := map[string]string{
		"candidate":        "新",
		"confirmed":        "已确认",
		"refuted":          "被推翻",
		"weird_old_status": "weird_old_status",
	}
	for status, want := range cases {
		if got := FactStatusWord(status); got != want {
			t.Errorf("FactStatusWord(%q) = %q, want %q", status, got, want)
		}
	}
}
