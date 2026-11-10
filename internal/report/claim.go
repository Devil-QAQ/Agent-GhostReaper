package report

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 单条发现的完整卷宗(P2-13,第五章 §1/§4):反证时间线(候选 → 竞争
// 解释 → 多轮反证 → Confirmed/待确认 → 重开)+ 收割定稿(复现步骤含
// Burp 原文)。发现详情抽屉与报告导出共用本装配——面板所见即报告所印,
// 同进同出、零复制(R17 同源纪律)。

// reproBodyClip 复现步骤正文的面板截断(全文在 blob,字节级保真;
// 面板与报告 md 同口径截断——所见即所印;zip 证据包收全文)。
const reproBodyClip = 4096

// CaseEntry 反证时间线单条(按账本 revision 排序;kind 决定字段)。
// JSON 字段即发现详情 API 的既有口径,前端零改动。
type CaseEntry struct {
	Rev  int64  `json:"rev"`
	Kind string `json:"kind"` // created|falsify|verdict|reopen|repro|state

	// created
	UnitID string `json:"unit_id,omitempty"`
	// falsify
	Round            int               `json:"round,omitempty"`
	StepID           string            `json:"step_id,omitempty"`
	ExistenceRefuted bool              `json:"existence_refuted,omitempty"`
	ImpactSupported  bool              `json:"impact_supported,omitempty"`
	ImpactLevel      string            `json:"impact_level,omitempty"`
	AltResults       []rings.AltResult `json:"alt_results,omitempty"`
	EvidenceHead     string            `json:"evidence_head,omitempty"`
	EvidenceRef      string            `json:"evidence_ref,omitempty"`
	Note             string            `json:"note,omitempty"`
	// verdict
	Decision      string                         `json:"decision,omitempty"`
	Satisfied     bool                           `json:"satisfied,omitempty"`
	PendingReason string                         `json:"pending_reason,omitempty"`
	Conditions    []rings.ArbiterConditionResult `json:"conditions,omitempty"`
	// reopen
	Trigger string `json:"trigger,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// repro
	Severity string `json:"severity,omitempty"`
	// state
	Status string `json:"status,omitempty"`
}

// AltView 冻结竞争解释的呈现:清单 + 当前战役排除状态。
type AltView struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Killed bool   `json:"killed"` // 当前战役内经结构化写回排除
}

// ReproStepView 复现步骤(收割定稿 + 原文回放):HTTP 交换摘要 + 三层
// 结构(短名 Short/做什么 Desc/作用 Purpose,P4-35 ②;旧记录缺省
// Short/Purpose,读侧退"步骤 N"/省略作用行)+ Burp 风格原文(面板与
// 报告同口径截断,全文在 blob/证据包)。TLSWord 为 https 交换的目标
// 证书校验结论(P4-08 ⑥:证书结论是包外信息行,不进包文)。Op 不上
// 面板/报告正文(P4-08 ⑤:证据包收)。
type ReproStepView struct {
	rings.ReproStep
	Raw     string `json:"raw,omitempty"`      // 请求/响应原文(Burp 风格,截断)
	RawClip bool   `json:"raw_clip"`           // 原文是否被截断(全文在 blob)
	TLSWord string `json:"tls_word,omitempty"` // https 交换的目标证书结论(包外信息行)
}

// FillRaw 按内容寻址文档回放原文(详情分段页与卷宗装配共用同一份;
// 解码失败如实保持空,呈现层退步骤摘要)。
func (sv *ReproStepView) FillRaw(doc []byte) {
	var ex mirror.Exchange
	if json.Unmarshal(doc, &ex) != nil {
		return
	}
	sv.Raw, sv.RawClip = renderExchangeRaw(&ex)
	if ex.Type == rings.MirrorRecHTTPS && ex.TLS != nil {
		sv.TLSWord = mirror.TLSLabel(ex.TLS.Validity)
	}
}

// reproStepHead 步骤标题(P4-35 ②):序号+短名("① 拿会话凭据"式)。
// 序号按列表位置(pos,1 起)——P4-35 最小集是从候选池挑的子集,存储
// Seq 保留候选编号(校对轮 seq 契约),呈现序号与存储解耦;旧记录
// Seq 本就等于位置,呈现不变。1~20 用圈号,超出退"N.";旧记录无短名
// 退"步骤 N"。面板与报告同款口径(前端 repro-steps 同式实现,所见
// 即所印)。
func reproStepHead(pos int, st ReproStepView) string {
	if st.Short == "" {
		return fmt.Sprintf("步骤 %d", pos)
	}
	return stepOrdinal(pos) + " " + st.Short
}

// stepOrdinal 步骤序号词形:1~20 圈号(①..⑳),超出"N."(数字精确,
// 不编造超出 Unicode 圈号表的词形)。
func stepOrdinal(n int) string {
	if n >= 1 && n <= 20 {
		return string(rune(0x245F + n))
	}
	return fmt.Sprintf("%d.", n)
}

// ReproFinal 收割定稿:最新 repro 记录(重开再确认后新定稿取代旧定稿)。
// P4-35 起最小复现集口径(问题清单 #57):Steps 为定稿时模型挑出的
// 必要步骤(一步漏洞恰 1 条,链式 N 条;对比交换/反证变体/枚举试错
// 不进),每步三层 = 短名 + 做什么(Desc) + 作用(Purpose);过程流量
// 计数在案(ProcessCount/VariantsCount,面板与报告计数行的口径来源)。
// P4-36 起 Variants/Process 大数组不进投影(巨记录瘦身):常规装配为
// 空,证据包导出经 EnrichReproForEvent 按 rev 定向读事件补齐。Narrative
// 为定稿冻结的来龙去脉叙述(P4-13:模型生成人话,读定稿零 LLM;旧定稿
// 无叙述为空,读侧如实标注不编造)。Rounds 为定稿冻结的反证时间线人话
// 三段(P4-56 ⑤:疑点→怎么验的→结论,旧定稿为空,读侧退机器层)。
// NarrativeUnpolished / Steps[].Unpolished / Rounds[].Unpolished
// 为文风校验未过的如实标注(P4-24 ②:校对+重写一次后仍含机器语特征,
// 原文保留,读侧渲染一行说明)。
type ReproFinal struct {
	Rev                 int64                     `json:"rev"`
	Severity            string                    `json:"severity"`
	Narrative           string                    `json:"narrative,omitempty"`
	NarrativeUnpolished bool                      `json:"narrative_unpolished,omitempty"`
	Rounds              []rings.FalsifyRoundHuman `json:"rounds,omitempty"`
	Steps               []ReproStepView           `json:"steps"`
	// NoReproReason 无复现包的原因(P4-62 A:模型空选/挑选失败如实呈现,
	// 面板与报告同句;旧记录为空——旧口径空步骤退既有空态文案)。
	NoReproReason string               `json:"no_repro_reason,omitempty"`
	Variants      []rings.ReproStep    `json:"variants,omitempty"` // 旧口径过程流量(存量;证据包导出补全,常规装配为空)
	Process       []rings.ReproProcessRef `json:"process,omitempty"` // 过程流量引用清单(P4-35;证据包导出补全,常规装配为空)
	ProcessCount  int                  `json:"process_count"`      // 过程流量条数(计数行口径;新记录 = Process 长度)
	VariantsCount int                  `json:"variants_count"`     // 旧口径变体条数(存量)
}

// ProcessUnlisted 未进最小集的过程交换条数(面板与报告同口径计数行:
// 新记录 = ProcessCount − Steps,旧记录 = VariantsCount,负值按 0;
// 计数随投影落库,与事件原账一致)。
func (r *ReproFinal) ProcessUnlisted() int {
	if r == nil {
		return 0
	}
	n := r.ProcessCount - len(r.Steps)
	if n < 0 {
		n = 0
	}
	return n + r.VariantsCount
}

// RetestFinal 最新复测结论(P3-06):修复状态是锤炼态之上的正交轴,
// 不进反证时间线(呈现走复测区),报告只取最新一轮。
type RetestFinal struct {
	Rev     int64  `json:"rev"`
	Round   int    `json:"round"`
	Verdict string `json:"verdict"` // fixed|unfixed|inconclusive
}

// ClaimCase 单条声明的卷宗:时间线 + 冻结解释 + 最新定稿 + 最新复测。
type ClaimCase struct {
	ClaimID      string       `json:"-"`
	Timeline     []CaseEntry  `json:"timeline"`
	Alternatives []AltView    `json:"alternatives"`
	Repro        *ReproFinal  `json:"repro,omitempty"`
	Retest       *RetestFinal `json:"retest,omitempty"`
	// Summary 定稿校对后的声明摘要(P4-59 ①,问题清单 #80①③):存在性
	// 描述/影响面/链上事实描述的报告级文风改写,随 repro 同批冻结;
	// 空字段读侧退节点原文。只改文风,包装不是证据(R2)。P4-64 ② 起
	// 立账批次亦写同列(锤炼中声明即有摘要),定稿覆盖(账本次序)。
	Summary *rings.SummaryDigest `json:"summary,omitempty"`
	// RoundHumans 逐轮反证人话(P4-64 ①:每轮随 falsify 记录同批冻结,
	// 按轮 upsert)。读侧与存量 repro.Rounds 合并——新事件优先,存量
	// 旧定稿的人话照读(重开前的旧轮)。
	RoundHumans map[int]rings.FalsifyRoundHuman `json:"-"`
	// RetestNotes 复测备注校对稿(P4-64 ⑦:按轮匹配;空 = 该轮无校对
	// 稿,读侧退机器记录 Note 原文)。
	RetestNotes map[int]string `json:"-"`
	// 复核标记不纳入报告(P4-30 ③):最新 report_review 记录为准(取代
	// 语义);账本不涂改,判断保留可追溯——报告只收已确认且非信息性。
	ReportExcluded       bool   `json:"report_excluded,omitempty"`
	ReportExcludedReason string `json:"report_excluded_reason,omitempty"`
}

// claimCases 装配全部声明卷宗(P4-36 起读 claim_cases 投影——写侧
// applyEvent 同事务维护,重放重建同源;不再扫描事件流。面板详情与
// 报告导出共用本装配,所见即所印)。Variants/Process 大数组不进投影:
// 证据包导出经 enrichReproForEvent 按 rev 定向读事件补齐。
func claimCases(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID string) (map[string]*ClaimCase, error) {
	claims, err := store.NodesByType(ctx, taskID, rings.NodeTestClaim, "")
	if err != nil {
		return nil, fmt.Errorf("report: 查发现: %w", err)
	}
	cases := make(map[string]*ClaimCase, len(claims))
	discoverer := map[string]string{}
	for _, c := range claims {
		cases[c.ID] = &ClaimCase{ClaimID: c.ID}
		discoverer[c.ID], _ = c.Detail["unit_id"].(string)
	}
	projs, err := store.ClaimCases(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("report: 查判断卷宗: %w", err)
	}
	for _, proj := range projs {
		cs, ok := cases[proj.ClaimID]
		if !ok {
			continue
		}
		if err := applyProjection(cs, proj, discoverer[proj.ClaimID], blobs); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

// applyProjection 单条卷宗投影 → ClaimCase(时间线逐条映射,与既有
// 事件扫描装配同义:created/state 自节点事件,记录类带原始负载,
// repro 只存级别摘要;evidence/retest/review 不进反证时间线——复测走
// 复测区,绑定走详情证据区,复核标记取代语义进 ReportExcluded)。
func applyProjection(cs *ClaimCase, proj *rings.ClaimCaseProjection, discovererID string, blobs *swarm.BlobStore) error {
	for _, ent := range proj.Timeline {
		switch ent.K {
		case "created":
			cs.Timeline = append(cs.Timeline, CaseEntry{Rev: ent.Rev, Kind: "created", UnitID: discovererID})
		case "state":
			var p struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(ent.P, &p) == nil {
				cs.Timeline = append(cs.Timeline, CaseEntry{Rev: ent.Rev, Kind: "state", Status: p.Status})
			}
		case "falsify", "verdict", "reopen", "suspend":
			var rec rings.TestClaimRecord
			if json.Unmarshal(ent.P, &rec) == nil {
				cs.Timeline = append(cs.Timeline, caseEntryOf(ent.Rev, rec))
			}
		case "repro":
			var p struct {
				Severity string `json:"severity"`
			}
			if json.Unmarshal(ent.P, &p) == nil {
				cs.Timeline = append(cs.Timeline, CaseEntry{Rev: ent.Rev, Kind: "repro", Severity: p.Severity})
			}
		case "retest":
			var rec rings.TestClaimRecord
			if json.Unmarshal(ent.P, &rec) == nil {
				cs.Retest = &RetestFinal{Rev: ent.Rev, Round: rec.Round, Verdict: rec.RetestVerdict}
			}
		}
	}
	// 冻结解释 + 当前战役排除状态(重开后全部复活)。
	labels := map[string]string{}
	for _, a := range proj.Alternatives {
		cs.Alternatives = append(cs.Alternatives, AltView{ID: a.ID, Label: a.Label, Killed: proj.AltKilled[a.ID]})
		labels[a.ID] = a.Label
	}
	for i := range cs.Timeline {
		for j := range cs.Timeline[i].AltResults {
			cs.Timeline[i].AltResults[j].Label = labels[cs.Timeline[i].AltResults[j].ID]
		}
	}
	cs.ReportExcluded, cs.ReportExcludedReason = proj.ReportExcluded, strings.TrimSpace(proj.ReportExcludedReason)
	cs.Summary = proj.Summary
	cs.RoundHumans = proj.RoundHumans
	cs.RetestNotes = proj.RetestNotes
	if proj.Repro != nil {
		out := &ReproFinal{
			Rev: proj.Repro.Rev, Severity: proj.Repro.Severity,
			Narrative: proj.Repro.Narrative, NarrativeUnpolished: proj.Repro.NarrativeUnpolished,
			Rounds: proj.Repro.Rounds, NoReproReason: proj.Repro.NoReproReason,
			ProcessCount: proj.Repro.ProcessCount, VariantsCount: proj.Repro.VariantsCount,
		}
		steps := make([]ReproStepView, 0, len(proj.Repro.Steps))
		for _, st := range proj.Repro.Steps {
			sv := ReproStepView{ReproStep: st}
			if st.BlobRef != "" && blobs != nil {
				if doc, err := blobs.Read(st.BlobRef); err == nil {
					sv.FillRaw(doc)
				}
			}
			steps = append(steps, sv)
		}
		out.Steps = steps
		cs.Repro = out
	}
	return nil
}

// EnrichReproForEvent 证据包导出的定稿补全(P4-36:巨记录大数组不进
// 投影——Variants(旧口径变体,含操作方法)与 Process(新口径引用
// 清单)按定稿 rev 定向读事件;步骤操作方法 Op 同源补回)。导出路径
// 专用,面板与报告 md 不消费(正文不显操作方法,P4-08 ⑤)。
func EnrichReproForEvent(ctx context.Context, store *rings.Store, taskID string, cs *ClaimCase) {
	if cs == nil || cs.Repro == nil {
		return
	}
	ev, err := store.EventByRevision(ctx, taskID, cs.Repro.Rev)
	if err != nil {
		return // 定向读失败如实退投影形态(导出步骤原文按 blob 可用)
	}
	var rec rings.TestClaimRecord
	if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.TestClaimRecRepro {
		return
	}
	for i := range cs.Repro.Steps {
		if i < len(rec.Steps) {
			st := cs.Repro.Steps[i]
			st.Op = rec.Steps[i].Op
			cs.Repro.Steps[i] = st
		}
	}
	cs.Repro.Variants = rec.Variants
	cs.Repro.Process = rec.Process
}

// caseEntryOf 状态机审计记录 → 时间线条目(旧数据兼容:无 Decision 的
// verdict 按 satisfied 推导)。
func caseEntryOf(rev int64, p rings.TestClaimRecord) CaseEntry {
	e := CaseEntry{Rev: rev}
	switch p.Type {
	case rings.TestClaimRecFalsify:
		e.Kind = "falsify"
		e.Round, e.StepID, e.UnitID = p.Round, p.StepID, p.UnitID
		e.ExistenceRefuted, e.ImpactSupported, e.ImpactLevel = p.ExistenceRefuted, p.ImpactSupported, p.ImpactLevel
		e.AltResults, e.EvidenceHead, e.EvidenceRef, e.Note = p.Alternatives, p.EvidenceHead, p.EvidenceRef, p.Note
	case rings.TestClaimRecVerdict:
		e.Kind = "verdict"
		e.Round, e.StepID, e.UnitID = p.Round, p.StepID, p.UnitID
		e.Decision, e.Satisfied, e.PendingReason, e.Conditions = p.Decision, p.Satisfied, p.PendingReason, p.Conditions
		if e.Decision == "" { // P1-08 旧记录按 satisfied 推导
			if p.Satisfied {
				e.Decision = rings.VerdictConfirm
			} else {
				e.Decision = rings.VerdictPending
			}
		}
	case rings.TestClaimRecReopen:
		e.Kind = "reopen"
		e.Trigger, e.Reason = p.Trigger, p.Reason
	case rings.TestClaimRecSuspend:
		// 操作者取消挂起(P4-54):与 reopen 同形(理由时间线可读)。
		e.Kind = "suspend"
		e.Reason = p.Reason
	case rings.TestClaimRecRepro:
		e.Kind = "repro"
		e.Severity = p.Severity
	}
	return e
}

// ClaimCaseOf 单条声明卷宗(发现详情 API 入口;P4-36 起按条读投影,
// 不再全量装配——详情按条取数,打开即回)。
func ClaimCaseOf(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID, claimID string) (*ClaimCase, error) {
	node, err := store.Node(ctx, taskID, claimID)
	if err != nil || node.Type != rings.NodeTestClaim {
		return nil, fmt.Errorf("report: 发现 %s 不存在", claimID)
	}
	discoverer, _ := node.Detail["unit_id"].(string)
	proj, ok, err := store.ClaimCaseOf(ctx, taskID, claimID)
	if err != nil {
		return nil, err
	}
	if !ok {
		proj = &rings.ClaimCaseProjection{ClaimDigest: rings.ClaimDigest{ClaimID: claimID}}
	}
	cs := &ClaimCase{ClaimID: claimID}
	if err := applyProjection(cs, proj, discoverer, blobs); err != nil {
		return nil, err
	}
	return cs, nil
}

// renderExchangeRaw 交换文档 → Burp 风格原文(请求行/头/体 + 状态行/
// 头/体);正文超长截断并如实标记(面板与报告 md 同口径)。
func renderExchangeRaw(ex *mirror.Exchange) (string, bool) {
	return renderExchange(ex, reproBodyClip)
}

// renderExchangeFull 交换文档 → Burp 风格全文(证据包用,不截断,
// 字节级保真)。
func renderExchangeFull(ex *mirror.Exchange) string {
	s, _ := renderExchange(ex, 0)
	return s
}

// ExchangeRaw 交换文档 → Burp 风格全文(P3-02 流量页回放出口,与证据包
// 同一装配——面板所见即证据包所印,零复制)。
func ExchangeRaw(ex *mirror.Exchange) string { return renderExchangeFull(ex) }

// renderExchange 统一装配:clipAt>0 时请求/响应正文截断并标记,≤0 全文。
// 请求行 origin-form(P4-08 ⑥);行尾与消息边界用线上格式(\r\n,
// 请求头后带空行终止——渲染出的请求包本身即完整可发的 HTTP 报文,
// 复制即贴 Burp Repeater 可发);目标证书结论不进包文(包外信息行:
// 面板走元数据条、报告走原文块前置行、zip 走文件头;审计事实在 blob)。
// 请求侧与响应侧分块写入(writeRequestBlock/writeResponseBlock),单侧
// 视图与全量同一装配。
func renderExchange(ex *mirror.Exchange, clipAt int) (string, bool) {
	var b strings.Builder
	t1 := writeRequestBlock(&b, ex, clipAt)
	var rb strings.Builder
	t2 := writeResponseBlock(&rb, ex, clipAt)
	resp := rb.String()
	if ex.Response != nil {
		b.WriteString("\r\n") // 请求与响应块间的分隔行(与原始报文形态一致)
		b.WriteString(resp)
	} else if resp != "" {
		// 无响应仅异常注记:注记自带前导空行,原样衔接。
		b.WriteString(resp)
	}
	return b.String(), t1 || t2
}

// writeRequestBlock 请求侧原文块:请求行/头/体(Burp 就绪,P4-08 ⑥:
// origin-form 请求行 + Host 头承载 + \r\n 行尾 + 头后空行终止)。
// 返回正文是否被截断。
func writeRequestBlock(b *strings.Builder, ex *mirror.Exchange, clipAt int) bool {
	trunc := false
	if ex.Request != nil {
		fmt.Fprintf(b, "%s %s %s\r\n", ex.Request.Method, requestTarget(ex.Request.URL), protoOf(ex))
		if host := requestHost(ex); host != "" {
			fmt.Fprintf(b, "Host: %s\r\n", host)
		}
		for _, h := range ex.Request.Headers {
			fmt.Fprintf(b, "%s: %s\r\n", h.Name, h.Value)
		}
		b.WriteString("\r\n") // 头终止空行(无体请求至此即完整可发)
		if len(ex.Request.Body) > 0 {
			s, t := clipBody(ex.Request.Body, clipAt)
			b.WriteString(s)
			trunc = trunc || t
			b.WriteString("\r\n")
		}
	}
	return trunc
}

// requestTarget 请求行目标(P4-08 ⑥):绝对 URI → origin-form(路径+
// 查询串,根路径兜底 "/");解析失败或本就无 host 的目标原样保留
// (如实不改写)。
func requestTarget(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// requestHost Host 头取值:留痕的 Host 优先(代理形态下独立携带),
// 缺省从请求 URL 推(host[:port] 原样)。
func requestHost(ex *mirror.Exchange) string {
	if ex.Request == nil {
		return ""
	}
	if ex.Request.Host != "" {
		return ex.Request.Host
	}
	if u, err := url.Parse(ex.Request.URL); err == nil {
		return u.Host
	}
	return ""
}

// protoOf 请求协议版本(留痕缺省退 HTTP/1.1——请求行必须有协议段)。
func protoOf(ex *mirror.Exchange) string {
	if ex.Request != nil && ex.Request.Proto != "" {
		return ex.Request.Proto
	}
	return "HTTP/1.1"
}

// writeResponseBlock 响应侧原文块:状态行/头/体 + 镜异常注记(无响应时
// 仅注记,自带前导空行)。返回正文是否被截断。
func writeResponseBlock(b *strings.Builder, ex *mirror.Exchange, clipAt int) bool {
	trunc := false
	if ex.Response != nil {
		if ex.Response.Proto != "" && ex.Response.StatusText != "" {
			fmt.Fprintf(b, "%s %d %s\r\n", ex.Response.Proto, ex.Response.Status, ex.Response.StatusText)
		} else {
			fmt.Fprintf(b, "HTTP %d\r\n", ex.Response.Status)
		}
		for _, h := range ex.Response.Headers {
			fmt.Fprintf(b, "%s: %s\r\n", h.Name, h.Value)
		}
		if len(ex.Response.Body) > 0 {
			b.WriteString("\r\n")
			s, t := clipBody(ex.Response.Body, clipAt)
			b.WriteString(s)
			trunc = trunc || t
			b.WriteString("\r\n")
		}
	}
	if ex.Error != "" {
		fmt.Fprintf(b, "\r\n[镜留痕异常: %s]\r\n", ex.Error)
	}
	return trunc
}

// clipBody 正文截断(clipAt≤0 全文)。
func clipBody(body []byte, clipAt int) (string, bool) {
	if clipAt > 0 && len(body) > clipAt {
		return string(body[:clipAt]), true
	}
	return string(body), false
}

// ExchangeRawSide 交换文档单侧原文(P3-02 审核迭代:请求包/返回包分开
// 展示):side=req 请求侧(Burp 就绪,P4-08 ⑥)、side=resp 响应侧(含镜
// 异常注记),不截断、字节级保真;与全量装配(报告/证据包)同源同一
// 分块。
func ExchangeRawSide(ex *mirror.Exchange, side string) string {
	var b strings.Builder
	switch side {
	case "req":
		writeRequestBlock(&b, ex, 0)
	case "resp":
		writeResponseBlock(&b, ex, 0)
		return strings.TrimPrefix(b.String(), "\r\n") // 独立视图不带前导空行
	}
	return b.String()
}
