// Package server 的发现未读红点 API(P4-47 A,问题清单 #62):红点第二
// 类语义——未读类(看过即消),与待办类(处理即消)并列。
//
// 判定:上次看过该子页签之后新达到"已确认"的成果 / 新确认的事实
// (nodes 确认时刻列,与页签展示同口径,见 rings.UnreadConfirms)。
// 成果与事实两个子页签各自独立记已读(键值表每任务各一键,上次看过
// 时刻);任务列表行与任务空间"发现"标签为两键汇总(任一有未读即亮)。
// "看过" = 点进该子页签的时刻(服务端盖戳,跨浏览器一致);其后新增
// (含停留期间)算新未读。
//
//	GET  /api/tasks/{id}/unread → {"claims":n,"facts":n}
//	POST /api/tasks/{id}/seen   {"kind":"claims"|"facts"} → 盖戳并返回最新计数

package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"ghostreaper/internal/runlog"
)

// 已读线键值表键(P4-47 A):每任务成果 / 事实各一键,值为上次看过时刻
// (RFC3339Nano UTC,与 nodes 时刻列同格式)。
const (
	readLineKeyClaims = "read.claims."
	readLineKeyFacts  = "read.facts."
)

// parseReadLine 已读线值 → 时刻;缺失 / 坏值退零值(从未看过,全部
// 已确认计为未读——如实语义)。
func parseReadLine(v string, ok bool) time.Time {
	if !ok || v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// unreadOf 任务未读计数(读两键已读线 + 查已确认节点;任何一步失败按
// 零值如实返回,红点不因读失败点亮)。
func (s *Server) unreadOf(ctx context.Context, taskID string) (claims, facts int) {
	cLine, cOK, _ := s.Store.Setting(ctx, readLineKeyClaims+taskID)
	fLine, fOK, _ := s.Store.Setting(ctx, readLineKeyFacts+taskID)
	claims, facts, err := s.Store.UnreadConfirms(ctx, taskID,
		parseReadLine(cLine.Value, cOK), parseReadLine(fLine.Value, fOK))
	if err != nil {
		runlog.Warnf("未读计数读取失败(任务 %s): %v", taskID, err)
		return 0, 0
	}
	return claims, facts
}

// handleUnread 未读计数(GET /api/tasks/{id}/unread):发现页签两枚
// 红点的数据源;发现 / 任务两主题实时推送驱动刷新(确认落账即变)。
func (s *Server) handleUnread(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	claims, facts := s.unreadOf(r.Context(), taskID)
	writeJSON(w, http.StatusOK, map[string]int{"claims": claims, "facts": facts})
}

// handleSeen 记已读线(POST /api/tasks/{id}/seen):kind=claims|facts,
// 服务端盖"现在"戳(点进子页签的时刻)后返回最新计数(供前端即时
// 灭点)。键值表写入走 SetSettingQuiet(已读线不是配置修改,不进
// "配置修改"日志行防刷屏,与任务草稿同款)。
func (s *Server) handleSeen(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var key string
	switch req.Kind {
	case "claims":
		key = readLineKeyClaims + taskID
	case "facts":
		key = readLineKeyFacts + taskID
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("kind 须为 claims 或 facts"))
		return
	}
	if err := s.Store.SetSettingQuiet(r.Context(), key,
		time.Now().UTC().Format(time.RFC3339Nano), "creator"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 已读线不经账本,live 推送无事件可发——主动标脏 tasks 主题,
	// 任务列表/任务空间标签的未读汇总订阅方即时重拉(轮询连接的
	// 客户端下一拍自然对齐)。
	if s.live != nil {
		s.live.markDirty(liveTopicTasks)
	}
	claims, facts := s.unreadOf(r.Context(), taskID)
	writeJSON(w, http.StatusOK, map[string]int{"claims": claims, "facts": facts})
}
