// Asset adoption uses ledger revision checks; choices never grant access.
package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ghostreaper/internal/overmind"

	"ghostreaper/internal/runlog"
)

// handleApprovalBatch applies independent inventory choices, one ledger decision per item.
func (s *Server) handleApprovalBatch(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	if s.taskHibernated(taskID) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中,审批单已冻结为只读(唤醒后恢复可审批)"))
		return
	}
	var req struct {
		IDs      []string `json:"ids"`
		Approved *bool    `json:"approved"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Approved == nil {
		writeErr(w, http.StatusBadRequest, errors.New("审批结果必须是布尔值"))
		return
	}
	ids := make([]string, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("勾选清单为空(至少勾选一张资产加入请求)"))
		return
	}
	ctx := r.Context()

	type itemFailure struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	handled := 0
	failed := []itemFailure{}
	for _, rid := range ids {
		var err error
		if strings.HasPrefix(rid, overmind.AssetProposalPrefix) {
			_, err = overmind.DecideAssetProposal(ctx, s.Store, taskID, rid, *req.Approved, "")
		} else {
			err = errors.New("只能批量处理资产加入请求")
		}

		if err != nil {
			failed = append(failed, itemFailure{ID: rid, Error: err.Error()})
			continue
		}
		handled++
	}
	verb := "不加入"
	if *req.Approved {
		verb = "加入资产"
	}
	if handled > 0 && *req.Approved {
		s.wake(taskID)
		runlog.Infof("资产加入请求批量处理: %s %s %d 张(失败 %d 张)", taskID, verb, handled, len(failed))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"handled": handled, "failed": failed,
	})
}
