package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"ghostreaper/internal/overmind"
)

func (s *Server) handleAssetAdoption(w http.ResponseWriter, r *http.Request, task, rid string, added bool, reason string) {
	if !s.taskExists(task) {
		writeErr(w, http.StatusNotFound, errors.New("任务不存在"))
		return
	}
	if s.taskHibernated(task) {
		writeErr(w, http.StatusConflict, errors.New("任务冬眠中，资产加入请求只读"))
		return
	}
	target, err := overmind.DecideAssetProposal(r.Context(), s.Store, task, rid, added, reason)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, overmind.ErrAssetProposalDecided) {
			status = http.StatusConflict
		}
		writeErr(w, status, err)
		return
	}
	if added {
		s.wake(task)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rid, "added": added, "target": target})
}

func (s *Server) assetProposalViews(r *http.Request, task string) ([]approvalView, error) {
	rows, err := s.Store.PendingAssetProposals(r.Context(), task)
	if err != nil {
		return nil, err
	}
	out := make([]approvalView, 0, len(rows))
	for _, row := range rows {
		var p overmind.AssetProposal
		if json.Unmarshal([]byte(row.ToolArgs), &p) != nil {
			return nil, errors.New("资产加入请求内容无效")
		}
		out = append(out, approvalView{ID: row.RequestID, Kind: "asset", Target: p.Title, UnitID: row.UnitID,
			Intent: row.Intent, Note: row.Note, CreatedAt: row.OccurredAt})
	}
	return out, nil
}
