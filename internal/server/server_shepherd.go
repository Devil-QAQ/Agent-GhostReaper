// Package server 的牧者会话面板 API(P2-07,第四章 §1 牧者 + 8.5
// Agent 会话页):对话流读写(消息带送达状态)、会话信息条数据
// (模型 / token 入出缓存 / 上下文消耗比例 / 时长,与 token 独立表
// 同源对账)、图上节点详情(fact# 等引用可点击核对)。
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
)

// shepherdInfoBar 会话顶部信息条数据(8.5 Agent 会话页组件:当前
// 模型 · 本会话 token 输入/输出/缓存 · 上下文消耗比例 · 会话时长)。
// token 三项与 token 独立表逐行同源(按 agent=shepherd 过滤求和,
// 对账口径一致);比例 = 最近一次模型调用的 input ÷ 模型上下文窗口
// (窗口来自模型库配置,compaction 后最近请求 input 回落、比例随之
// 回落);时长锚会话首事件时刻。
type shepherdInfoBar struct {
	Model         string  `json:"model,omitempty"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CacheTokens   int     `json:"cache_tokens"`
	ContextWindow int     `json:"context_window,omitempty"` // 0 = 未配置(比例不呈现)
	ContextRatio  float64 `json:"context_ratio,omitempty"`  // 0~1(最近请求 input ÷ 窗口)
	Calls         int     `json:"calls"`                    // 本会话模型调用次数
}

// handleShepherdGet 牧者会话读视图:对话流(消息/送达状态/工具折叠
// 卡/思考段)+ 建议裁决轨迹 + 信息条数据 + 会话忙闲 + 任务态(P2-08:
// 冬眠冻结与待裁决笔数——入口提示与只读切换的数据面)。
func (s *Server) handleShepherdGet(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.Shepherd == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("牧者会话服务未装配"))
		return
	}
	view, err := s.Shepherd.View(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	taskStatus := ""
	if t := s.Manager.Task(taskID); t != nil {
		taskStatus = t.Status()
	}
	// 会话标记(P3-13b ②):牧者会话的组织信息,锚 = 转录 ID(shepherd-<taskID>)。
	meta := sessionMetaView{}
	if labels, lerr := s.Store.SessionLabels(r.Context(), taskID); lerr == nil {
		meta = sessionMetaOf(labels, shepherd.SessionID(taskID))
	}
	out := struct {
		*shepherd.View
		Info             shepherdInfoBar       `json:"info"`
		TaskStatus       string                `json:"task_status"`
		Hibernated       bool                  `json:"hibernated"`
		HibernateSec     float64               `json:"hibernate_seconds"` // P4-02 ④ 时长冻结口径的冬眠抵扣(含进行中)
		PendingApprovals int                   `json:"pending_approvals"`
		Meta             sessionMetaView       `json:"meta"`
		Followups        []shepherdFollowupRow `json:"followups"` // 成果追问会话列表(P4-14,牧者组左侧入口)
	}{
		View: view, Info: s.shepherdInfo(r, taskID),
		TaskStatus: taskStatus, Hibernated: s.taskHibernated(taskID),
		HibernateSec:     s.Store.HibernateDeduct(r.Context(), taskID, time.Now()).Seconds(),
		PendingApprovals: len(s.Gate.Pending(taskID)),
		Meta:             meta,
		Followups:        s.shepherdFollowups(r, taskID),
	}
	writeJSON(w, http.StatusOK, out)
}

// shepherdFollowupRow 会话页牧者组的成果追问条目(P4-14:每 (成果 × 轮)
// 一条,标题取声明原文,面板截断呈现)。
type shepherdFollowupRow struct {
	ClaimID   string `json:"claim_id"`
	Statement string `json:"statement"` // 声明原文(漏洞名称)
	Status    string `json:"status"`    // 声明当前状态
	Round     int    `json:"round"`
	Questions int    `json:"questions"`
	StartedAt string `json:"started_at,omitempty"`
	LastAt    string `json:"last_at,omitempty"`
}

// shepherdFollowups 成果追问条目装配(事件流 + 声明节点标题,零 LLM)。
func (s *Server) shepherdFollowups(r *http.Request, taskID string) []shepherdFollowupRow {
	if s.Shepherd == nil {
		return []shepherdFollowupRow{}
	}
	sessions, err := s.Shepherd.ClaimQASessions(r.Context(), taskID)
	if err != nil || len(sessions) == 0 {
		return []shepherdFollowupRow{}
	}
	claims := map[string]rings.NodeFull{}
	if list, err := s.Store.NodesByType(r.Context(), taskID, rings.NodeTestClaim, ""); err == nil {
		for _, c := range list {
			claims[c.ID] = c
		}
	}
	out := make([]shepherdFollowupRow, 0, len(sessions))
	for _, x := range sessions {
		row := shepherdFollowupRow{
			ClaimID: x.ClaimID, Round: x.Round, Questions: x.Questions,
			StartedAt: x.StartedAt, LastAt: x.LastAt,
		}
		if c, ok := claims[x.ClaimID]; ok {
			row.Statement, row.Status = c.Title, c.Status
		}
		out = append(out, row)
	}
	return out
}

// shepherdInfo 从 token 独立表与模型库装配信息条(纯读,零额外采集)。
func (s *Server) shepherdInfo(r *http.Request, taskID string) shepherdInfoBar {
	return s.shepherdInfoBySID(r, taskID, shepherd.SessionID(taskID))
}

// shepherdInfoBySID 按转录锚聚合牧者侧信息条(主对话与成果追问会话
// 共用同一口径;追问会话按其独立会话锚过滤,与主对话分开对账)。
func (s *Server) shepherdInfoBySID(r *http.Request, taskID, sid string) shepherdInfoBar {
	var info shepherdInfoBar
	rows, err := s.Store.TokenUsageRows(r.Context(), taskID)
	if err != nil {
		return info
	}
	lastModel, lastInput := "", 0
	for _, x := range rows {
		if x.Agent != shepherd.ActorShepherd || x.TranscriptID != sid {
			continue
		}
		info.InputTokens += x.InputTokens
		info.OutputTokens += x.OutputTokens
		info.CacheTokens += x.CacheReadTokens + x.CacheWriteTokens
		info.Calls++
		lastModel, lastInput = x.Model, x.InputTokens
		info.Model = x.Model
	}
	if info.Model == "" && s.Chain != nil {
		info.Model = s.Chain.ActiveModel()
	}
	window := failover.ContextWindowFor(r.Context(), s.Store, lastModel)
	info.ContextWindow = window
	if window > 0 {
		// 比例 = 最近请求 input ÷ 窗口(一行 = 一次模型调用的该请求
		// 全量输入;compaction 后最近请求 input 回落,比例随之回落)。
		info.ContextRatio = float64(lastInput) / float64(window)
	}
	return info
}

// ---- 成果追问会话(P4-14,问题清单 #24) ----

// handleShepherdClaimGet 成果追问读视图:该成果指定轮(缺省最新轮)的
// 问答流 + 独立信息条(按该轮会话锚过滤 token 行)+ 轮次信息与冻结态。
func (s *Server) handleShepherdClaimGet(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.Shepherd == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("牧者会话服务未装配"))
		return
	}
	if _, err := s.Store.Node(r.Context(), taskID, claimID); err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	round := 0
	if q := r.URL.Query().Get("round"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			round = n
		}
	}
	view, err := s.Shepherd.ViewClaim(r.Context(), taskID, claimID, round)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	latest := s.Shepherd.CurrentRound(r.Context(), taskID, claimID)
	out := struct {
		*shepherd.View
		Info         shepherdInfoBar `json:"info"`
		Hibernated   bool            `json:"hibernated"`
		HibernateSec float64         `json:"hibernate_seconds"`
		LatestRound  int             `json:"latest_round"`
	}{
		View: view, Info: s.shepherdInfoBySID(r, taskID, shepherd.ClaimQASID(claimID, view.Round)),
		Hibernated:   s.taskHibernated(taskID),
		HibernateSec: s.Store.HibernateDeduct(r.Context(), taskID, time.Now()).Seconds(),
		LatestRound:  latest,
	}
	writeJSON(w, http.StatusOK, out)
}

// handleShepherdClaimPost 成果追问发言入队(P4-14):一成果一会话、续接
// 不新建;new_round=true 时下一问起新轮(旧轮留档)。会话开场的首问由
// 服务端确定性装配成果链路预载包(与发现详情同源:定稿叙述 / 溯源 /
// 耗时拆解),注入模型会话,面板消息流只呈问话原文。
func (s *Server) handleShepherdClaimPost(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.Shepherd == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("牧者会话服务未装配"))
		return
	}
	var req struct {
		Text     string `json:"text"`
		NewRound bool   `json:"new_round"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	node, err := s.Store.Node(r.Context(), taskID, claimID)
	if err != nil || node.Type != rings.NodeTestClaim {
		writeErr(w, http.StatusNotFound, fmt.Errorf("发现 %s 不存在", claimID))
		return
	}
	prelude := s.claimFollowupPrelude(r.Context(), taskID, node)
	id, round, err := s.Shepherd.EnqueueClaim(r.Context(), taskID, claimID,
		req.Text, req.NewRound, prelude)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message_id": id, "round": round,
	})
}

// claimFollowupPrelude 成果链路预载包(确定性装配,零 LLM;与发现详情
// 同源):声明两维与级别、P4-13 定稿叙述、发现与反证单位、确认时刻,
// 外加一条应答纪律(围绕该成果、查账本再答、引用带编号)。追问会话的
// 开场上下文——不空白开场,牧者第一答即锚定成果。
func (s *Server) claimFollowupPrelude(ctx context.Context, taskID string, node *rings.NodeFull) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【成果】%s:%s\n", node.ID, node.Title)
	if impact, _ := node.Detail["impact"].(string); impact != "" {
		fmt.Fprintf(&b, "影响面:%s\n", impact)
	}
	if cs, err := report.ClaimCaseOf(ctx, s.Store, s.blobStore(), taskID, node.ID); err == nil {
		if cs.Repro != nil {
			if sev := cs.Severity(); sev != "" {
				fmt.Fprintf(&b, "严重级别:%s\n", sev)
			}
			if cs.Repro.Narrative != "" {
				fmt.Fprintf(&b, "来龙去脉(定稿叙述):%s\n", cs.Repro.Narrative)
			}
		}
	}
	origin := s.claimOriginOf(ctx, taskID, node.ID)
	if origin.Discoverer != nil {
		line := "发现单位:" + origin.Discoverer.UnitID
		if origin.Discoverer.StepID != "" {
			line += "(步骤 " + origin.Discoverer.StepID + ")"
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	if n := len(origin.Falsifiers); n > 0 {
		units := make([]string, 0, n)
		for _, f := range origin.Falsifiers {
			units = append(units, f.UnitID)
		}
		fmt.Fprintf(&b, "反证:%d 轮(单位 %s)\n", n, strings.Join(units, "、"))
	} else {
		b.WriteString("反证:无记录\n")
	}
	timing := s.claimTimingOf(ctx, taskID, node.ID)
	if timing.TerminalAt != "" {
		// "确认"措辞只给 Confirmed:待确认声明的终态是"待确认"裁决
		// 时刻,不是确认(如实陈述,不编造)。
		if node.Status == rings.TestClaimConfirmed {
			fmt.Fprintf(&b, "确认时刻:%s\n", timing.TerminalAt)
		} else {
			fmt.Fprintf(&b, "结束时刻:%s\n", timing.TerminalAt)
		}
	} else {
		fmt.Fprintf(&b, "结束时刻:进行中(开始 %s)\n", timing.LandedAt)
	}
	fmt.Fprintf(&b, "判断当前状态:%s\n", node.Status)
	b.WriteString("应答纪律:围绕该成果回答造物主的追问;事实先查账本(read_findings / read_progress),人话陈述、数字精确;引用图上节点带编号(如 claim:ID、step:ID、fact:ID、asset:ID),造物主可点击核对;账本里没有的不编造。")
	return b.String()
}

// handleShepherdPost 造物主发言入队(已排队即返回;应答异步完成,送达
// 状态与回复经 GET 轮询可见——对忙着的牧者打字不石沉大海)。
func (s *Server) handleShepherdPost(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.Shepherd == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("牧者会话服务未装配"))
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id, err := s.Shepherd.Enqueue(r.Context(), taskID, req.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message_id": id, "state": "queued"})
}

// handleNodeDetail 图上节点详情(fact# / claim / step / asset 引用的
// 点击核对面;会话页与后续页面共用)。
func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	n, err := s.Store.Node(r.Context(), taskID, r.PathValue("nid"))
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("节点不存在: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": n.ID, "type": string(n.Type), "title": n.Title, "status": n.Status,
		"asset_class": string(n.AssetClass), "detail": n.Detail,
		"created_rev": n.CreatedRev, "updated_rev": n.UpdatedRev,
	})
}
