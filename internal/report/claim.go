package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ghostreaper/internal/arbiter"
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

// ReproStepView 复现步骤(收割定稿 + 原文回放):操作方法 + HTTP 交换
// 摘要 + Burp 风格原文(面板与报告同口径截断,全文在 blob/证据包)。
type ReproStepView struct {
	rings.ReproStep
	Raw     string `json:"raw"`      // 请求/响应原文(Burp 风格,截断)
	RawClip bool   `json:"raw_clip"` // 原文是否被截断(全文在 blob)
}

// ReproFinal 收割定稿:最新 repro 记录(重开再确认后新定稿取代旧定稿)。
type ReproFinal struct {
	Rev      int64           `json:"rev"`
	Severity string          `json:"severity"`
	Steps    []ReproStepView `json:"steps"`
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
}

// claimCases 一次事件流扫描装配全部声明卷宗(报告渲染与详情 API 共用)。
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
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("report: 查事件流: %w", err)
	}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil || p.NodeType != rings.NodeTestClaim {
				continue
			}
			if cs := cases[p.NodeID]; cs != nil {
				cs.Timeline = append(cs.Timeline, CaseEntry{Rev: ev.Revision, Kind: "created", UnitID: discoverer[p.NodeID]})
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if cs := cases[p.NodeID]; cs != nil {
				cs.Timeline = append(cs.Timeline, CaseEntry{Rev: ev.Revision, Kind: "state", Status: p.Status})
			}
		case rings.EventTestClaimRecord:
			var p rings.TestClaimRecord
			if err := json.Unmarshal(ev.Payload, &p); err != nil || cases[p.ClaimID] == nil {
				continue
			}
			// evidence 绑定记录(P3-02)与 retest 复测结论(P3-06)不进
			// 反证时间线——绑定是证据管理事实(呈现走详情的流量证据区),
			// 复测是修复状态轴(呈现走复测区),与锤炼状态机轨迹分列。
			if p.Type == rings.TestClaimRecEvidence || p.Type == rings.TestClaimRecRetest {
				continue
			}
			cases[p.ClaimID].Timeline = append(cases[p.ClaimID].Timeline, caseEntryOf(ev.Revision, p))
		}
	}
	for id, cs := range cases {
		// 冻结解释 + 当前战役排除状态(仲裁者同口径:重开后全部复活)。
		// 解析失败如实降级:不展示解释状态,但不拖垮定稿装配——解释是
		// 审计侧富化,证据内容(复现步骤)不因它缺失而丢(审核迭代 2 修:
		// 旧实现此处 continue,无冻结标准的声明连 Repro 定稿一起丢)。
		frozen, err := arbiter.FrozenAlternatives(ctx, store, taskID, id)
		if err != nil {
			frozen = nil
		}
		alive := map[string]bool{}
		if list, err := arbiter.AliveAlternatives(ctx, store, taskID, id); err == nil {
			for _, a := range list {
				alive[a.ID] = true
			}
		}
		for _, a := range frozen {
			cs.Alternatives = append(cs.Alternatives, AltView{ID: a.ID, Label: a.Label, Killed: !alive[a.ID]})
		}
		// 最新 repro(取代语义)→ 定稿视图 + blob 原文回放。
		for i := len(cs.Timeline) - 1; i >= 0; i-- {
			if cs.Timeline[i].Kind != "repro" {
				continue
			}
			cs.Repro = &ReproFinal{Rev: cs.Timeline[i].Rev, Severity: cs.Timeline[i].Severity}
			break
		}
		if cs.Repro != nil {
			cs.Repro.Steps = reproStepsOf(blobs, cs.Repro.Severity, evs, id)
		}
		// 最新复测结论(P3-06,取代语义同 repro:取最新一轮;时间线不含
		// retest 记录——修复状态轴与锤炼轨迹分列呈现)。
		cs.Retest = retestFinalOf(evs, id)
	}
	return cases, nil
}

// retestFinalOf 最新复测结论装配(事件流一次倒序扫描)。
func retestFinalOf(evs []rings.RawEvent, claimID string) *RetestFinal {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != rings.EventTestClaimRecord {
			continue
		}
		var p rings.TestClaimRecord
		if json.Unmarshal(evs[i].Payload, &p) != nil ||
			p.ClaimID != claimID || p.Type != rings.TestClaimRecRetest {
			continue
		}
		return &RetestFinal{Rev: evs[i].Revision, Round: p.Round, Verdict: p.RetestVerdict}
	}
	return nil
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
	case rings.TestClaimRecRepro:
		e.Kind = "repro"
		e.Severity = p.Severity
	}
	return e
}

// reproStepsOf 定稿步骤装配:最新 repro 记录的步骤 + blob 交换原文回放。
func reproStepsOf(blobs *swarm.BlobStore, severity string, evs []rings.RawEvent, claimID string) []ReproStepView {
	var latest *rings.TestClaimRecord
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != rings.EventTestClaimRecord {
			continue
		}
		var p rings.TestClaimRecord
		if json.Unmarshal(evs[i].Payload, &p) == nil && p.ClaimID == claimID && p.Type == rings.TestClaimRecRepro {
			latest = &p
			break
		}
	}
	if latest == nil {
		return nil
	}
	steps := make([]ReproStepView, 0, len(latest.Steps))
	for _, st := range latest.Steps {
		sv := ReproStepView{ReproStep: st}
		if st.BlobRef != "" && blobs != nil {
			if doc, err := blobs.Read(st.BlobRef); err == nil {
				var ex mirror.Exchange
				if json.Unmarshal(doc, &ex) == nil {
					sv.Raw, sv.RawClip = renderExchangeRaw(&ex)
				}
			}
		}
		steps = append(steps, sv)
	}
	return steps
}

// ClaimCaseOf 单条声明卷宗(发现详情 API 入口;任务量级下与全量装配
// 同为一次事件流扫描)。
func ClaimCaseOf(ctx context.Context, store *rings.Store, blobs *swarm.BlobStore, taskID, claimID string) (*ClaimCase, error) {
	cases, err := claimCases(ctx, store, blobs, taskID)
	if err != nil {
		return nil, err
	}
	cs, ok := cases[claimID]
	if !ok {
		return nil, fmt.Errorf("report: 发现 %s 不存在", claimID)
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
// https 交换(P3-01 解密留痕)首行标注目标证书校验结论——审计复现时
// 一眼可辨自签/过期目标的信任事实。请求侧与响应侧分块写入
// (writeRequestBlock/writeResponseBlock),单侧视图与全量同一装配。
func renderExchange(ex *mirror.Exchange, clipAt int) (string, bool) {
	var b strings.Builder
	t1 := writeRequestBlock(&b, ex, clipAt)
	var rb strings.Builder
	t2 := writeResponseBlock(&rb, ex, clipAt)
	resp := rb.String()
	if ex.Response != nil {
		b.WriteString("\n") // 请求与响应间的空行分隔(与原始 Burp 报文形态一致)
		b.WriteString(resp)
	} else if resp != "" {
		// 无响应仅异常注记:注记自带前导空行,原样衔接。
		b.WriteString(resp)
	}
	return b.String(), t1 || t2
}

// writeRequestBlock 请求侧原文块:目标证书标注(https) + 请求行/头/体。
// 返回正文是否被截断。
func writeRequestBlock(b *strings.Builder, ex *mirror.Exchange, clipAt int) bool {
	if ex.Type == rings.MirrorRecHTTPS && ex.TLS != nil {
		fmt.Fprintf(b, "[目标证书: %s]\n", mirror.TLSLabel(ex.TLS.Validity))
	}
	trunc := false
	if ex.Request != nil {
		fmt.Fprintf(b, "%s %s %s\n", ex.Request.Method, ex.Request.URL, ex.Request.Proto)
		if ex.Request.Host != "" {
			fmt.Fprintf(b, "Host: %s\n", ex.Request.Host)
		}
		for _, h := range ex.Request.Headers {
			fmt.Fprintf(b, "%s: %s\n", h.Name, h.Value)
		}
		if len(ex.Request.Body) > 0 {
			b.WriteString("\n")
			s, t := clipBody(ex.Request.Body, clipAt)
			b.WriteString(s)
			trunc = trunc || t
			b.WriteString("\n")
		}
	}
	return trunc
}

// writeResponseBlock 响应侧原文块:状态行/头/体 + 镜异常注记(无响应时
// 仅注记,自带前导空行)。返回正文是否被截断。
func writeResponseBlock(b *strings.Builder, ex *mirror.Exchange, clipAt int) bool {
	trunc := false
	if ex.Response != nil {
		if ex.Response.Proto != "" && ex.Response.StatusText != "" {
			fmt.Fprintf(b, "%s %d %s\n", ex.Response.Proto, ex.Response.Status, ex.Response.StatusText)
		} else {
			fmt.Fprintf(b, "HTTP %d\n", ex.Response.Status)
		}
		for _, h := range ex.Response.Headers {
			fmt.Fprintf(b, "%s: %s\n", h.Name, h.Value)
		}
		if len(ex.Response.Body) > 0 {
			b.WriteString("\n")
			s, t := clipBody(ex.Response.Body, clipAt)
			b.WriteString(s)
			trunc = trunc || t
			b.WriteString("\n")
		}
	}
	if ex.Error != "" {
		fmt.Fprintf(b, "\n[镜留痕异常: %s]\n", ex.Error)
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
// 展示):side=req 请求侧(含目标证书标注)、side=resp 响应侧(含镜异常
// 注记),不截断、字节级保真;与全量装配(报告/证据包)同源同一分块。
func ExchangeRawSide(ex *mirror.Exchange, side string) string {
	var b strings.Builder
	switch side {
	case "req":
		writeRequestBlock(&b, ex, 0)
	case "resp":
		writeResponseBlock(&b, ex, 0)
		return strings.TrimPrefix(b.String(), "\n") // 独立视图不带前导空行
	}
	return b.String()
}
