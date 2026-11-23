// Package server 的牧者会话面板 API(P2-07,第四章 §1 牧者 + 8.5
// Agent 会话页):对话流读写(消息带送达状态)、会话信息条数据
// (模型 / token 入出缓存 / 上下文消耗比例 / 时长,与 token 独立表
// 同源对账)、图上节点详情(fact# 等引用可点击核对)。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
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
	Kind      string `json:"kind"`
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
	for _, typ := range []rings.NodeType{rings.NodeTestClaim, rings.NodeFact} {
		if list, err := s.Store.NodesByType(r.Context(), taskID, typ, ""); err == nil {
			for _, c := range list {
				claims[c.ID] = c
			}
		}
	}
	titles := s.polishedNodeTitleOf(r.Context(), taskID)
	out := make([]shepherdFollowupRow, 0, len(sessions))
	for _, x := range sessions {
		row := shepherdFollowupRow{
			ClaimID: x.ClaimID, Round: x.Round, Questions: x.Questions,
			StartedAt: x.StartedAt, LastAt: x.LastAt,
		}
		if c, ok := claims[x.ClaimID]; ok {
			row.Statement, row.Status = c.Title, c.Status
			row.Kind = "claim"
			if c.Type == rings.NodeFact {
				row.Kind = "fact"
			}
			if title := titles[c.ID]; title != "" {
				row.Statement = title
			}
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
		if x.HasConversationInput() {
			lastModel, lastInput = x.Model, x.InputTokens
			info.Model = x.Model
		}
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

// handleShepherdClaimPost 锚定追问发言入队(P4-14 成果;P5-07② 单一
// 追问入口扩到事实——锚定条目为漏洞或事实):一条目一会话、续接不
// 新建;new_round=true 时下一问起新轮(旧轮留档)。会话开场首问由
// 服务端确定性装配链路预载包(漏洞与发现详情同源;事实含陈述/资产
// 锚点/确认时刻),注入模型会话,面板消息流只呈问话原文。问与吩咐
// 同窗:问→牧者读账本作答;吩咐继续挖→牧者落实成动作(急活通道,
// 最高权限操作面既有),全走对话记账。
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
	if err != nil || (node.Type != rings.NodeTestClaim && node.Type != rings.NodeFact) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("条目 %s 不存在", claimID))
		return
	}
	prelude := s.claimFollowupPrelude(r.Context(), taskID, node)
	if node.Type == rings.NodeFact {
		prelude = s.factFollowupPrelude(r.Context(), taskID, node)
	}
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
	title, statement := node.Title, node.Title
	impact, _ := node.Detail["impact"].(string)
	cs, caseErr := report.ClaimCaseOf(ctx, s.Store, s.blobStore(), taskID, node.ID)
	if caseErr == nil && cs.Summary != nil {
		if cs.Summary.Title != "" {
			title = cs.Summary.Title
		}
		if cs.Summary.Statement != "" {
			statement = cs.Summary.Statement
		}
		if cs.Summary.Impact != "" {
			impact = cs.Summary.Impact
		}
	}
	fmt.Fprintf(&b, "【成果】%s:%s\n", node.ID, title)
	if statement != title {
		fmt.Fprintf(&b, "说明:%s\n", statement)
	}
	if impact != "" {
		fmt.Fprintf(&b, "影响面:%s\n", impact)
	}
	if caseErr == nil {
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
	// 落地/终态时刻读卷宗时间线(旧耗时拆解投影已随反证轮耗时链路退役
	// 删除,P5-03③;时刻语义不变)。
	landedAt, terminalAt := "", ""
	if proj, ok, perr := s.Store.ClaimCaseOf(ctx, taskID, node.ID); perr == nil && ok {
		for _, ent := range proj.Timeline {
			switch ent.K {
			case "created":
				landedAt = rings.FmtLocal(ent.At)
			case "state":
				var st struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(ent.P, &st) == nil {
					switch st.Status {
					case rings.TestClaimConfirmed, rings.TestClaimInsufficient, rings.TestClaimRefuted,
						rings.TestClaimFixed, "pending", "testing", "awaiting_round":
						terminalAt = rings.FmtLocal(ent.At)
					}
				}
			}
		}
	}
	if terminalAt != "" {
		// "确认"措辞只给 Confirmed:其余终态按结束时刻如实陈述。
		if node.Status == rings.TestClaimConfirmed {
			fmt.Fprintf(&b, "确认时刻:%s\n", terminalAt)
		} else {
			fmt.Fprintf(&b, "结束时刻:%s\n", terminalAt)
		}
	} else {
		fmt.Fprintf(&b, "结束时刻:进行中(开始 %s)\n", landedAt)
	}
	fmt.Fprintf(&b, "判断当前状态:%s\n", node.Status)
	b.WriteString("应答纪律:围绕该条目接待造物主。造物主问:先查账本(read_findings / read_progress)再答,人话陈述、数字精确,引用图上节点带编号(如 claim:ID、step:ID、fact:ID、asset:ID),造物主可点击核对,账本里没有的不编造。造物主吩咐(如\"继续深入\"\"方向拿账号\"):用 urgent_intent 登记高优先级意图落实成动作,并在答复里说明派发了什么。")
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
// 点击核对面;会话页与后续页面共用)。事实节点带深入按钮禁用态
// (P4-48:成果详情走发现详情端点同款字段)。P4-64 ⑤:节点抽屉定制
// 字段区按类型补发数据面——步骤的执行单位与会话记录、事实的关联
// 资产与文本包装(node_polish)。
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
	view := map[string]any{
		"id": n.ID, "type": string(n.Type), "title": n.Title, "status": n.Status,
		"asset_class": string(n.AssetClass), "detail": n.Detail,
		"created_rev": n.CreatedRev, "updated_rev": n.UpdatedRev,
		// 落页时刻(P5-07③:追问窗事实预载等读侧用;事实=确认时刻,
		// 未确认退入账时刻)。
		"added_at": n.AddedAt, "confirmed_at": n.ConfirmedAt,
	}
	if n.Type == rings.NodeFact || n.Type == rings.NodeStep {
		if np, err := s.Store.NodePolish(r.Context(), taskID); err == nil {
			if p := np[n.ID]; p.Title != "" || p.Statement != "" {
				view["polished_title"], view["polished_statement"] = p.Title, p.Statement
			}
		}
	}
	switch n.Type {
	case rings.NodeFact:
		view["result_types"] = rings.FactResultsOf(*n)
		// 关联资产(about 边直查,带标题)。
		if abouts, aerr := s.Store.Edges(r.Context(), taskID, rings.EdgeAbout); aerr == nil {
			assets := []map[string]string{}
			if ag, gerr := s.Store.AssetGraph(r.Context(), taskID); gerr == nil {
				titles := map[string]string{}
				for _, a := range ag.Assets {
					titles[a.ID] = a.Title
				}
				for _, e := range abouts {
					if e.From != n.ID {
						continue
					}
					assets = append(assets, map[string]string{"id": e.To, "title": titles[e.To]})
				}
			}
			view["assets"] = assets
		}
		// 追问摘要(P5-07②:事实详情追问按钮的数据面,与漏洞详情同源)。
		if s.Shepherd != nil {
			if sessions, serr := s.Shepherd.ClaimQASessions(r.Context(), taskID); serr == nil {
				round, rounds, questions := 0, 0, 0
				for _, x := range sessions {
					if x.ClaimID != n.ID {
						continue
					}
					rounds++
					if x.Round > round {
						round, questions = x.Round, x.Questions
					}
				}
				view["followup"] = map[string]int{"round": round, "rounds": rounds, "questions": questions}
			}
		}
	case rings.NodeStep:
		// 执行单位与会话记录(最近一次单位会话;重派轮各有会话时取最近)。
		if unit, sid, ok, uerr := s.Store.StepLatestSession(r.Context(), taskID, n.ID); uerr == nil && ok {
			view["unit_id"] = unit
			view["transcript_id"] = sid
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// handleNodeEvidence 仅取该任务事实实际引用的证据，不接受任意内容引用。
// 大证据保留在文件层，详情只加载摘要；按需下载完整原文。
func (s *Server) handleNodeEvidence(w http.ResponseWriter, r *http.Request) {
	task, id := r.PathValue("id"), r.PathValue("nid")
	if !s.taskExists(task) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务不存在"))
		return
	}
	n, err := s.Store.Node(r.Context(), task, id)
	if err != nil || n.Type != rings.NodeFact {
		writeErr(w, http.StatusNotFound, fmt.Errorf("事实不存在"))
		return
	}
	download := func(modTime time.Time, content io.ReadSeeker) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "evidence-" + id + ".txt"}))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, "", modTime, content)
	}
	if ref, _ := n.Detail["evidence_ref"].(string); ref != "" {
		path, err := s.blobStore().Path(ref)
		if err != nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("证据原文不可用"))
			return
		}
		file, err := os.Open(path)
		if err != nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("证据原文不可用"))
			return
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("证据读取失败"))
			return
		}
		download(stat.ModTime(), file)
		return
	}
	text, _ := n.Detail["evidence"].(string)
	if text == "" {
		text, _ = n.Detail["evidence_head"].(string)
	}
	if text == "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("未记录证据原文"))
		return
	}
	download(time.Time{}, strings.NewReader(text))
}

// factFollowupPrelude 事实链路预载包(P5-07② 单一追问入口·事实锚定;
// 确定性装配,零 LLM):事实陈述与推断标记、关联资产、发现单位、确认
// 时刻、证据截头,外加与漏洞预载同款的应答纪律。窗内问与吩咐同走
// 牧者——问读账本作答,吩咐继续挖落实成急活动作。
func (s *Server) factFollowupPrelude(ctx context.Context, taskID string, node *rings.NodeFull) string {
	var b strings.Builder
	title, statement := node.Title, node.Title
	if polishes, err := s.Store.NodePolish(ctx, taskID); err == nil {
		if p := polishes[node.ID]; !p.Unpolished {
			if p.Title != "" {
				title = p.Title
			}
			if p.Statement != "" {
				statement = p.Statement
			}
		}
	}
	fmt.Fprintf(&b, "【事实】%s:%s\n", node.ID, title)
	if statement != title {
		fmt.Fprintf(&b, "说明:%s\n", statement)
	}
	b.WriteString("本轮追问锚定以上这一条事实。其他漏洞或事实仅作相关背景，不将它们的成果归给本条；没有直接关系的发现不在答复中展开。\n")
	abouts, err := s.Store.Edges(ctx, taskID, rings.EdgeAbout)
	if err == nil {
		var anchors []string
		assetTitle := map[string]string{}
		if ag, gerr := s.Store.AssetGraph(ctx, taskID); gerr == nil {
			for _, a := range ag.Assets {
				assetTitle[a.ID] = a.Title
			}
		}
		for _, e := range abouts {
			if e.From != node.ID {
				continue
			}
			if t := assetTitle[e.To]; t != "" {
				anchors = append(anchors, fmt.Sprintf("%s(%s)", e.To, t))
			} else {
				anchors = append(anchors, e.To)
			}
		}
		if len(anchors) > 0 {
			fmt.Fprintf(&b, "关联资产:%s\n", strings.Join(anchors, "、"))
		}
	}
	if unit, _ := node.Detail["unit_id"].(string); unit != "" {
		fmt.Fprintf(&b, "发现单位:%s\n", unit)
	}
	if node.ConfirmedAt != "" {
		fmt.Fprintf(&b, "确认时刻:%s\n", node.ConfirmedAt)
	} else {
		fmt.Fprintf(&b, "确认时刻:%s\n", node.AddedAt)
	}
	if head, _ := node.Detail["evidence_head"].(string); head != "" {
		fmt.Fprintf(&b, "证据截头:%s\n", head)
	}
	b.WriteString("应答纪律:围绕该条目接待造物主。造物主问:先查账本(read_findings / read_progress)再答,人话陈述、数字精确,引用图上节点带编号(如 fact:ID、step:ID、claim:ID、asset:ID),造物主可点击核对,账本里没有的不编造。造物主吩咐(如\"继续深入\"\"方向拿账号\"):用 urgent_intent 登记高优先级意图落实成动作,并在答复里说明派发了什么。")
	return b.String()
}
