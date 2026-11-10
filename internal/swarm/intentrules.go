package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"ghostreaper/internal/rings"
)

// ---- 任务级意图拦截(P2-08e,第四章 §3 v2.1 判定次序③ / §4) ----
//
// 规则 = 资产范围 × 动作类型(操作者语言)× 处置(禁止 / 允许 + 次数
// 上限),随 RoE 快照提交、下一轮领取读到新值(R7 无旁路)。动作归类
// 是确定性代码:按工具与参数判定,不依赖模型声明;归类不出词表的调用
// 不命中任何规则(放行链照常走后续门槛)。量化额度:允许 + 次数上限
// 的规则,每次放行计数入账(guard.record),超额自动转禁止。

// intentClassifiers 动作归类的确定性规则:命令文本模式 → 动作类型。
// 只对 bash 命令分类(http_probe/port_probe 按工具直判)。清单按危险
// 向排序逐条试:爆破 → 爬取 → 注入 → 上传 → 数据读取,先命中先归类。
var intentClassifiers = []struct {
	action  rings.IntentAction
	pattern *regexp.Regexp
}{
	{rings.IntentBruteForce, regexp.MustCompile(`(?i)\b(hydra|medusa|john|hashcat|ncrack|patator)\b`)},
	{rings.IntentCrawl, regexp.MustCompile(`(?i)\b(gobuster|dirb|dirsearch|ffuf|feroxbuster|nikto)\b|\bwget\b[^|;&]*(\s-r\b|--mirror|--recursive)`)},
	{rings.IntentInjectTest, regexp.MustCompile(`(?i)\bsqlmap\b|\bcurl\b[^|;&]*(\s-d\b|--data|--data-raw|--data-urlencode|--json\b)`)},
	{rings.IntentUpload, regexp.MustCompile(`(?i)\bcurl\b[^|;&]*(\s-T\b|--upload-file|\s-F\b|--form\b)|\bwget\b[^|;&]*(--post-file|--post-data)`)},
	{rings.IntentPortScan, regexp.MustCompile(`(?i)\b(nmap|masscan|rustscan)\b`)},
	{rings.IntentDataRead, regexp.MustCompile(`(?i)\b(curl|wget)\b`)},
}

// classifyIntentAction 把一次工具调用确定性归类为动作类型词表之一;
// 归不出来返回 ""(本地命令、受控回流入口等,无规则可命中)。
func classifyIntentAction(toolName string, input json.RawMessage) rings.IntentAction {
	switch toolName {
	case "port_probe":
		return rings.IntentPortScan
	case "http_probe":
		return rings.IntentDataRead
	case "bash":
		cmd := bashCommandOf(input)
		if strings.TrimSpace(cmd) == "" {
			return ""
		}
		for _, c := range intentClassifiers {
			if c.pattern.MatchString(cmd) {
				return c.action
			}
		}
	}
	return ""
}

// bashCommandOf 抽 bash 参数里的命令文本;非 bash 参数形态返回空。
func bashCommandOf(input json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	return args.Command
}

// ---- 资产范围匹配 ----

// hostPort 主机与可选端口的定位(anchor/target 通用的最小单元)。
type hostPort struct {
	host string
	port string
}

// assetAnchor 从资产标题解析定位(URL 或 host:port 形态)。解析不出
// (纯名称型标题)返回 ok=false——此类资产只靠步骤 covers 命中。
func assetAnchor(title string) (hostPort, bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return hostPort{}, false
	}
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		if u, err := url.Parse(t); err == nil && u.Hostname() != "" {
			return hostPort{host: u.Hostname(), port: u.Port()}, true
		}
		return hostPort{}, false
	}
	// host:port / host 形态(端口数字校验防误把句子拆开)。
	if i := strings.LastIndex(t, ":"); i > 0 && !strings.ContainsAny(t[:i], "/ ") {
		p := t[i+1:]
		if p != "" && strings.Trim(p, "0123456789") == "" {
			return hostPort{host: t[:i], port: p}, true
		}
	}
	if t != "" && !strings.ContainsAny(t, " /") {
		return hostPort{host: t}, true
	}
	return hostPort{}, false
}

// urlRe 命令文本里的 http(s) URL 提取(端口随 URL)。
var urlRe = regexp.MustCompile(`https?://[^\s'"|;&<>]+`)

// callTargets 解析一次调用指向的目标定位:http_probe 的 url 参数、
// port_probe 的 host/port 参数、bash 命令文本里的全部 URL。
func callTargets(toolName string, input json.RawMessage) []hostPort {
	switch toolName {
	case "http_probe":
		var args struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(input, &args) == nil && args.URL != "" {
			if u, err := url.Parse(strings.TrimSpace(args.URL)); err == nil && u.Hostname() != "" {
				return []hostPort{{host: u.Hostname(), port: u.Port()}}
			}
		}
	case "port_probe":
		var args struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		if json.Unmarshal(input, &args) == nil && args.Host != "" {
			return []hostPort{{host: args.Host, port: fmt.Sprintf("%d", args.Port)}}
		}
	case "bash":
		var out []hostPort
		for _, raw := range urlRe.FindAllString(bashCommandOf(input), -1) {
			raw = strings.TrimRight(raw, ".,);]")
			if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
				out = append(out, hostPort{host: u.Hostname(), port: u.Port()})
			}
		}
		return out
	}
	return nil
}

// anchorHits 定位相等:主机相等,且双方都带端口时端口也相等(任一方
// 缺端口按主机匹配——协议默认端口不构成区隔)。
func anchorHits(a, b hostPort) bool {
	if a.host == "" || b.host == "" || a.host != b.host {
		return false
	}
	if a.port != "" && b.port != "" {
		return a.port == b.port
	}
	return true
}

// intentRuleScopeHit 规则资产范围是否命中本次调用:范围为空 = 全部
// 资产恒命中;否则 ① 单位步骤 covers 的资产里有规则资产(声明范围),
// 或 ② 调用目标定位命中规则资产的锚点(实际指向)。packAssets 是步骤
// covers 的资产(含标题),covers 集合按 ID 匹配。
func intentRuleScopeHit(rule rings.IntentRule, coveredIDs map[string]bool, packAssets []AssetBrief, targets []hostPort) bool {
	if len(rule.AssetIDs) == 0 {
		return true
	}
	for _, id := range rule.AssetIDs {
		if coveredIDs[id] {
			return true
		}
	}
	for _, id := range rule.AssetIDs {
		for _, a := range packAssets {
			if a.ID != id {
				continue
			}
			if anchor, ok := assetAnchor(a.Title); ok {
				for _, t := range targets {
					if anchorHits(anchor, t) {
						return true
					}
				}
			}
		}
	}
	return false
}

// assetScopeNames 规则资产范围的操作者呈现(资产标题顿号连接,超长截断)。
func assetScopeNames(rule rings.IntentRule, assets []AssetBrief) string {
	if len(rule.AssetIDs) == 0 {
		return ""
	}
	titles := map[string]string{}
	for _, a := range assets {
		titles[a.ID] = a.Title
	}
	var parts []string
	for _, id := range rule.AssetIDs {
		if t, ok := titles[id]; ok {
			parts = append(parts, t)
		} else {
			parts = append(parts, id)
		}
	}
	return clipLine(strings.Join(parts, "、"), 80)
}

// intentDenyReason 任务级拦截拒绝原因(审批记录 reason 字段)。
func intentDenyReason(rule rings.IntentRule, assets []AssetBrief, quotaExhausted bool) string {
	scope := "全部资产"
	if names := assetScopeNames(rule, assets); names != "" {
		scope = names
	}
	if quotaExhausted {
		return fmt.Sprintf("任务级拦截:对 %s 的%s已用满 %d 次上限,自动转禁止", scope, rule.Action, rule.Quota)
	}
	return fmt.Sprintf("任务级拦截:禁止对 %s 做%s", scope, rule.Action)
}

// countQuotaUsed 任务内某规则已计数次数(guard.record quota_count 事件
// 计数;读失败返回 0,由调用方按"未用尽"处理——计数缺失不拦放行,
// 计数本身的账面由入账方保证)。
func countQuotaUsed(ctx context.Context, store *rings.Store, taskID, ruleID string) int {
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return 0
	}
	n := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventGuardRecord {
			continue
		}
		var rec rings.GuardRecord
		if json.Unmarshal(ev.Payload, &rec) == nil &&
			rec.Type == rings.GuardRecQuotaCount && rec.RuleID == ruleID {
			n++
		}
	}
	return n
}

// liveIntentRules 判定用规则集(审核迭代:现读语义)——每次工具调用读
// RoE 当前值,改动对下一次判定即时生效;范围过滤在 checkIntentRules
// 内做(范围外单位连求值都不发生)。读失败退任务包冻结副本(fail-safe:
// 有旧规则可依好过没有),零规则照常放行(其余判定链兜底)。
func (env *unitEnv) liveIntentRules(fallback []rings.IntentRule) []rings.IntentRule {
	if env.Executor.Store == nil {
		return fallback
	}
	roe, err := env.Executor.Store.CurrentRoE(guardLedgerCtx, env.TaskID)
	if err != nil || len(roe.IntentRules) == 0 {
		return fallback
	}
	return roe.IntentRules
}

// checkIntentRules 判定次序③(P2-08e;审核迭代三轮:规则自带正则 +
// 现读 + 不熔断):按 RoE 当前任务级规则对本次调用做禁止 / 额度判定,
// 规则范围与本单位相交才参与求值(测其他资产的单位零感知)。动作匹配
// 两路:规则带 Patterns(描述推导或手写的正则)→ 对调用参数文本逐条
// 正则匹配(确定性,同硬规则);不带 → 按内置动作词表分类比对(描述
// 恰好是词表词的兼容路径,旧规则继续生效)。命中禁止或超额返回拒绝
// 原因与规则 ID(P4-63 B:同规则累计拒绝升级杀的计数键)——只拒绝本次
// 调用、单位继续其余工作(升级保险在 denyIntentAction);允许 + 次数
// 上限在此计数入账(guard.record,临界区防并发重号;额度全任务共享)。
func (env *unitEnv) checkIntentRules(pack *TaskPack, toolName string, input json.RawMessage) (string, string, bool) {
	rules := env.liveIntentRules(pack.Authorization.IntentRules)
	if len(rules) == 0 {
		return "", "", false
	}
	action := classifyIntentAction(toolName, input)
	covered := make(map[string]bool, len(pack.Assets))
	for _, a := range pack.Assets {
		covered[a.ID] = true
	}
	targets := callTargets(toolName, input)
	for _, r := range rules {
		if !intentRuleActionHit(r, action, toolName, input) {
			continue
		}
		if !intentRuleScopeHit(r, covered, pack.Assets, targets) {
			continue
		}
		if r.Disposition != rings.IntentForbid {
			// 允许 + 次数上限:读计数 → 判 → 入账,临界区串行。
			// 上限 0 = 不限(操作者明确给定才限次)。
			if r.Quota > 0 {
				env.Executor.quotaMu.Lock()
				used := countQuotaUsed(guardLedgerCtx, env.Executor.Store, env.TaskID, r.ID)
				if used >= r.Quota {
					env.Executor.quotaMu.Unlock()
					return intentDenyReason(r, pack.Assets, true), r.ID, true
				}
				env.recordQuotaCount(r, action, used+1, toolName)
				env.Executor.quotaMu.Unlock()
			}
			continue
		}
		return intentDenyReason(r, pack.Assets, false), r.ID, true
	}
	return "", "", false
}

// intentRuleActionHit 动作匹配:带 Patterns 走正则(对参数文本,同硬
// 规则的匹配载体),否则按内置词表分类比对(Patterns 空 = 描述是词表
// 词或旧规则)。正则编译失败的条目跳过(存入口已校验,防脏数据)。
func intentRuleActionHit(r rings.IntentRule, classified rings.IntentAction, toolName string, input json.RawMessage) bool {
	if len(r.Patterns) > 0 {
		for _, p := range r.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			for _, value := range argStringValuesSlice(input) {
				if strings.TrimSpace(value) == "" {
					continue
				}
				if re.MatchString(value) {
					return true
				}
			}
		}
		return false
	}
	if classified == "" {
		return false // 归不进词表的调用,无词表路径可命中
	}
	return r.Action == string(classified)
}

// recordQuotaCount 量化额度计数入账:每次在允许层放行记第 Nth 次
// (独立 ctx,判定留痕不因任务熔断而断账)。
func (env *unitEnv) recordQuotaCount(r rings.IntentRule, action rings.IntentAction, nth int, toolName string) {
	_, _ = env.Executor.Store.Append(guardLedgerCtx, env.TaskID, rings.Event{
		Kind: rings.EventGuardRecord, Actor: "swarm:" + env.UnitID,
		Payload: rings.GuardRecord{
			StepID: env.StepID, UnitID: env.UnitID, Type: rings.GuardRecQuotaCount,
			RuleID: r.ID, Action: string(action), Nth: nth, ToolName: toolName,
		},
	})
}
