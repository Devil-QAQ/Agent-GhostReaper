package server

import (
	"errors"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/swarm"
	"net/http"
)

func (s *Server) handleApprovalAutomation(w http.ResponseWriter, r *http.Request) {
	task := r.PathValue("id")
	if !s.taskExists(task) {
		writeErr(w, 404, errors.New("任务不存在"))
		return
	}
	if s.taskHibernated(task) {
		writeErr(w, 409, errors.New("任务冬眠中，唤醒后再修改自动处理"))
		return
	}
	var req struct {
		AutoApprove *bool `json:"auto_approve"`
		AutoJoin    *bool `json:"auto_join"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.AutoApprove == nil && req.AutoJoin == nil {
		writeErr(w, 400, errors.New("未指定自动处理开关"))
		return
	}
	defer func() {
		if s.live != nil {
			s.live.markDirty(liveTopicApproval + task)
		}
	}()
	for flag, value := range map[string]*bool{"auto_approve": req.AutoApprove, "auto_join": req.AutoJoin} {
		if value != nil {
			if err := s.Store.SetApprovalAutomationFlag(r.Context(), task, flag, *value); err != nil {
				writeErr(w, 500, errors.New("自动处理设置未保存"))
				return
			}
		}
	}
	actions, err := swarm.AutoApprovePending(r.Context(), s.Store, s.Gate, task)
	if err != nil {
		writeErr(w, 500, errors.New("设置已保存，部分待决动作未处理"))
		return
	}
	assets, err := overmind.AutoJoinPending(r.Context(), s.Store, task)
	if err != nil {
		writeErr(w, 500, errors.New("设置已保存，部分待加入资产未处理"))
		return
	}
	options, err := s.Store.ApprovalAutomation(r.Context(), task)
	if err != nil {
		writeErr(w, 500, errors.New("无法读取自动处理设置"))
		return
	}
	s.wake(task)
	writeJSON(w, 200, map[string]any{"automation": options, "actions_handled": actions, "assets_handled": assets})
}
