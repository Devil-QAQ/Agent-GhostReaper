package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 单条发现的完整卷宗(P2-13,第五章 §1/§4):时间线(交卷 → 核验 →
// 确认/证据不足 → 重开)+ 收割定稿(复现步骤含
// Burp 原文)。发现详情抽屉与报告导出共用本装配——面板所见即报告所印,
// 同进同出、零复制(R17 同源纪律)。

// reproBodyClip 复现步骤正文的面板截断(全文在 blob,字节级保真;
// 面板与报告 md 同口径截断——所见即所印;zip 证据包收全文)。
const reproBodyClip = 4096

// CaseEntry 反证时间线单条(按账本 revision 排序;kind 决定字段)。
// JSON 字段即发现详情 API 的既有口径,前端零改动。P5-02⑥:旧反证
// 结构化结论字段(存在性推翻/影响面/竞争解释排除)已随砍除清单删——
// 旧账本 falsify 记录的时间线原文(payload)照存,读侧不再解析呈现。
type CaseEntry struct {
	Rev  int64  `json:"rev"`
	Kind string `json:"kind"` // created|submit|falsify|verdict|reopen|repro|state

	// created/submit
	UnitID string `json:"unit_id,omitempty"`
	// falsify(旧账本)
	Round        int    `json:"round,omitempty"`
	StepID       string `json:"step_id,omitempty"`
	EvidenceHead string `json:"evidence_head,omitempty"`
	EvidenceRef  string `json:"evidence_ref,omitempty"`
	Note         string `json:"note,omitempty"`
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
	Rev                 int64           `json:"rev"`
	Severity            string          `json:"severity"`
	Narrative           string          `json:"narrative,omitempty"`
	NarrativeUnpolished bool            `json:"narrative_unpolished,omitempty"`
	Steps               []ReproStepView `json:"steps"`
	// NoReproReason 无复现包的原因(P4-62 A:模型空选/挑选失败如实呈现,
	// 面板与报告同句;旧记录为空——旧口径空步骤退既有空态文案)。
	NoReproReason string `json:"no_repro_reason,omitempty"`
	// StepsIncomplete 流程步骤未生成的如实标注(P4-73 ④:复现包只含
	// 证明交换种子、模型补全挑选失败;面板与报告同句)。
	StepsIncomplete string                  `json:"steps_incomplete,omitempty"`
	Variants        []rings.ReproStep       `json:"variants,omitempty"` // 旧口径过程流量(存量;证据包导出补全,常规装配为空)
	Process         []rings.ReproProcessRef `json:"process,omitempty"`  // 过程流量引用清单(P4-35;证据包导出补全,常规装配为空)
	ProcessCount    int                     `json:"process_count"`      // 过程流量条数(计数行口径;新记录 = Process 长度)
	VariantsCount   int                     `json:"variants_count"`     // 旧口径变体条数(存量)
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

// RetestFinal 最新复测结论(P3-06):修复状态与确认态正交,
// 不进时间线(呈现走复测区),报告只取最新一轮。
type RetestFinal struct {
	Rev     int64  `json:"rev"`
	Round   int    `json:"round"`
	Verdict string `json:"verdict"` // fixed|unfixed|inconclusive
}

// ClaimCase 单条声明的卷宗:时间线 + 最新定稿 + 最新复测。P5-02⑥:
// 冻结竞争解释清单(Alternatives)已随砍除清单删除——判定收口机器
// 三查,不再有结构化解释排除呈现。
type ClaimCase struct {
	ClaimID  string       `json:"-"`
	Timeline []CaseEntry  `json:"timeline"`
	Repro    *ReproFinal  `json:"repro,omitempty"`
	Retest   *RetestFinal `json:"retest,omitempty"`
	// Summary 定稿校对后的声明摘要(P4-59 ①,问题清单 #80①③):存在性
	// 描述/影响面/链上事实描述的报告级文风改写,随 repro 同批冻结;
	// 空字段读侧退节点原文。只改文风,包装不是证据(R2)。P4-64 ② 起
	// 立账批次亦写同列(交卷声明即有摘要),定稿覆盖(账本次序)。
	Summary *rings.SummaryDigest `json:"summary,omitempty"`
	// ImpactNote 最新交换单申报的影响列点(P5-07⑦b:自由文本可列几条,
	// 从 submit 载荷直读;不进时间线渲染——首交卷由 created 条目承载,
	// 列点经详情影响区与报告影响行如实呈现)。
	ImpactNote string `json:"-"`
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
		case "submit":
			// 影响列点(P5-07⑦b):不进时间线渲染,最新一份随卷宗透出。
			var rec rings.TestClaimRecord
			if json.Unmarshal(ent.P, &rec) == nil && strings.TrimSpace(rec.ImpactNote) != "" {
				cs.ImpactNote = rec.ImpactNote
			}
		case "state":
			var p struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(ent.P, &p) == nil {
				cs.Timeline = append(cs.Timeline, CaseEntry{Rev: ent.Rev, Kind: "state", Status: p.Status})
			}
		case "verdict", "reopen", "suspend":
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
	cs.ReportExcluded, cs.ReportExcludedReason = proj.ReportExcluded, strings.TrimSpace(proj.ReportExcludedReason)
	cs.Summary = proj.Summary
	cs.RetestNotes = proj.RetestNotes
	if proj.Repro != nil {
		out := &ReproFinal{
			Rev: proj.Repro.Rev, Severity: proj.Repro.Severity,
			Narrative: proj.Repro.Narrative, NarrativeUnpolished: proj.Repro.NarrativeUnpolished,
			NoReproReason:   proj.Repro.NoReproReason,
			StepsIncomplete: proj.Repro.StepsIncomplete,
			ProcessCount:    proj.Repro.ProcessCount, VariantsCount: proj.Repro.VariantsCount,
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
// verdict 按 satisfied 推导;旧 falsify 记录的结构化结论字段已随 P5-01③
// 物理删除,解码按空值处理——存在性未推翻/影响面无支撑/无解释结论)。
func caseEntryOf(rev int64, p rings.TestClaimRecord) CaseEntry {
	e := CaseEntry{Rev: rev}
	switch p.Type {
	case rings.TestClaimRecSubmit:
		// 交卷单(P5-01②)。时间线不渲染 submit 条目(首交卷由 created
		// 承载);影响列点经 ClaimCase.ImpactNote 自投影直读(P5-07⑦b)。
		e.Kind = "submit"
		e.StepID, e.UnitID = p.StepID, p.UnitID
		e.EvidenceHead, e.Note = p.EvidenceHead, p.Note
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

// ImpactNoteOf 最新交换单申报的影响列点(P5-07⑦b:自由文本可列几条,
// 详情与报告按列点如实展示;空 = 未申报)——applyProjection 自 submit
// 载荷直读(最新一份)。
func (cs *ClaimCase) ImpactNoteOf() string {
	if cs == nil {
		return ""
	}
	return cs.ImpactNote
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
// P5-04 起装配核心下沉 mirror 包(报告/面板/虫群牧者工具三方同源),
// 此处只按报告口径取裁剪档。
func renderExchangeRaw(ex *mirror.Exchange) (string, bool) {
	return mirror.RenderExchange(ex, reproBodyClip)
}

// renderExchangeFull 交换文档 → Burp 风格全文(证据包用,不截断,
// 字节级保真)。
func renderExchangeFull(ex *mirror.Exchange) string {
	s, _ := mirror.RenderExchange(ex, 0)
	return s
}

// ExchangeRaw 交换文档 → Burp 风格全文(P3-02 流量页回放出口,与证据包
// 同一装配——面板所见即证据包所印,零复制)。
func ExchangeRaw(ex *mirror.Exchange) string { return renderExchangeFull(ex) }

// ExchangeRawSide 交换文档单侧原文(P3-02 审核迭代:请求包/返回包分开
// 展示):side=req 请求侧(Burp 就绪,P4-08 ⑥)、side=resp 响应侧(含镜
// 异常注记),不截断、字节级保真;与全量装配(报告/证据包)同源同一
// 分块(装配核心在 mirror 包)。
func ExchangeRawSide(ex *mirror.Exchange, side string) string {
	return mirror.RenderExchangeSide(ex, side)
}
