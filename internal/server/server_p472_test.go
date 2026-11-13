// ---- P4-72(问题清单 #88 B 方案):合一留痕与结论词表的读面 ----
package server

import (
	"strings"
	"testing"

	"ghostreaper/internal/swarm"
)

// TestP472TriageNoteWords 合一五值词表翻译＋targets= 清单呈现:性质判
// 定记录的人话形态(非目标/范围内/信息源获取/信息源攻击),旧词表值
// 兼容;targets 清单段如实渲染。
func TestP472TriageNoteWords(t *testing.T) {
	cases := []struct{ note, want string }{
		{"verdict=" + swarm.TriageNotTarget + " basis=速率参数值 targets=nmap,400",
			"性质判定:非目标;依据:速率参数值;本次判定目标:nmap,400"},
		{"verdict=" + swarm.TriageInfoAccess + " basis=公开文档站,与目标无归属",
			"性质判定:信息源获取;依据:公开文档站,与目标无归属"},
		{"verdict=" + swarm.TriageInScope + " basis=种子范围内",
			"性质判定:范围内;依据:种子范围内"},
		{"verdict=" + swarm.TriageInfoAttack + " basis=扫描动作",
			"性质判定:信息源攻击;依据:扫描动作"},
		// 旧词表值(存量 tg- 记录)。
		{"verdict=" + swarm.TriageMethodical + " basis=公开代码托管",
			"性质判定:方法性访问;依据:公开代码托管"},
	}
	for _, c := range cases {
		got := triageNoteText(c.note)
		if got != c.want {
			t.Errorf("%q: 得 %q,欲 %q", c.note, got, c.want)
		}
	}
	// 非目标过滤可观测不算拦截:性质判定记录不是 denied 拦截行。
	if strings.Contains(triageNoteText("verdict="+swarm.TriageNotTarget+" basis=x"), "拒绝") {
		t.Fatalf("非目标过滤不是拦截")
	}
}
