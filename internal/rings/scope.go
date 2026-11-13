// P4-44(问题清单 #61 ①②):资产范围归属与任务类型。范围从"默认全包
// ＋提示词软话"改为一等硬边界——种子闭包按造物主定的归属规则确定性
// 推导。P4-50(问题清单 #73 ①):范围写法即授权粒度——裸域名 = 该域名
// 全部端口;`*.域` = 基域＋任意子域;裸 IP = 整台全部端口;`host:端口`
// = 仅此端口;URL 等同裸域名;多种子并集;种子域名当前解析 IP 并入
// 范围(同 IP 共站风险已接受)。新资产入图时对照闭包判定"范围内/
// 范围外"(冻结进 detail.scope),范围外资产按任务类型走待批制(渗透)
// 或直接可测(红队)。判定全部为确定性代码,零 LLM。
package rings

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 任务类型词表(P4-44 ②):建任务必填,建后不可改(无变更端点);存量
// 任务无类型("")按红队行为呈现(现状不锁),徽标如实标"未标"。
const (
	TaskTypePentest = "pentest" // 渗透任务:范围外新资产挂"待批",锁定不测
	TaskTypeRedteam = "redteam" // 红队任务:范围外资产直接可测(现状行为)
)

var taskTypes = map[string]bool{
	TaskTypePentest: true,
	TaskTypeRedteam: true,
}

// IsValidTaskType 任务类型是否在词表内(""=存量未标,不算合法新值)。
func IsValidTaskType(s string) bool { return taskTypes[s] }

// TaskTypeOf created 事件 detail 里的任务类型归一:未标/缺省返回 ""
// (存量任务,行为等同红队但不锁,徽标如实呈现)。
func TaskTypeOf(detail map[string]any) string {
	if s, _ := detail["task_type"].(string); IsValidTaskType(s) {
		return s
	}
	return ""
}

// 资产范围归属标记(P4-44 ①):收割入图时对照种子闭包确定性判定并冻结
// 进 detail["scope"];种子/手动资产不判定(天然范围内)。缺省按范围内
// 兜底(存量与未判定历史)。
const (
	AssetScopeIn  = "in"  // 范围内:在种子闭包内(或不可判定,保守入内)
	AssetScopeOut = "out" // 范围外:不在种子闭包内
)

// AssetScopeOf 资产节点 detail 的范围归属归一(缺省 in)。
func AssetScopeOf(detail map[string]any) string {
	if s, _ := detail["scope"].(string); s == AssetScopeOut {
		return AssetScopeOut
	}
	return AssetScopeIn
}

// AssetStatusRetired 资产的"退役"态(P4-68 ①:范围卡第三键"允许访问"
// ——目标转信息源后不收编不入资产图):host 获访问授权时,图上同主机的
// 资产退役(状态迁移留痕,不删节点不删边,审计照旧);资产图/待批清单/
// 主宰快照/报告等读面一律过滤——等同"从资产图消失"(不算覆盖、不被
// 规划测试、不进资产树分组),与 P4-67 信息源"不入资产图"同一语义。
const AssetStatusRetired = "retired"

// StepScopeWait 步骤的"等待批准"态(P4-51 ①,问题清单 #73②):渗透
// 任务的命令命中范围外,单位冻结收场让位名额后步骤挂此态——不可领
// 取,等"加入资产"裁决回 dispatched(插队队首)或"禁止测试"裁决落
// aborted。swarm(写入)/overmind(对账唤醒)/manager(终局清点)/
// server(显示)共用;界面词"等待批准"。
const StepScopeWait = "scope_wait"

// ScopeCardIDPrefix 范围待决卡请求号前缀(P4-51:同一目标一张卡的
// RequestID = "sc-"+host[+":"+port],裁决记录同号收口;与弹门 ap- 号
// 区分,server 裁决端点据此分流)。
const ScopeCardIDPrefix = "sc-"

// ScopeCardID 范围待决卡请求号(端口非空带端口——写法即授权粒度,
// 端口不同即不同授权问题)。
func ScopeCardID(host, port string) string {
	id := ScopeCardIDPrefix + host
	if port != "" {
		id += ":" + port
	}
	return id
}

// AssetCardRecPrefix 待批资产卡裁决记录的请求号前缀(P4-52 ⑤:待批
// 资产以资产 ID 作卡 ID,裁决记录以 ac- 前缀留档,与拦截卡 sc- 同族
// ——锚定的是资产不是一次命令,记录校验豁免命令四字段与分级要求)。
const AssetCardRecPrefix = "ac-"

// TriageCardIDPrefix 研判分诊留痕的请求号前缀(P4-67:tg-<host>,同一
// host 多笔追加(判定历史),读侧按序列取最新即任务级分诊缓存——
// 按 host 缓存,同 host 不同端口共享判定,同任务内零重复调用)。
const TriageCardIDPrefix = "tg-"

// TriageCardID 研判分诊留痕请求号(host 级)。
func TriageCardID(host string) string {
	return TriageCardIDPrefix + host
}

// InfoSourceKey 信息源清单条目的键(host 形态;判定命中按 host 全放行
// ——查资料的公开服务与端口无关)。
func InfoSourceKey(host, port string) string {
	_ = port // 端口不参与键(host 级清单)
	return host
}

// IsScopeCardRecord 审批记录是否属范围待决卡生命周期(卡面 scope_wait
// 或其同号裁决 approved/denied——红队发现卡无步骤/单位/工具上下文,
// 记录校验据此豁免命令四字段与分级要求;P4-52 ⑤ 待批资产卡裁决
// (ac- 前缀)同族豁免;P4-67 研判分诊留痕(tg- 前缀/triage 类型)锚定
// 的是目标不是一次命令,同族豁免)。
func IsScopeCardRecord(p ApprovalRecord) bool {
	return p.Type == ApprovalRecScopeWait || p.Type == ApprovalRecTriage ||
		(p.RequestID != "" && len(p.RequestID) > len(ScopeCardIDPrefix) &&
			p.RequestID[:len(ScopeCardIDPrefix)] == ScopeCardIDPrefix) ||
		(p.RequestID != "" && len(p.RequestID) > len(AssetCardRecPrefix) &&
			p.RequestID[:len(AssetCardRecPrefix)] == AssetCardRecPrefix) ||
		(p.RequestID != "" && len(p.RequestID) > len(TriageCardIDPrefix) &&
			p.RequestID[:len(TriageCardIDPrefix)] == TriageCardIDPrefix)
}

// hostRe 合法主机名字符集(ASCII 字母/数字/点/连字符/下划线;IPv6 字面
// 量由 net.ParseIP 认)。纯名称型标题(人员/组织/泄露信息——中文、
// 邮箱/账号形态)不匹配:此类资产无主机锚点,不参与闭包匹配。
var hostRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// isDigits 端口段是否为纯数字(host:port 锚点的端口校验)。
func isDigits(s string) bool {
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

// ScopeAnchor 资产标识解析出的主机锚点(P4-50 ③:写法即授权粒度——
// Port 非空 = 端口限定种子(仅此端口);Wildcard = `*.域` 写法(基域＋
// 任意子域);URL 种子的端口忽略(等同裸域名,全端口)。
type ScopeAnchor struct {
	Host     string
	Port     string // 端口限定(空 = 全端口)
	IsIP     bool
	Wildcard bool // *.域 写法(基域＋任意子域,全端口)
}

// ScopeAnchorOf 资产标识 → 主机锚点(host 小写;URL 形态经 url.Parse,
// 裸 host[:port] 按 net.SplitHostPort 兼容 [IPv6])。解析不出 host 或
// host 不是合法主机形态(纯名称型:人员/组织/泄露信息等)返回 ok=false
// ——此类资产无主机锚点,按范围内处理(硬拦只对主机目标生效,无锚点
// 资产无从"发起测试")。通配写法不与端口组合(`*.域:端口` 不支持,
// 按无锚点保守处理)。
func ScopeAnchorOf(title string) (ScopeAnchor, bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return ScopeAnchor{}, false
	}
	wildcard := false
	if strings.HasPrefix(t, "*.") {
		wildcard = true
		t = t[2:]
	}
	if strings.Contains(t, "*") {
		return ScopeAnchor{}, false // * 只认 *.-前缀通配写法
	}
	var host, port string
	if i := strings.Index(t, "://"); i >= 0 {
		u, err := url.Parse(t)
		if err != nil || u.Hostname() == "" {
			return ScopeAnchor{}, false
		}
		host = strings.ToLower(u.Hostname()) // URL 等同裸域名,端口忽略
	} else {
		if strings.ContainsAny(t, "/?# \t@") {
			return ScopeAnchor{}, false // 含路径/空白/账号符的标识不是主机类
		}
		if h, p, err := net.SplitHostPort(t); err == nil && h != "" {
			host, port = strings.ToLower(h), p
		} else {
			host = strings.ToLower(t)
		}
	}
	if host == "" || (!hostRe.MatchString(host) && net.ParseIP(host) == nil) {
		return ScopeAnchor{}, false
	}
	if port != "" && !isDigits(port) {
		return ScopeAnchor{}, false
	}
	if wildcard && (port != "" || net.ParseIP(host) != nil) {
		return ScopeAnchor{}, false // 通配不带端口,也不适用 IP
	}
	return ScopeAnchor{Host: host, Port: port, IsIP: net.ParseIP(host) != nil, Wildcard: wildcard}, true
}

// ScopeClosure 种子闭包(P4-44 归属规则 + P4-50 ③ 写法即授权粒度):
// 裸域名 = 该域名全部端口;`*.域` = 基域＋任意子域全部端口;裸 IP =
// 整台全部端口(解析到该 IP 的域名视为同机);`host:端口` = 仅此端口
// (不扩展同机);URL 种子提取 host 等同裸域名;多条种子取并集。闭包
// 成员 = 种子资产 ∪ 手动资产 ∪ 已放行的发现资产。
type ScopeClosure struct {
	domains   map[string]bool // 裸域名种子(全端口)
	wildcards map[string]bool // *.域 种子的基域(基域＋任意子域,全端口)
	ips       map[string]bool // 裸 IP 种子(整台,全端口)
	hostPorts map[string]bool // "host:port" 端口限定种子(仅此端口)

	// P4-50 ②种子域名当前解析 IP 并入范围:判定时经 resolve 解析种子
	// 域名(闭包级缓存,解析一次不再重复;失败缓存空集按范围外保守)。
	mu       sync.Mutex
	resolved map[string][]string // 种子域名 → 解析 IP 集
}

// ScopeSeed 闭包构建输入(调用方从图上资产投影:NodeFull/AssetNodeView
// 两侧各自一行转换,免整包结构对齐)。
type ScopeSeed struct {
	ID     string
	Title  string
	Source string
}

// BuildScopeClosure 从图上资产构建闭包:source ∈ {seed, manual} 或
// approved 集合内的 discovered 资产贡献主机锚点;写法粒度按锚点落位
// (裸域名/裸 IP = 全端口;*.域 = 通配;host:port = 仅此端口;URL 等同
// 裸域名)。excluded(P4-54 修复:摘除即去权)——排除清单内的资产不贡
// 献闭包:手动/种子资产被摘除后同样不再授权(否则领取时闭包仍含其
// 主机,命令级范围检查会被"闭包内"短路,摘除后的在途/新领单位照打
// 该目标)。nil = 无排除清单(兼容旧调用)。
func BuildScopeClosure(seeds []ScopeSeed, approved, excluded map[string]bool) *ScopeClosure {
	c := &ScopeClosure{
		domains:   map[string]bool{},
		wildcards: map[string]bool{},
		ips:       map[string]bool{},
		hostPorts: map[string]bool{},
		resolved:  map[string][]string{},
	}
	for _, a := range seeds {
		if a.Source == AssetSourceDiscovered && !approved[a.ID] {
			continue
		}
		if excluded != nil && excluded[a.ID] {
			continue
		}
		anchor, ok := ScopeAnchorOf(a.Title)
		if !ok {
			continue
		}
		switch {
		case anchor.Wildcard:
			c.wildcards[anchor.Host] = true
		case anchor.Port != "":
			c.hostPorts[anchor.Host+":"+anchor.Port] = true
		case anchor.IsIP:
			c.ips[anchor.Host] = true
		default:
			c.domains[anchor.Host] = true
		}
	}
	return c
}

// Empty 闭包是否为空(无种子锚点——判定无从谈起,调用方按不拦处理)。
func (c *ScopeClosure) Empty() bool {
	return c == nil || (len(c.domains) == 0 && len(c.wildcards) == 0 &&
		len(c.ips) == 0 && len(c.hostPorts) == 0)
}

// Summary 授权范围写法清单(P4-72:合一判定 in_scope 的对照输入——
// 域名/通配/IP/端口限定条目逐一列出,排序稳定供前缀缓存;空闭包返
// 回空串)。模型按写法粒度对照判定;代码侧最终以 CoversTarget 复核,
// 模型判定不作放行依据。
func (c *ScopeClosure) Summary() string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, len(c.domains)+len(c.wildcards)+len(c.ips)+len(c.hostPorts))
	for d := range c.domains {
		parts = append(parts, d)
	}
	for w := range c.wildcards {
		parts = append(parts, "*."+w)
	}
	for ip := range c.ips {
		parts = append(parts, ip)
	}
	for hp := range c.hostPorts {
		parts = append(parts, hp)
	}
	sort.Strings(parts)
	return strings.Join(parts, "、")
}

// ScopeResolver 域名 → 解析到的 IP 清单(DNS 前向解析;nil/空 = 解析
// 不了)。判定侧注入,测试可打桩。
type ScopeResolver func(host string) []string

// DefaultScopeResolver 生产解析器:本机 resolver,单次 2 秒超时;失败
// 返回 nil(按解析不了处理,判定取保守方向)。
func DefaultScopeResolver(host string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	return ips
}

// wildcardCovers 通配种子是否覆盖 host(基域自身或任意层级子域)。
func (c *ScopeClosure) wildcardCovers(host string) bool {
	for base := range c.wildcards {
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}

// seedIPsOf 种子域名的当前解析 IP 集(闭包级缓存;失败缓存空集,解析
// 一次不再重复——闭包随每次领取重建,解析寿命与任务包一致)。
func (c *ScopeClosure) seedIPsOf(seed string, resolve ScopeResolver) []string {
	c.mu.Lock()
	ips, done := c.resolved[seed]
	c.mu.Unlock()
	if done {
		return ips
	}
	ips = nil
	if resolve != nil {
		for _, ip := range resolve(seed) {
			ips = append(ips, strings.ToLower(ip))
		}
	}
	if ips == nil {
		ips = []string{} // 解析失败:缓存空集,判定按范围外保守
	}
	c.mu.Lock()
	c.resolved[seed] = ips
	c.mu.Unlock()
	return ips
}

// coversSeedIP host(IP 字面量)是否为任一种子域名的当前解析 IP。
func (c *ScopeClosure) coversSeedIP(host string, resolve ScopeResolver) bool {
	for d := range c.domains {
		for _, ip := range c.seedIPsOf(d, resolve) {
			if ip == host {
				return true
			}
		}
	}
	for d := range c.wildcards {
		for _, ip := range c.seedIPsOf(d, resolve) {
			if ip == host {
				return true
			}
		}
	}
	return false
}

// CoversTarget 目标(host+可选端口)是否在闭包内(P4-50 ③粒度判定):
// port 为空 = 全端口意图(如 nmap 裸目标)——须全端口授权才放行,仅
// host:端口 授权不放行;port 非空 = 该端口须被授权(全端口授权或精确
// 端口限定均可)。种子域名(全端口类)的当前解析 IP 视为范围内(②);
// 域名目标解析到裸 IP 种子视为同机(既有);端口限定种子不做同机扩展
// (小授权面不放大)。resolve 为 nil 或解析失败按不在范围内(保守)。
func (c *ScopeClosure) CoversTarget(host, port string, resolve ScopeResolver) bool {
	if c.Empty() || host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if h, p, err := net.SplitHostPort(host); err == nil && h != "" && p != "" {
		host, port = strings.ToLower(h), p // 调用侧可能带端口传入,归一拆开
	}
	if c.domains[host] || c.ips[host] || c.wildcardCovers(host) {
		return true
	}
	if port != "" && c.hostPorts[host+":"+port] {
		return true
	}
	if resolve == nil {
		return false
	}
	if net.ParseIP(host) != nil {
		// ②种子域名当前解析 IP 并入:裸域名/通配种子 = 该 IP 全端口;
		// 端口限定域名种子 = 仅同端口。
		if c.coversSeedIP(host, resolve) {
			return true
		}
		if port != "" {
			for key := range c.hostPorts {
				h, p, ok := splitHostPortKey(key)
				if !ok || p != port || net.ParseIP(h) != nil {
					continue // 只对照域名形态的端口限定种子(IP:端口已精确匹配过)
				}
				for _, ip := range c.seedIPsOf(h, resolve) {
					if ip == host {
						return true
					}
				}
			}
		}
		return false
	}
	// 域名目标:解析到裸 IP 种子或种子域名解析 IP 视为同机在范围内
	//(给 IP/给域名 = 整台/该站,同 IP 共站风险已由归属规则接受)。
	for _, ip := range resolve(host) {
		ip = strings.ToLower(ip)
		if c.ips[ip] {
			return true
		}
		if c.coversSeedIP(ip, resolve) {
			return true
		}
	}
	return false
}

// splitHostPortKey 拆闭包里 "host:port" 键(IPv6 host 不带方括号存,
// 无冒号歧义由构建侧保证——纯 IPv6 字面量不会构成 hostPorts 键的
// host 部分)。
func splitHostPortKey(key string) (host, port string, ok bool) {
	i := strings.LastIndex(key, ":")
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// 资产范围呈现态(P4-44 ③④,server/overmind/面板共用口径)。
const (
	// ScopeStatePending 待批:渗透任务的范围外发现资产,锁定不测,
	// 面板放行后转范围内。
	ScopeStatePending = "pending"
	// ScopeStateOutOpen 范围外可测:红队/未标任务的发现资产(现状行为,
	// 归属标注照显示,资产树"发现的其他资产"分组)。
	ScopeStateOutOpen = "out_open"
)

// ScopeStateOf 单资产的范围呈现态:范围内返回 ""(常规呈现,无标注);
// 摘除资产(excluded)交由 P4-05 既有"范围外"徽标,这里同样返回 ""。
// 判定输入:任务类型、资产来源、入图冻结的 scope 标记、放行/摘除集合
// 命中与否。
func ScopeStateOf(taskType, source, frozenScope string, approved, excluded bool) string {
	if excluded || approved {
		return "" // 摘除走既有徽标;放行即转范围内
	}
	if source != AssetSourceDiscovered || frozenScope != AssetScopeOut {
		return "" // 种子/手动/范围内发现资产:常规呈现
	}
	if taskType == TaskTypePentest {
		return ScopeStatePending
	}
	return ScopeStateOutOpen
}

// JudgeAssetScope 判定一个新发现资产是否在种子闭包内(P4-44 ①:收割
// 入图时对照闭包确定性判定,结果冻结进 detail.scope;P4-50 ③:资产标题
// 带端口按端口限定判定)。无主机锚点的资产(人员/组织/泄露信息等)按
// 范围内——硬拦只对主机目标生效,无锚点资产无从"发起测试",待批只会
// 徒增红点噪音。resolve 注入(生产 DefaultScopeResolver,测试打桩)。
func JudgeAssetScope(c *ScopeClosure, title string, resolve ScopeResolver) string {
	anchor, ok := ScopeAnchorOf(title)
	if !ok {
		return AssetScopeIn
	}
	if c.CoversTarget(anchor.Host, anchor.Port, resolve) {
		return AssetScopeIn
	}
	return AssetScopeOut
}

// ScopeSeedsOf 图上资产全量(NodeFull 投影)→ 闭包构建输入。
func ScopeSeedsOf(assets []NodeFull) []ScopeSeed {
	out := make([]ScopeSeed, 0, len(assets))
	for _, a := range assets {
		out = append(out, ScopeSeed{ID: a.ID, Title: a.Title, Source: AssetSourceOf(a.Detail)})
	}
	return out
}

// ExcludedAssetsCovering 已摘除资产中按写法粒度覆盖目标(host+可选
// 端口)的资产 ID 集(ExcludedCoversTarget 的清单形态,P4-69 ① 对账
// 分族用——挂等步骤的意图覆盖判定取这批资产求交)。覆盖规则与
// ExcludedCoversTarget 同源对称,两者须同步改。
func ExcludedAssetsCovering(assets []NodeFull, excluded map[string]bool, host, port string) []string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil
	}
	var out []string
	for _, a := range assets {
		if !excluded[a.ID] {
			continue
		}
		anchor, ok := ScopeAnchorOf(a.Title)
		if !ok {
			continue
		}
		if anchor.Wildcard {
			if host == anchor.Host || strings.HasSuffix(host, "."+anchor.Host) {
				out = append(out, a.ID)
			}
			continue
		}
		if anchor.Host != host {
			continue
		}
		if anchor.Port == "" || (anchor.Port == port && port != "") {
			out = append(out, a.ID)
		}
	}
	return out
}

// ExcludedCoversTarget 目标(host+可选端口)是否被已摘除资产按写法粒度
// 覆盖(P4-51 ⑦ 防骚扰:已排除目标的命令直接拒、不再出卡;裁决与摘除
// 是同一排除机制)。摘除资产的标题锚点覆盖规则与授权粒度对称:摘裸
// 域名/IP = 整台全端口;摘 host:端口 = 仅此端口;通配摘除 = 基域＋子域。
// 目标端口为空(全端口意图)时仅全端口摘除才算覆盖。
func ExcludedCoversTarget(assets []NodeFull, excluded map[string]bool, host, port string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, a := range assets {
		if !excluded[a.ID] {
			continue
		}
		anchor, ok := ScopeAnchorOf(a.Title)
		if !ok {
			continue
		}
		if anchor.Wildcard {
			// *.域摘除(基域＋任意子域,全端口):host 等于基域或为其子域。
			if host == anchor.Host || strings.HasSuffix(host, "."+anchor.Host) {
				return true
			}
			continue
		}
		if anchor.Host != host {
			continue
		}
		switch {
		case anchor.Port == "":
			return true // 整台摘除(裸域名/IP):全端口
		case anchor.Port == port && port != "":
			return true // 端口限定摘除:仅此端口
		}
	}
	return false
}

// PendingScopeAssets 任务当前的待批资产清单(P4-44 ③:任务行/侧栏红点
// 计数与资产树分组的数据源;红队/未标任务恒空)。单任务三查小查询
// (created 首事件 + roe 行 + 资产投影),零事件流全量扫描。
func (s *Store) PendingScopeAssets(ctx context.Context, taskID string) ([]ExcludedAssetView, error) {
	detail, err := s.CreatedDetail(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if TaskTypeOf(detail) != TaskTypePentest {
		return nil, nil
	}
	roe, err := s.CurrentRoE(ctx, taskID)
	if err != nil {
		return nil, err
	}
	approved := roe.ScopeApprovedSet()
	excluded := roe.ExcludedAssetSet()
	assets, err := s.NodesByType(ctx, taskID, NodeAsset, "")
	if err != nil {
		return nil, err
	}
	out := make([]ExcludedAssetView, 0, 4)
	for _, a := range assets {
		// P4-68:退役资产(允许访问转信息源)不在待批清单——卡随资产
		// 退役自然消失。
		if a.Status == AssetStatusRetired {
			continue
		}
		if ScopeStateOf(TaskTypePentest, AssetSourceOf(a.Detail), AssetScopeOf(a.Detail), approved[a.ID], excluded[a.ID]) == ScopeStatePending {
			v := ExcludedAssetView{ID: a.ID, Title: a.Title, AddedAt: a.AddedAt}
			v.StepID, _ = a.Detail["step_id"].(string)
			v.UnitID, _ = a.Detail["unit_id"].(string)
			out = append(out, v)
		}
	}
	return out, nil
}

// ReconcileRoeExclusivity 排除与批准互斥的存量一次清理(P4-69 ②,问题
// 清单 #86 D 实锤 a-72dab0e5 同列两清单):读当前 RoE,摘除清单成员
// 一律从批准清单剔除——旧机制下"先批准后摘除"的脏快照经此归位(账本
// 只追加:修正以新 roe.updated 快照落账,历史如实)。干净任务零落笔
// (幂等,重启收养重复执行无痕);返回本次剔除的条目数。
func (s *Store) ReconcileRoeExclusivity(ctx context.Context, taskID, actor string) (int, error) {
	st, err := s.CurrentRoE(ctx, taskID)
	if err != nil {
		return 0, fmt.Errorf("rings: 互斥清理读 RoE: %w", err)
	}
	if len(st.ExcludedAssets) == 0 || len(st.ScopeApproved) == 0 {
		return 0, nil
	}
	excluded := st.ExcludedAssetSet()
	next := make([]string, 0, len(st.ScopeApproved))
	for _, id := range st.ScopeApproved {
		if !excluded[id] {
			next = append(next, id)
		}
	}
	if len(next) == len(st.ScopeApproved) {
		return 0, nil // 两清单无交集:零落笔
	}
	removed := len(st.ScopeApproved) - len(next)
	if _, err := s.Append(ctx, taskID, Event{
		Kind: EventRoeUpdated, Actor: actor,
		Payload: RoeUpdated{
			AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
			ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
			ExcludedAssets: st.ExcludedAssets, ScopeApproved: next,
			Note: fmt.Sprintf("状态互斥清理:摘除清单成员 %d 个从批准清单剔除", removed),
		},
	}); err != nil {
		return 0, fmt.Errorf("rings: 互斥清理落账: %w", err)
	}
	return removed, nil
}
