// P4-67(问题清单 #86 A②,RoE 机制重构批次第二层·研判分诊):范围外
// 命中(经 P4-66 豁免后仍命中)在出卡前的单次轻量模型分诊。输入＝
// 命令原文/发现内容＋当前步骤意图＋任务目标摘要(+意图拆解的外部
// 服务声明);输出三类判定——方法性访问/相关资产候选/信息源上的
// 攻击行为(声明命中为第四值,处置与方法性访问同)。判据全部在
// 提示词层(零站点枚举、零正则规则——攻击判定不写规则,防 PoC/exp
// 代码本身被特征匹配误拦)。调用失败按"拿不准"处理(偏向出卡,宁可
// 多问不误放)。
//
// P4-68 ③b(分诊等待口径修正):删除 30 秒墙钟——分诊等结果本身,
// 失败判定交给 LLM 链既有语义(重试/主备切换/读超时);链真报错才按
// 拿不准出卡,慢不算失败。分诊是审批门里的单次问话(无会话、不占
// 名额),单位原地待命。
//
// 任务级缓存两层:信息源清单(roe_state)＝info_source 结论的落账
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

// 分诊判定值域(模型输出的契约词表;解析失败按拿不准)。
const (
	TriageDeclared   = "declared_source"   // 命中外部服务声明(处置同方法性访问)
	TriageMethodical = "methodical_access" // 方法性访问:公开信息服务且与目标组织无归属,动作是获取使用
	TriageAttack     = "attack_action"     // 信息源上的攻击行为:对信息源的主动攻击动作(写入/扫描/利用尝试)
	TriageCandidate  = "related_candidate" // 相关资产候选:有归属线索或归属不明确(拿不准偏向这里)
)

var triageVerdicts = map[string]bool{
	TriageDeclared:   true,
	TriageMethodical: true,
	TriageAttack:     true,
	TriageCandidate:  true,
}

// TriageRevoked 撤销标记(P4-67 ④):信息源撤销落的 tg- 缓存态——撤销
// 是操作者推翻模型的分类,同模型重新分诊只会得出同一结论再自动授,
// 撤销就成空操作;故撤销粘性:后续访问按候选出卡,由"加入资产/禁止
// 测试"一锤定音。牧者/面板重新标记信息源(入清单)优先于本标记。
const TriageRevoked = "revoked"

// IsTriageInfoSource 判定结论是否属"信息源"(自动授访问权、入清单)。
func IsTriageInfoSource(verdict string) bool {
	return verdict == TriageDeclared || verdict == TriageMethodical
}

// TriageInput 一次分诊的输入上下文。命令路带 Command(命令原文),
// 发现路带 Discovered(发现内容摘要)——两者其一。ListedInfoSource
// 置位时该 host 已在信息源清单(P4-68 ①:只判本次动作是获取使用还是
// 主动攻击,不重判 host 归属)。
type TriageInput struct {
	Host             string // 访问目标(host[:port] 显示形态)
	Command          string // 命令原文(命令路)
	Discovered       string // 发现内容摘要(发现路:发现事实标题)
	Intent           string // 当前步骤意图
	GoalSummary      string // 任务目标摘要
	DeclaredServices string // 外部服务声明(可选;命中即 declared_source)
	ListedInfoSource bool   // host 已在信息源清单(动作复查,见上)
}

// triagePrompt 分诊系统段(判据语义逐条对应条目 ②,提示词层语义判据:
// 零站点枚举、零正则)。
const triagePrompt = `你是渗透测试任务的边界研判员。一次操作要访问任务授权范围外的地址,由你判定这次访问的性质,供系统决定放行、出卡或人工审批。

判定标准(按语义判断,不枚举站点、不做关键字匹配):
1. methodical_access(方法性访问):该地址属公开信息服务,且与目标组织无归属关系,这次动作是正常获取使用。归属线索:与任务目标同根域、同注册主体、同 IP 段、目标页面或其 JS 引用的服务域名等——只要存在任何归属线索,或归属无法确认,就不判此项。
2. attack_action(信息源上的攻击行为):该地址属公开信息服务且与目标组织无归属,但这次动作是对它的主动攻击(写入、扫描、利用尝试),不是正常获取使用。注意:连通性测试、从代码托管站下载利用代码、查搜索引擎、查公共文档,都属正常获取,不是攻击;判断依据是动作意图,不是命令里出现什么词。
3. related_candidate(相关资产候选):存在归属线索,或归属不明确。拿不准时一律判此项。
4. declared_source(声明命中):本次访问的地址属于「外部服务声明」所描述的那类公开信息服务(按服务形态判断,不按站名)。声明存在且匹配时优先判此项。

只输出 JSON:{"verdict":"methodical_access|attack_action|related_candidate|declared_source","basis":"一句话依据,写给操作者看"}`

// TriageFunc 一次分诊调用(可注入:测试打桩;生产走任务 LLM 链)。
// 返回判定词表值与依据摘要;错误(含超时)由调用方按"拿不准"处理。
type TriageFunc func(ctx context.Context, in TriageInput) (verdict, basis string, err error)

// TriageFor 生产实现构造(命令路与发现路共用):任务 LLM 链单次
// Complete,输出上限收紧;解析失败或词表外值返回错误。P4-68 ③b 起
// 不设分诊自身墙钟——等结果本身,失败判定交给链既有语义(重试/主备
// 切换/读超时),链真报错才由调用方按"拿不准"处理(慢不算失败)。
func TriageFor(provider llm.Provider) TriageFunc {
	return func(ctx context.Context, in TriageInput) (string, string, error) {
		if provider == nil {
			return "", "", fmt.Errorf("swarm: 分诊无可用模型链")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "任务目标:%s\n", in.GoalSummary)
		fmt.Fprintf(&b, "当前步骤意图:%s\n", in.Intent)
		if in.DeclaredServices != "" {
			fmt.Fprintf(&b, "步骤写明的外部服务:%s\n", in.DeclaredServices)
		}
		fmt.Fprintf(&b, "访问目标:%s\n", in.Host)
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
			MaxTokens: 300,
			Thinking:  "disabled",
		})
		if err != nil {
			return "", "", err
		}
		return parseTriageReply(msgText(msg))
	}
}

// parseTriageReply 解析分诊输出(JSON 词表校验;宽容剥 markdown 围栏)。
func parseTriageReply(text string) (string, string, error) {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "{"); i >= 0 {
		if j := strings.LastIndex(t, "}"); j > i {
			t = t[i : j+1]
		}
	}
	var out struct {
		Verdict string `json:"verdict"`
		Basis   string `json:"basis"`
	}
	if err := json.Unmarshal([]byte(t), &out); err != nil {
		return "", "", fmt.Errorf("swarm: 分诊输出解析失败: %w", err)
	}
	if !triageVerdicts[out.Verdict] {
		return "", "", fmt.Errorf("swarm: 分诊判定不在词表内: %q", out.Verdict)
	}
	basis := strings.TrimSpace(out.Basis)
	if basis == "" {
		basis = "未给出依据"
	}
	if len([]rune(basis)) > 200 {
		basis = string([]rune(basis)[:200])
	}
	return out.Verdict, basis, nil
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
// (解析契约)。
func TriageVerdictOf(ctx context.Context, store *rings.Store, taskID, host string) (string, string) {
	rows, err := store.ApprovalByRequest(ctx, taskID, rings.TriageCardID(host))
	if err != nil || len(rows) == 0 {
		return "", ""
	}
	latest := rows[len(rows)-1]
	verdict, basis, _ := strings.Cut(latest.Note, " basis=")
	verdict = strings.TrimPrefix(verdict, "verdict=")
	if !triageVerdicts[verdict] && verdict != TriageRevoked {
		return "", ""
	}
	return verdict, basis
}

// BuildTriageRecord 构造分诊留痕(approval.record triage;命令路单位侧
// 与发现路收割侧共用——结论与依据入账,可审计,P4-07 统计按类型可分)。
func BuildTriageRecord(host, verdict, basis string, rec rings.ApprovalRecord) rings.ApprovalRecord {
	rec.RequestID = rings.TriageCardID(host)
	rec.Type = rings.ApprovalRecTriage
	rec.Note = "verdict=" + verdict + " basis=" + basis
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
