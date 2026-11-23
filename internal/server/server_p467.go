// P4-67(问题清单 #86 A②,RoE 机制重构批次第二层):研判分诊与信息源
// 清单的服务端读面与写口——RoE 面板信息源区(host＋研判依据摘要＋撤销
// 按钮)、撤销端点(与牧者标记/分诊自动授同一账本动作)。攻击行为卡
// 的待决呈现与裁决走既有弹门通道(Kind="attack" 仅是读面区分)。
package server

import (
	"fmt"
	"strings"
)

// triageVerdictWords 分诊结论的人话词表(研判留痕与信息源区共用;
// P4-72 合一五值＋存量旧词表兼容读)。
var triageVerdictWords = map[string]string{
	"info_access":       "信息源获取",
	"info_attack":       "信息源攻击",
	"in_scope":          "范围内",
	"not_target":        "非目标",
	"related_candidate": "疑似目标相关资产",
	// P4-67 旧词表(存量 tg- 记录如实翻译)。
	"methodical_access": "方法性访问",
	"declared_source":   "命中任务写明的外部服务",
	"attack_action":     "信息源上的攻击行为",
	"revoked":           "已撤销",
}

// triageNoteText 研判留痕 Note 的人话化("verdict=X basis=Y[ targets=…]"
// → "性质判定:结论;依据:Y;目标清单:…");解析不出原样返回(防御)。
func triageNoteText(note string) string {
	targets := ""
	if i := strings.Index(note, " targets="); i >= 0 {
		targets = note[i+len(" targets="):]
		note = note[:i]
	}
	verdict, basis, ok := strings.Cut(note, " basis=")
	if !ok {
		return note
	}
	verdict = strings.TrimPrefix(verdict, "verdict=")
	word, known := triageVerdictWords[verdict]
	if !known {
		return note
	}
	if targets != "" {
		return fmt.Sprintf("性质判定:%s;依据:%s;本次判定目标:%s", word, basis, targets)
	}
	return fmt.Sprintf("性质判定:%s;依据:%s", word, basis)
}
