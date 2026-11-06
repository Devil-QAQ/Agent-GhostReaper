// P4-44(问题清单 #61 ①②):资产范围归属与任务类型。范围从"默认全包
// ＋提示词软话"改为一等硬边界——种子闭包按造物主定的归属规则确定性
// 推导:给域名=该域名自身可测(任意路径+自有 JS/静态资源+同域名各端口);
// 给 IP=该服务器全部可测(所有端口与域名);混合给取并集。新资产入图时
// 对照闭包判定"范围内/范围外"(冻结进 detail.scope),范围外资产按任务
// 类型走待批制(渗透)或直接可测(红队)。判定全部为确定性代码,零 LLM。
package rings

import (
	"context"
	"net"
	"net/url"
	"regexp"
	"strings"
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

// hostRe 合法主机名字符集(ASCII 字母/数字/点/连字符/下划线;IPv6 字面
// 量由 net.ParseIP 认)。纯名称型标题(人员/组织/泄露信息——中文、
// 邮箱/账号形态)不匹配:此类资产无主机锚点,不参与闭包匹配。
var hostRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ScopeAnchorHost 资产标识 → 闭包主机锚点(host 小写,端口剥离;URL
// 形态经 url.Parse,裸 host[:port] 按 net.SplitHostPort 兼容 [IPv6])。
// 解析不出 host 或 host 不是合法主机形态(纯名称型:人员/组织/泄露
// 信息等)返回 ok=false——此类资产无主机锚点,按范围内处理(硬拦只对
// 主机目标生效,无锚点资产无从"发起测试")。
func ScopeAnchorHost(title string) (host string, isIP bool, ok bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", false, false
	}
	if i := strings.Index(t, "://"); i >= 0 {
		u, err := url.Parse(t)
		if err != nil || u.Hostname() == "" {
			return "", false, false
		}
		host = strings.ToLower(u.Hostname())
	} else {
		if strings.ContainsAny(t, "/?# \t@") {
			return "", false, false // 含路径/空白/账号符的标识不是主机类
		}
		if h, _, err := net.SplitHostPort(t); err == nil && h != "" {
			host = strings.ToLower(h)
		} else {
			host = strings.ToLower(t)
		}
	}
	if host == "" || (!hostRe.MatchString(host) && net.ParseIP(host) == nil) {
		return "", false, false
	}
	return host, net.ParseIP(host) != nil, true
}

// ScopeClosure 种子闭包(P4-44 归属规则的判定面):域名集合按主机级授权
// (任意端口);IP 集合按服务器级授权(任意端口;给 IP 时解析到该 IP 的
// 域名视为同机,亦在范围内)。闭包成员 = 种子资产 ∪ 手动资产 ∪ 已放行
// 的发现资产。
type ScopeClosure struct {
	domains map[string]bool // 域名主机(小写)
	ips     map[string]bool // IP 字符串(小写)
}

// ScopeSeed 闭包构建输入(调用方从图上资产投影:NodeFull/AssetNodeView
// 两侧各自一行转换,免整包结构对齐)。
type ScopeSeed struct {
	ID     string
	Title  string
	Source string
}

// BuildScopeClosure 从图上资产构建闭包:source ∈ {seed, manual} 或
// approved 集合内的 discovered 资产贡献主机锚点(端口一律剥离——给域名
// = 同域名各端口,给 IP = 所有端口,归属规则不按端口切)。
func BuildScopeClosure(seeds []ScopeSeed, approved map[string]bool) *ScopeClosure {
	c := &ScopeClosure{domains: map[string]bool{}, ips: map[string]bool{}}
	for _, a := range seeds {
		if a.Source == AssetSourceDiscovered && !approved[a.ID] {
			continue
		}
		host, isIP, ok := ScopeAnchorHost(a.Title)
		if !ok {
			continue
		}
		if isIP {
			c.ips[host] = true
		} else {
			c.domains[host] = true
		}
	}
	return c
}

// Empty 闭包是否为空(无种子锚点——判定无从谈起,调用方按不拦处理)。
func (c *ScopeClosure) Empty() bool {
	return c == nil || (len(c.domains) == 0 && len(c.ips) == 0)
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

// CoversHost 主机是否在闭包内(归属规则判定):域名精确匹配(子域名/
// 兄弟域名不在内——"该域名自身可测");IP 直接匹配;给 IP 的闭包下,
// 域名经 resolve 解析到任一闭包 IP 即视为同机在范围内(所有端口与
// 域名)。resolve 为 nil 或解析失败按不在范围内(保守)。
func (c *ScopeClosure) CoversHost(host string, resolve ScopeResolver) bool {
	if c.Empty() || host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h // 调用侧可能带端口传入;端口不参与判定,剥掉
	}
	if c.domains[host] {
		return true
	}
	if net.ParseIP(host) != nil {
		return c.ips[host]
	}
	if len(c.ips) == 0 || resolve == nil {
		return false
	}
	for _, ip := range resolve(host) {
		if c.ips[strings.ToLower(ip)] {
			return true
		}
	}
	return false
}

// NeedsResolve 主机是否须 DNS 解析才能定论:域名且不在域名集合内、而
// 闭包里还有 IP 种子(解析到该 IP 即同机在范围内)。消费侧据此决定
// 是否走带缓存的解析路径。
func (c *ScopeClosure) NeedsResolve(host string) bool {
	if c.Empty() || host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	return net.ParseIP(host) == nil && !c.domains[host] && len(c.ips) > 0
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
// 入图时对照闭包确定性判定,结果冻结进 detail.scope)。无主机锚点的
// 资产(人员/组织/泄露信息等)按范围内——硬拦只对主机目标生效,无锚
// 点资产无从"发起测试",待批只会徒增红点噪音。resolve 注入(生产
// DefaultScopeResolver,测试打桩)。
func JudgeAssetScope(c *ScopeClosure, title string, resolve ScopeResolver) string {
	host, _, ok := ScopeAnchorHost(title)
	if !ok {
		return AssetScopeIn
	}
	if c.CoversHost(host, resolve) {
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
		if ScopeStateOf(TaskTypePentest, AssetSourceOf(a.Detail), AssetScopeOf(a.Detail), approved[a.ID], excluded[a.ID]) == ScopeStatePending {
			out = append(out, ExcludedAssetView{ID: a.ID, Title: a.Title})
		}
	}
	return out, nil
}
