package rings

import "testing"

func TestClaimStatusWord(t *testing.T) {
	cases := map[string]string{
		TestClaimCandidate:    "待机器核验",
		TestClaimConfirmed:    "已确认",
		TestClaimInsufficient: "证据不足",
		TestClaimRefuted:      "被推翻",
		TestClaimDiscarded:    "已废弃",
		TestClaimFixed:        "已修复",
		"testing":             "testing", // 旧过程态原样退回(存量账本)
		"weird_old_status":    "weird_old_status",
	}
	for status, want := range cases {
		if got := ClaimStatusWord(status); got != want {
			t.Errorf("ClaimStatusWord(%q) = %q, want %q", status, got, want)
		}
	}
}
