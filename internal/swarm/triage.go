// P4-67(问题清单 #86 A②,RoE 机制重构批次第二层):范围外命中(经
// P4-66 豁免后仍命中)在出卡前的单次轻量模型分诊。判据全部在提示
// 词层(零站点枚举、零正则规则——攻击判定不写规则,防 PoC/exp 代码
// 本身被特征匹配误拦)。调用失败按"拿不准"处理(偏向出卡,宁可多
// 问不误放)。
//
// P4-68 ③b(分诊等待口径修正):删除 30 秒墙钟——分诊等结果本身,
// 失败判定交给 LLM 链既有语义(重试/主备切换/读超时);链真报错才按
// 拿不准出卡,慢不算失败。分诊是审批门里的单次问话(无会话、不占
// 名额),单位原地待命。
//
// P4-72(问题清单 #88 B 方案,实测修正批次第一阶):分诊契约升级为
// **模型提取＋定性合一**——一次调用两问合一:"这条命令实际连接哪
// 些目标?每个目标什么性质?"输出目标清单＋逐目标判定(五值词表),
// 取代逐 host 单值的旧契约(合并扩展,不是并列第二套分诊)。快路径
// 未全解的命令整条交模型(静默漏拦根除:"不认识的旗标"这一概念从
// 机制中消失);判据校准并入提示词——正例(无归属站的连通性测距/
// 下载审计/搜索查档＝方法性)、反例(同根域站即使表述为连通测试也
// 判候选,归属优先)、example.com 类判定钉死。存量 tg- 缓存的旧词表
// 值兼容读(triageSemantic 归一)。
//
// 任务级缓存两层:信息源清单(roe_state)＝info_access 结论的落账
// 形态;tg-<host> 研判留痕(approval.record triage)追加式记录,读侧
// 取最新即任务级分诊缓存——同任务内同 host 零重复调用。缓存的
// 是判过的结论,不是预设知识。本文件为命令路(单位侧)与发现路
// (收割侧)共用的实现面。
package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// P4-72 合一五值(模型输出的契约词表;解析失败按"拿不准")。
const (
	TriageInScope    = "in_scope"          // 范围内:对照任务授权范围清单判在范围内(代码按闭包复核)
	TriageInfoAccess = "info_access"       // 信息源获取:公开信息服务且与目标组织无归属,动作是获取使用
	TriageInfoAttack = "info_attack"       // 信息源攻击:对信息源的主动攻击动作(写入/扫描/利用尝试)
	TriageCandidate  = "related_candidate" // 相关资产候选:有归属线索或归属不明确(拿不准偏向这里)
	TriageNotTarget  = "not_target"        // 非目标:不是实际连接目标(参数值/端口数字/速率数字/文档字符串等),静默过滤
)

// P4-67 旧四值(存量 tg- 缓存兼容读;新调用不再输出,语义经
// triageSemantic 归一到五值)。
const (
	TriageDeclared   = "declared_source"   // ≈ info_access(命中外部服务声明)
	TriageMethodical = "methodical_access" // ≈ info_access
	TriageAttack     = "attack_action"     // ≈ info_attack
)

// TriageRevoked 撤销标记(P4-67 ④):信息源撤销落的 tg- 缓存态——撤销
// 是操作者推翻模型的分类,同模型重新分诊只会得出同一结论再自动授,
// 撤销就成空操作;故撤销粘性:后续访问按候选出卡,由"加入资产/禁止
// 测试"一锤定音。牧者/面板重新标记信息源(入清单)优先于本标记。
const TriageRevoked = "revoked"

var triageVerdicts = map[string]bool{
	TriageInScope:    true,
	TriageInfoAccess: true,
	TriageInfoAttack: true,
	TriageCandidate:  true,
	TriageNotTarget:  true,
	// 旧词表(存量缓存兼容)。
	TriageDeclared: true, TriageMethodical: true, TriageAttack: true,
}

// triageSemantic 旧词表值归一到 P4-72 五值语义(处置分支只看五值)。
func triageSemantic(verdict string) string {
	switch verdict {
	case TriageDeclared, TriageMethodical:
		return TriageInfoAccess
	case TriageAttack:
		return TriageInfoAttack
	default:
		return verdict // 五值与 revoked 原样
	}
}

// IsTriageInfoSource 判定结论是否属"信息源"(自动授访问权、入清单)。
func IsTriageInfoSource(verdict string) bool {
	return triageSemantic(verdict) == TriageInfoAccess
}

// TriageTarget 合一清单的单个目标判定(P4-72:目标清单＋逐目标判定)。
type TriageTarget struct {
	Host    string `json:"host"`
	Verdict string `json:"verdict"`
	Basis   string `json:"basis"`
}

// TriageReply 合一调用输出:模型提取的目标清单(逐目标带判定与依据)。
type TriageReply struct {
	Targets []TriageTarget
}

// Single 单目标形态(动作复查/发现路:取指定 host 的判定;模型未列出
// 该 host 返回 ok=false——调用方按拿不准处置)。兼容:整份清单仅一条
// 且未具名 host 时(旧单值契约形态),视为锚点目标的判定。
func (r TriageReply) Single(host string) (TriageTarget, bool) {
	want := strings.ToLower(strings.TrimSpace(host))
	if a, ok := rings.ScopeAnchorOf(want); ok {
		want = a.Host
	}
	for _, t := range r.Targets {
		got := strings.ToLower(strings.TrimSpace(t.Host))
		if a, ok := rings.ScopeAnchorOf(got); ok {
			got = a.Host // 模型输出带端口/URL 形态,归一到裸 host 匹配
		}
		if strings.EqualFold(got, want) {
			return t, true
		}
	}
	if len(r.Targets) == 1 && strings.TrimSpace(r.Targets[0].Host) == "" {
		return r.Targets[0], true
	}
	return TriageTarget{}, false
}

// TriageInput 一次合一判定的输入上下文。命令路带 Command(命令原文)
// 与 Candidates(确定性提取的范围外待判目标,模型逐个定性并自行从
// 命令原文补提遗漏目标);发现路带 Discovered(发现内容摘要)＋Host;
// 动作复查(ListedInfoSource)只判该 host 的动作性质。ScopeSummary 为
// 任务授权范围写法清单(in_scope 判定对照;代码侧按闭包复核,模型
// 判定不作放行依据)。
type TriageInput struct {
	Host             string   // 单目标形态锚点(动作复查/发现路)
	Command          string   // 命令原文(命令路)
	Discovered       string   // 发现内容摘要(发现路:发现事实标题)
	Intent           string   // 当前步骤意图
	GoalSummary      string   // 任务目标摘要
	DeclaredServices string   // 外部服务声明(可选;命中即信息源获取)
	ListedInfoSource bool     // host 已在信息源清单(动作复查:只判动作性质)
	ScopeSummary     string   // 任务授权范围写法清单(in_scope 对照)
	Candidates       []string // 确定性提取的待判目标(host[:port] 显示形态)
}

// triagePrompt 合一系统段(一次调用两问合一;判据语义逐条对应条目
// ②③,提示词层语义判据:零站点枚举、零正则)。
const triagePrompt = `你是渗透测试任务的边界研判员。给你一条命令(或一个发现),一次回答两个问题:它实际连接/涉及哪些目标主机?每个目标各是什么性质?

先从命令原文里找全目标:凡是这条命令实际会发起连接的主机都要列出(确定性提取的候选目标必须逐个判定;候选之外命令里还连了谁也要补上)。参数值、端口数字、速率数字、文档字符串、注入载荷里写了但没有实际连接动作的地址不是目标。

再对每个目标给判定(词表五值)。选择顺序:先对范围——落在授权写法内=in_scope;再定归属——与目标组织无归属的公开服务按动作性质二选一(info_access 或 info_attack),判了无归属就不要再选 related_candidate;存在归属线索或归属不明确才 related_candidate。
1. in_scope(范围内):判定顺序最先——该目标确实落在「任务授权范围」清单的写法内即判此项,与本次动作性质无关(动作判定只用于范围外地址)。写法对照规则:裸域名=仅该域名本身(不含任何子域,子域一律范围外)、*.域=该域与全部子域、裸 IP=整台、IP:端口/域名:端口=仅此端口、种子域名解析出的 IP 同范围。严格按写法对照,不做"可能是同一台机器/同一组织"之类的推断——写法对不上的,无论多像目标都不判此项。
2. info_access(信息源获取):该地址属公开信息服务,且与目标组织无归属关系,这次动作是正常获取使用。归属线索:与任务目标同根域、同注册主体、同 IP 段、目标页面或其 JS 引用的服务域名等——只要存在任何归属线索,或归属无法确认,就不判此项。
3. info_attack(信息源攻击):该地址属公开信息服务且与目标组织无归属,但这次动作是对它的主动攻击(写入、扫描、利用尝试),不是正常获取使用。分界:端口扫描、服务探测、目录爆破、漏洞利用尝试都属攻击——即使动机是"获取信息";以服务正常功能使用它(打开网页、下载文件、搜索、查公共文档、调用公开 API)才是获取使用。判断依据是动作意图,不是命令里出现什么词。
4. related_candidate(相关资产候选):存在归属线索,或归属不明确。归属不明确指无法判断该地址是否与目标组织相关(线索矛盾、信息不足);"大型公开服务平台与一个具体目标无关"是明确的判断,不算不明确——代码托管/搜索引擎/公共文档/CDN 这类服务对具体渗透目标无归属是清楚的,不要因为"没查过"就当不明确。拿不准时一律判此项。
5. not_target(非目标):该地址不会被这条命令实际连接——纯载荷/文档字符串/示例文本/参数数值里的地址。判此项要能说出"为什么不连接";不认识的工具、看不懂的旗标、不确定是否连接的地址一律判 related_candidate(宁可疑不漏放)。

校准判例:
- 无归属站的连通性测距、下载审计、搜索查档=info_access;example.com/example.org/example.net 等示例保留域是文档对照站,连通测试判 info_access。
- 同根域站点即使命令表述为"测试连通性"也判 related_candidate(归属优先于动作表述)。
- 不认识的工具带地址类参数(如 mytool --endpoint 1.2.3.4、mytool --target evil.xx)按实际连接处理判 related_candidate——只有确定不连接(纯载荷/文档/数值)才判 not_target,工具不认识不等于不连接。

只输出 JSON:{"targets":[{"host":"目标主机","verdict":"in_scope|info_access|info_attack|related_candidate|not_target","basis":"一句话依据,写给操作者看"}]}`

// TriageFunc 一次合一判定调用(可注入:测试打桩;生产走任务 LLM 链)。
// 返回目标清单;错误(含超时)由调用方按"拿不准"处理。
type TriageFunc func(ctx context.Context, in TriageInput) (TriageReply, error)

// TriageFor 生产实现构造(命令路与发现路共用):任务 LLM 链单次
// Complete,输出上限收紧;解析失败或词表外值返回错误。P4-68 ③b 起
// 不设分诊自身墙钟——等结果本身,失败判定交给链既有语义(重试/主备
// 切换/读超时),链真报错才由调用方按"拿不准"处理(慢不算失败)。
func TriageFor(provider llm.Provider) TriageFunc {
	return func(ctx context.Context, in TriageInput) (TriageReply, error) {
		if provider == nil {
			return TriageReply{}, fmt.Errorf("swarm: 分诊无可用模型链")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "任务目标:%s\n", in.GoalSummary)
		fmt.Fprintf(&b, "当前步骤意图:%s\n", in.Intent)
		if in.DeclaredServices != "" {
			fmt.Fprintf(&b, "步骤写明的外部服务:%s\n", in.DeclaredServices)
		}
		if in.ScopeSummary != "" {
			fmt.Fprintf(&b, "任务授权范围:%s\n", in.ScopeSummary)
		}
		if len(in.Candidates) > 0 {
			fmt.Fprintf(&b, "确定性提取的待判目标(逐个给判定):%s\n", strings.Join(in.Candidates, "、"))
		}
		if in.Host != "" {
			fmt.Fprintf(&b, "访问目标:%s\n", in.Host)
		}
		if in.ListedInfoSource {
			b.WriteString("该地址已在任务信息源清单(查资料用的公开服务),归属无需重判,只需判断本次动作是正常获取使用还是对它的主动攻击\n")
		}
		if in.Command != "" {
			fmt.Fprintf(&b, "命令原文:%s", in.Command)
		} else if in.Discovered != "" {
			fmt.Fprintf(&b, "发现内容:%s", in.Discovered)
		}
		msg, _, _, err := provider.Complete(ctx, llm.CompletionRequest{
			System:    []string{triagePrompt},
			Messages:  []llm.Message{llm.UserText(b.String())},
			MaxTokens: 500,
			Thinking:  "disabled",
		})
		if err != nil {
			return TriageReply{}, err
		}
		return parseTriageReply(msgText(msg))
	}
}

// parseTriageReply 解析合一输出(targets 数组逐项词表校验;宽容剥
// markdown 围栏;单项依据空补默认、超长截断;全部非法返回错误——
// 调用方按拿不准处理)。
func parseTriageReply(text string) (TriageReply, error) {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "{"); i >= 0 {
		if j := strings.LastIndex(t, "}"); j > i {
			t = t[i : j+1]
		}
	}
	var out struct {
		Targets []TriageTarget `json:"targets"`
		// 兼容旧单值形态(打桩/过渡):{"verdict":..,"basis":..}
		Verdict string `json:"verdict"`
		Basis   string `json:"basis"`
		Host    string `json:"host"`
	}
	if err := json.Unmarshal([]byte(t), &out); err != nil {
		return TriageReply{}, fmt.Errorf("swarm: 分诊输出解析失败: %w", err)
	}
	reply := TriageReply{Targets: out.Targets}
	if len(reply.Targets) == 0 && out.Verdict != "" {
		reply.Targets = []TriageTarget{{Host: out.Host, Verdict: out.Verdict, Basis: out.Basis}}
	}
	if len(reply.Targets) == 0 {
		return TriageReply{}, fmt.Errorf("swarm: 分诊输出目标清单为空")
	}
	for i := range reply.Targets {
		tt := &reply.Targets[i]
		tt.Host = strings.ToLower(strings.TrimSpace(tt.Host))
		if !triageVerdicts[tt.Verdict] {
			return TriageReply{}, fmt.Errorf("swarm: 分诊判定不在词表内: %q", tt.Verdict)
		}
		if tt.Basis = strings.TrimSpace(tt.Basis); tt.Basis == "" {
			tt.Basis = "未给出依据"
		}
		if len([]rune(tt.Basis)) > 200 {
			tt.Basis = string([]rune(tt.Basis)[:200])
		}
	}
	return reply, nil
}

// msgText 消息的纯文本形态(分诊输出只取 text 块)。
func msgText(m llm.Message) string {
	var parts []string
	for _, b := range m.Content {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ---- 任务级分诊缓存与落账(tg- 研判留痕) ----

// TriageVerdictOf 任务级缓存查询:tg-<host> 研判留痕按序列取最新结论
// (按 host 缓存——同 host 不同端口的访问共享判定,同任务内零重复
// 调用;信息源清单命中在调用方先行判定,撤销落 revoked 后此处返回
// 空→重新分诊)。读不出按无记录。Note 形态由 BuildTriageRecord 统一
// (解析契约):"verdict=X basis=… targets=a,b,c"(targets＝P4-72 合一
// 清单摘要,旧记录无此段兼容)。
func TriageVerdictOf(ctx context.Context, store *rings.Store, taskID, host string) (string, string) {
	rows, err := store.ApprovalByRequest(ctx, taskID, rings.TriageCardID(host))
	if err != nil || len(rows) == 0 {
		return "", ""
	}
	latest := rows[len(rows)-1]
	note := latest.Note
	if i := strings.Index(note, " targets="); i >= 0 {
		note = note[:i] // 剥清单摘要段,basis 解析不受影响
	}
	verdict, basis, _ := strings.Cut(note, " basis=")
	verdict = strings.TrimPrefix(verdict, "verdict=")
	if !triageVerdicts[verdict] && verdict != TriageRevoked {
		return "", ""
	}
	return verdict, basis
}

// BuildTriageRecord 构造分诊留痕(approval.record triage;命令路单位侧
// 与发现路收割侧共用——结论与依据入账,可审计,P4-07 统计按类型可
// 分;非目标过滤/not_target 经此留痕可观测、不算拦截)。P4-72:Note
// 追加 targets= 清单摘要(本命令合一判定的全部目标,审计一眼见全貌)。
func BuildTriageRecord(host, verdict, basis string, rec rings.ApprovalRecord) rings.ApprovalRecord {
	return BuildUnifiedTriageRecord(host, verdict, basis, nil, rec)
}

// BuildUnifiedTriageRecord 合一留痕构造:targets 为本命令判定的全部
// 目标清单(host 列表,空则不落 targets 段——单目标形态兼容)。
func BuildUnifiedTriageRecord(host, verdict, basis string, targets []string, rec rings.ApprovalRecord) rings.ApprovalRecord {
	rec.RequestID = rings.TriageCardID(host)
	rec.Type = rings.ApprovalRecTriage
	note := "verdict=" + verdict + " basis=" + basis
	if len(targets) > 0 {
		note += " targets=" + strings.Join(targets, ",")
	}
	rec.Note = note
	return rec
}

// GrantInfoSource 信息源入清单(roe.updated 快照提交,与牧者标记/面板
// 撤销同一账本动作)。taskID 维度任务级;条目键 host[:port],判定命中
// 按 host 全放行(RoeState.InfoSourceHit)。basis 为研判依据摘要。快照
// 透传当前值(整体覆盖投影,漏带即误清空)。
func GrantInfoSource(ctx context.Context, store *rings.Store, taskID, host, basis, actor string) error {
	st, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return err
	}
	key := rings.InfoSourceKey(host, "")
	list := make([]rings.RoeInfoSource, 0, len(st.InfoSources)+1)
	for _, s := range st.InfoSources {
		if s.Host == key {
			return nil // 已在清单(缓存命中路径的防御性写)
		}
		list = append(list, s)
	}
	list = append(list, rings.RoeInfoSource{
		Host: key, Basis: basis, At: time.Now().Format("2006-01-02 15:04:05"),
	})
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: actor,
		Payload: rings.RoeUpdated{
			AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
			ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
			ExcludedAssets: st.ExcludedAssets, ScopeApproved: st.ScopeApproved,
			InfoSources: &list, Note: "信息源入清单: " + key,
		},
	}); err != nil {
		return err
	}
	runlog.Infof("信息源入清单: %s %s(%s)", taskID, key, basis)
	return nil
}

// RevokeInfoSource 信息源撤销(移出清单,后续访问恢复拦截——按候选
// 出卡,由范围卡两键一锤定音;与面板撤销/牧者"别当信息源了"同一
// 动作)。同时落一笔 revoked 标记(粘性,见 TriageRevoked)。
func RevokeInfoSource(ctx context.Context, store *rings.Store, taskID, key, actor string) error {
	// 键归一到 host(面板/牧者可能带端口传入;清单条目是 host 形态)。
	if a, ok := rings.ScopeAnchorOf(key); ok {
		key = a.Host
	}
	st, err := store.CurrentRoE(ctx, taskID)
	if err != nil {
		return err
	}
	// 清空语义必须用非 nil 空切片:指针指向 nil 切片经 JSON 往返会变
	// null(等价"本次不动")——撤销最后一个条目时清单正确落空。
	list := []rings.RoeInfoSource{}
	for _, s := range st.InfoSources {
		if s.Host != key {
			list = append(list, s)
		}
	}
	if len(list) == len(st.InfoSources) {
		return fmt.Errorf("swarm: %s 不在信息源清单", key)
	}
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: actor,
		Payload: rings.RoeUpdated{
			AllowedTools: st.AllowedTools, DeniedTools: st.DeniedTools,
			ApprovalThreshold: st.ApprovalThreshold, IntentRules: st.IntentRules,
			ExcludedAssets: st.ExcludedAssets, ScopeApproved: st.ScopeApproved,
			InfoSources: &list, Note: "信息源撤销: " + key,
		},
	}); err != nil {
		return err
	}
	// 落撤销标记(粘性:后续访问按候选出卡,见 TriageRevoked)。
	if _, err := store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventApprovalRecord, Actor: actor,
		Payload: BuildTriageRecord(key, "revoked", "信息源已撤销,后续访问重新判定", rings.ApprovalRecord{}),
	}); err != nil {
		return err
	}
	runlog.Infof("信息源撤销: %s %s", taskID, key)
	return nil
}
