// Package server 的报告导出 API(P1-10 预览 + P2-13 格式矩阵/漏洞级/
// 跨任务 + P3-02 报告存档):导出全部为 report 包确定性组装的只读下载,
// 零 LLM(R17);存档是唯一写路径——渲染原文入 blob(R18)、登记入账本,
// 证据版本乐观锁(旧版本写入 409)。
//
// 路由:
//
//	GET /api/tasks/{id}/report               任务报告 md(面板预览,同旧)
//	GET /api/tasks/{id}/report/export        任务级格式矩阵
//	  ?format=md|zip|csv|json & claims=a,b(缺省=全部;"当前筛选/勾选项"
//	  由面板解析成清单传入)
//	GET /api/tasks/{id}/claims/{cid}/export  漏洞级导出 ?format=md|zip
//	  (zip 附关联流量证据包)
//	GET /api/report/export                   跨任务导出(一级·报告页)
//	  ?tasks=a,b,c & format=md|zip|csv|json
//	GET /api/tasks/{id}/report/archives      存档清单 + 当前证据版本(P3-02)
//	POST /api/tasks/{id}/report/archive      存档写入 ?evidence_version 乐观锁
//	GET /api/tasks/{id}/report/archives/{rev} 存档原文下载

package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"ghostreaper/internal/report"
	"ghostreaper/internal/swarm"
)

// blobStore 镜留痕内容寻址存储(发现原文回放与证据包共用)。
func (s *Server) blobStore() *swarm.BlobStore {
	return swarm.NewBlobStore(filepath.Join(s.Manager.RootDir(), "blobs"))
}

// handleReport 任务报告预览 API:确定性组装的 Markdown(零 LLM,R17),
// 相同账本状态重复请求字节级一致。
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	md, err := report.Render(r.Context(), s.Store, s.blobStore(), taskID, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(md))
}

// handleTaskReportExport 任务级导出(格式矩阵 × 发现范围)。
func (s *Server) handleTaskReportExport(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	format := r.URL.Query().Get("format")
	out, err := report.TaskExport(r.Context(), s.Store, s.blobStore(), taskID, format, claimScope(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeDownload(w, out)
}

// handleClaimExport 漏洞级导出:单份发现报告(md)或含流量证据包(zip)。
func (s *Server) handleClaimExport(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	format := r.URL.Query().Get("format")
	out, err := report.ClaimExport(r.Context(), s.Store, s.blobStore(), taskID, claimID, format)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeDownload(w, out)
}

// handleCrossReportExport 跨任务导出(一级导航·报告页)。
func (s *Server) handleCrossReportExport(w http.ResponseWriter, r *http.Request) {
	var taskIDs []string
	for _, id := range strings.Split(r.URL.Query().Get("tasks"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			if !s.taskExists(id) {
				writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", id))
				return
			}
			taskIDs = append(taskIDs, id)
		}
	}
	format := r.URL.Query().Get("format")
	out, err := report.CrossTaskExport(r.Context(), s.Store, s.blobStore(), format, taskIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeDownload(w, out)
}

// handleReportArchives 存档清单(P3-02):report.record 事件流倒序 +
// 当前任务证据版本;Stale 由版本差推导——补绑/收割定稿后旧存档自动
// 呈"待更新",无人工改档。
func (s *Server) handleReportArchives(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	list, current, err := report.Archives(r.Context(), s.Store, taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []report.ArchiveView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"archives": list, "current_evidence_version": current,
	})
}

// handleReportArchive 存档写入(P3-02):提交体携带客户端所见的任务证据
// 版本,与当前不符即 409(乐观锁——证据更新后的旧版本写入不落库)。
// 通过则确定性渲染全文、原文入 blob、登记入账本。
func (s *Server) handleReportArchive(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		EvidenceVersion *int64 `json:"evidence_version"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.EvidenceVersion == nil {
		writeErr(w, http.StatusBadRequest, errors.New("须携带 evidence_version(面板所见的当前证据版本)"))
		return
	}
	view, err := report.ArchiveTask(r.Context(), s.Store, s.blobStore(), taskID, *req.EvidenceVersion, "creator")
	if err != nil {
		var stale *report.ErrEvidenceStale
		if errors.As(err, &stale) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":                      stale.Error(),
				"current_evidence_version":   stale.Current,
				"submitted_evidence_version": stale.Submitted,
			})
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleReportArchiveDownload 存档原文下载(登记 rev 定位 → blob 回放,
// 字节级)。
func (s *Server) handleReportArchiveDownload(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	rev, err := strconv.ParseInt(r.PathValue("rev"), 10, 64)
	if err != nil || rev <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("存档版本号非法"))
		return
	}
	rec, err := report.ArchiveByRev(r.Context(), s.Store, taskID, rev)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	body, err := report.ArchiveBody(s.blobStore(), rec.Ref)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeDownload(w, &report.Exported{
		Body:     body,
		Filename: fmt.Sprintf("report-%s-r%d.%s", taskID, rev, rec.Format),
		MimeType: "text/markdown; charset=utf-8",
	})
}

// claimScope ?claims=a,b → 声明 ID 清单(空 = 全部发现)。
func claimScope(r *http.Request) []string {
	raw := strings.TrimSpace(r.URL.Query().Get("claims"))
	if raw == "" || raw == "all" {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// writeDownload 导出产物下载(文件名规约由 report 定,Disposition 携带)。
func writeDownload(w http.ResponseWriter, out *report.Exported) {
	w.Header().Set("Content-Type", out.MimeType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=\"%s\"", sanitizeFilename(out.Filename)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Body)
}

// sanitizeFilename Disposition 文件名只留安全字符(ID 体系为受控短串,
// 此处兜底防注入)。
func sanitizeFilename(name string) string {
	return strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			return c
		case c == '-', c == '.', c == '_':
			return c
		default:
			return -1
		}
	}, name)
}
