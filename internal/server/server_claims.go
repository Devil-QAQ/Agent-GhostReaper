// Package server 的发现详情与重开 API(P2-02,第三章 §5 + 第八章 8.8):
// 漏洞详情 Sheet 的数据源——完整反证时间线(候选 → 竞争解释 → 多轮
// 反证 → Confirmed/待确认 → 重开)与收割定稿的复现步骤(操作方法 +
// HTTP 请求/响应原文,Burp 可验证);重开 = 造物主反驳结论的面板入口
//(R7:先记账后唤醒,再裁决归仲裁者机器)。
//
// P2-13:时间线/竞争解释/定稿的装配收敛到 report.ClaimCaseOf 单一
// 实现(与报告导出同源,面板所见即报告所印),本文件只保留视图组装
//(锚点/攻击链/重开)。

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"ghostreaper/internal/overmind"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// claimEvidenceView 声明绑定的流量证据单条(P3-02):交换摘要(账本侧)
// + 绑定版本刻度。原文回放走流量页同一端点(/api/mirror/exchanges/…)。
type claimEvidenceView struct {
	ExchangeID string `json:"exchange_id"`
	Type       string `json:"type"`
	Method     string `json:"method,omitempty"`
	URL        string `json:"url,omitempty"`
	Status     int    `json:"status,omitempty"`
	TLS        string `json:"tls,omitempty"`
	Error      string `json:"error,omitempty"`
	BoundRev   int64  `json:"bound_rev"` // 首次绑定的账本版本号
}

// unitOriginView 成果溯源的一个单位锚点(P3-13b ①):单位 / 步骤 /
// 会话锚(SessionID 为跳转协议目标——?unit= 直达该单位会话页)。
type unitOriginView struct {
	UnitID    string `json:"unit_id"`
	StepID    string `json:"step_id,omitempty"`
	SessionID string `json:"session_id,omitempty"` // 单位会话锚(缺失 = 无会话记录)
	Round     int    `json:"round,omitempty"`      // 反证单位:所属反证轮次
}

// claimOriginView 成果溯源视图:发现单位 + 反证单位序列(R12 发现者
// 不得自证——两列并陈,审计可见独立性;账本数据补展示)。
type claimOriginView struct {
	Discoverer *unitOriginView  `json:"discoverer,omitempty"`
	Falsifiers []unitOriginView `json:"falsifiers"`
}

// claimOriginOf 成果溯源装配(P3-13b ①):P4-36 起读 claim_cases 投影的
// 溯源列(发现单位锚由 applyClaimNodeAdded 在写入时定位——detail 的
// 转录锚优先,缺省回退该单位×步骤最近的 session_started)与时间线的
// falsify 条目;反证单位的会话锚按条目 rev 回查 unit_sessions。失败
// 如实返回空视图(详情其余区不受影响)。
func (s *Server) claimOriginOf(ctx context.Context, taskID, claimID string) claimOriginView {
	view := claimOriginView{Falsifiers: []unitOriginView{}}
	proj, ok, err := s.Store.ClaimCaseOf(ctx, taskID, claimID)
	if err != nil || !ok {
		return view
	}
	if proj.Discoverer != "" {
		view.Discoverer = &unitOriginView{
			UnitID: proj.Discoverer, StepID: proj.DiscoverStep, SessionID: proj.DiscoverSess,
		}
	}
	for _, ent := range proj.Timeline {
		if ent.K != "falsify" {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ent.P, &rec) != nil || rec.UnitID == "" {
			continue
		}
		sid, _ := s.Store.SessionAnchorOf(ctx, taskID, rec.UnitID, rec.StepID, ent.Rev)
		view.Falsifiers = append(view.Falsifiers, unitOriginView{
			UnitID: rec.UnitID, StepID: rec.StepID, SessionID: sid, Round: rec.Round,
		})
	}
	return view
}

// claimTimingView 确认耗时拆解(P4-08 ②,问题清单 #17):发现落地 →
// 终态的排队/执行/轮次。P4-36 起读 claim_cases.timing 投影(写侧
// applyCaseStateChanged 增量维护,战役边界=reopen 清游标,与事件回放
// 同口径)。
type claimTimingView struct {
	LandedAt   string  `json:"landed_at"`             // 落地时刻(候选声明入账)
	TerminalAt string  `json:"terminal_at,omitempty"` // 终态时刻(Confirmed/待确认;进行中缺省)
	QueueSec   float64 `json:"queue_seconds"`         // 排队合计(反证步骤 dispatched→running 段合计)
	ExecSec    float64 `json:"exec_seconds"`          // 执行合计(running→下一状态段合计,含收场)
	Rounds     int     `json:"rounds"`                // 反证轮次(当前战役反证步骤数)
}

func (s *Server) claimTimingOf(ctx context.Context, taskID, claimID string) claimTimingView {
	var v claimTimingView
	proj, ok, err := s.Store.ClaimCaseOf(ctx, taskID, claimID)
	if err != nil || !ok {
		return v
	}
	v.LandedAt = proj.Timing.LandedAt
	v.TerminalAt = proj.Timing.TerminalAt
	v.QueueSec = proj.Timing.QueueSec
	v.ExecSec = proj.Timing.ExecSec
	v.Rounds = proj.Timing.Rounds
	return v
}

// claimDetailView 发现详情:节点 + 两维声明 + 冻结解释(存活状态)+
// 完整反证时间线 + 收割定稿(复现步骤与严重级别)+ 修复建议(8.8 修复
// 建议区:report.Remediation 确定性查表,与报告导出同源同一张表)+
// 绑定的流量证据与声明证据版本(P3-02)。
// Timeline/Alternatives/Repro 即 report 卷宗类型(JSON 口径不变)。
// RoundHumans 逐轮反证人话(P4-64 ①:每轮随 falsify 记录入账冻结,
// 锤炼中声明即有;存量旧定稿的 repro.Rounds 由前端合读,同轮号新
// 事件优先)。
type claimDetailView struct {
	Claim           claimView             `json:"claim"`
	Alternatives    []report.AltView      `json:"alternatives"`
	Timeline        []report.CaseEntry    `json:"timeline"`
	RoundHumans     []rings.FalsifyRoundHuman `json:"round_humans,omitempty"`
	Remediation     []string              `json:"remediation"` // 查表修复建议(未命中为空,读侧如实呈现)
	Repro           *report.ReproFinal    `json:"repro,omitempty"`
	Evidence        []claimEvidenceView   `json:"evidence"`                // 绑定的流量证据(首绑次序,去重)
	EvidenceVersion int64                 `json:"evidence_version"`        // 声明证据版本(绑定+定稿累计)
	Reopenable      bool                  `json:"reopenable"`              // Confirmed/不成立/待确认 可重开(结论可反驳;不成立=P4-55 ②)
	Retests         []retestView          `json:"retests"`                 // 复测历史(第 1 轮起,最新在尾;P3-06)
	Replays         []replayView          `json:"replays"`                 // 重放校验历史(第 1 轮起,最新在尾;P3-07)
	Origin          claimOriginView       `json:"origin"`                  // 成果溯源:发现单位与反证单位(P3-13b ①)
	Timing          claimTimingView       `json:"timing"`                  // 确认耗时拆解(P4-08 ②:落地→终态 排队/执行/轮次)
	Followup        claimFollowupView     `json:"followup"`                // 成果追问会话摘要(P4-14:入口按钮"已 N 问"的数据面)
	DiveDisabled    string                `json:"dive_disabled,omitempty"` // 深入按钮禁用态(P4-48:空 = 可用;终态任务 = 悬停文案"任务已结束")
}

// claimFollowupView 成果追问摘要(P4-14):当前轮次(0 = 尚未追问)、
// 最新轮问数与最近活动时刻——发现详情"向牧者追问 / 继续追问(已 N 问)"
// 按钮的数据面;轮次与会话页牧者组同源(shepherd 事件流派生)。
type claimFollowupView struct {
	Round     int    `json:"round"` // 最新轮(0 = 尚未追问)
	Rounds    int    `json:"rounds"`
	Questions int    `json:"questions"` // 最新轮问数
	LastAt    string `json:"last_at,omitempty"`
}

// retestView 一轮复测(详情抽屉的历史条目,P3-06)。SessionID 即会话
// 锚(转录名),原文下钻走既有会话原文端点。
type retestView struct {
	Round        int    `json:"round"`
	Verdict      string `json:"verdict"` // fixed|unfixed|inconclusive
	At           string `json:"at"`
	Note         string `json:"note,omitempty"`
	EvidenceHead string `json:"evidence_head,omitempty"`
	EvidenceRef  string `json:"evidence_ref,omitempty"`
	SessionID    string `json:"session_id"`
	UnitID       string `json:"unit_id,omitempty"`
}

// replayView 一轮重放校验(P3-07,详情抽屉的历史条目):结论 + 逐条
// 交换的新旧对照事实;Anchor 即归因锚(流量页按此过滤本轮新流量)。
type replayView struct {
	Round   int              `json:"round"`
	Verdict string           `json:"verdict"` // reproduced|diverged|unreachable
	At      string           `json:"at"`
	Note    string           `json:"note,omitempty"`
	Anchor  string           `json:"anchor"`
	Items   []replayItemView `json:"items"`
}

// replayItemView 重放的单条交换对照:原交换与本次重放的新交换(镜
// 留痕可回放)、双侧响应事实与失败原因。
type replayItemView struct {
	ExchangeID    string `json:"exchange_id"`
	NewExchangeID string `json:"new_exchange_id,omitempty"`
	Method        string `json:"method,omitempty"`
	URL           string `json:"url,omitempty"`
	OldStatus     int    `json:"old_status,omitempty"`
	NewStatus     int    `json:"new_status,omitempty"`
	BodySame      bool   `json:"body_same,omitempty"`
	OldBytes      int64  `json:"old_bytes,omitempty"`
	NewBytes      int64  `json:"new_bytes,omitempty"`
	Error         string `json:"error,omitempty"`
}

// chainLinksOf 组合声明的攻击链环节装配(P2-06;P2-11 起列表与详情
// 共用——年轮图·攻击链透镜读列表即得链序与角色,不另建投影)。
// polished 为定稿校对后的事实描述优先表(P4-59 ①,空表 = 无校对稿,
// 全部退节点标题)。
func (s *Server) chainLinksOf(ctx context.Context, taskID string, node *rings.NodeFull, polished map[string]string) []ChainLinkView {
	raw, ok := node.Detail["chain"].([]any)
	if !ok {
		return nil
	}
	links := make([]ChainLinkView, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		link := ChainLinkView{FactID: ""}
		link.FactID, _ = m["fact_id"].(string)
		link.Role, _ = m["role"].(string)
		if link.FactID != "" {
			if f, err := s.Store.Node(ctx, taskID, link.FactID); err == nil && f.Type == rings.NodeFact {
				link.Title = f.Title
				if p := polished[link.FactID]; p != "" {
					link.Title = p
				}
			}
		}
		links = append(links, link)
	}
	return links
}

// polishedFactMapOf 任务级"定稿校对后的事实描述"优先表(P4-59 ①):
// 反查自各判断的声明摘要(claim_cases.summary 小列),后定稿覆盖先
// 定稿——取代语义同其他冻结文本;读侧退节点标题。
func polishedFactMapOf(digests map[string]rings.ClaimDigest) map[string]string {
	out := map[string]string{}
	for _, d := range digests {
		for _, f := range d.Facts {
			if f.Statement != "" {
				out[f.FactID] = f.Statement
			}
		}
	}
	return out
}

// overlayFactPolish 事实确认批次文本覆盖(P4-64 ③,问题清单 #82):
// node_polish 投影里的事实描述(收割确认时生成,不限于已定稿判断链
// 上的事实)优先于判断摘要反查——直接覆盖进优先表;读侧空表零变化。
func (s *Server) overlayFactPolish(ctx context.Context, taskID string, polished map[string]string) {
	np, err := s.Store.NodePolish(ctx, taskID)
	if err != nil {
		return // 读包装失败如实退既有优先表,不影响其余装配
	}
	for id, p := range np {
		if p.Statement != "" {
			polished[id] = p.Statement
		}
	}
}

// polishedNodeTitleOf 任务级"节点标题短句"优先表(P4-64 ⑥:事实/假设
// 标题短句标准校对,年轮图/进度图节点卡与链路图等全站消费点共用;
// 空表 = 全退节点标题)。
func (s *Server) polishedNodeTitleOf(ctx context.Context, taskID string) map[string]string {
	out := map[string]string{}
	np, err := s.Store.NodePolish(ctx, taskID)
	if err != nil {
		return out
	}
	for id, p := range np {
		if p.Title != "" {
			out[id] = p.Title
		}
	}
	return out
}

// handleClaimDetail 发现详情 API(GET /api/tasks/{id}/claims/{cid}):
// 列表接口保持轻量,详情按 finding_id 惰性拉取(第五章 §4)。全部读账本
// 确定性组装,零 LLM(R17 同源);卷宗(时间线/解释/定稿+原文回放)与
// 报告导出同一装配(report.ClaimCaseOf)。
func (s *Server) handleClaimDetail(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	ctx := r.Context()
	node, err := s.Store.Node(ctx, taskID, claimID)
	if err != nil || node.Type != rings.NodeTestClaim {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	abouts, err := s.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	v := claimDetailView{Claim: claimView{
		ID: node.ID, Status: node.Status, Statement: node.Title, CreatedRev: node.CreatedRev,
	}, Retests: []retestView{}, Replays: []replayView{}}
	v.Origin = s.claimOriginOf(ctx, taskID, claimID)
	v.Timing = s.claimTimingOf(ctx, taskID, claimID)
	v.Claim.Impact, _ = node.Detail["impact"].(string)
	v.Claim.Vulnclass, _ = node.Detail["vulnclass"].(string)
	v.Claim.Discoverer, _ = node.Detail["unit_id"].(string)
	v.Claim.DiscoverStep, _ = node.Detail["step_id"].(string)
	for _, e := range abouts {
		if e.From == claimID {
			v.Claim.AssetIDs = append(v.Claim.AssetIDs, e.To)
		}
	}

	cs, err := report.ClaimCaseOf(ctx, s.Store, nil, taskID, claimID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	v.Alternatives = cs.Alternatives
	v.Timeline = cs.Timeline
	// 逐轮人话(P4-64 ①:按轮升序下发;锤炼中声明无需等定稿)。
	if len(cs.RoundHumans) > 0 {
		rounds := make([]int, 0, len(cs.RoundHumans))
		for r := range cs.RoundHumans {
			rounds = append(rounds, r)
		}
		sort.Ints(rounds)
		for _, r := range rounds {
			v.RoundHumans = append(v.RoundHumans, cs.RoundHumans[r])
		}
	}
	// 声明文本优先定稿校对稿(P4-59 ①:报告级文风——描述/危害/事实
	// 描述;未生成退节点原文,机器判定与审计仍读节点)。
	polished := map[string]string{}
	if cs.Summary != nil {
		if cs.Summary.Statement != "" {
			v.Claim.Statement = cs.Summary.Statement
		}
		if cs.Summary.Impact != "" {
			v.Claim.Impact = cs.Summary.Impact
		}
		for _, f := range cs.Summary.Facts {
			if f.Statement != "" {
				polished[f.FactID] = f.Statement
			}
		}
	}
	// P4-64 ③:事实自身的确认批次文本(node_polish)优先于判断摘要反查
	// ——不限于已定稿判断链上的事实;标题短句(P4-64 ⑥)。
	s.overlayFactPolish(ctx, taskID, polished)
	// 攻击链环节(P2-06 组合声明):detail.chain 逐环节下钻事实陈述
	//(血缘边即 derived_from→Fact,两侧同源)。列表与详情共用同一装配;
	// 环节事实标题优先定稿校对稿(P4-59 ①,链上事实即本判断摘要里的
	// 事实,读侧退节点标题)。
	if combined, _ := node.Detail["combined"].(bool); combined {
		v.Claim.Combined = true
		v.Claim.Chain = s.chainLinksOf(ctx, taskID, node, polished)
	}
	// 定稿轻量视图(P4-36 ②:详情按条取,复现步骤按步分段取——步骝始终
	// 走分段端点 /claims/{cid}/repro,详情只带小字段与计数,巨记录详情
	// 打开不卡死;blob 原文回放随分段逐页渲染)。
	if cs.Repro != nil {
		light := *cs.Repro
		light.Steps = nil
		v.Repro = &light
	}
	v.Claim.Severity = cs.Severity()
	v.Claim.Verifier = cs.Verifier()
	v.Claim.FalsifyStep = cs.FalsifyStep()
	v.Claim.PendingReason = cs.PendingReason()
	v.Claim.VerdictRev = cs.VerdictRev()
	// 四页签显示键(P4-56:详情头与列表同词同口径——等待反证/反证中/
	// 反证暂停/证据不足/已废弃,带重派与判定悬停细分)。
	detailDC := s.loadDisplayCtx(ctx, taskID)
	v.Claim.Display, v.Claim.DisplayHint = ClaimDisplayFor(detailDC, claimID, node.Status, v.Claim.PendingReason)
	// 血缘引用的事实(锤炼条目点回事实,详情同源;标题优先定稿校对稿)。
	{
		derivedEdges, derr := s.Store.Edges(ctx, taskID, rings.EdgeDerivedFrom)
		if derr == nil {
			for _, e := range derivedEdges {
				if e.From != claimID {
					continue
				}
				if n, nerr := s.Store.Node(ctx, taskID, e.To); nerr == nil && n.Type == rings.NodeFact {
					title := n.Title
					if p := polished[n.ID]; p != "" {
						title = p
					}
					v.Claim.FactRefs = append(v.Claim.FactRefs, FactRefView{ID: n.ID, Title: title})
				}
			}
		}
	}
	v.Claim.ReportExcluded, v.Claim.ReportExcludedReason = cs.ReportExcluded, cs.ReportExcludedReason
	v.Reopenable = node.Status == rings.TestClaimConfirmed || node.Status == rings.TestClaimRefuted || node.Status == rings.TestClaimPending
	v.Remediation = report.Remediation(v.Claim.Vulnclass)
	v.Evidence, v.EvidenceVersion = s.claimEvidenceOf(ctx, taskID, claimID)
	// 复测面(P3-06):历史轮次与最新结论;活会话从执行器登记读(进程
	// 内权威,进程重启后账本悬空已收口不误报)。
	v.Retests, v.Claim.RetestVerdict, v.Claim.RetestRound, v.Claim.RetestAt = s.claimRetestOf(ctx, taskID, claimID)
	v.Claim.Retestable = swarm.Retestable(node.Status)
	if exec := s.swarmExec(); exec != nil {
		v.Claim.RetestRunning = exec.RetestRunning(taskID, claimID)
	}
	// 重放面(P3-07):历史轮次与最新结论;活重放从执行器登记读;
	// 可重放 = 声明有绑定流量证据(重放对象是绑定证据,与状态机无关)。
	v.Replays, v.Claim.ReplayVerdict, v.Claim.ReplayRound, v.Claim.ReplayAt = s.claimReplayOf(ctx, taskID, claimID)
	v.Claim.Replayable = len(v.Evidence) > 0
	if exec := s.swarmExec(); exec != nil {
		v.Claim.ReplayRunning = exec.ReplayRunning(taskID, claimID)
	}
	// 成果追问摘要(P4-14):入口按钮数据面,与追问会话同源。
	if s.Shepherd != nil {
		if sessions, serr := s.Shepherd.ClaimQASessions(ctx, taskID); serr == nil {
			for _, x := range sessions {
				if x.ClaimID != claimID {
					continue
				}
				v.Followup.Rounds++
				if x.Round > v.Followup.Round {
					v.Followup.Round, v.Followup.Questions, v.Followup.LastAt = x.Round, x.Questions, x.LastAt
				}
			}
		}
	}
	// 深入按钮禁用态(P4-48):一切状态都可深入,只有任务终态置灰。
	v.DiveDisabled = s.diveDisabledOf(ctx, taskID)
	writeJSON(w, http.StatusOK, v)
}

// claimRetestOf 声明复测历史装配(P3-06):P4-36 起读卷宗时间线的 retest
// 条目(原始负载,事件序即历史序,最新在尾);返回的最新三元组供列表
// 字段同源填充。
func (s *Server) claimRetestOf(ctx context.Context, taskID, claimID string) ([]retestView, string, int, string) {
	out := []retestView{}
	var lastVerdict string
	var lastRound int
	var lastAt string
	proj, ok, err := s.Store.ClaimCaseOf(ctx, taskID, claimID)
	if err != nil || !ok {
		return out, "", 0, ""
	}
	for _, ent := range proj.Timeline {
		if ent.K != "retest" {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ent.P, &rec) != nil {
			continue
		}
		note := rec.Note
		// P4-64 ⑦:复测备注优先校对稿(入账时过通道冻结);无校对稿退
		// 机器记录 Note 原文(存量旧轮/未生成)。
		if n := proj.RetestNotes[rec.Round]; n != "" {
			note = n
		}
		rv := retestView{
			Round: rec.Round, Verdict: rec.RetestVerdict,
			At:           rings.FmtLocal(ent.At),
			Note:         note,
			EvidenceHead: rec.EvidenceHead, EvidenceRef: rec.EvidenceRef,
			SessionID: rec.StepID, UnitID: rec.UnitID,
		}
		out = append(out, rv)
		lastVerdict, lastRound, lastAt = rec.RetestVerdict, rec.Round, rv.At
	}
	return out, lastVerdict, lastRound, lastAt
}

// handleClaimRetest 发起复测(POST /api/tasks/{id}/claims/{cid}/retest,
// P3-06):登记起飞(同步前置校验:状态/依赖/并发互斥)后异步驱动会话;
// 面板立即拿到轮次与会话锚,进度经发现列表/详情与虫群会话页轮询可见。
func (s *Server) handleClaimRetest(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if _, err := s.Store.Node(r.Context(), taskID, claimID); err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	exec := s.swarmExec()
	if exec == nil {
		writeErr(w, http.StatusServiceUnavailable, swarm.ErrRetestUnavailable)
		return
	}
	run, err := exec.BeginRetest(r.Context(), taskID, claimID)
	if err != nil {
		switch {
		case errors.Is(err, swarm.ErrRetestInFlight):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, swarm.ErrRetestNotRetestable):
			writeErr(w, http.StatusBadRequest, err)
		case errors.Is(err, swarm.ErrRetestUnavailable):
			writeErr(w, http.StatusServiceUnavailable, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	go func() {
		_, _ = exec.DriveRetest(context.Background(), run)
	}()
	writeJSON(w, http.StatusOK, map[string]any{
		"round": run.Round(), "session_id": run.SID(),
	})
}

// handleClaimReplay 发起重放校验(POST /api/tasks/{id}/claims/{cid}/replay,
// P3-07):登记起飞(同步前置校验:声明/绑定证据/镜装配/并发互斥)后
// 异步确定性驱动(零 LLM);面板立即拿到轮次与归因锚,进度经发现列表/
// 详情轮询可见,新流量在流量页按锚可查。
func (s *Server) handleClaimReplay(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if _, err := s.Store.Node(r.Context(), taskID, claimID); err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	exec := s.swarmExec()
	if exec == nil {
		writeErr(w, http.StatusServiceUnavailable, swarm.ErrReplayUnavailable)
		return
	}
	run, err := exec.BeginReplay(r.Context(), taskID, claimID)
	if err != nil {
		switch {
		case errors.Is(err, swarm.ErrReplayInFlight):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, swarm.ErrReplayNoEvidence):
			writeErr(w, http.StatusBadRequest, err)
		case errors.Is(err, swarm.ErrReplayUnavailable):
			writeErr(w, http.StatusServiceUnavailable, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	go func() {
		_ = exec.DriveReplay(context.Background(), run)
	}()
	writeJSON(w, http.StatusOK, map[string]any{
		"round": run.Round(), "anchor": run.SID(),
	})
}

// claimReplayOf 声明重放历史装配(P3-07):P4-36 起读卷宗时间线的 replay
// 条目(事件序即历史序,最新在尾);返回的最新三元组供列表字段同源填充。
func (s *Server) claimReplayOf(ctx context.Context, taskID, claimID string) ([]replayView, string, int, string) {
	out := []replayView{}
	var lastVerdict string
	var lastRound int
	var lastAt string
	proj, ok, err := s.Store.ClaimCaseOf(ctx, taskID, claimID)
	if err != nil || !ok {
		return out, "", 0, ""
	}
	for _, ent := range proj.Timeline {
		if ent.K != "replay" {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ent.P, &rec) != nil {
			continue
		}
		rv := replayView{
			Round: rec.Round, Verdict: rec.ReplayVerdict,
			At:    rings.FmtLocal(ent.At),
			Note:  rec.Note,
			Items: replayItemViews(rec.ReplayResults),
		}
		out = append(out, rv)
		lastVerdict, lastRound, lastAt = rec.ReplayVerdict, rec.Round, rv.At
	}
	return out, lastVerdict, lastRound, lastAt
}

// replayItemViews 重放逐条交换事实的面板视图(数值直传,呈现归前端)。
func replayItemViews(rs []rings.ReplayResult) []replayItemView {
	out := make([]replayItemView, 0, len(rs))
	for _, r := range rs {
		out = append(out, replayItemView{
			ExchangeID: r.ExchangeID, NewExchangeID: r.NewExchangeID,
			Method: r.Method, URL: r.URL,
			OldStatus: r.OldStatus, NewStatus: r.NewStatus,
			BodySame: r.BodySame, OldBytes: r.OldBytes, NewBytes: r.NewBytes,
			Error: r.Error,
		})
	}
	return out
}

// claimEvidenceOf 声明流量证据装配(P3-02):P4-36 起读卷宗投影的绑定
// 清单(首绑次序,写侧 applyClaimRecord 同事务维护)+ mirror_briefs 摘要
// + 声明证据版本(绑定 + 定稿计数)。悬空引用(留痕被特权清除等极端
// 情形)如实跳过——绑定事实仍在事件流。
func (s *Server) claimEvidenceOf(ctx context.Context, taskID, claimID string) ([]claimEvidenceView, int64) {
	proj, ok, err := s.Store.ClaimCaseOf(ctx, taskID, claimID)
	if err != nil || !ok {
		return []claimEvidenceView{}, 0
	}
	version := int64(proj.EvidenceBinds + proj.ReproEvents)
	if len(proj.Evidence) == 0 {
		return []claimEvidenceView{}, version
	}
	ids := make([]string, 0, len(proj.Evidence))
	for _, b := range proj.Evidence {
		ids = append(ids, b.ExchangeID)
	}
	briefs, err := s.Store.MirrorBriefs(ctx, taskID, ids)
	if err != nil {
		return []claimEvidenceView{}, version
	}
	out := make([]claimEvidenceView, 0, len(proj.Evidence))
	for _, b := range proj.Evidence {
		m, ok := briefs[b.ExchangeID]
		if !ok {
			continue
		}
		out = append(out, claimEvidenceView{
			ExchangeID: b.ExchangeID, Type: m.Type, Method: m.Method,
			URL: m.URL, Status: m.Status, TLS: m.TLS, Error: m.Error,
			BoundRev: b.Rev,
		})
	}
	return out, version
}

// handleClaimReopen 重开 API(POST /api/tasks/{id}/claims/{cid}/reopen):
// 造物主反驳结论——reopen 记录(触发词+理由)与新一轮反证 Step 同批
// 落账后唤醒主宰跑重开轮(R7:一切操控走账本;再裁决仍归仲裁者)。
func (s *Server) handleClaimReopen(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Reason  string `json:"reason"`
		Trigger string `json:"trigger"` // new_evidence|manual|expired(空=manual)
	}
	if !readJSON(w, r, &req) {
		return
	}
	trigger := strings.TrimSpace(req.Trigger)
	if trigger == "" {
		trigger = rings.ReopenManual
	}
	if _, err := s.Store.Node(r.Context(), taskID, claimID); err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	if err := overmind.ReopenClaim(context.Background(), s.Store, taskID, claimID,
		trigger, req.Reason, "creator"); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.wake(taskID)
	writeJSON(w, http.StatusOK, map[string]string{"id": claimID, "status": rings.TestClaimTesting})
}

// ---- 复现步骤分段取(P4-36 ②,问题清单 #57) ----

// reproPageSize 复现步骤分段页大小(定值;一步型/链式几页即完,存量
// 巨记录去同型步骤数百条也逐页消化,单页含 blob 原文回放,页过大反而
// 卡界面)。
const reproPageSize = 8

// reproStepsPage 分段页视图:步骤(含 blob 原文回放,与报告同源同一
// 装配)+ 分段游标。NoReproReason 为无复现包的原因(P4-62 A:空选/挑选
// 失败如实呈现,前端空态渲染)。
type reproStepsPage struct {
	Rev           int64                  `json:"rev"`
	Severity      string                 `json:"severity,omitempty"`
	NoReproReason string                 `json:"no_repro_reason,omitempty"`
	Steps         []report.ReproStepView `json:"steps"`
	Offset        int                    `json:"offset"`
	Total         int                    `json:"total"`
	HasMore       bool                   `json:"has_more"`
}

// handleClaimRepro 复现步骤分段 API(GET /api/tasks/{id}/claims/{cid}/repro
// ?offset=&limit=):定稿步骤按页下发,每步原文回放同报告装配(blob 截断
// 同口径);offset/limit 缺省取首页/页大小。详情响应不带步骤(轻量),
// 步骤始终经本端点分段拉取——存量巨记录(步骤数百条)详情打开不卡死。
func (s *Server) handleClaimRepro(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	node, err := s.Store.Node(r.Context(), taskID, claimID)
	if err != nil || node.Type != rings.NodeTestClaim {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = reproPageSize
	}
	proj, ok, err := s.Store.ClaimCaseOf(r.Context(), taskID, claimID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	page := reproStepsPage{Offset: offset, Steps: []report.ReproStepView{}}
	if !ok || proj.Repro == nil {
		writeJSON(w, http.StatusOK, page)
		return
	}
	page.Rev, page.Severity, page.NoReproReason = proj.Repro.Rev, proj.Repro.Severity, proj.Repro.NoReproReason
	steps := proj.Repro.Steps
	page.Total = len(steps)
	if offset < len(steps) {
		end := offset + limit
		if end > len(steps) {
			end = len(steps)
		}
		blobs := s.blobStore()
		for _, st := range steps[offset:end] {
			sv := report.ReproStepView{ReproStep: st}
			if st.BlobRef != "" && blobs != nil {
				if raw, rerr := blobs.Read(st.BlobRef); rerr == nil {
					sv.FillRaw(raw)
				}
			}
			page.Steps = append(page.Steps, sv)
		}
	}
	page.HasMore = offset+len(page.Steps) < page.Total
	writeJSON(w, http.StatusOK, page)
}
