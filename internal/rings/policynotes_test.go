package rings

import (
	"strings"
	"testing"
)

func TestPolicyNotesPreservesOperatorRequirements(t *testing.T) {
	original := "只处理测试报告，不访问其他环境"
	rules := []IntentRule{{ID: "old-1", Action: "访问外部服务", AssetIDs: []string{"a1", "a2"}, Disposition: "forbid", Note: "需另行授权"},
		{ID: "old-2", Action: "读取测试接口", Disposition: "allow", Quota: 2}}
	got := PolicyNotes(original, rules)
	for _, part := range []string{original, "禁止访问外部服务", "a1、a2", "需另行授权", "次数上限2", "次数未经完整核对时须审批"} {
		if !strings.Contains(got, part) {
			t.Fatalf("operator requirement missing: %q", part)
		}
	}
	if rules[0].Note != "需另行授权" || original != "只处理测试报告，不访问其他环境" {
		t.Fatal("policy rendering mutated operator data")
	}
	if PolicyNotes(original, nil) != original {
		t.Fatal("plain notes changed without legacy constraints")
	}
}
