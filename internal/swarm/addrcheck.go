// P4-72(问题清单 #88 B 方案,实测修正批次第一阶)第零层·目标识别的
// 第一道·语法守门:"长得像不像地址"的同源单函数,零成本滤掉零争议
// 垃圾——裸数字(nmap --min-rate 400 的 400)、超 255 数字段、无点
// 无字母串一律不是目标。语法检查非语义规则(类比表单电话号码校验),
// 语义归模型(第三条道)。同源单函数挂三处:提取出口(scopeCallTargets
// 过滤与各提取位预检)、卡创建(denyScope 出卡分支与 enterAttackCard
// 入口)、资产入图(overmind/harvest.go 发现路)——三处同一判定,垃圾
// 值既不出卡也不入图。
//
// 形态口径:合法 IPv4(四段、每段 ≤255)/合法 IPv6/含点域名(标签形
// 态合法、至少一个标签含字母——全数字段含点但非合法 IPv4 的
// 400.0.0.1、1.2.3.4.5 一律拒)。URL(http(s)://…)取 host 后同判。
// 无点串(localhost、单标签词、裸数字)不是可提取目标——围栏闭包按
// 写法粒度匹配不上无点主机名,提取必出无穷卡;localhost 等环回由
// "不进判定=放行"天然豁免,单标签内网名与变量拼接同口径(实际去向
// 由镜流量留痕对账)。
package swarm

import (
	"net"
	"regexp"
	"strings"
)

// ipv4Re 点分四段数字(IPv4 候选;每段 ≤255 由 addressLikeHost 校验)。
var ipv4Re = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

// hostLabelRe 域名标签形态(字母数字连字符,首尾非连字符)。
var hostLabelRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// dottedCandidateRe token 内的点分候选子串(连续的字母数字点横线段;
// 由 addressLikeHost 把关——候选宽、判定严)。正则字符类不含 : / 空白
// 等分隔符,天然按词内段切("Chrome/120.0.0.0" 取 "120.0.0.0"、
// "evil.com:8080" 取 "evil.com")。
var dottedCandidateRe = regexp.MustCompile(`[A-Za-z0-9](?:[A-Za-z0-9.-]{0,250}[A-Za-z0-9])?`)

// AddressLikeHost host 是否长得像地址(P4-72 语法守门单函数,提取出
// 口/卡创建/资产入图三挂点共用;overmind 发现路经导出名调用):合法
// IPv4/IPv6/含点域名。裸数字、超 255 数字段、全数字段非 IPv4、无点
// 串、含下划线等非法域名形态一律 false。
func AddressLikeHost(h string) bool { return addressLikeHost(h) }

// addressLikeHost 同名实现(包内调用面;导出包装供跨包挂点)。
func addressLikeHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || len(h) > 253 {
		return false
	}
	if m := ipv4Re.FindStringSubmatch(h); m != nil {
		for _, seg := range m[1:] {
			if n := atoiSeg(seg); n > 255 {
				return false
			}
		}
		return true
	}
	if strings.Contains(h, ":") {
		return net.ParseIP(h) != nil // IPv6(裸形态;带端口形态经 addressHostFrom 拆分)
	}
	// 域名:含至少一个点、每标签形态合法、至少一个标签含字母(纯数字
	// 段集合不是域名——"400.0.0.1" 类已被 IPv4 通道拒)。
	if !strings.Contains(h, ".") {
		return false
	}
	hasAlpha := false
	for _, l := range strings.Split(h, ".") {
		if l == "" || !hostLabelRe.MatchString(l) {
			return false
		}
		if strings.ContainsAny(l, "abcdefghijklmnopqrstuvwxyz") {
			hasAlpha = true
		}
	}
	return hasAlpha
}

// addressHostFrom 一个 token(或资产标识)提取地址形态的主机部分:
// URL(http(s)://host[:port]/…)取 host、[IPv6] 与 host:port 拆分后按
// addressLikeHost 判。不是地址形态返回 ok=false。语法守门在 token 级
// 的统一入口(提取出口与资产入图挂点)。
func addressHostFrom(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", false
	}
	if i := strings.Index(t, "://"); i >= 0 {
		if hp, ok := urlTokenTarget(t); ok {
			return hp.host, true
		}
		return "", false
	}
	h := t
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1] // [IPv6] 括号形态
		return strings.ToLower(h), addressLikeHost(h)
	}
	if host, _, err := net.SplitHostPort(h); err == nil && host != "" && strings.Contains(h, ":") {
		return strings.ToLower(host), addressLikeHost(host)
	}
	if strings.ContainsAny(h, "/?#@ ") {
		return "", false // 路径/账号符形态:交由 URL 专项,这里不当地址
	}
	return strings.ToLower(h), addressLikeHost(h)
}

// addrSubtokensOf 命令文本中全部地址形态子串(小写 host、去重;剥
// heredoc 后按 token 挖取)——快路径"全解性"检查的扫描对象。挖取三
// 路:URL 子串(urlRe 既有)、含 ≥2 冒号的 IPv6 token、点分候选子串
// (dottedCandidateRe 候选后过语法守门)。
func addrSubtokensOf(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, seg := range shellSegments(text) {
		for _, tok := range seg {
			for _, raw := range urlRe.FindAllString(tok.text, -1) {
				if hp, ok := urlTokenTarget(raw); ok {
					add(hp.host)
				}
			}
			if strings.Count(tok.text, ":") >= 2 {
				if h, ok := addressHostFrom(tok.text); ok {
					add(h)
				}
			}
			for _, cand := range dottedCandidateRe.FindAllString(tok.text, -1) {
				if !strings.Contains(cand, ".") {
					continue
				}
				cand = strings.Trim(cand, ".-")
				if h, ok := addressHostFrom(cand); ok {
					add(h)
				}
			}
		}
	}
	return out
}

// atoiSeg 数字段转整(空/非数字/超长返回 0——守门只看 ≤255,宽进
// 严出)。
func atoiSeg(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 999 {
			return n
		}
	}
	return n
}
