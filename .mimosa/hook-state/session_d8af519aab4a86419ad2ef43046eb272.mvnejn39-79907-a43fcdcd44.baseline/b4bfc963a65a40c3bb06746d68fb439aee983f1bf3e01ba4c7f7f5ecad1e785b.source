package server

import (
	"errors"
	"net/http"
	"strings"

	"ghostreaper/internal/rings"
)

// 命令面板搜索(P3-10):⌘J 搜索跳转版的数据面——按关键词跨任务搜
// 任务与发现(声明),只做定位:任务命中跳任务空间,声明命中跳所属
// 任务的发现页。GET /api/search?q=<关键词>。

// handleSearch 任务 + 发现跨任务搜索。任务按标题/ID 子串匹配(账本
// 任务量小,内存过滤足够);发现走 nodes 投影 LIKE(EscapeLike 转义)。
// 各封顶 20 条,排序稳定(任务按列表序,声明按任务/创建序)。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, http.StatusBadRequest, errors.New("关键词不能为空"))
		return
	}
	rows, err := s.Store.Tasks(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	needle := strings.ToLower(q)
	type taskHit struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	tasks := make([]taskHit, 0, 8)
	for _, row := range rows {
		if row.Deleted {
			continue
		}
		if strings.Contains(strings.ToLower(row.Title), needle) ||
			strings.Contains(strings.ToLower(row.ID), needle) {
			tasks = append(tasks, taskHit{ID: row.ID, Title: row.Title})
			if len(tasks) >= 20 {
				break
			}
		}
	}
	claims, err := s.Store.SearchClaims(r.Context(), rings.EscapeLike(q), 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type claimHit struct {
		TaskID    string `json:"task_id"`
		ClaimID   string `json:"claim_id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		TaskTitle string `json:"task_title"`
	}
	claimOut := make([]claimHit, 0, len(claims))
	for _, c := range claims {
		claimOut = append(claimOut, claimHit{
			TaskID: c.TaskID, ClaimID: c.ClaimID, Title: c.Title,
			Status: c.Status, TaskTitle: c.TaskTitle,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "claims": claimOut})
}
