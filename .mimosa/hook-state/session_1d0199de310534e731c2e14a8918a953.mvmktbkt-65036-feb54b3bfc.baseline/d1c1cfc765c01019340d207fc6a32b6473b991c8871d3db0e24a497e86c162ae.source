// Package server 的会话管理 API(P3-13b ②,子计划「会话管理」):所有
// 会话支持重命名 + 备注(默认名自动)+ 置顶,面板操作入账留痕
// (session.meta 事件,R7 无旁路)。会话锚 = 转录 ID(虫群单位 swarm-* /
// 牧者 shepherd-*)或稳定键(主宰面板会话 overmind);写入口受 rings
// 白名单校验,读侧 SessionLabels 投影(虫群单位列表 / 牧者 / 主宰三处
// 会话视图同源携带)。
package server

import (
	"fmt"
	"net/http"

	"ghostreaper/internal/rings"
)

// sessionMetaView 会话组织信息的面板视图(默认名外的自定义信息;
// 零值 = 全默认,如实呈现)。
type sessionMetaView struct {
	Title  string `json:"title,omitempty"`  // 自定义名称(空 = 默认名)
	Note   string `json:"note,omitempty"`   // 备注
	Pinned bool   `json:"pinned,omitempty"` // 置顶(会话列表内靠前)
}

// sessionMetaOf 单个会话的标签视图(标签表缺项 = 零值)。
func sessionMetaOf(labels map[string]rings.SessionLabel, sid string) sessionMetaView {
	l := labels[sid]
	return sessionMetaView{Title: l.Title, Note: l.Note, Pinned: l.Pinned}
}

// handleSessionMeta 会话标记写(POST /api/tasks/{id}/sessions/{sid}/meta):
// 重命名 / 备注 / 置顶一次提交(字段缺省 = 不改;名称与备注传空串 =
// 清除回默认)。写路径与读路径同源:入账 session.meta,读视图经
// SessionLabels 投影(面板轮询自然刷新)。
func (s *Server) handleSessionMeta(w http.ResponseWriter, r *http.Request) {
	taskID, sid := r.PathValue("id"), r.PathValue("sid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Title  *string `json:"title"`
		Note   *string `json:"note"`
		Pinned *bool   `json:"pinned"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	payload := rings.SessionMeta{SessionID: sid, Title: req.Title, Note: req.Note, Pinned: req.Pinned}
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind: rings.EventSessionMeta, Actor: "creator", Payload: payload,
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	labels, err := s.Store.SessionLabels(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionMetaOf(labels, sid))
}
