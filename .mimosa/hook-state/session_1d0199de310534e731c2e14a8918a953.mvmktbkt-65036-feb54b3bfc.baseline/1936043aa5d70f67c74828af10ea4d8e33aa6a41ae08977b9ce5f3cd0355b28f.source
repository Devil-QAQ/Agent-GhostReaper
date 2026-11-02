package swarm

// 环回自指排除(P4-25 ①,问题清单 #38 镜自指):本机环回(127.0.0.1 /
// localhost / ::1)与系统自身设施(镜、面板等,均绑本机环回)不作为发现
// 对象——事实/判断/猜测的资产锚点或证据流量指向本机环回即不立,提交
// 在受控入口拒收,内容如实记为环境观察(黑匣子 env_observation,信息
// 不丢)供排查。操作者声明的目标例外:种子/手动入账的环回资产(本地
// 靶标)是操作者意志,不受排除——真实目标不受影响。
//
// 判定全确定性(零 LLM):环回主机的识别与端口对账都是纯函数;端口
// 粒度区分"同一台机器上的目标与系统设施"(靶标 127.0.0.1:4481 与镜
// 127.0.0.1:60437 同主机不同端口)。

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/rings"
)

// loopbackTokenRe 文本里的环回主机(带可选端口):组 1 主机、组 3 端口。
// 只识别环回主机——非环回地址与发现无关,不进正则;IPv6 兼容
// [::1]:port 与 ::1 形态。前缀污染(fe80::1 内嵌 ::1 等)由扫描侧的
// 前字符检查排除(Go regexp 无 lookbehind)。
var loopbackTokenRe = regexp.MustCompile(`(localhost|\[::1\]|::1|((?:\d{1,3}\.){3}\d{1,3}))(?::(\d{2,5}))?`)

// loopbackToken 一条环回提法(主机 + 可选端口)。
type loopbackToken struct {
	host string
	port string
	raw  string
}

// scanLoopbackTokens 扫文本中的环回提法:跳过前字符是字母数字、点、
// 冒号或连字符的匹配(fe80::1、x127.0.0.1、1.127.0.0.1 类前缀污染)。
func scanLoopbackTokens(s string) []loopbackToken {
	var out []loopbackToken
	for _, m := range loopbackTokenRe.FindAllStringSubmatchIndex(s, -1) {
		start := m[0]
		if start > 0 {
			c := s[start-1]
			if c == ':' || c == '.' || c == '-' ||
				(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') ||
				(c >= 'A' && c <= 'Z') {
				continue
			}
		}
		host := s[m[2]:m[3]]
		port := ""
		if m[6] > 0 {
			port = s[m[6]:m[7]]
		}
		if !isLoopbackHost(host) {
			continue
		}
		out = append(out, loopbackToken{host: host, port: port, raw: s[m[0]:m[1]]})
	}
	return out
}

// isLoopbackHost 主机名是否本机环回:localhost、::1、127.0.0.0/8。
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(host), "[]")))
	if h == "localhost" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// selfRefAnchor 锚点自指判定:锚点文本(资产标识/新对象申报标识)是
// 环回主机即自指。操作者资产(种子/手动)在 allowedLoopback 对账,命中
// 则不算自指(本地靶标是目标)。返回命中的环回提法(供环境观察留痕)。
func (env *unitEnv) selfRefAnchor(ctx context.Context, anchor string) (string, bool) {
	for _, t := range scanLoopbackTokens(anchor) {
		if !env.allowedLoopback(ctx, t) {
			return t.raw, true
		}
	}
	return "", false
}

// selfRefAssetAnchor 既有资产锚点的自指判定:发现型资产(收割入图的
// 回流对象)且标识属本机环回即自指;操作者资产(种子/手动)是声明过
// 的目标,不是自指。
func (env *unitEnv) selfRefAssetAnchor(ctx context.Context, assetID string) (string, bool) {
	a, err := env.Executor.Store.Node(ctx, env.TaskID, assetID)
	if err != nil || rings.AssetSourceOf(a.Detail) != rings.AssetSourceDiscovered {
		return "", false
	}
	return env.selfRefAnchor(ctx, a.Title)
}

// selfRefEvidence 证据流量自指判定:证据里的环回"主机:端口"指向
// 操作者目标之外的地址即自指(镜/面板等系统设施都在本机环回的非常规
// 端口上)。只对带端口的环回提法判定——目标内容里裸提环回地址(如
// 响应头回显 127.0.0.1)不是流量指向本机,不拦,防误伤真实发现。
func (env *unitEnv) selfRefEvidence(ctx context.Context, evidence string) (string, bool) {
	for _, t := range scanLoopbackTokens(evidence) {
		if t.port == "" {
			continue
		}
		if !env.allowedLoopback(ctx, t) {
			return t.raw, true
		}
	}
	return "", false
}

// allowedLoopback 环回提法是否操作者声明的目标:与任一种子/手动资产
// 的环回标识对账——资产带端口要求端口一致(靶标 4481 不放行镜 60437);
// 资产为裸环回主机(无端口)视为主机整段授权,放行该主机任意端口
// (操作者把整台环回主机声明为目标时的意志优先于排除)。读失败按
// 未授权处理(fail-closed:自指排除不因读故障失效)。
func (env *unitEnv) allowedLoopback(ctx context.Context, t loopbackToken) bool {
	assets, err := env.Executor.Store.NodesByType(ctx, env.TaskID, rings.NodeAsset, "")
	if err != nil {
		return false
	}
	for _, a := range assets {
		if rings.AssetSourceOf(a.Detail) == rings.AssetSourceDiscovered {
			continue // 发现型资产不是操作者意志(本排除的拦截对象)
		}
		seed := loopbackAnchorOf(a.Title)
		if seed.host == "" || !sameLoopbackHost(seed.host, t.host) {
			continue
		}
		if seed.port == "" || seed.port == t.port {
			return true
		}
	}
	return false
}

// loopbackAnchorOf 资产标识里的首个环回提法(无环回返回零值)。
func loopbackAnchorOf(title string) loopbackToken {
	for _, t := range scanLoopbackTokens(title) {
		return t
	}
	return loopbackToken{}
}

// sameLoopbackHost 两个环回主机提法是否同一主机(localhost、127.x、
// ::1 都是本机,归一比对)。
func sameLoopbackHost(a, b string) bool {
	return isLoopbackHost(a) && isLoopbackHost(b)
}

// recordEnvObservation 环境观察留痕(P4-25 ①):被拒收的自指提交原文
// 进黑匣子,信息不丢、供排查。kind 为提交类(待确认事实/漏洞判断/
// 猜测),anchor 为命中自指的锚点/证据提法。留痕失败不挡拒收(观察
// 原文仍在单位转录里,审计可下钻)。
func (env *unitEnv) recordEnvObservation(kind, anchor, statement, evidence string) {
	note := fmt.Sprintf("拒收%s:%s 属本机环回(测试环境自身),不作为发现对象", kind, anchor)
	detail := map[string]any{
		"note":      note,
		"kind":      kind,
		"anchor":    anchor,
		"statement": clipLine(statement, 200),
	}
	if evidence != "" {
		detail["evidence_head"] = clipLine(evidence, 200)
	}
	_ = blackbox.Record(context.Background(), env.Executor.Store, env.TaskID,
		"swarm:"+env.UnitID, rings.BlackboxRecord{
			Type:   rings.BlackboxEnvObservation,
			StepID: env.StepID, UnitID: env.UnitID,
			Detail: detail,
		})
}
