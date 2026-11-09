// P4-44 ③(问题清单 #61)→ P4-51 ①(问题清单 #73②):渗透任务的范围
// 外拦截,从"直接拒绝留痕＋熔断"改为阻塞等批准。判定面 = 任务包冻结
// 的种子闭包(种子/手动/已放行资产的主机锚点集,按写法粒度授权);
// 判定对象 = 一次工具调用实际发起连接的目标位置。命中后按目标现值
// 分流:已排除(禁止测试/RoE 摘除)直接拒留痕熔断(防骚扰);已获准
// 但本单位闭包过期,步骤回可领取重领;其余出待决卡(同一目标一张卡,
// RequestID=sc-<host[:port]>)+单位冻结收场让位名额(步骤落 scope_wait
// 态,批准后插队队首重领原会话续跑)。挂在两处:bash 等非只读工具在
// 审批门回调(approvalGate),只读探测(port_probe/http_probe)在门卫
// 补口(guardReadOnlyCall)。红队/未标任务闭包为 nil,不拦(可否决卡
// 在收割侧入图时出,见 overmind/harvest.go)。
//
// P4-50 ①(问题清单 #73):提取面收窄治误伤——bash 命令只提取实际
// 发起连接的目标位置(curl/wget 目标 URL、Host 头值、--resolve 与
// openssl -servername 的目标侧、openssl -connect、/dev/(tcp|udp)/
// host/port、nmap/nc 等扫描工具的目标参数位);UA 字符串、Referer/
// XFF 等请求头值、注入载荷参数值里的网址、版本号数字一概不当目标
// (留痕不拦——与"防漂移不防规避"哲学一致:这些面本拦不住真越界、
// 只误伤正常测试)。变量拼接照旧跳过(实际去向由镜流量留痕对账)。
package swarm

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"ghostreaper/internal/rings"
)

// hostFormRe 合法主机形态(域名/IPv4 字面量;IPv6 由 net.ParseIP 认)。
// bash 命令文本里经 shell 变量拼接的目标会提取出 "$hp" 这类非主机
// 形态——无法判定主机归属,跳过。
var hostFormRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

// plausibleHost 提取出的目标是否为可判定的主机形态。
func plausibleHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	return net.ParseIP(h) != nil || hostFormRe.MatchString(h)
}

// reservedTLDR 保留 TLD(RFC 2606:invalid/example/test/localhost)——
// 永不可能真实解析,是测试对照名(如缓存对比的假 Host),不是实际
// 连接目标,提取时过滤。
func reservedTLDR(host string) bool {
	i := strings.LastIndex(host, ".")
	if i < 0 {
		return false
	}
	switch host[i+1:] {
	case "invalid", "example", "test", "localhost":
		return true
	}
	return false
}

// scopeCallTargets 一次调用指向的目标主机(http_probe/port_probe 参数、
// bash 命令实际发起连接的目标位置)。web_fetch/read/web_search 无目标
// 提取(外网查资料不构成对目标的测试,不在硬拦面;流量留痕照旧)。
func scopeCallTargets(toolName string, input json.RawMessage) []hostPort {
	var raw []hostPort
	switch toolName {
	case "http_probe", "port_probe":
		raw = callTargets(toolName, input)
	case "bash":
		raw = bashScopeTargets(bashCommandOf(input))
	}
	out := make([]hostPort, 0, len(raw))
	for _, t := range raw {
		if plausibleHost(t.host) && !reservedTLDR(t.host) {
			out = append(out, t)
		}
	}
	return out
}

// ---- bash 命令目标提取(P4-50 ① 收窄面) ----

// shToken shell 分词后的一个词(引号剥除);vari = 含 $ 展开(值不可
// 知,按提取位语义取舍)。
type shToken struct {
	text string
	vari bool
}

// heredocStartRe heredoc 起始标记(<<[-]'EOF' / <<"EOF" / <<EOF;起止
// 引号宽容,分隔词 m[2])。
var heredocStartRe = regexp.MustCompile(`<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?`)

// stripHeredocs 剥除 heredoc 块(<<'EOF'…EOF 整块)——heredoc 是数据
// (UA 清单/脚本内容),不是命令,其中的网址与数字不当目标。
func stripHeredocs(cmd string) string {
	var b strings.Builder
	inHeredoc := false
	var delim string
	for _, line := range strings.Split(cmd, "\n") {
		if inHeredoc {
			if strings.TrimSpace(line) == delim {
				inHeredoc = false
			}
			continue
		}
		if m := heredocStartRe.FindStringSubmatch(line); m != nil {
			delim = m[2]
			inHeredoc = true
			b.WriteString(line[:strings.Index(line, m[0])] + "\n")
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// shellSegments 简化 shell 分词并把命令切成段:空白分词,引号(单引号
// 原样/双引号认 $)与反斜杠转义,&& || ; | & 换行为段分隔,行内裸 #
// 起注释截断。不求完整 shell 语义——只求目标位置提取可靠:引号内
// 分隔符不切词,值不可知的 $ 词做标记由提取位取舍。
func shellSegments(cmd string) [][]shToken {
	var segs [][]shToken
	var cur []shToken
	var tok strings.Builder
	vari := false
	inSingle, inDouble := false, false
	flush := func() {
		if tok.Len() > 0 {
			cur = append(cur, shToken{text: tok.String(), vari: vari})
		}
		tok.Reset()
		vari = false
	}
	endSeg := func() {
		flush()
		if len(cur) > 0 {
			segs = append(segs, cur)
		}
		cur = nil
	}
	src := []byte(cmd)
	for i := 0; i < len(src); i++ {
		ch := src[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				tok.WriteByte(ch)
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			} else {
				if ch == '$' {
					vari = true
				}
				tok.WriteByte(ch)
			}
		default:
			switch ch {
			case '\'':
				flush()
				inSingle = true
			case '"':
				flush()
				inDouble = true
			case '\\':
				if i+1 < len(src) {
					tok.WriteByte(src[i+1])
					i++
				}
			case '$':
				vari = true
				tok.WriteByte(ch)
			case '#':
				if tok.Len() == 0 {
					i = len(src) // 段内裸 # 起是注释
				} else {
					tok.WriteByte(ch)
				}
			case ' ', '\t', '\r':
				flush()
			case '\n', ';', '&', '|':
				flush()
				endSeg()
				if i+1 < len(src) && (src[i+1] == '&' || src[i+1] == '|') {
					i++
				}
			default:
				tok.WriteByte(ch)
			}
		}
	}
	endSeg()
	return segs
}

// shellWrapCmds 段首包装命令:跳过后才到实际命令(timeout/sudo/env/
// nohup/nice/time/command/exec);VAR=xxx 环境前缀同理跳过。
var shellWrapCmds = map[string]bool{
	"timeout": true, "sudo": true, "env": true, "nohup": true,
	"nice": true, "time": true, "command": true, "exec": true,
}

// segmentLead 段的命令词与其后 token 起点索引(跳过包装命令及其参数、
// VAR=xxx 环境前缀;分不出返回 "")。包装命令(timeout N cmd)的数字
// 参数一并跳过。
func segmentLead(toks []shToken) (string, int) {
	for i, t := range toks {
		if t.vari {
			continue
		}
		if isAllDigits(t.text) {
			continue // timeout 等包装命令的数值参数
		}
		if j := strings.Index(t.text, "="); j > 0 {
			continue // VAR=xxx 环境前缀
		}
		if !shellWrapCmds[t.text] {
			return t.text, i + 1
		}
	}
	return "", len(toks)
}

// devTcpRe /dev/(tcp|udp)/host/port 形态(bash 内建重定向,实际连接
// 目标;host 与 port 均认 $ 变量形态,变量跳过)。对词原文匹配(重定
// 向符 3<>/dev/tcp/… 与词连写,不依赖分词边界)。m[2]=host、m[3]=port。
var devTcpRe = regexp.MustCompile(`/dev/(tcp|udp)/([^/\s'"|;&]+|\$\{?[A-Za-z_][A-Za-z0-9_]*\}?)/([^/\s'"|;&<>]+)`)

// 提取位的值选项表按工具分家(同短选项跨工具语义相反的教训在案:curl
// 的 -s 是静默布尔、nc 的 -s 是源地址值选项——合并表会把 curl -s 后
// 的目标 URL 当值吞掉)。表外未知值选项的值可能被误当位置参数——只
// 影响多提取,过滤面(plausibleHost/保留 TLD)兜底。
var (
	// httpFlagValue curl/wget 的值选项(后随词是值;-H/--header 与
	// --resolve 另走专项提取)。
	httpFlagValue = map[string]bool{
		"-A": true, "--user-agent": true, "-e": true, "--referer": true,
		"-b": true, "--cookie": true, "-d": true, "--data": true,
		"--data-raw": true, "--data-binary": true, "--data-urlencode": true,
		"--json": true, "-F": true, "--form": true, "-o": true, "--output": true,
		"-O": true, "--output-document": true, "-w": true, "--write-out": true,
		"-D": true, "--dump-header": true, "-m": true, "--max-time": true,
		"--connect-timeout": true, "-x": true, "--proxy": true, "--preproxy": true,
		"--noproxy": true, "-u": true, "--user": true, "-X": true, "--request": true,
		"-T": true, "--upload-file": true, "--cert": true, "--key": true,
		"--cacert": true, "-E": true, "--interface": true, "-K": true,
		"--config": true, "--retry": true, "--limit-rate": true, "--url": true,
		"--load-cookies": true, "--save-cookies": true, "--post-data": true,
		"--post-file": true, "--ciphers": true, "--pass": true,
	}
	// scanFlagValue nmap/masscan/rustscan 的值选项(-p 端口另走专项)。
	scanFlagValue = map[string]bool{
		"--script": true, "--script-args": true, "--script-args-file": true,
		"-oN": true, "-oG": true, "-oX": true, "-oA": true, "-oM": true, "-oS": true,
		"-iL": true, "--exclude": true, "--excludefile": true, "-S": true,
		"-D": true, "--proxies": true, "--datadir": true, "--servicedb": true,
		"--host-timeout": true, "--scan-delay": true, "--max-retries": true,
		"--max-rtt-timeout": true, "--min-rtt-timeout": true, "--rate": true,
		"--max-rate": true, "-g": true, "--source-port": true,
	}
	// ncFlagValue nc/ncat/netcat 的值选项。
	ncFlagValue = map[string]bool{
		"-w": true, "-s": true, "--source": true, "-X": true, "-x": true,
		"--proxy-type": true, "--ssl-cert": true, "--ssl-key": true, "-i": true,
		"--max-conns": true, "-c": true, "--sh-exec": true, "--proxy-auth": true,
		"-p": true, // 本地源端口(监听/出站均非目标端口)
	}
	// opensslFlagValue openssl s_client 的值选项(-connect/-servername
	// 另走专项)。
	opensslFlagValue = map[string]bool{
		"-proxy": true, "-cipher": true, "-CAfile": true, "-CApath": true,
		"-verify_hostname": true, "-cert": true, "-key": true, "-pass": true,
		"-curves": true, "-groups": true, "-sigalgs": true,
	}
)

// urlTokenTarget http(s) URL 词 → 目标(scheme 默认端口补全 http 80 /
// https 443;host 含 $ 变量不可知则跳过)。
func urlTokenTarget(s string) (hostPort, bool) {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return hostPort{}, false
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return hostPort{}, false
	}
	host := strings.ToLower(u.Hostname())
	if !plausibleHost(host) {
		return hostPort{}, false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return hostPort{host: host, port: port}, true
}

// hostHeaderTarget Host 头值("Host: x.com[:port]" 形态)→ 目标。空值
// (删头写法 Host:)与其他头(XFF/Referer 等)返回 false——元数据头不
// 是连接目标。
func hostHeaderTarget(val string) (hostPort, bool) {
	if len(val) < 5 || !strings.EqualFold(val[:4], "host") || val[4] != ':' {
		return hostPort{}, false
	}
	rest := strings.TrimSpace(val[5:])
	if rest == "" {
		return hostPort{}, false
	}
	host, port := rest, ""
	if h, p, err := net.SplitHostPort(rest); err == nil && h != "" && p != "" {
		host, port = strings.ToLower(h), p
	} else {
		host = strings.ToLower(rest)
	}
	if !plausibleHost(host) {
		return hostPort{}, false
	}
	return hostPort{host: host, port: port}, true
}

// resolveFlagTarget --resolve HOST:PORT:ADDRESS 的目标侧 HOST(实际
// 测试谁);ADDRESS 是对照定向值,不当目标。HOST 含变量跳过;PORT 含
// 变量按端口不可知(仅判 host)。
func resolveFlagTarget(val string) (hostPort, bool) {
	parts := strings.Split(val, ":")
	if len(parts) < 3 {
		return hostPort{}, false // 含 IPv6 的多段形态,不强行解析
	}
	host := strings.ToLower(parts[0])
	if !plausibleHost(host) {
		return hostPort{}, false
	}
	port := parts[1]
	if port == "" || !isAllDigits(port) {
		port = "" // 变量/空/非数字:端口不可知
	}
	return hostPort{host: host, port: port}, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// connectFlagTarget openssl -connect host[:port] / nc 位置参数 host[:port]
// 形态 → 目标。含变量跳过。
func connectFlagTarget(val string) (hostPort, bool) {
	if !plausibleHost(strings.Split(val, ":")[0]) {
		return hostPort{}, false
	}
	host, port := val, ""
	if h, p, err := net.SplitHostPort(val); err == nil && h != "" && p != "" {
		host, port = strings.ToLower(h), p
	} else {
		host = strings.ToLower(val)
	}
	if !plausibleHost(host) {
		return hostPort{}, false
	}
	return hostPort{host: host, port: port}, true
}

// httpToolTargets curl/wget 参数扫描:目标 URL 位置参数(http(s) 词)、
// Host 头值(-H/--header 专项)、--resolve 目标侧;其余值选项的值跳过。
func httpToolTargets(toks []shToken) []hostPort {
	var out []hostPort
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if !strings.HasPrefix(t.text, "-") || t.vari {
			if !t.vari {
				if hp, ok := urlTokenTarget(t.text); ok {
					out = append(out, hp)
				}
			}
			continue
		}
		flag, inline := t.text, ""
		if j := strings.Index(flag, "="); j > 0 {
			flag, inline = flag[:j], flag[j+1:]
		}
		val := inline
		switch flag {
		case "-H", "--header", "--resolve":
			if val == "" && i+1 < len(toks) {
				val = toks[i+1].text
				i++
			}
			var hp hostPort
			var ok bool
			if flag == "--resolve" {
				hp, ok = resolveFlagTarget(val)
			} else {
				hp, ok = hostHeaderTarget(val)
			}
			if ok {
				out = append(out, hp)
			}
		default:
			if httpFlagValue[flag] && val == "" {
				i++ // 值选项:吞掉后随值
			}
		}
	}
	return out
}

// opensslTargets openssl s_client 的 -connect(实际连接处)与
// -servername(SNI 目标侧)。非 s_client 子命令不联网,不提取。
func opensslTargets(toks []shToken) []hostPort {
	sclient := false
	for _, t := range toks {
		if t.text == "s_client" {
			sclient = true
			break
		}
	}
	if !sclient {
		return nil
	}
	var out []hostPort
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.vari || t.text == "s_client" {
			continue
		}
		if strings.HasPrefix(t.text, "-") {
			flag, inline := t.text, ""
			if j := strings.Index(flag, "="); j > 0 {
				flag, inline = flag[:j], flag[j+1:]
			}
			if flag != "-connect" && flag != "-servername" {
				if opensslFlagValue[flag] && inline == "" {
					i++
				}
				continue
			}
			val := inline
			if val == "" && i+1 < len(toks) {
				val = toks[i+1].text
				i++
			}
			if hp, ok := connectFlagTarget(val); ok {
				out = append(out, hp)
			}
		}
	}
	return out
}

// scanHostToken nmap/masscan 等位置参数的主机形态:IPv4、IPv4 范围
// (a.b.c.d-N,取首 IP 判定——授权闭包为单主机粒度,段授权必然不匹
// 配,防漂移取保守)、CIDR(取网络地址判定,同理)、域名。返回空串
// 表示不是主机形态。
func scanHostToken(s string) string {
	low := strings.ToLower(s)
	if m := ipv4RangeRe.FindStringSubmatch(low); m != nil {
		low = m[1] // a.b.c.d-N 范围:取首 IP 判定(授权为单主机粒度)
	} else if m := cidrRe.FindStringSubmatch(low); m != nil {
		low = m[1] // CIDR:取网络地址判定(同理,段授权必然不匹配)
	}
	if plausibleHost(low) && !strings.ContainsAny(low, "*[]") {
		return low
	}
	return ""
}

var (
	ipv4RangeRe = regexp.MustCompile(`^(\d{1,3}(\.\d{1,3}){3})-\d{1,3}$`)
	cidrRe      = regexp.MustCompile(`^(\d{1,3}(\.\d{1,3}){3})/\d{1,2}$`)
)

// scanTargets nmap/masscan/rustscan 参数扫描(参数从命令词之后起):
// 跳过值选项的值,位置参数里的主机形态即目标;-p 端口列表(80,443 或
// 1-100)解析后逐端口绑定目标(无 -p 的裸目标 = 全端口意图)。
func scanTargets(toks []shToken) []hostPort {
	var out []hostPort
	var ports []string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.vari {
			continue
		}
		if strings.HasPrefix(t.text, "-") {
			flag, inline := t.text, ""
			if j := strings.Index(flag, "="); j > 0 {
				flag, inline = flag[:j], flag[j+1:]
			}
			if flag == "-p" || flag == "--ports" {
				val := inline
				if val == "" && i+1 < len(toks) {
					val = toks[i+1].text
					i++
				}
				ports = scanPortList(val)
				continue
			}
			if scanFlagValue[flag] && inline == "" && i+1 < len(toks) {
				i++ // 值选项:吞掉后随值
			}
			continue
		}
		if host := scanHostToken(t.text); host != "" {
			if len(ports) == 0 {
				out = append(out, hostPort{host: host})
			}
			for _, p := range ports {
				out = append(out, hostPort{host: host, port: p})
			}
		}
	}
	return out
}

// scanPortList nmap -p 端口列表(80,443 / 1-100 / T:80,U:53)→ 端口串
// 清单;范围段展开上限 256 防超长,非法项跳过。
func scanPortList(val string) []string {
	var out []string
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if k := strings.Index(part, ":"); k >= 0 { // T:80/U:53 协议前缀
			part = part[k+1:]
		}
		if part == "" || !isAllDigits(part) {
			if m := portRangeRe.FindStringSubmatch(part); m != nil {
				lo, hi := atoiClamp(m[1]), atoiClamp(m[2])
				if hi < lo {
					lo, hi = hi, lo
				}
				if hi-lo > 256 {
					hi = lo + 256
				}
				for p := lo; p <= hi; p++ {
					out = append(out, fmt.Sprintf("%d", p))
				}
			}
			continue
		}
		out = append(out, part)
	}
	return out
}

var portRangeRe = regexp.MustCompile(`^(\d{1,5})-(\d{1,5})$`)

func atoiClamp(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 65535
		}
	}
	return n
}

// ncTargets nc/ncat/netcat 参数扫描(参数从命令词之后起):-l 监听模式
// 不外连不提取;位置参数第一个主机形态是目标 host,其后首个纯数字位
// 置参数是端口。
func ncTargets(toks []shToken) []hostPort {
	for _, t := range toks {
		if !t.vari && t.text == "-l" {
			return nil
		}
	}
	var out []hostPort
	portTaken := false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.vari {
			continue
		}
		if strings.HasPrefix(t.text, "-") {
			flag, inline := t.text, ""
			if j := strings.Index(flag, "="); j > 0 {
				flag, inline = flag[:j], flag[j+1:]
			}
			if ncFlagValue[flag] && inline == "" && i+1 < len(toks) {
				i++ // 值选项:吞掉后随值
			}
			continue
		}
		if isAllDigits(t.text) {
			if !portTaken && len(out) > 0 {
				out[len(out)-1].port = t.text
				portTaken = true
			}
			continue
		}
		if hp, ok := connectFlagTarget(t.text); ok {
			out = append(out, hp)
		}
	}
	return out
}

// bashScopeTargets bash 命令文本 → 实际发起连接的目标位置(P4-50 ①
// 收窄面):剥 heredoc 与注释 → 分段分词 → /dev/(tcp|udp) 形态正则 +
// 工具分派(参数取命令词之后;curl/wget 目标 URL 与 Host 头与
// --resolve 侧、openssl s_client、nmap 族扫描目标、nc 族)。其余工具
// (dig/ping/python 等)与命令替换/$() 不提取——留痕兜底,与"防漂移
// 不防规避"一致。
func bashScopeTargets(cmd string) []hostPort {
	var out []hostPort
	for _, seg := range shellSegments(stripHeredocs(cmd)) {
		for _, tok := range seg {
			if m := devTcpRe.FindStringSubmatch(tok.text); m != nil &&
				plausibleHost(m[2]) && !strings.HasPrefix(m[2], "$") && isAllDigits(m[3]) {
				out = append(out, hostPort{host: strings.ToLower(m[2]), port: m[3]})
			}
		}
		lead, argsAt := segmentLead(seg)
		args := seg[argsAt:]
		switch lead {
		case "curl", "wget":
			out = append(out, httpToolTargets(args)...)
		case "openssl":
			out = append(out, opensslTargets(seg)...)
		case "nmap", "masscan", "rustscan":
			out = append(out, scanTargets(args)...)
		case "nc", "ncat", "netcat":
			out = append(out, ncTargets(args)...)
		}
	}
	return out
}

// targetInScope 目标(host+可选端口)是否在任务包闭包内(带单位级
// 判定缓存:host:port 一次判定不再重复;种子域名解析缓存在闭包内,
// 多单位共享)。解析器可注入(测试打桩;nil=DefaultScopeResolver)。
func (env *unitEnv) targetInScope(pack *TaskPack, host, port string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || pack.ScopeClosure == nil {
		return true // 无闭包(红队/未标)无从判越界,放行
	}
	key := host
	if port != "" {
		key += ":" + port
	}
	env.scopeMu.Lock()
	if env.scopeTried == nil {
		env.scopeHits, env.scopeTried = map[string]bool{}, map[string]bool{}
	}
	if _, ok := env.scopeTried[key]; ok {
		hit := env.scopeHits[key]
		env.scopeMu.Unlock()
		return hit
	}
	env.scopeMu.Unlock()
	resolve := env.scopeResolve
	if resolve == nil {
		resolve = rings.DefaultScopeResolver
	}
	hit := pack.ScopeClosure.CoversTarget(host, port, resolve)
	env.scopeMu.Lock()
	env.scopeTried[key] = true
	env.scopeHits[key] = hit
	env.scopeMu.Unlock()
	return hit
}

// denyScope 范围外拦截的统一挂点(P4-51 ① 从"直接拒＋熔断"改为阻塞等
// 批准):调用目标里首个不在闭包内的主机,按目标现值分流——已排除
// (禁止测试/摘除)直接拒留痕熔断(防骚扰,不再出卡);已获准但本单位
// 闭包过期(领取时快照)步骤回可领取重领新包;其余拦下出待决卡(同一
// 目标一张卡,去重)＋单位冻结收场让位名额(env.scopeHold,drive 收段
// 后落 scope_wait 态)。返回人话原因(审批门/门卫补口据此回给模型);
// 无命中/无闭包返回空串放行。display 为主机呈现(host[:port] 原样)。
func (env *unitEnv) denyScope(pack *TaskPack, toolName string, input json.RawMessage) string {
	if pack.ScopeClosure == nil || pack.ScopeClosure.Empty() {
		return ""
	}
	for _, t := range scopeCallTargets(toolName, input) {
		if env.targetInScope(pack, t.host, t.port) {
			continue
		}
		display := t.host
		if t.port != "" {
			display += ":" + t.port
		}
		// 复测单位(P3-06)不进冻结:侧会话不经主宰调度,scope_wait 态
		// 无人唤醒——照旧直接拒(复测目标由面板复测发起时圈定)。
		if env.retest != nil {
			reason := fmt.Sprintf(
				"任务范围外:目标 %s 不在本任务授权范围内,复测会话不适用等待批准,换授权范围内的方向", display)
			env.denyGuard(pack, toolName, input, rings.DenyLineRoeScope, reason)
			return reason
		}
		now := env.scopeNowLazy(pack)
		// ① 已排除目标(禁止测试/RoE 摘除,同一排除机制):直接拒不出卡
		//(裁决在案,翻案走 RoE 勾回)。
		if now.excluded != nil && rings.ExcludedCoversTarget(now.assets, now.excluded, t.host, t.port) {
			reason := fmt.Sprintf(
				"任务范围外:目标 %s 已被禁止测试(排除在授权范围外,不再出卡),换授权范围内的方向", display)
			env.denyGuard(pack, toolName, input, rings.DenyLineRoeScope, reason)
			return reason
		}
		// ② 目标已获准(现值闭包含它),本单位闭包是领取时快照:步骤回
		// 可领取,重领的新任务包含该目标,原会话 Resume 续跑重发命令。
		resolve := env.scopeResolve
		if resolve == nil {
			resolve = rings.DefaultScopeResolver
		}
		if now.closure != nil && now.closure.CoversTarget(t.host, t.port, resolve) {
			env.scopeReclaim = true
			env.fuseSegment()
			return fmt.Sprintf(
				"目标 %s 已加入任务范围;本单位任务包为领取时快照,即将重新领取后继续执行该命令(已执行过的动作勿重复)", display)
		}
		// ③ 出待决卡(同一目标一张卡去重);该卡已裁"禁止测试"的不再
		// 重复出卡,直接拒。
		rid := scopeCardID(t.host, t.port)
		switch env.scopeCardState(pack, rid) {
		case scopeCardDenied:
			reason := fmt.Sprintf(
				"任务范围外:目标 %s 已被禁止测试(不再出卡),换授权范围内的方向", display)
			env.denyGuard(pack, toolName, input, rings.DenyLineRoeScope, reason)
			return reason
		case scopeCardPending:
			// 已有待决卡:并入等待,不重复出卡。
		case scopeCardAllowed:
			// 卡已裁"加入资产"(②闭包现值判定的兜底路径):按闭包过期处理,
			// 步骤回可领取重领新包。
			env.scopeReclaim = true
			env.fuseSegment()
			return fmt.Sprintf(
				"目标 %s 已加入任务范围;本单位任务包为领取时快照,即将重新领取后继续执行该命令(已执行过的动作勿重复)", display)
		default:
			env.recordApproval(rings.ApprovalRecord{
				RequestID: rid, StepID: pack.StepID, UnitID: env.UnitID,
				Type: rings.ApprovalRecScopeWait, ActionLevel: pack.ActionLevel,
				HarmClass: pack.HarmClass, Intent: pack.StepIntent,
				ToolName: toolName, ToolArgs: input,
				Note: "渗透任务:命令目标 " + display + " 在范围外,拦下等待批准(加入资产/禁止测试)",
			})
		}
		env.scopeHold = &scopeHoldInfo{host: t.host, port: t.port, disp: display}
		env.fuseSegment()
		return fmt.Sprintf(
			"该命令已拦下等待批准:目标 %s 在任务范围外,已提交面板待决(同一目标一张卡);本单位暂停让位,批准后自动继续执行这条命令", display)
	}
	return ""
}

// scopeHoldInfo 一次范围冻结的目标上下文(P4-51 ①):drive 收段后据此
// 让单位冻结收场(session_finished 带 scope_wait 标记,步骤态携带目标
// 供对账唤醒重判)。
type scopeHoldInfo struct {
	host string
	port string
	disp string
}

// scopeCardID 范围待决卡请求号(同一目标同一张卡,写法即授权粒度——
// 端口不同即不同授权问题);裁决记录同 RequestID 收口。统一走
// rings.ScopeCardID(server/overmind 同源)。
func scopeCardID(host, port string) string {
	return rings.ScopeCardID(host, port)
}

// scopeNowState 范围判定的"现值"缓存(单位级,一次构建):现 RoE 与现
// 资产构建的闭包/排除集——批准通道改的是现值,重领后才冻结进任务包。
type scopeNowState struct {
	loaded   bool
	closure  *rings.ScopeClosure
	excluded map[string]bool
	assets   []rings.NodeFull
}

// scopeNowLazy 现值懒加载;读失败如实留空集(保守方向:不触发重领,
// 走出卡冻结——由裁决通道兜底)。
func (env *unitEnv) scopeNowLazy(pack *TaskPack) *scopeNowState {
	env.scopeMu.Lock()
	defer env.scopeMu.Unlock()
	if env.scopeNow == nil {
		env.scopeNow = &scopeNowState{}
	}
	st := env.scopeNow
	if st.loaded {
		return st
	}
	if roe, err := env.Executor.Store.CurrentRoE(guardLedgerCtx, pack.TaskID); err == nil {
		st.excluded = roe.ExcludedAssetSet()
		approved := roe.ScopeApprovedSet()
		if approved == nil {
			approved = map[string]bool{}
		}
		if assets, aerr := env.Executor.Store.NodesByType(guardLedgerCtx, pack.TaskID, rings.NodeAsset, ""); aerr == nil {
			st.assets = assets
			// 摘除即去权(P4-54 修复):排除资产不贡献现值闭包。
			st.closure = rings.BuildScopeClosure(rings.ScopeSeedsOf(assets), approved, st.excluded)
		}
	}
	st.loaded = true
	return st
}

// 范围待决卡在账本里的状态(按 RequestID 的记录序列判定)。
const (
	scopeCardOpen    = iota // 无卡:出卡冻结
	scopeCardPending        // 待决中:并入等待不重复出卡
	scopeCardDenied         // 已裁禁止测试:直接拒不出卡
	scopeCardAllowed        // 已裁加入资产:闭包现值兜底(重领路径)
)

// scopeCardState 该目标待决卡的当前状态(approval_records 投影按
// RequestID 查;rev 升序留在最后的即最新)。
func (env *unitEnv) scopeCardState(pack *TaskPack, rid string) int {
	rows, err := env.Executor.Store.ApprovalByRequest(guardLedgerCtx, pack.TaskID, rid)
	if err != nil {
		return scopeCardPending // 读不出按已有卡保守处理(不出新卡,冻结等待裁决)
	}
	state := scopeCardOpen
	for _, r := range rows {
		switch r.Type {
		case rings.ApprovalRecScopeWait:
			state = scopeCardPending
		case rings.ApprovalRecDenied:
			state = scopeCardDenied
		case rings.ApprovalRecApproved:
			state = scopeCardAllowed
		}
	}
	return state
}
