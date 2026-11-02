package swarm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"ghostreaper/internal/rings"
)

// ---- P4-27 ⑨ 审批卡危害说明三段式(问题清单 #54 ②) ----
//
// 待决审批卡加一行危害说明:要做什么(动作)+ 做了会怎么样(目标
// 变化)+ 导致什么(最坏后果与不可逆性)。确定性模板按意图分级 +
// 命令特征拼装——零 LLM、平实书面白话、禁口语禁机器语言禁命令原文
// 直贴;现组装现服务(不落库,与审批卡同一数据源)。
//
// 锚例(造物主逐字定稿,2026-10-24;目标以完整标识 host:port 呈现,
// 裸端口不上界面——P4-27 ③ 同律):
//
//	注册类(L4):将向 {host} 提交注册表单创建账号。目标会新增一个可
//	  登录账号。账号会留存于目标系统，管理员可能察觉异常注册。
//	查看类(L1):将执行 pwd 与 ls 查看目录和文件列表。目标不会发生
//	  任何变化。

// HarmNote 危害说明三段式:按意图分级 + 命令特征确定性拼装。level
// 缺失(0 级)按只读呈现——分级缺失的请求会被拒绝线拦下,到不了卡上,
// 此处只是兜底。返回空串 = 无可拼装(调用方不渲染该行)。
func HarmNote(level rings.ActionLevel, toolName string, toolArgs json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
		URL     string `json:"url"`
	}
	_ = json.Unmarshal(toolArgs, &args)

	cmd := strings.TrimSpace(args.Command)
	verbs := commandVerbs(cmd)
	switch ActionLevelRank(level) {
	case 4:
		return harmNoteModifyPersist(toolName, cmd, verbs)
	case 3:
		return harmNoteExploit(toolName, cmd, verbs)
	case 2:
		return harmNoteActiveTest(toolName, cmd, verbs)
	default:
		return harmNoteReadOnly(toolName, cmd, verbs)
	}
}

// ---- L4 修改与持久化:写入/创建/删除及其留存与察觉风险 ----

// harmNoteModifyPersist L4 三段式。注册类提交(向注册端点 POST 表单)
// 走造物主锚例逐字;删除类单列不可逆;其余写入走通用写入句式。
func harmNoteModifyPersist(toolName, cmd string, verbs []string) string {
	if host, ok := registerSubmitTarget(cmd); ok {
		// 锚例逐字(目标以完整标识呈现):
		// "将向 {host} 提交注册表单创建账号。目标会新增一个可登录账号。
		//  账号会留存于目标系统，管理员可能察觉异常注册。"
		return fmt.Sprintf(
			"将向 %s 提交注册表单创建账号。目标会新增一个可登录账号。账号会留存于目标系统，管理员可能察觉异常注册。", host)
	}
	if hasAnyVerb(verbs, "rm", "rmdir", "shred", "unlink", "truncate") {
		return fmt.Sprintf(
			"将执行 %s 删除目标上的数据。被删除的数据会立即消失。删除可能无法恢复。", verbPhrase(verbs, "删除"))
	}
	return fmt.Sprintf(
		"将执行 %s 修改目标的数据或配置。目标会产生新的内容或设置变化。变更会留存于目标系统，可能无法自动撤销。",
		verbPhrase(verbs, "写入"))
}

// ---- L3 利用深入:接触授权外数据,不修改目标 ----

func harmNoteExploit(toolName, cmd string, verbs []string) string {
	return fmt.Sprintf(
		"将执行 %s 利用已确认的弱点接触目标的信息或功能。目标本身不会发生改变。取得的信息会留在测试记录里。",
		verbPhrase(verbs, "读取"))
}

// ---- L2 主动测试:发送测试载荷,可能触发告警 ----

func harmNoteActiveTest(toolName, cmd string, verbs []string) string {
	return fmt.Sprintf(
		"将执行 %s 向目标发送测试请求。目标的日志会多出这些测试请求。测试流量可能触发目标的安全告警。",
		verbPhrase(verbs, "探测命令"))
}

// ---- L1 只读探测:仅查看,无变化(查看类锚例逐字) ----

// readOnlyEffect 查看类命令 → 效果短语(pwd/ls 组合走锚例句式)。
func readOnlyEffect(verbs []string) string {
	hasPwd, hasLs := hasAnyVerb(verbs, "pwd"), hasAnyVerb(verbs, "ls")
	if hasPwd && hasLs && len(verbs) <= 2 {
		return "pwd 与 ls 查看目录和文件列表"
	}
	only := func(vs ...string) bool {
		for _, v := range verbs {
			if !hasAnyVerb(vs, v) {
				return false
			}
		}
		return len(verbs) > 0
	}
	switch {
	case only("cat", "head", "tail", "less", "more"):
		return strings.Join(verbs, " 与 ") + " 查看文件内容"
	case only("find", "stat", "file", "wc"):
		return strings.Join(verbs, " 与 ") + " 查看文件信息"
	case only("whoami", "id", "uname", "hostname", "env", "ps"):
		return strings.Join(verbs, " 与 ") + " 查看当前环境信息"
	case only("grep", "awk", "sed"):
		return strings.Join(verbs, " 与 ") + " 检索文本"
	}
	if len(verbs) > 0 {
		return strings.Join(verbs, " 与 ") + " 查看目标信息"
	}
	return "查看目标信息"
}

func harmNoteReadOnly(toolName, cmd string, verbs []string) string {
	// 锚例逐字:"将执行 pwd 与 ls 查看目录和文件列表。目标不会发生任何变化。"
	return fmt.Sprintf("将执行 %s。目标不会发生任何变化。", readOnlyEffect(verbs))
}

// ---- 命令特征提取(确定性,零 LLM) ----

// verbRe 命令首词切分:空白与常见连接符(; && || | &&)分段的每段首词。
var verbSplitRe = regexp.MustCompile(`\s*(?:;|&&|\|\||\|)\s*`)

// commandVerbs 命令串里的动词序列(每段首词,去路径前缀;保持出现序,
// 去重;上界 4 个防长串)。
func commandVerbs(cmd string) []string {
	if cmd == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, seg := range verbSplitRe.Split(cmd, -1) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		v := strings.TrimSuffix(fields[0], ";")
		if i := strings.LastIndex(v, "/"); i >= 0 {
			v = v[i+1:]
		}
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= 4 {
			break
		}
	}
	return out
}

func hasAnyVerb(verbs []string, want ...string) bool {
	for _, v := range verbs {
		for _, w := range want {
			if v == w {
				return true
			}
		}
	}
	return false
}

// verbPhrase 动词短语(动作句用):动词拼接,空时退默认动作词。
func verbPhrase(verbs []string, fallback string) string {
	if len(verbs) == 0 {
		return fallback
	}
	return strings.Join(verbs, " 与 ")
}

// registerWordRe 注册类端点特征(路径或参数含注册词)。
var registerWordRe = regexp.MustCompile(`(?i)regist|signup|sign-up|sign_up|create[-_]?user|new[-_]?user|注册|创建账号`)

// postFlagRe 带载荷提交特征(POST/表单/数据体)。
var postFlagRe = regexp.MustCompile(`(?i)(-X\s*POST|--data|-d\s|--form|-F\s|-T\s|--upload)`)

// harmURLRe 命令串里的 URL 特征(http/https)。
var harmURLRe = regexp.MustCompile(`https?://[^\s'"；;]+`)

// hostPortOf URL 的 host[:port] 完整标识(去路径;无端口如实去)。
func hostPortOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

// registerSubmitTarget 判定命令是否"向注册端点提交表单":带载荷提交
// 且目标含注册词 → 返回完整标识 host:port。
func registerSubmitTarget(cmd string) (string, bool) {
	if !postFlagRe.MatchString(cmd) {
		return "", false
	}
	m := harmURLRe.FindString(cmd)
	if m == "" || !registerWordRe.MatchString(cmd) {
		return "", false
	}
	return hostPortOf(m), true
}
