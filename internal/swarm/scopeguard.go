// P4-44 ③(问题清单 #61):渗透任务的范围外硬拦。判定面 = 任务包冻结
// 的种子闭包(种子/手动/已放行资产的主机锚点集);判定对象 = 一次工具
// 调用指向的目标(http_probe 的 url、port_probe 的 host、bash 命令文本
// 里的全部 URL 与裸 IPv4 字面量)。与内置拦截规则同级:确定性代码、
// 命中直接拒绝留痕(approval.record denied)并熔断单位,主宰收割可见
// 拒因换方向。挂在两处:bash 等非只读工具在审批门回调(approvalGate),
// 只读探测(port_probe/http_probe)在门卫补口(guardReadOnlyCall)——
// 只读工具在 Norma 权限管线第 3 阶段自动放行、不进 CanUseTool,与
// 硬规则同款补口。红队/未标任务闭包为 nil,现状行为不拦。
package swarm

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"

	"ghostreaper/internal/rings"
)

// ipv4Re bash 命令文本里的裸 IPv4 字面量(可选 :port)。URL 形态由
// urlRe 另行提取;此处兜"nmap 10.0.0.5"这类无 scheme 的目标写法。
// 四段各 1~3 位,段值校验在后(防把版本号日期误当 IP)。
var ipv4Re = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3})(?::(\d{1,5}))?\b`)

// validIPv4 点分四段是否为合法 IPv4(每段 ≤255)。
func validIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// hostFormRe 合法主机形态(域名/IPv4 字面量;IPv6 由 net.ParseIP 认)。
// bash 命令文本里经 shell 变量拼接的 URL(http://$hp/)会提取出 "$hp"
// 这类非主机形态——无法判定主机归属,跳过(字面 IP/域名照判;变量拼接
// 的实际去向由镜流量留痕对账,硬边界防的是漂移越界不是对抗规避)。
var hostFormRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

// plausibleHost 提取出的目标是否为可判定的主机形态。
func plausibleHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	return net.ParseIP(h) != nil || hostFormRe.MatchString(h)
}

// scopeCallTargets 一次调用指向的目标主机(http_probe/port_probe 参数、
// bash 命令里的 URL 与裸 IPv4)。web_fetch/read/web_search 无目标提取
// (外网查资料不构成对目标的测试,不在硬拦面;流量留痕照旧)。
func scopeCallTargets(toolName string, input json.RawMessage) []hostPort {
	var raw []hostPort
	switch toolName {
	case "http_probe", "port_probe":
		raw = callTargets(toolName, input)
	case "bash":
		raw = callTargets(toolName, input) // 命令文本里的 http(s) URL
		if raw == nil {
			raw = []hostPort{}
		}
		for _, m := range ipv4Re.FindAllStringSubmatch(bashCommandOf(input), -1) {
			if !validIPv4(m[1]) {
				continue
			}
			raw = append(raw, hostPort{host: m[1], port: m[2]})
		}
	}
	out := make([]hostPort, 0, len(raw))
	for _, t := range raw {
		if plausibleHost(t.host) {
			out = append(out, t)
		}
	}
	return out
}

// hostInScope 主机是否在任务包闭包内(带单位级 DNS 缓存:种子给 IP 的
// 闭包下域名目标解析一次不再重复;解析失败按范围外,保守方向)。
func (env *unitEnv) hostInScope(pack *TaskPack, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || pack.ScopeClosure == nil {
		return true // 无闭包(红队/未标)无从判越界,放行
	}
	if !pack.ScopeClosure.NeedsResolve(host) {
		return pack.ScopeClosure.CoversHost(host, nil)
	}
	env.scopeMu.Lock()
	if env.scopeTried == nil {
		env.scopeHits, env.scopeTried = map[string]bool{}, map[string]bool{}
	}
	if _, ok := env.scopeTried[host]; ok {
		hit := env.scopeHits[host]
		env.scopeMu.Unlock()
		return hit
	}
	env.scopeMu.Unlock()
	hit := pack.ScopeClosure.CoversHost(host, rings.DefaultScopeResolver)
	env.scopeMu.Lock()
	env.scopeTried[host] = true
	env.scopeHits[host] = hit
	env.scopeMu.Unlock()
	return hit
}

// denyScope 范围外硬拦的统一挂点:调用目标里首个不在闭包内的主机命中
// 即留痕(denyGuard:approval.record denied + DenyLineRoeScope)并熔断
// 单位,返回人话原因(审批门据此拒绝调用、门卫补口据此返回错误结果,
// 主宰收割可见拒因换方向);无命中/无闭包返回空串放行。display 为主机
// 呈现(host[:port] 原样)。
func (env *unitEnv) denyScope(pack *TaskPack, toolName string, input json.RawMessage) string {
	if pack.ScopeClosure == nil || pack.ScopeClosure.Empty() {
		return ""
	}
	for _, t := range scopeCallTargets(toolName, input) {
		if env.hostInScope(pack, t.host) {
			continue
		}
		display := t.host
		if t.port != "" {
			display += ":" + t.port
		}
		reason := fmt.Sprintf(
			"任务范围外:目标 %s 不在本任务授权范围内(渗透任务发现的相邻资产待批,锁定不测;面板放行后才可测试),换授权范围内的方向",
			display)
		env.denyGuard(pack, toolName, input, rings.DenyLineRoeScope, reason)
		return reason
	}
	return ""
}
