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
	"strings"
	"time"

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

// claimOriginOf 成果溯源装配(P3-13b ①):事件流一遍扫描——
// swarm.record session_started 维护"单位×步骤 → 会话锚"的当前值(事件
// 序保证先起飞后写回),node.added 命中本声明时取发现单位(detail 的
// 转录锚优先,旧数据缺省退当前会话表),falsify_result 命中时按当前
// 会话表归位反证单位。失败如实返回空视图(详情其余区不受影响)。
func (s *Server) claimOriginOf(ctx context.Context, taskID, claimID string) claimOriginView {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return claimOriginView{Falsifiers: []unitOriginView{}}
	}
	sessionOf := map[[2]string]string{}
	view := claimOriginView{Falsifiers: []unitOriginView{}}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventSwarmRecord:
			var rec rings.SwarmRecord
			if json.Unmarshal(ev.Payload, &rec) != nil || rec.TranscriptID == "" {
				continue
			}
			if rec.Type == rings.SwarmRecSessionStarted {
				sessionOf[[2]string{rec.UnitID, rec.StepID}] = rec.TranscriptID
			}
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil || p.NodeID != claimID || p.NodeType != rings.NodeTestClaim {
				continue
			}
			unit, _ := p.Detail["unit_id"].(string)
			step, _ := p.Detail["step_id"].(string)
			sid, _ := p.Detail["transcript_id"].(string)
			if sid == "" {
				sid = sessionOf[[2]string{unit, step}]
			}
			if unit != "" {
				view.Discoverer = &unitOriginView{UnitID: unit, StepID: step, SessionID: sid}
			}
		case rings.EventTestClaimRecord:
			var rec rings.TestClaimRecord
			if json.Unmarshal(ev.Payload, &rec) != nil ||
				rec.Type != rings.TestClaimRecFalsify || rec.ClaimID != claimID || rec.UnitID == "" {
				continue
			}
			view.Falsifiers = append(view.Falsifiers, unitOriginView{
				UnitID: rec.UnitID, StepID: rec.StepID,
				SessionID: sessionOf[[2]string{rec.UnitID, rec.StepID}],
				Round:     rec.Round,
			})
		}
	}
	return view
}

// claimTimingView 确认耗时拆解(P4-08 ②,问题清单 #17):发现落地 →
// 终态的排队/执行/轮次——账本时间戳直读,零新增采集。落地 = 候选声明
// 入账;终态 = 最近一次 Confirmed/待确认 迁移(进行中为空,如实不造);
// 排队/执行/轮次只算当前战役的反证步骤(重开前的旧战役不混计,口径与
// 复现步骤归因同源 overmind.ClaimFalsifySteps)。
type claimTimingView struct {
	LandedAt   string  `json:"landed_at"`             // 落地时刻(候选声明入账)
	TerminalAt string  `json:"terminal_at,omitempty"` // 终态时刻(Confirmed/待确认;进行中缺省)
	QueueSec   float64 `json:"queue_seconds"`         // 排队合计(反证步骤 dispatched→running 段合计)
	ExecSec    float64 `json:"exec_seconds"`          // 执行合计(running→下一状态段合计,含收场)
	Rounds     int     `json:"rounds"`                // 反证轮次(当前战役反证步骤数)
}

// claimTimingOf 耗时拆解装配:事件流一次扫描(声明入账/终态 + 战役反证
// 步骤的状态段迁移)。失败如实返回零值视图(详情其余区不受影响)。
func (s *Server) claimTimingOf(ctx context.Context, taskID, claimID string) claimTimingView {
	var v claimTimingView
	steps, err := overmind.ClaimFalsifySteps(ctx, s.Store, taskID, claimID)
	if err != nil {
		return v
	}
	inCampaign := map[string]bool{}
	for _, st := range steps {
		inCampaign[st.ID] = true
	}
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return v
	}
	v.Rounds = len(steps)
	// 战役步骤的状态段游标(逐事件推进):dispatched→running 计排队,
	// running→其余计执行(R14 重领的来回段如实累加)。
	lastState := map[string]string{}
	lastAt := map[string]time.Time{}
	acc := func(from, to string, d time.Duration) {
		switch {
		case from == "dispatched" && to == "running":
			v.QueueSec += d.Seconds()
		case from == "running" && to != "running":
			v.ExecSec += d.Seconds()
		}
	}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeAdded:
			var p rings.NodeAdded
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if p.NodeType == rings.NodeTestClaim && p.NodeID == claimID {
				v.LandedAt = ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
				continue
			}
			if p.NodeType == rings.NodeStep && inCampaign[p.NodeID] {
				lastState[p.NodeID], lastAt[p.NodeID] = "dispatched", ev.OccurredAt
			}
		case rings.EventNodeStateChanged:
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if p.NodeID == claimID {
				if p.Status == rings.TestClaimConfirmed || p.Status == rings.TestClaimPending {
					v.TerminalAt = ev.OccurredAt.Local().Format("2006-01-02 15:04:05")
				}
				continue
			}
			if !inCampaign[p.NodeID] {
				continue
			}
			if prev, ok := lastState[p.NodeID]; ok {
				acc(prev, p.Status, ev.OccurredAt.Sub(lastAt[p.NodeID]))
			}
			lastState[p.NodeID], lastAt[p.NodeID] = p.Status, ev.OccurredAt
		}
	}
	return v
}

// claimDetailView 发现详情:节点 + 两维声明 + 冻结解释(存活状态)+
// 完整反证时间线 + 收割定稿(复现步骤与严重级别)+ 修复建议(8.8 修复
// 建议区:report.Remediation 确定性查表,与报告导出同源同一张表)+
// 绑定的流量证据与声明证据版本(P3-02)。
// Timeline/Alternatives/Repro 即 report 卷宗类型(JSON 口径不变)。
type claimDetailView struct {
	Claim           claimView           `json:"claim"`
	Alternatives    []report.AltView    `json:"alternatives"`
	Timeline        []report.CaseEntry  `json:"timeline"`
	Remediation     []string            `json:"remediation"` // 查表修复建议(未命中为空,读侧如实呈现)
	Repro           *report.ReproFinal  `json:"repro,omitempty"`
	Evidence        []claimEvidenceView `json:"evidence"`         // 绑定的流量证据(首绑次序,去重)
	EvidenceVersion int64               `json:"evidence_version"` // 声明证据版本(绑定+定稿累计)
	Reopenable      bool                `json:"reopenable"`       // Confirmed/待确认 可重开(结论可反驳)
	Retests         []retestView        `json:"retests"`          // 复测历史(第 1 轮起,最新在尾;P3-06)
	Replays         []replayView        `json:"replays"`          // 重放校验历史(第 1 轮起,最新在尾;P3-07)
	Origin          claimOriginView     `json:"origin"`           // 成果溯源:发现单位与反证单位(P3-13b ①)
	Timing          claimTimingView     `json:"timing"`           // 确认耗时拆解(P4-08 ②:落地→终态 排队/执行/轮次)
	Followup        claimFollowupView   `json:"followup"`         // 成果追问会话摘要(P4-14:入口按钮"已 N 问"的数据面)
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
func (s *Server) chainLinksOf(ctx context.Context, taskID string, node *rings.NodeFull) []ChainLinkView {
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
			}
		}
		links = append(links, link)
	}
	return links
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
	// 攻击链环节(P2-06 组合声明):detail.chain 逐环节下钻事实陈述
	//(血缘边即 derived_from→Fact,两侧同源)。列表与详情共用同一装配。
	if combined, _ := node.Detail["combined"].(bool); combined {
		v.Claim.Combined = true
		v.Claim.Chain = s.chainLinksOf(ctx, taskID, node)
	}

	cs, err := report.ClaimCaseOf(ctx, s.Store, s.blobStore(), taskID, claimID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	v.Alternatives = cs.Alternatives
	v.Timeline = cs.Timeline
	v.Repro = cs.Repro
	v.Claim.Severity = cs.Severity()
	v.Claim.Verifier = cs.Verifier()
	v.Claim.FalsifyStep = cs.FalsifyStep()
	v.Claim.PendingReason = cs.PendingReason()
	v.Claim.VerdictRev = cs.VerdictRev()
	v.Claim.ReportExcluded, v.Claim.ReportExcludedReason = cs.ReportExcluded, cs.ReportExcludedReason
	v.Reopenable = node.Status == rings.TestClaimConfirmed || node.Status == rings.TestClaimPending
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
	writeJSON(w, http.StatusOK, v)
}

// claimRetestOf 声明复测历史装配(P3-06):事件流一次扫描,历史按轮次
// 顺序(最新在尾);返回的最新三元组供列表字段同源填充。
func (s *Server) claimRetestOf(ctx context.Context, taskID, claimID string) ([]retestView, string, int, string) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return []retestView{}, "", 0, ""
	}
	out := []retestView{}
	var lastVerdict string
	var lastRound int
	var lastAt string
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &rec) != nil ||
			rec.Type != rings.TestClaimRecRetest || rec.ClaimID != claimID {
			continue
		}
		rv := retestView{
			Round: rec.Round, Verdict: rec.RetestVerdict,
			At:           ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
			Note:         rec.Note,
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

// claimReplayOf 声明重放历史装配(P3-07):事件流一次扫描,历史按轮次
// 顺序(最新在尾);返回的最新三元组供列表字段同源填充。
func (s *Server) claimReplayOf(ctx context.Context, taskID, claimID string) ([]replayView, string, int, string) {
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return []replayView{}, "", 0, ""
	}
	out := []replayView{}
	var lastVerdict string
	var lastRound int
	var lastAt string
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var rec rings.TestClaimRecord
		if json.Unmarshal(ev.Payload, &rec) != nil ||
			rec.Type != rings.TestClaimRecReplay || rec.ClaimID != claimID {
			continue
		}
		rv := replayView{
			Round: rec.Round, Verdict: rec.ReplayVerdict,
			At:    ev.OccurredAt.Local().Format("2006-01-02 15:04:05"),
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

// claimEvidenceOf 声明流量证据装配(P3-02):绑定事件展开(首绑次序去重)
// + 交换摘要(账本 mirror.record 一次扫描建索引)+ 声明证据版本。
// 悬空引用(留痕被特权清除等极端情形)如实跳过——绑定事实仍在事件流。
func (s *Server) claimEvidenceOf(ctx context.Context, taskID, claimID string) ([]claimEvidenceView, int64) {
	perClaim, _, err := s.Store.EvidenceVersions(ctx, taskID)
	if err != nil {
		return []claimEvidenceView{}, 0
	}
	exchanges, _, err := s.Store.ClaimEvidence(ctx, taskID, claimID)
	if err != nil || len(exchanges) == 0 {
		return []claimEvidenceView{}, perClaim[claimID]
	}
	evs, err := s.Store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return []claimEvidenceView{}, perClaim[claimID]
	}
	type mirrorBrief struct {
		rec rings.MirrorRecord
		rev int64
	}
	firstRev := map[string]int64{}
	briefs := map[string]mirrorBrief{}
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventMirrorRecord:
			var m rings.MirrorRecord
			if json.Unmarshal(ev.Payload, &m) == nil {
				briefs[m.ExchangeID] = mirrorBrief{rec: m, rev: ev.Revision}
			}
		case rings.EventTestClaimRecord:
			var tcr rings.TestClaimRecord
			if json.Unmarshal(ev.Payload, &tcr) != nil || tcr.ClaimID != claimID || tcr.Type != rings.TestClaimRecEvidence {
				continue
			}
			for _, ex := range tcr.Exchanges {
				if _, ok := firstRev[ex]; !ok {
					firstRev[ex] = ev.Revision
				}
			}
		}
	}
	out := make([]claimEvidenceView, 0, len(exchanges))
	for _, ex := range exchanges {
		b, ok := briefs[ex]
		if !ok {
			continue
		}
		out = append(out, claimEvidenceView{
			ExchangeID: ex, Type: b.rec.Type, Method: b.rec.Method,
			URL: b.rec.URL, Status: b.rec.Status, TLS: b.rec.TLS, Error: b.rec.Error,
			BoundRev: firstRev[ex],
		})
	}
	return out, perClaim[claimID]
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
