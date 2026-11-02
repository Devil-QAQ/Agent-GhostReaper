package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ghostreaper/internal/rings"
)

// ---- 全局内置硬规则(P2-08e,第四章 §3 v2.1 判定次序②) ----
//
// 参数级确定性正则清单:命中直接拒绝留痕,不依赖模型声明(补 harm_class
// 声明漏报的洞),插在 RoE 工具边界之后、意图分级之前——分级无论怎么
// 声明都拦。默认最保守:系统级自毁与目标数据破坏类均 deny;授权确需
// 破坏性验证时逃生门 = 面板禁用 / 编辑对应规则(操作本身留痕,做完
// 重新启用)。规则是数据不是代码:内置清单只在启动时播种进键值表,
// 此后面板可禁用 / 编辑 / 删除,播种幂等不覆盖用户改动(已存在的键
// 不动)。删除内置规则记墓碑,重启播种不复活。外泄管道组播种为禁用
// 态——反弹 shell / 数据回传是渗透合法动作,不误伤,需要时手动开。
// 不做优先级链、不做 LLM 裁判:弹门即人审兜底,保持确定性哲学。

// 硬规则配置键(rings v6 通用配置键值表)。
const (
	keyHardRulePrefix = "guard.hardrule."
	keyHardRuleTomb   = "guard.hardrule.tombstones"
	// keyHardRuleBypassOff P4-21 一次性迁移标记:存量部署的内置代理旁路
	// 条目已随默认改禁用置为不启用。标记写过即不再触碰——面板此后的
	// 启用 / 禁用是用户意志,重启播种不回改。
	keyHardRuleBypassOff = "guard.hardrule.proxybypass.defaultoff"
)

// 硬规则组词表(面板分组呈现)。
const (
	HardGroupSysDestroy = "系统破坏命令"
	HardGroupDBDestroy  = "数据库破坏"
	HardGroupHTTPKill   = "HTTP 破坏请求"
	HardGroupBulkWipe   = "批量清空路径"
	HardGroupExfilPipe  = "外泄管道"
	// HardGroupProxyBypass 代理旁路(P3-05b ④,P3-05 实测挂账提前收;
	// P4-03 ② 处置改弹门,P4-21 默认改禁用):模型用 --noproxy / env -i /
	// unset 等退出镜注入的代理环境,流量绕开镜 = 直连不经镜。弹门随
	// 单位数轰炸的审批疲劳在案(问题清单 #35,20 分钟 15 次弹门),
	// **默认不启用**——旁路命令直接放行执行(命令与执行照旧全量
	// 留痕,仅直连流量不经镜);严审计部署面板一键启用,规则定义与
	// P4-03 科学对照能力保留不删。
	HardGroupProxyBypass = "代理旁路"
)

var hardGroups = map[string]bool{
	HardGroupSysDestroy:  true,
	HardGroupDBDestroy:   true,
	HardGroupHTTPKill:    true,
	HardGroupBulkWipe:    true,
	HardGroupExfilPipe:   true,
	HardGroupProxyBypass: true,
}

// IsValidHardGroup 硬规则组是否在词表内。
func IsValidHardGroup(g string) bool { return hardGroups[g] }

// HardRuleBuiltinPrefix 内置规则的名称前缀(面板区分内置与自建)。
const HardRuleBuiltinPrefix = "[内置]"

// 硬规则处置词表(P4-03 ②):空 = 拒绝(命中直接拒绝并终止该单位,
// 默认);gate = 弹门(命中挂起等面板人审,批准执行、拒绝照旧熔断)。
const HardActionGate = "gate"

// HardRule 一条参数级硬规则。Pattern 对工具调用参数的字符串值做正则
// 匹配(参数 JSON 递归抽取全部字符串值,逐条匹配);ToolScope 限定
// 适用工具(空 = 全部工具);Action 选处置(拒绝 / 弹门)。
type HardRule struct {
	ID        string `json:"id"`
	Group     string `json:"group"`            // 组(词表)
	Name      string `json:"name"`             // 名称(内置条目带 [内置] 前缀)
	Pattern   string `json:"pattern"`          // 参数值正则(RE2)
	ToolScope string `json:"tool_scope"`       // 适用工具(空=全部)
	Action    string `json:"action,omitempty"` // 处置:""=拒绝(默认) / gate=弹门
	Enabled   bool   `json:"enabled"`
	Note      string `json:"note,omitempty"`
}

// hr 精简构造(内置清单用)。
func hr(id, group, name, pattern, scope string, enabled bool) HardRule {
	return HardRule{ID: id, Group: group, Name: HardRuleBuiltinPrefix + name,
		Pattern: pattern, ToolScope: scope, Enabled: enabled}
}

// builtinHardRules 内置清单(参考同类成熟清单拟定,四组 + 外泄管道组,
// 默认最保守全 deny——逃生门是面板禁用/编辑对应规则)。数据库破坏组
// 不限定工具——DROP TABLE 无论出现在 bash 命令还是注入 URL 参数里都
// 是数据销毁;其余各组限定 bash 通道。
func builtinHardRules() []HardRule {
	rules := []HardRule{
		// 系统破坏命令
		hr("hr-rm-root", HardGroupSysDestroy, "删除根目录",
			`\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+/(\s|\*|$)`, "bash", true),
		hr("hr-rm-home", HardGroupSysDestroy, "删除家目录",
			`\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+(~|\$HOME)(/\s|/\*|\s|$)`, "bash", true),
		hr("hr-mkfs", HardGroupSysDestroy, "格式化磁盘",
			`\b(mkfs(\.\w+)?|wipefs)\b`, "bash", true),
		hr("hr-dd-disk", HardGroupSysDestroy, "dd 直写磁盘设备",
			`\bdd\b[^|;&]*\bof=/dev/(sd|nvme|hd|vd|disk)`, "bash", true),
		hr("hr-redir-disk", HardGroupSysDestroy, "重定向清空磁盘设备",
			`>\s*/dev/(sd|nvme|hd|vd|disk)[a-z0-9]*`, "bash", true),
		hr("hr-shutdown", HardGroupSysDestroy, "关机重启",
			`\b(shutdown|reboot|halt|poweroff)\b|\binit\s+[06]\b`, "bash", true),
		hr("hr-forkbomb", HardGroupSysDestroy, "fork 炸弹",
			`:\(\)\s*\{[^}]*\};\s*:`, "bash", true),
		hr("hr-chmod-root", HardGroupSysDestroy, "全盘改权限",
			`\bchmod\s+-R\s+[0-7]+\s+/(\s|$)`, "bash", true),
		hr("hr-chown-root", HardGroupSysDestroy, "全盘改属主",
			`\bchown\s+-R\b[^|;&]*\s+/(\s|$)`, "bash", true),
		// 数据库破坏(不限工具:SQL 语句出现在命令或注入参数里同理)
		hr("hr-drop-table", HardGroupDBDestroy, "删除表",
			`(?i)\bDROP\s+TABLE\b`, "", true),
		hr("hr-drop-db", HardGroupDBDestroy, "删除数据库",
			`(?i)\bDROP\s+(DATABASE|SCHEMA)\b`, "", true),
		hr("hr-truncate", HardGroupDBDestroy, "清空表",
			`(?i)\bTRUNCATE\s+(TABLE\s+)?["\x60]?[\w.\[\]$-]+`, "", true),
		hr("hr-delete-nowhere", HardGroupDBDestroy, "无条件删除行",
			`(?i)\bDELETE\s+FROM\s+[\w.\[\]$-]+\s*(;|$)`, "", true),
		hr("hr-alter-drop", HardGroupDBDestroy, "删除表结构",
			`(?i)\bALTER\s+TABLE\s+\S+\s+DROP\b`, "", true),
		// HTTP 破坏请求
		hr("hr-http-delete", HardGroupHTTPKill, "HTTP 删除方法请求",
			`(?i)\b(curl|wget)\b[^|;&]*(-X\s*|--request\s*|--method\s*)DELETE\b`, "bash", true),
		hr("hr-http-purge", HardGroupHTTPKill, "破坏类管理端点请求",
			`(?i)\b(curl|wget)\b[^|;&]*/(admin|api)?[^\s|;&]*(purge|reset|flush|clear-all)\b`, "bash", true),
		// 批量清空路径
		hr("hr-find-delete", HardGroupBulkWipe, "find 批量删除",
			`(?i)\bfind\s+[^|;&]*\s-delete\b`, "bash", true),
		hr("hr-find-exec-rm", HardGroupBulkWipe, "find 逐个执行删除",
			`(?i)\bfind\s+[^|;&]*-exec\s+rm\b`, "bash", true),
		hr("hr-shred", HardGroupBulkWipe, "文件粉碎",
			`\bshred\b`, "bash", true),
		hr("hr-rm-star", HardGroupBulkWipe, "通配清空目录内容",
			`\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+\./?\*(\s|$)`, "bash", true),
		// 外泄管道(默认禁用:反弹 shell / 数据回传是渗透合法动作)
		hr("hr-devtcp", HardGroupExfilPipe, "反弹 shell(/dev/tcp 重定向)",
			`(>\s*&|<&)\s*/dev/(tcp|udp)/`, "bash", false),
		hr("hr-nc-exec", HardGroupExfilPipe, "netcat 反弹执行",
			`(?i)\b(nc|ncat|netcat|socat)\b[^|;&]*\s(-e|-c|--exec)\b`, "bash", false),
		hr("hr-pipe-exfil", HardGroupExfilPipe, "管道数据外传",
			`(?i)\b(curl|wget)\b[^|;&]*(-d\s*@-|--data-binary\s*@-|-F\s*\"?file=@|--upload-file\s+-|-T\s+-)`, "bash", false),
		// 代理旁路(P4-21 默认禁用:弹门随单位数轰炸的审批疲劳在案,
		// 旁路命令直接放行执行、命令与执行照旧全量留痕,仅直连流量
		// 不经镜;严审计部署面板一键启用。规则定义与 P4-03 ② 弹门处置
		// 保留不删——如怀疑观测被镜的本地代理污染、需 --noproxy 直连
		// 复测的科学对照场景,启用后由人裁决)。四类模式:curl 显式绕
		// 代理(--noproxy / 置空代理)、wget 绕代理、env -i 清空注入
		// 环境、unset/空赋值/通配 no_proxy 清除代理变量。
		hr("hr-proxy-curl-bypass", HardGroupProxyBypass, "curl 绕过代理",
			`(?i)\bcurl\b[^|;&]*(--noproxy\b|--proxy[= ]?["']{2}|-x[= ]?["']{2})`, "bash", false),
		hr("hr-proxy-wget-bypass", HardGroupProxyBypass, "wget 绕过代理",
			`(?i)\bwget\b[^|;&]*(--no-proxy\b|--proxy=["']{2})`, "bash", false),
		hr("hr-proxy-env-clear", HardGroupProxyBypass, "清空环境绕过代理",
			`(?i)\benv\s+(-i\b|--ignore-environment\b)`, "bash", false),
		hr("hr-proxy-unset-vars", HardGroupProxyBypass, "清除代理变量",
			`(?i)\bunset\b[^|;&]*(https?_proxy|all_proxy|no_proxy)\b|\b(https?_proxy|all_proxy|no_proxy)=(["']{2}|["']?\*|\s|$)`, "bash", false),
	}
	for i := range rules {
		if rules[i].Group == HardGroupProxyBypass {
			rules[i].Note = proxyBypassNote
			rules[i].Action = HardActionGate
		}
	}
	return rules
}

// proxyBypassNote 代理旁路组的提示(弹门上下文与拒绝原因共用,P4-03 ②
// 起该组处置为弹门:拼进待决审批卡的上下文说明)。
const proxyBypassNote = "流量必须经镜"

// isBuiltinHardRuleID 判断规则 ID 是否属于内置清单。
func isBuiltinHardRuleID(id string) bool {
	for _, r := range builtinHardRules() {
		if r.ID == id {
			return true
		}
	}
	return false
}

// hardRuleID 生成自建规则短 ID(hr-<rand>,与内置 hr-* 同段但随机后缀)。
func hardRuleID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("hr-%d", time.Now().UnixNano())
	}
	return "hr-" + hex.EncodeToString(b[:])
}

// SeedHardRules 启动播种:内置清单逐条写入缺失键;已存在的键一律不动
// (用户改过 / 禁用过 / 编辑过的条目保持用户状态——播种不覆盖用户改动)。
// 两个历史例外(均为一次性迁移,只认内置 ID、不碰自建条目):
//   - P4-03 ② 处置迁移:旧库内置代理旁路条目无 action 字段(字段当时
//     不存在,不存在用户意志),补写 gate;用户此后经面板改过处置
//     (action 字段已有值)则不再触碰。
//   - P4-21 默认禁用迁移:存量部署的内置代理旁路条目一律置为不启用
//     (与默认一致,行为统一);标记键写过即不再触碰,面板此后的
//     启用是用户意志,重启不回改。
//
// 删除过的内置条目在墓碑内,不再复活。幂等,可每次启动调用。
func SeedHardRules(ctx context.Context, store *rings.Store) error {
	if store == nil {
		return nil
	}
	tombs := hardRuleTombstones(ctx, store)
	for _, r := range builtinHardRules() {
		if tombs[r.ID] {
			continue
		}
		if e, ok, err := store.Setting(ctx, keyHardRulePrefix+r.ID); err != nil {
			return fmt.Errorf("swarm: 播种硬规则 %s: %w", r.ID, err)
		} else if ok {
			if r.Group == HardGroupProxyBypass && r.Action == HardActionGate {
				var old HardRule
				if json.Unmarshal([]byte(e.Value), &old) == nil && old.ID == r.ID && old.Action == "" {
					old.Action = HardActionGate
					if raw, merr := json.Marshal(old); merr == nil {
						if serr := store.SetSetting(ctx, keyHardRulePrefix+r.ID, string(raw), "system"); serr != nil {
							return fmt.Errorf("swarm: 迁移硬规则处置 %s: %w", r.ID, serr)
						}
					}
				}
			}
			continue
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("swarm: 序列化硬规则 %s: %w", r.ID, err)
		}
		if err := store.SetSetting(ctx, keyHardRulePrefix+r.ID, string(raw), "system"); err != nil {
			return fmt.Errorf("swarm: 播种硬规则 %s: %w", r.ID, err)
		}
	}
	return migrateProxyBypassDefaultOff(ctx, store, tombs)
}

// migrateProxyBypassDefaultOff P4-21 一次性迁移:内置代理旁路条目(默认
// 曾经启用)一律置为不启用,存量部署与新默认行为统一。只认内置清单里的
// 代理旁路 ID 且库存条目仍属该组(自建条目、被用户改组的条目不动);
// 已是禁用态的条目跳过写。迁移完成写标记键,此后重启不再触碰。
func migrateProxyBypassDefaultOff(ctx context.Context, store *rings.Store, tombs map[string]bool) error {
	if e, ok, err := store.Setting(ctx, keyHardRuleBypassOff); err != nil {
		return fmt.Errorf("swarm: 读代理旁路迁移标记: %w", err)
	} else if ok && e.Value != "" {
		return nil
	}
	for _, r := range builtinHardRules() {
		if r.Group != HardGroupProxyBypass || tombs[r.ID] {
			continue
		}
		e, ok, err := store.Setting(ctx, keyHardRulePrefix+r.ID)
		if err != nil {
			return fmt.Errorf("swarm: 迁移代理旁路默认禁用 %s: %w", r.ID, err)
		}
		if !ok {
			continue
		}
		var old HardRule
		if json.Unmarshal([]byte(e.Value), &old) != nil || old.ID != r.ID || old.Group != HardGroupProxyBypass || !old.Enabled {
			continue
		}
		old.Enabled = false
		raw, merr := json.Marshal(old)
		if merr != nil {
			return fmt.Errorf("swarm: 序列化硬规则 %s: %w", r.ID, merr)
		}
		if serr := store.SetSetting(ctx, keyHardRulePrefix+r.ID, string(raw), "system"); serr != nil {
			return fmt.Errorf("swarm: 迁移代理旁路默认禁用 %s: %w", r.ID, serr)
		}
	}
	if err := store.SetSetting(ctx, keyHardRuleBypassOff, "1", "system"); err != nil {
		return fmt.Errorf("swarm: 写代理旁路迁移标记: %w", err)
	}
	return nil
}

// hardRuleTombstones 已删除的内置规则 ID 集合(读失败按空处理)。
func hardRuleTombstones(ctx context.Context, store *rings.Store) map[string]bool {
	out := map[string]bool{}
	e, ok, err := store.Setting(ctx, keyHardRuleTomb)
	if err != nil || !ok {
		return out
	}
	var ids []string
	if json.Unmarshal([]byte(e.Value), &ids) != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// ListHardRules 键值表内的硬规则全量清单(按组稳定排序)。键值表为空
// (未播种)时返回空清单——判定路径另有内置兜底,面板在启动播种后
// 恒有清单。
func ListHardRules(ctx context.Context, store *rings.Store) ([]HardRule, error) {
	entries, err := store.SettingsWithPrefix(ctx, keyHardRulePrefix)
	if err != nil {
		return nil, fmt.Errorf("swarm: 读硬规则清单: %w", err)
	}
	var out []HardRule
	for _, e := range entries {
		var r HardRule
		if err := json.Unmarshal([]byte(e.Value), &r); err != nil {
			continue // 脏值跳过(单条坏数据不拖垮整表读取)
		}
		out = append(out, r)
	}
	sortHardRules(out)
	return out, nil
}

// sortHardRules 组内稳定排序(播种序→名称,ID 兜底)。
func sortHardRules(rules []HardRule) {
	rank := map[string]int{}
	for i, b := range builtinHardRules() {
		rank[b.ID] = i
	}
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0 && hardRuleLess(rules[j], rules[j-1], rank); j-- {
			rules[j], rules[j-1] = rules[j-1], rules[j]
		}
	}
}

func hardRuleLess(a, b HardRule, rank map[string]int) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	ra, oka := rank[a.ID]
	rb, okb := rank[b.ID]
	if oka != okb {
		return okb
	}
	if ra != rb {
		return ra < rb
	}
	return a.ID < b.ID
}

// SaveHardRule 保存(新建 / 编辑)一条硬规则:名称非空、组与工具范围在
// 词表内、正则可编译、处置在词表内;[内置] 前缀只许内置条目用(自建
// 不得冒名)。编辑内置条目 = 覆盖同 ID 键,播种不会再覆盖它(键已存在)。
func SaveHardRule(ctx context.Context, store *rings.Store, r HardRule, by string) (string, error) {
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	r.Group = strings.TrimSpace(r.Group)
	r.ToolScope = strings.TrimSpace(r.ToolScope)
	r.Action = strings.TrimSpace(r.Action)
	if r.Name == "" || len(r.Name) > 64 {
		return "", fmt.Errorf("swarm: 规则名称须为 1~64 个字符")
	}
	if strings.HasPrefix(r.Name, HardRuleBuiltinPrefix) && !isBuiltinHardRuleID(r.ID) {
		return "", fmt.Errorf("swarm: 名称不得以 %s 开头(内置前缀)", HardRuleBuiltinPrefix)
	}
	if !IsValidHardGroup(r.Group) {
		return "", fmt.Errorf("swarm: 规则组 %q 不在词表内", r.Group)
	}
	if r.ToolScope != "" && !knownToolScopes[r.ToolScope] {
		return "", fmt.Errorf("swarm: 适用工具 %q 不在工具全集内", r.ToolScope)
	}
	if r.Action != "" && r.Action != HardActionGate {
		return "", fmt.Errorf("swarm: 处置 %q 不在词表内(空=拒绝 / gate=需批准)", r.Action)
	}
	if _, err := regexp.Compile(r.Pattern); err != nil {
		return "", fmt.Errorf("swarm: 匹配模式编译失败: %w", err)
	}
	if r.ID == "" {
		r.ID = hardRuleID()
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("swarm: 序列化硬规则: %w", err)
	}
	if err := store.SetSetting(ctx, keyHardRulePrefix+r.ID, string(raw), by); err != nil {
		return "", err
	}
	return r.ID, nil
}

// knownToolScopes 适用工具词表(虫群工具全集,tools.go toolset)。
var knownToolScopes = map[string]bool{
	"bash": true, "port_probe": true, "http_probe": true,
	"list_claims":           true,
	"submit_candidate_fact": true, "submit_hypothesis": true,
	"submit_hypothesis_result": true, "submit_test_claim": true,
	"submit_falsify_result": true,
}

// DeleteHardRule 删除一条硬规则:内置条目同时记入墓碑(重启播种不复活)。
func DeleteHardRule(ctx context.Context, store *rings.Store, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("swarm: 规则 ID 不能为空")
	}
	if _, ok, err := store.Setting(ctx, keyHardRulePrefix+id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("swarm: 硬规则 %s 不存在", id)
	}
	if err := store.DeleteSetting(ctx, keyHardRulePrefix+id); err != nil {
		return err
	}
	if isBuiltinHardRuleID(id) {
		tombs := hardRuleTombstones(ctx, store)
		if !tombs[id] {
			ids := make([]string, 0, len(tombs)+1)
			for t := range tombs {
				ids = append(ids, t)
			}
			ids = append(ids, id)
			raw, err := json.Marshal(ids)
			if err != nil {
				return fmt.Errorf("swarm: 序列化墓碑: %w", err)
			}
			if err := store.SetSetting(ctx, keyHardRuleTomb, string(raw), "creator"); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- 判定(每次工具调用逐条读当前规则,永远新鲜) ----

// activeHardRules 判定用规则集:读键值表;键值表整体为空(未播种的
// 新库 / 读失败)退代码内置清单的启用项——拦截底线不因配置不可读而
// 失守(fail-safe 方向:宁可多拦,交给面板禁用来放开)。
func activeHardRules(ctx context.Context, store *rings.Store) []HardRule {
	if store != nil {
		if rules, err := ListHardRules(ctx, store); err == nil && len(rules) > 0 {
			return rules
		}
	}
	var out []HardRule
	for _, r := range builtinHardRules() {
		out = append(out, r)
	}
	return out
}

// matchHardRule 对一次工具调用匹配硬规则:命中返回规则与匹配串。
// 匹配目标 = 参数 JSON 递归抽取的全部字符串值(对 bash 是 command、对
// http_probe 是 url 等);禁用的规则不参与;适用工具不符的不参与;
// 模式编译失败的条目跳过(存入口已校验,仅防键值表脏数据)。
func matchHardRule(ctx context.Context, store *rings.Store, toolName string, input json.RawMessage) (HardRule, string, bool) {
	text := argStringValues(input)
	if strings.TrimSpace(text) == "" {
		return HardRule{}, "", false
	}
	for _, r := range activeHardRules(ctx, store) {
		if !r.Enabled {
			continue
		}
		if r.ToolScope != "" && r.ToolScope != toolName {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		if m := re.FindString(text); m != "" {
			return r, m, true
		}
	}
	return HardRule{}, "", false
}

// argStringValues 递归抽取 JSON 参数里的全部字符串值,拼成多行文本
// (参数级匹配的载体:正则对值生效,不对键名与 JSON 转义形态生效)。
func argStringValues(input json.RawMessage) string {
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return string(input) // 非 JSON 入参退化为原文整体匹配
	}
	var b strings.Builder
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case []any:
			for _, item := range t {
				walk(item)
			}
		case map[string]any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return b.String()
}

// hardRuleDenyReason 硬规则拒绝原因(审批记录 reason 字段,面板与模型
// 均可见)。规则的 Note(如代理旁路组的"流量必须经镜")拼在尾部。
func hardRuleDenyReason(r HardRule, matched string) string {
	reason := fmt.Sprintf("内置硬规则命中:%s · %s(命中片段 %q)", r.Group, strings.TrimPrefix(r.Name, HardRuleBuiltinPrefix), clipLine(matched, 80))
	if n := strings.TrimSpace(r.Note); n != "" {
		reason += " · " + n
	}
	return reason
}

// hardRuleGateNote 弹门处置规则的待决卡上下文(P4-03 ②):说明为何命中
// 拦截规则却到了人面前——命令原文在审批卡的参数区,这里只给规则与
// 命中片段(人裁决"直连复测是否正当"所需的最小事实)。
func hardRuleGateNote(r HardRule, matched string) string {
	note := fmt.Sprintf("命中内置拦截规则:%s · %s(命中片段 %q)", r.Group, strings.TrimPrefix(r.Name, HardRuleBuiltinPrefix), clipLine(matched, 80))
	if n := strings.TrimSpace(r.Note); n != "" {
		note += " · " + n
	}
	return note
}
