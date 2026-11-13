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
// 变化)+ 导致什么(最坏后果与不可逆性)。确定性模板按命令特征
// 拼装——零 LLM、平实书面白话、禁口语禁机器语言禁命令原文
// 直贴;现组装现服务(不落库,与审批卡同一数据源)。
//
// 锚例(造物主逐字定稿,2026-10-24;目标以完整标识 host:port 呈现,
// 裸端口不上界面——P4-27 ③ 同律):
//
//	注册类(L4):将向 {host} 提交注册表单创建账号。目标会新增一个可
//	  登录账号。账号会留存于目标系统，管理员可能察觉异常注册。
//	查看类(L1):将执行 pwd 与 ls 查看目录和文件列表。目标不会发生
//	  任何变化。
//
// P4-74 ①(问题清单 #90):卡面以命令为准——模板按命令实际内容
// 分级拼装(ClassifyCommandAction),不再拿步骤分级当脸;步骤分级
// 降为背景行(StepGroundNote:"所属步骤含修改类动作,按步骤门槛过门")。
// 命令认不出(动词全不在已知族)时回退步骤分级——分级是既有审计
// 字段,是拿得到的最好陈述。

// HarmNote 危害说明三段式:按命令实际内容分级 + 命令特征确定性拼装
// (P4-74 ①)。命令认不出时回退意图分级——分级缺失(0 级)按只读呈现
// (分级缺失的请求会被拒绝线拦下,到不了卡上,此处只是兜底)。返回
// 空串 = 无可拼装(调用方不渲染该行)。
func HarmNote(level rings.ActionLevel, toolName string, toolArgs json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
		URL     string `json:"url"`
	}
	_ = json.Unmarshal(toolArgs, &args)

	cmd := strings.TrimSpace(args.Command)
	if cmdLevel, ok := ClassifyCommandAction(toolName, toolArgs); ok {
		level = cmdLevel
	}
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

// StepGroundNote 步骤分级背景行(P4-74 ①):命令实际分级低于所属步骤
// 分级时,说明这张卡为什么弹——步骤含更高危动作、按步骤门槛过门。
// 无落差(命令认不出或分级不低于步骤)返回空串,不渲染。
func StepGroundNote(stepLevel rings.ActionLevel, toolName string, toolArgs json.RawMessage) string {
	cmdLevel, ok := ClassifyCommandAction(toolName, toolArgs)
	if !ok || ActionLevelRank(cmdLevel) >= ActionLevelRank(stepLevel) {
		return ""
	}
	switch stepLevel {
	case rings.ActionModifyPersist:
		return "所属步骤含修改类动作，按步骤门槛过门"
	case rings.ActionExploit:
		return "所属步骤含利用类动作，按步骤门槛过门"
	case rings.ActionActiveTest:
		return "所属步骤含测试类动作，按步骤门槛过门"
	}
	return ""
}

// ---- 命令实际内容分级(P4-74 ①,确定性零 LLM) ----

// 命令动词族:按动词判定这条命令实际做什么(与意图分级解耦——
// 步骤分级是意图拆解的预期,命令才是真要执行的内容)。
var (
	// writeVerbs 写入/删除/改配置类(L4):目标会留痕或数据会变。
	writeVerbs = []string{
		"rm", "rmdir", "shred", "unlink", "truncate", "mkdir", "touch",
		"mv", "cp", "tee", "dd", "chmod", "chown", "chattr", "ln",
		"useradd", "adduser", "userdel", "usermod", "groupadd", "groupdel",
		"passwd", "chpasswd", "crontab", "mount", "umount", "swapoff",
		"insmod", "rmmod", "modprobe", "sysctl", "systemctl", "service",
		"iptables", "ufw", "firewall-cmd", "setcap", "chage",
	}
	// exploitVerbs 数据接入类(L3):以既得条件接触目标数据或功能。
	exploitVerbs = []string{
		"mysql", "mysqldump", "psql", "pg_dump", "redis-cli", "mongo",
		"mongosh", "ssh", "scp", "sftp", "telnet", "ftp", "smbclient",
		"rpcclient", "ldapsearch", "vncviewer",
	}
	// testVerbs 主动测试类(L2):向目标发测试载荷(扫描/爆破/注入)。
	testVerbs = []string{
		"nmap", "masscan", "naabu", "rustscan", "nuclei", "sqlmap",
		"hydra", "medusa", "john", "hashcat", "gobuster", "ffuf",
		"dirb", "dirsearch", "wfuzz", "nikto", "whatweb", "wpscan",
		"skipfish", "commix", "arachni", "onesixtyone", "snmpcheck",
		"enum4linux", "crackmapexec", "netexec", "dotdotpwn",
	}
	// netReadVerbs 网络读取类(L1):拉取响应/连通测距,不发载荷。
	netReadVerbs = []string{
		"curl", "wget", "ping", "ping6", "dig", "nslookup", "host",
		"traceroute", "traceroute6", "tracepath", "fping", "arping",
		"nc", "ncat", "socat", "openssl",
	}
	// sedInPlaceRe sed 原地改写特征(-i);不带 -i 的 sed 是文本流变换。
	sedInPlaceRe = regexp.MustCompile(`(^|\s)-i\b`)
	// writeRedirectRe 输出重定向特征(> 与 >>;先剥 2>/dev/null 与
	// >/dev/null 这类无害丢弃,再查余下重定向——echo x > file 是写)。
	devNullRedirectRe = regexp.MustCompile(`[0-9]?>{1,2}\s*/dev/null`)
	writeRedirectRe   = regexp.MustCompile(`>{1,2}[^&]`)
	// readOnlyToolNames 引擎侧只读工具(工具名即判 L1:探测/拉取/本地
	// 读/查账,不写目标)。
	readOnlyToolNames = map[string]bool{
		"port_probe": true, "http_probe": true,
		"web_fetch": true, "web_search": true, "read": true,
	}
)

// ClassifyCommandAction 按命令实际内容判分级(P4-74 ①):bash 按动词
// 族与提交/重定向特征判,只读工具按工具名判 L1。多段命令(;;&&|||)
// 按最高危段判——命令里含哪个族的动作就按哪个族说。认不出(动词
// 全不在已知族/无命令)返回 ok=false,调用方回退步骤分级。
func ClassifyCommandAction(toolName string, toolArgs json.RawMessage) (rings.ActionLevel, bool) {
	if readOnlyToolNames[toolName] {
		return rings.ActionReadOnly, true
	}
	if toolName != "bash" {
		return "", false
	}
	var args struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(toolArgs, &args)
	cmd := strings.TrimSpace(args.Command)
	if cmd == "" {
		return "", false
	}
	verbs := commandVerbs(cmd)
	if len(verbs) == 0 {
		return "", false
	}
	// 带载荷提交(curl/wget/nc 加 POST/数据/上传旗标)＝写入。
	postish := hasAnyVerb(verbs, "curl", "wget", "nc", "ncat") && postFlagRe.MatchString(cmd)
	// 输出重定向到真实文件＝写入(剥掉 >/dev/null 丢弃后仍有 >)。
	redir := writeRedirectRe.MatchString(devNullRedirectRe.ReplaceAllString(cmd, ""))
	switch {
	case postish || redir || hasAnyVerb(verbs, writeVerbs...) ||
		(hasAnyVerb(verbs, "sed") && sedInPlaceRe.MatchString(cmd)):
		return rings.ActionModifyPersist, true
	case hasAnyVerb(verbs, exploitVerbs...):
		return rings.ActionExploit, true
	case hasAnyVerb(verbs, testVerbs...):
		return rings.ActionActiveTest, true
	}
	// 只读成立要全部动词都在只读族(本地查看族 + 网络读取族)——夹一
	// 个未知动词就回退步骤分级,不猜。
	if ro := readOnlyVerbs(verbs); len(ro) == len(verbs) {
		return rings.ActionReadOnly, true
	}
	return "", false
}

// readOnlyVerbs verbs 里属只读族的动词子集(readOnlyEffect 已知族 +
// 网络读取族)。
func readOnlyVerbs(verbs []string) []string {
	var out []string
	for _, v := range verbs {
		switch v {
		case "pwd", "ls", "cat", "head", "tail", "less", "more",
			"find", "stat", "file", "wc", "whoami", "id", "uname",
			"hostname", "env", "printenv", "ps", "grep", "awk", "sed",
			"which", "whereis", "type", "du", "df", "free", "date",
			"uptime", "who", "last", "w", "netstat", "ss", "lsof",
			"ifconfig", "ip", "arp", "getent":
			out = append(out, v)
		default:
			if hasAnyVerb(netReadVerbs, v) {
				out = append(out, v)
			}
		}
	}
	return out
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
	// 网络读取族(curl/wget/ping/dig/nc…):拉取响应与连通测距——全部
	// 动词都是网络读取才用族短语,混本地查看动词走通用句。
	if len(verbs) > 0 {
		allNet := true
		for _, v := range verbs {
			if !hasAnyVerb(netReadVerbs, v) {
				allNet = false
				break
			}
		}
		if allNet {
			return strings.Join(verbs, " 与 ") + " 探测目标连通性与响应，仅获取信息"
		}
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

// postFlagRe 带载荷提交特征(POST/表单/数据体;--post 覆盖 wget 的
// --post-data/--post-file 两种形态)。
var postFlagRe = regexp.MustCompile(`(?i)(-X\s*POST|--data|--post|-d[\s'"]|--form|-F[\s'"]|-T[\s'"]|--upload)`)

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
