// Package server 的牧者会话面板 API(P2-07,第四章 §1 牧者 + 8.5
// Agent 会话页):对话流读写(消息带送达状态)、会话信息条数据
// (模型 / token 入出缓存 / 上下文消耗比例 / 时长,与 token 独立表
// 同源对账)、图上节点详情(fact# 等引用可点击核对)。
package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"ghostreaper/internal/failover"
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
		Info             shepherdInfoBar `json:"info"`
		TaskStatus       string          `json:"task_status"`
		Hibernated       bool            `json:"hibernated"`
		HibernateSec     float64         `json:"hibernate_seconds"` // P4-02 ④ 时长冻结口径的冬眠抵扣(含进行中)
		PendingApprovals int             `json:"pending_approvals"`
		Meta             sessionMetaView `json:"meta"`
	}{
		View: view, Info: s.shepherdInfo(r, taskID),
		TaskStatus: taskStatus, Hibernated: s.taskHibernated(taskID),
		HibernateSec:     s.Store.HibernateDeduct(r.Context(), taskID, time.Now()).Seconds(),
		PendingApprovals: len(s.Gate.Pending(taskID)),
		Meta:             meta,
	}
	writeJSON(w, http.StatusOK, out)
}

// shepherdInfo 从 token 独立表与模型库装配信息条(纯读,零额外采集)。
func (s *Server) shepherdInfo(r *http.Request, taskID string) shepherdInfoBar {
	var info shepherdInfoBar
	rows, err := s.Store.TokenUsageRows(r.Context(), taskID)
	if err != nil {
		return info
	}
	sid := shepherd.SessionID(taskID)
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
