// Package server 的任务范围端点(P4-44 ③,问题清单 #61):渗透任务的
// 待批资产批量放行/忽略。写路径与 RoE 面板同源——都走 roe.updated 事件
// (放行进 scope_approved 清单、忽略进 excluded_assets 清单),面板/牧者/
// 主宰读同一账本投影,无旁路;放行后任务包闭包在下一批单位领取时纳入
// 该资产(在跑单位按领取时冻结值跑完),主宰被唤醒重规划。
package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ghostreaper/internal/rings"
)

// handleScopeApprove POST /api/tasks/{id}/scope/approve {assets:[资产ID]}
// 批量放行待批资产:清单并入 scope_approved(整体快照语义,其余字段
// 透传现值),审计 note 自动备注放行了什么;唤醒主宰(放行的资产转入
// 范围内,下一轮规划可派发)。
func (s *Server) handleScopeApprove(w http.ResponseWriter, r *http.Request) {
	s.handleScopeBatch(w, r, true)
}

// handleScopeIgnore POST /api/tasks/{id}/scope/ignore {assets:[资产ID]}
// 批量忽略待批资产:清单并入授权范围摘除清单(excluded_assets)——
// 忽略=明确不测,离开待批分组与红点;RoE 面板纳入(摘除清单去掉该
// 资产)可回到待批。其余快照字段透传现值。
func (s *Server) handleScopeIgnore(w http.ResponseWriter, r *http.Request) {
	s.handleScopeBatch(w, r, false)
}

// handleScopeBatch 放行/忽略共用体:ids 须全部是本任务当前待批资产
// (非待批条目如实拒收,不静默混入);红队/未标任务无待批语义,人话
// 拒绝。写入即唤醒主宰。
func (s *Server) handleScopeBatch(w http.ResponseWriter, r *http.Request, approve bool) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		Assets []string `json:"assets"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	pending, err := s.Store.PendingScopeAssets(ctx, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("该任务没有待批资产(红队任务与未标任务的发现资产直接可测)"))
		return
	}
	pendingSet := map[string]bool{}
	for _, p := range pending {
		pendingSet[p.ID] = true
	}
	ids := make([]string, 0, len(req.Assets))
	for _, id := range req.Assets {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !pendingSet[id] {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("资产 %s 不是待批资产(可能已放行或已忽略)", id))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("资产清单为空(至少勾选一项)"))
		return
	}
	st, err := s.Store.CurrentRoE(ctx, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 标题解析(审计备注人话可读)。
	title := map[string]string{}
	for _, p := range pending {
		title[p.ID] = p.Title
	}
	var parts []string
	for _, id := range ids {
		if t := title[id]; t != "" {
			parts = append(parts, t)
			continue
		}
		parts = append(parts, id)
	}
	verb := "忽略"
	if approve {
		verb = "放行"
	}
	note := fmt.Sprintf("%s %s", verb, strings.Join(parts, "、"))
	next := rings.RoeUpdated{
		AllowedTools:      st.AllowedTools,
		DeniedTools:       st.DeniedTools,
		ApprovalThreshold: st.ApprovalThreshold,
		IntentRules:       st.IntentRules,
		ExcludedAssets:    st.ExcludedAssets,
		ScopeApproved:     st.ScopeApproved,
		Note:              note,
	}
	if approve {
		next.ScopeApproved = mergeIDs(st.ScopeApproved, ids)
	} else {
		next.ExcludedAssets = mergeIDs(st.ExcludedAssets, ids)
	}
	if _, err := s.Store.Append(ctx, taskID, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator", Payload: next,
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.wake(taskID)
	remain := 0
	if pending, err := s.Store.PendingScopeAssets(ctx, taskID); err == nil {
		remain = len(pending)
	}
	writeJSON(w, http.StatusOK, map[string]any{"handled": len(ids), "pending": remain})
}

// mergeIDs 清单并集(去重保序;快照语义下的"并入"辅助)。
func mergeIDs(base, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(add))
	for _, id := range base {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range add {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
