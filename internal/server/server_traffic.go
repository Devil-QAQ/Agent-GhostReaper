// Package server 的流量页与证据绑定 API(P3-02,8.3 一级·流量 + 第五章
// 流量证据绑定三机制):镜留痕的跨任务列表(任务/host/方法/状态/类型/
// 时间筛选)、单条交换的元数据与分段原文回放(blob 惰性读,大正文分段
// 下发)、TestClaim ↔ 流量证据绑定/补绑(面板操作入账,R7 无旁路)。
//
// 路由:
//
//	GET  /api/mirror/exchanges                     流量列表(筛选见参数)
//	GET  /api/mirror/exchanges/{exID}?task=        单条交换元数据(无正文)
//	GET  /api/mirror/exchanges/{exID}/raw?task=    原文分段回放 ?offset=&limit=
//	POST /api/tasks/{id}/claims/{cid}/evidence     绑定/补绑流量证据
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// 流量原文分段口径:默认段 64KiB,单段上限 1MiB(16MB 正文最多 256 段,
// 分段读原文——大正文不一次下发)。
const (
	rawSegDefault = 64 << 10
	rawSegMax     = 1 << 20
)

// handleTrafficList 流量页列表:镜留痕摘要(账本事件),原文不随行
// (R18:原文在 blob,详情按交换 ID 惰性回放)。跨任务列举是 Manager
// 视角的合法读(同 Tasks)。
func (s *Server) handleTrafficList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := rings.TrafficFilter{
		TaskID: strings.TrimSpace(q.Get("task")),
		Host:   strings.TrimSpace(q.Get("host")),
		Method: strings.TrimSpace(q.Get("method")),
		Type:   strings.TrimSpace(q.Get("type")),
	}
	if f.TaskID != "" && !s.taskExists(f.TaskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", f.TaskID))
		return
	}
	switch f.Type {
	case "", rings.MirrorRecHTTP, rings.MirrorRecHTTPS, rings.MirrorRecTunnel:
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("类型 %q 不在词表内(http|https|tunnel)", f.Type))
		return
	}
	if sc := strings.TrimSpace(q.Get("status")); sc != "" {
		n, err := strconv.Atoi(sc)
		if err != nil || n < 2 || n > 5 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("状态筛选 %q 非法(2~5 对应 2xx~5xx)", sc))
			return
		}
		f.StatusClass = n
	}
	var timeErr error
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		if f.Since, timeErr = parseTimeParam(v); timeErr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("起始时间非法(须 RFC3339): %v", timeErr))
			return
		}
	}
	if v := strings.TrimSpace(q.Get("until")); v != "" {
		if f.Until, timeErr = parseTimeParam(v); timeErr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("截止时间非法(须 RFC3339): %v", timeErr))
			return
		}
	}
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > rings.TrafficFilterLimitMax {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("limit 须为 1~%d", rings.TrafficFilterLimitMax))
			return
		}
		f.Limit = n
	}
	// P3-05c ③ 标准分页:page(1 起)按每页条数折算 offset;offset
	// 参数保留(既有调用兼容),page 优先。
	page := 1
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, errors.New("page 须为正整数(1 起)"))
			return
		}
		page = n
		f.Offset = (page - 1) * f.Limit
	} else if v := strings.TrimSpace(q.Get("offset")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("offset 须为非负整数"))
			return
		}
		f.Offset = n
	}
	rows, total, err := s.Store.TrafficRows(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type rowJSON struct {
		TaskID       string `json:"task_id"`
		ExchangeID   string `json:"exchange_id"`
		Type         string `json:"type"`
		Method       string `json:"method,omitempty"`
		URL          string `json:"url,omitempty"`
		Host         string `json:"host,omitempty"`
		Status       int    `json:"status,omitempty"`
		ReqBytes     int64  `json:"req_bytes,omitempty"`
		RespBytes    int64  `json:"resp_bytes,omitempty"`
		ElapsedMS    int64  `json:"elapsed_ms,omitempty"`
		TLS          string `json:"tls,omitempty"`
		Error        string `json:"error,omitempty"`
		StepID       string `json:"step_id"`
		UnitID       string `json:"unit_id"`
		TranscriptID string `json:"transcript_id,omitempty"`
		HasBlob      bool   `json:"has_blob"` // http/https 有原文可回放;tunnel 仅元数据
		StartedAt    string `json:"started_at"`
	}
	out := make([]rowJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowJSON{
			TaskID: row.TaskID, ExchangeID: row.ExchangeID, Type: row.Type,
			Method: row.Method, URL: row.URL, Host: row.Host, Status: row.Status,
			ReqBytes: row.ReqBytes, RespBytes: row.RespBytes, ElapsedMS: row.ElapsedMS,
			TLS: row.TLS, Error: row.Error, StepID: row.StepID, UnitID: row.UnitID,
			TranscriptID: row.TranscriptID, HasBlob: row.BlobRef != "",
			StartedAt: row.OccurredAt.Local().Format("2006-01-02 15:04:05"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": out, "count": len(out),
		"total": total, "page": page, "page_size": f.Limit,
	})
}

// parseTimeParam 时间筛选参数(RFC3339,面板侧 datetime-local 转 ISO)。
func parseTimeParam(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

// mirrorExchange 定位任务内一条镜留痕:账本摘要 + blob 原文文档
// (http/https;tunnel 无原文)。交换 ID 任务内定位(会话前缀随机,
// 跨任务理论可撞,任务内唯一)。P4-58:按 kind 定向直查 mirror.record,
// 不再全量扫事件流。
func (s *Server) mirrorExchange(ctx context.Context, taskID, exID string) (*rings.MirrorRecord, *mirror.Exchange, time.Time, error) {
	evs, err := s.Store.EventsByKinds(ctx, taskID, rings.EventMirrorRecord)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	var rec *rings.MirrorRecord
	var at time.Time
	for i := range evs {
		if evs[i].Kind != rings.EventMirrorRecord {
			continue
		}
		var m rings.MirrorRecord
		if json.Unmarshal(evs[i].Payload, &m) != nil || m.ExchangeID != exID {
			continue
		}
		rec, at = &m, evs[i].OccurredAt
		break
	}
	if rec == nil {
		// P4-62 C(b):组包复现步骤的交换文档(工具调用记录重构,"ta-"
		// 前缀)不在镜事件流——按定稿步骤的编号回退取回,与镜交换同一
		// 渲染通道(展示与操作同源)。
		if st, ok := s.reproToolStep(ctx, taskID, exID); ok {
			return st.record(), st.exchange(s.blobStore()), st.at, nil
		}
		return nil, nil, time.Time{}, fmt.Errorf("流量记录 %s 不在任务 %s 留痕内", exID, taskID)
	}
	var ex mirror.Exchange
	if rec.BlobRef != "" {
		doc, err := s.blobStore().Read(rec.BlobRef)
		if err != nil {
			return rec, nil, at, fmt.Errorf("读交换原文 %s: %w", rec.BlobRef, err)
		}
		if err := json.Unmarshal(doc, &ex); err != nil {
			return rec, nil, at, fmt.Errorf("解码交换原文 %s: %w", rec.BlobRef, err)
		}
	}
	return rec, &ex, at, nil
}

// reproToolStep 定稿步骤里的组包交换(P4-62 C(b)):扫任务的 repro
// 记录,按 ExchangeID 命中 source=tool 的步骤(P4-35 后记录 KB 级,
// 定稿记录有限,定向读不触全量事件)。读不到返回 ok=false(按不存在
// 处理,调用方如实 404)。
func (s *Server) reproToolStep(ctx context.Context, taskID, exID string) (*reproToolStepRef, bool) {
	evs, err := s.Store.EventsByKinds(ctx, taskID, rings.EventTestClaimRecord)
	if err != nil {
		return nil, false
	}
	for i := range evs {
		var r rings.TestClaimRecord
		if json.Unmarshal(evs[i].Payload, &r) != nil || r.Type != rings.TestClaimRecRepro {
			continue
		}
		for _, st := range r.Steps {
			if st.ExchangeID == exID && st.Source == "tool" {
				return &reproToolStepRef{step: st, at: evs[i].OccurredAt}, true
			}
		}
	}
	return nil, false
}

// reproToolStepRef 组包步骤的回放引用。
type reproToolStepRef struct {
	step rings.ReproStep
	at   time.Time
}

// record 镜摘要形态(元数据条口径;Type http——组包文档本身是 http
// 请求包形态,与镜交换同渲染)。
func (ref *reproToolStepRef) record() *rings.MirrorRecord {
	return &rings.MirrorRecord{
		ExchangeID: ref.step.ExchangeID, Type: rings.MirrorRecHTTP,
		StepID: ref.step.StepID, UnitID: ref.step.UnitID,
		Method: ref.step.Method, URL: ref.step.URL, Status: ref.step.Status,
		BlobRef: ref.step.BlobRef,
	}
}

// exchange blob 交换文档(读不到如实 nil:调用方按无原文处理)。
func (ref *reproToolStepRef) exchange(blobs *swarm.BlobStore) *mirror.Exchange {
	if blobs == nil || ref.step.BlobRef == "" {
		return nil
	}
	doc, err := blobs.Read(ref.step.BlobRef)
	if err != nil {
		return nil
	}
	var ex mirror.Exchange
	if json.Unmarshal(doc, &ex) != nil {
		return nil
	}
	return &ex
}

// handleTrafficDetail 单条交换元数据:请求/响应行与头 + 正文尺寸(正文
// 不随行,分段回放走 /raw)。https 附目标证书校验事实(P3-01 如实标注)。
func (s *Server) handleTrafficDetail(w http.ResponseWriter, r *http.Request) {
	exID := r.PathValue("exID")
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" || !s.taskExists(taskID) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("须以 ?task= 指定流量记录所属任务"))
		return
	}
	rec, ex, at, err := s.mirrorExchange(r.Context(), taskID, exID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	type reqJSON struct {
		Method        string          `json:"method,omitempty"`
		URL           string          `json:"url,omitempty"`
		Proto         string          `json:"proto,omitempty"`
		Host          string          `json:"host,omitempty"`
		Headers       []mirror.Header `json:"headers,omitempty"`
		BodySize      int             `json:"body_size"`
		BodyTruncated bool            `json:"body_truncated,omitempty"`
	}
	type respJSON struct {
		Proto         string          `json:"proto,omitempty"`
		Status        int             `json:"status,omitempty"`
		StatusText    string          `json:"status_text,omitempty"`
		Headers       []mirror.Header `json:"headers,omitempty"`
		BodySize      int             `json:"body_size"`
		BodyTruncated bool            `json:"body_truncated,omitempty"`
	}
	v := struct {
		TaskID       string   `json:"task_id"`
		ID           string   `json:"id"`
		Type         string   `json:"type"`
		TLS          string   `json:"tls,omitempty"`
		TLSLabel     string   `json:"tls_label,omitempty"`
		TLSSubject   string   `json:"tls_subject,omitempty"`
		TLSIssuer    string   `json:"tls_issuer,omitempty"`
		TLSVerifyErr string   `json:"tls_verify_error,omitempty"`
		Error        string   `json:"error,omitempty"`
		StartedAt    string   `json:"started_at"`
		ElapsedMS    int64    `json:"elapsed_ms,omitempty"`
		StepID       string   `json:"step_id"`
		UnitID       string   `json:"unit_id"`
		TranscriptID string   `json:"transcript_id,omitempty"`
		BlobRef      string   `json:"blob_ref,omitempty"`
		RawTotal     int      `json:"raw_total"`
		Request      reqJSON  `json:"request,omitempty"`
		Response     respJSON `json:"response,omitempty"`
	}{
		TaskID: taskID, ID: exID, Type: rec.Type,
		Error: rec.Error, StepID: rec.StepID, UnitID: rec.UnitID,
		TranscriptID: rec.TranscriptID, BlobRef: rec.BlobRef,
		StartedAt: at.Local().Format("2006-01-02 15:04:05"), ElapsedMS: rec.ElapsedMS,
	}
	if ex.TLS != nil {
		v.TLS = ex.TLS.Validity
		v.TLSLabel = mirror.TLSLabel(ex.TLS.Validity)
		v.TLSSubject, v.TLSIssuer, v.TLSVerifyErr = ex.TLS.PeerSubject, ex.TLS.PeerIssuer, ex.TLS.VerifyError
	}
	if ex.Request != nil {
		v.Request = reqJSON{
			Method: ex.Request.Method, URL: ex.Request.URL, Proto: ex.Request.Proto,
			Host: ex.Request.Host, Headers: ex.Request.Headers,
			BodySize: len(ex.Request.Body), BodyTruncated: ex.Request.BodyTruncated,
		}
		v.RawTotal = len(report.ExchangeRaw(ex))
	}
	if ex.Response != nil {
		v.Response = respJSON{
			Proto: ex.Response.Proto, Status: ex.Response.Status, StatusText: ex.Response.StatusText,
			Headers:  ex.Response.Headers,
			BodySize: len(ex.Response.Body), BodyTruncated: ex.Response.BodyTruncated,
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// handleTrafficRaw 原文分段回放:Burp 风格原文(report 同一装配)按
// offset/limit 分段下发。?side=req|resp 单侧视图(请求包/返回包分开展
// 示,审核迭代);缺省 = 全量(请求+响应)。段拼接 == 该侧全文,全文与
// blob 文档字段逐字一致(装配确定性)。
func (s *Server) handleTrafficRaw(w http.ResponseWriter, r *http.Request) {
	exID := r.PathValue("exID")
	taskID := strings.TrimSpace(r.URL.Query().Get("task"))
	if taskID == "" || !s.taskExists(taskID) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("须以 ?task= 指定流量记录所属任务"))
		return
	}
	rec, ex, _, err := s.mirrorExchange(r.Context(), taskID, exID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if rec.Type == rings.MirrorRecTunnel || rec.BlobRef == "" {
		writeErr(w, http.StatusBadRequest, errors.New("该流量记录无原文(隧道仅元数据留痕)"))
		return
	}
	side := strings.TrimSpace(r.URL.Query().Get("side"))
	var full string
	switch side {
	case "":
		full = report.ExchangeRaw(ex)
	case "req", "resp":
		full = report.ExchangeRawSide(ex, side)
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("side %q 非法(req|resp,缺省=全量)", side))
		return
	}
	offset, limit := 0, rawSegDefault
	if v := strings.TrimSpace(r.URL.Query().Get("offset")); v != "" {
		if offset, err = strconv.Atoi(v); err != nil || offset < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("offset 须为非负整数"))
			return
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > rawSegMax {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("limit 须为 1~%d", rawSegMax))
			return
		}
	}
	if offset > len(full) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("offset %d 超出原文长度 %d", offset, len(full)))
		return
	}
	end := offset + limit
	if end > len(full) {
		end = len(full)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text": full[offset:end], "offset": offset, "total": len(full),
		"has_more": end < len(full), "side": side,
	})
}

// handleClaimEvidenceBind 绑定/补绑流量证据(POST):声明与镜交换的显式
// 关联入账(testclaim.record evidence)。绑定与补绑同型——声明已有证据
// 再绑即补绑,读取侧按事件累计。校验(声明存在、交换在本任务留痕内且
// 有原文)由账本受控入口物理强制;通过后返回声明与任务的证据版本。
func (s *Server) handleClaimEvidenceBind(w http.ResponseWriter, r *http.Request) {
	taskID, claimID := r.PathValue("id"), r.PathValue("cid")
	if !s.taskExists(taskID) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("任务 %s 不存在", taskID))
		return
	}
	var req struct {
		ExchangeIDs []string `json:"exchange_ids"`
		Note        string   `json:"note"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ids := make([]string, 0, len(req.ExchangeIDs))
	for _, id := range req.ExchangeIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("绑定的流量记录清单不能为空"))
		return
	}
	rec := rings.TestClaimRecord{
		ClaimID: claimID, Type: rings.TestClaimRecEvidence,
		Exchanges: ids, Note: strings.TrimSpace(req.Note), By: "creator",
	}
	if _, err := s.Store.Append(r.Context(), taskID, rings.Event{
		Kind: rings.EventTestClaimRecord, Actor: "creator", Payload: rec,
	}); err != nil {
		// 交换不在留痕内/隧道无原文/声明不存在等受控入口校验原样透出。
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	perClaim, total, err := s.Store.EvidenceVersions(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"claim_id": claimID, "bound": len(ids),
		"evidence_version": perClaim[claimID], "task_evidence_version": total,
	})
}
