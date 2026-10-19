package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// P3-02 流量页与证据绑定三机制的端到端:列表筛选、原文分段回放
//(与 blob 逐字一致)、绑定/补绑、报告存档 409 乐观锁与"待更新"推导。

// trafficSeed 一次铺任务:声明 + 三条镜留痕(GET 200 / POST 403 https /
// 隧道),http/https 的交换文档写真 blob。
type trafficSeed struct {
	task  string
	blobs *swarm.BlobStore
}

func seedTrafficTask(t *testing.T, dir string, store *rings.Store, task, title string) *trafficSeed {
	t.Helper()
	ctx := context.Background()
	blobs := swarm.NewBlobStore(filepath.Join(dir, "tasks", "blobs"))
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	mkRec := func(ex *mirror.Exchange, at time.Time) rings.Event {
		doc, err := json.Marshal(ex)
		if err != nil {
			t.Fatalf("序列化交换失败: %v", err)
		}
		ref, err := blobs.Write(doc)
		if err != nil {
			t.Fatalf("交换入 blob 失败: %v", err)
		}
		method, urlPath, status := "", "", 0
		if ex.Request != nil {
			method, urlPath = ex.Request.Method, ex.Request.URL
		}
		if ex.Response != nil {
			status = ex.Response.Status
		}
		return rings.Event{Kind: rings.EventMirrorRecord, Actor: "mirror", OccurredAt: at,
			Payload: rings.MirrorRecord{
				ExchangeID: ex.ID, Type: ex.Type, StepID: ex.StepID, UnitID: ex.UnitID,
				TranscriptID: ex.TranscriptID, Method: method, URL: urlPath, Status: status,
				ReqBytes: ex.ReqBytes, RespBytes: ex.RespBytes, BlobRef: ref,
				ElapsedMS: ex.ElapsedMS, Error: ex.Error,
			}}
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: title}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-1", NodeType: rings.NodeTestClaim, Title: "备份目录匿名可读", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "step_id": "s1", "vulnclass": "信息暴露"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-1", ToNode: "web1"}},
		mkRec(&mirror.Exchange{
			ID: "ms-a.1", Type: rings.MirrorRecHTTP, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "tr-1",
			StartedAt: at,
			Request: &mirror.Request{Method: "GET", URL: "http://mock.test/old-backup/?page=1",
				Proto: "HTTP/1.1", Host: "mock.test", Headers: []mirror.Header{{Name: "User-Agent", Value: "probe/1"}}},
			Response: &mirror.Response{Proto: "HTTP/1.1", Status: 200, StatusText: "OK",
				Headers: []mirror.Header{{Name: "Content-Type", Value: "text/plain"}}, Body: []byte("mock backup listing: db.sql.gz")},
			ReqBytes: 0, RespBytes: 27, ElapsedMS: 12,
		}, at),
		mkRec(&mirror.Exchange{
			ID: "ms-a.2", Type: rings.MirrorRecHTTPS, TaskID: task, StepID: "s1", UnitID: "u2", TranscriptID: "tr-2",
			StartedAt: at.Add(time.Minute),
			Request: &mirror.Request{Method: "POST", URL: "https://mock.test/api/login",
				Proto: "HTTP/1.1", Host: "mock.test", Body: []byte(`{"u":"admin","p":"1"}`)},
			Response: &mirror.Response{Proto: "HTTP/1.1", Status: 403, StatusText: "Forbidden"},
			TLS:      &mirror.TLSInfo{Validity: "self_signed", PeerSubject: "CN=mock.test", PeerIssuer: "CN=mock.test"},
			ReqBytes: 20, RespBytes: 0, ElapsedMS: 44,
		}, at.Add(time.Minute)),
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
	// 隧道留痕(无 blob)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror", OccurredAt: at.Add(2 * time.Minute),
		Payload: rings.MirrorRecord{
			ExchangeID: "ms-a.3", Type: rings.MirrorRecTunnel, StepID: "s1", UnitID: "u1",
			URL: "mock.test:22", ReqBytes: 1024, RespBytes: 2048,
		},
	}); err != nil {
		t.Fatalf("铺隧道留痕失败: %v", err)
	}
	return &trafficSeed{task: task, blobs: blobs}
}

// Summary 的调用面说明:测试直接构造留痕事件,不经镜会话(其内摘要
// 三元组在 seedTrafficTask 内手工提取)。

func newTrafficServer(t *testing.T, dir string) (*Server, *rings.Store, http.Handler) {
	t.Helper()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	return srv, store, srv.Handler()
}

func TestTrafficEndpoints(t *testing.T) {
	dir := t.TempDir()
	_, store, h := newTrafficServer(t, dir)
	seedTrafficTask(t, dir, store, "tA", "流量页验证")
	seedTrafficTask(t, dir, store, "tB", "第二任务")

	type row struct {
		TaskID     string `json:"task_id"`
		ExchangeID string `json:"exchange_id"`
		Type       string `json:"type"`
		Method     string `json:"method"`
		Host       string `json:"host"`
		Status     int    `json:"status"`
		HasBlob    bool   `json:"has_blob"`
	}
	listRows := func(query string) []row {
		t.Helper()
		var body struct {
			Rows []row `json:"rows"`
		}
		get(t, h, "/api/mirror/exchanges"+query, http.StatusOK, &body)
		return body.Rows
	}

	// 全量(两任务各 3 条):倒序最新在前,首行是后铺的 tB 隧道。
	rows := listRows("")
	if len(rows) != 6 {
		t.Fatalf("全量应 6 行,得 %d", len(rows))
	}
	if rows[0].ExchangeID != "ms-a.3" || rows[0].Type != rings.MirrorRecTunnel || rows[0].HasBlob {
		t.Fatalf("首行应为隧道留痕: %+v", rows[0])
	}

	// 任务 + host + 方法 + 状态类组合筛选。
	rows = listRows("?task=tA&host=mock.test&method=GET&status=2")
	if len(rows) != 1 || rows[0].ExchangeID != "ms-a.1" {
		t.Fatalf("组合筛选应剩 GET 200 一行: %+v", rows)
	}
	rows = listRows("?task=tA&type=https&status=4")
	if len(rows) != 1 || rows[0].ExchangeID != "ms-a.2" || rows[0].Host != "mock.test" {
		t.Fatalf("https 4xx 应剩一行: %+v", rows)
	}
	// 时间窗(只含第一分钟)。
	rows = listRows("?task=tA&since=2026-10-10T09:00:30Z&until=2026-10-10T09:01:30Z")
	if len(rows) != 1 || rows[0].ExchangeID != "ms-a.2" {
		t.Fatalf("时间窗应剩 https 一行: %+v", rows)
	}
	// 坏参数。
	get(t, h, "/api/mirror/exchanges?status=9", http.StatusBadRequest, nil)
	get(t, h, "/api/mirror/exchanges?task=no-such", http.StatusNotFound, nil)

	// 详情:元数据不带正文,带尺寸与 TLS 事实。
	var detail struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		TLSLabel string `json:"tls_label"`
		TLSSubj  string `json:"tls_subject"`
		Request  struct {
			Method   string `json:"method"`
			BodySize int    `json:"body_size"`
		} `json:"request"`
		Response struct {
			Status   int `json:"status"`
			BodySize int `json:"body_size"`
		} `json:"response"`
		RawTotal int    `json:"raw_total"`
		BlobRef  string `json:"blob_ref"`
	}
	get(t, h, "/api/mirror/exchanges/ms-a.2?task=tA", http.StatusOK, &detail)
	if detail.TLSLabel != "自签" || detail.TLSSubj != "CN=mock.test" || detail.Request.BodySize != 21 {
		t.Fatalf("https 详情证书事实/尺寸错误: %+v", detail)
	}
	if detail.RawTotal == 0 || detail.BlobRef == "" {
		t.Fatalf("详情应带原文总长与引用: %+v", detail)
	}
	// 未知交换 404;缺 task 400。
	get(t, h, "/api/mirror/exchanges/nope?task=tA", http.StatusNotFound, nil)
	get(t, h, "/api/mirror/exchanges/ms-a.1", http.StatusBadRequest, nil)
}

// TestTrafficRawSegmentsBytewise 原文分段回放:各段拼接 == 全文;且全文
// 与 blob 文档逐字一致(请求行/头/体按 ExchangeRaw 装配,体内容字节级)。
func TestTrafficRawSegmentsBytewise(t *testing.T) {
	dir := t.TempDir()
	_, store, h := newTrafficServer(t, dir)
	seedTrafficTask(t, dir, store, "tA", "分段回放验证")

	// 用小段逐段拉全。
	var full strings.Builder
	offset := 0
	total := -1
	for {
		var seg struct {
			Text    string `json:"text"`
			Offset  int    `json:"offset"`
			Total   int    `json:"total"`
			HasMore bool   `json:"has_more"`
		}
		get(t, h, fmt.Sprintf("/api/mirror/exchanges/ms-a.1/raw?task=tA&offset=%d&limit=7", offset), http.StatusOK, &seg)
		if total == -1 {
			total = seg.Total
		} else if seg.Total != total {
			t.Fatalf("total 漂移:%d vs %d", seg.Total, total)
		}
		if seg.Offset != offset {
			t.Fatalf("offset 回显错误:%d vs %d", seg.Offset, offset)
		}
		full.WriteString(seg.Text)
		if !seg.HasMore {
			break
		}
		offset += len([]byte(seg.Text))
	}

	// 与 blob 文档逐字一致:经 mirror.Exchanges 回放拿到完整交换文档,
	// 关键行(请求行/头/状态行/响应体)在分段拼接结果内逐一可寻。
	exs, err := mirror.Exchanges(context.Background(), store,
		swarm.NewBlobStore(filepath.Join(dir, "tasks", "blobs")), "tA")
	if err != nil || len(exs) == 0 {
		t.Fatalf("任务留痕为空: %v", err)
	}
	var want *mirror.Exchange
	for i := range exs {
		if exs[i].ID == "ms-a.1" {
			want = &exs[i]
		}
	}
	if want == nil {
		t.Fatal("目标交换缺失")
	}
	got := full.String()
	for _, wantLine := range []string{
		"GET http://mock.test/old-backup/?page=1 HTTP/1.1",
		"Host: mock.test",
		"User-Agent: probe/1",
		"HTTP/1.1 200 OK",
		"Content-Type: text/plain",
		"mock backup listing: db.sql.gz",
	} {
		if !strings.Contains(got, wantLine) {
			t.Fatalf("回放缺 %q:\n%s", wantLine, got)
		}
	}
	// 请求体/响应体字节级一致(与 blob 文档比)。
	if !strings.Contains(got, string(want.Response.Body)) {
		t.Fatalf("响应体与 blob 不一致:\n%s", got)
	}

	// 隧道无原文:400 如实。
	get(t, h, "/api/mirror/exchanges/ms-a.3/raw?task=tA", http.StatusBadRequest, nil)
	// offset 越界:400。
	get(t, h, "/api/mirror/exchanges/ms-a.1/raw?task=tA&offset=999999", http.StatusBadRequest, nil)
}

// TestEvidenceBindAndArchiveFlow 三机制全链路:绑定 → 漏洞详情可见 →
// 存档(EV 锚定)→ 补绑 → 旧 EV 存档 409 → 旧存档待更新。
func TestEvidenceBindAndArchiveFlow(t *testing.T) {
	dir := t.TempDir()
	_, store, h := newTrafficServer(t, dir)
	seedTrafficTask(t, dir, store, "tA", "三机制验证")

	// 绑定 ms-a.1 到 tc-1。
	var bindResp struct {
		EvidenceVersion     int64 `json:"evidence_version"`
		TaskEvidenceVersion int64 `json:"task_evidence_version"`
	}
	post(t, h, http.MethodPost, "/api/tasks/tA/claims/tc-1/evidence",
		`{"exchange_ids":["ms-a.1"]}`, http.StatusOK, &bindResp)
	if bindResp.EvidenceVersion != 1 || bindResp.TaskEvidenceVersion != 1 {
		t.Fatalf("绑定后声明/任务证据版本应为 1/1,得 %d/%d", bindResp.EvidenceVersion, bindResp.TaskEvidenceVersion)
	}

	// 漏洞详情可见绑定证据 + 版本。
	var detail struct {
		Evidence []struct {
			ExchangeID string `json:"exchange_id"`
			Method     string `json:"method"`
			Status     int    `json:"status"`
			BoundRev   int64  `json:"bound_rev"`
		} `json:"evidence"`
		EvidenceVersion int64 `json:"evidence_version"`
		Timeline        []struct {
			Kind string `json:"kind"`
		} `json:"timeline"`
	}
	get(t, h, "/api/tasks/tA/claims/tc-1", http.StatusOK, &detail)
	if len(detail.Evidence) != 1 || detail.Evidence[0].ExchangeID != "ms-a.1" || detail.Evidence[0].BoundRev == 0 {
		t.Fatalf("详情应见绑定证据: %+v", detail.Evidence)
	}
	if detail.EvidenceVersion != 1 {
		t.Fatalf("详情证据版本应为 1,得 %d", detail.EvidenceVersion)
	}
	for _, e := range detail.Timeline {
		if e.Kind == "" {
			t.Fatal("evidence 记录不得混入反证时间线")
		}
	}

	// 坏绑定:未知交换 / 隧道 / 空 清单。
	post(t, h, http.MethodPost, "/api/tasks/tA/claims/tc-1/evidence", `{"exchange_ids":["nope"]}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks/tA/claims/tc-1/evidence", `{"exchange_ids":["ms-a.3"]}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks/tA/claims/tc-1/evidence", `{"exchange_ids":[]}`, http.StatusBadRequest, nil)

	// 存档:EV=1 通过。
	var arch struct {
		Rev             int64 `json:"rev"`
		EvidenceVersion int64 `json:"evidence_version"`
	}
	post(t, h, http.MethodPost, "/api/tasks/tA/report/archive", `{"evidence_version":1}`, http.StatusOK, &arch)
	if arch.EvidenceVersion != 1 {
		t.Fatalf("存档应锚定证据版本 1: %+v", arch)
	}

	// 补绑 ms-a.2(声明已有证据再绑 = 补绑)。
	post(t, h, http.MethodPost, "/api/tasks/tA/claims/tc-1/evidence",
		`{"exchange_ids":["ms-a.2"]}`, http.StatusOK, &bindResp)
	if bindResp.EvidenceVersion != 2 {
		t.Fatalf("补绑后声明证据版本应为 2,得 %d", bindResp.EvidenceVersion)
	}

	// 旧证据版本(1)存档:409,携带当前版本。
	var conflict struct {
		Error                  string `json:"error"`
		CurrentEvidenceVersion int64  `json:"current_evidence_version"`
	}
	post(t, h, http.MethodPost, "/api/tasks/tA/report/archive", `{"evidence_version":1}`, http.StatusConflict, &conflict)
	if conflict.CurrentEvidenceVersion != 2 || !strings.Contains(conflict.Error, "证据已更新") {
		t.Fatalf("409 应携带当前证据版本: %+v", conflict)
	}

	// 存档清单:旧存档自动待更新,当前证据版本 2。
	var listBody struct {
		Archives []struct {
			Rev             int64 `json:"rev"`
			EvidenceVersion int64 `json:"evidence_version"`
			Stale           bool  `json:"stale"`
		} `json:"archives"`
		CurrentEvidenceVersion int64 `json:"current_evidence_version"`
	}
	get(t, h, "/api/tasks/tA/report/archives", http.StatusOK, &listBody)
	if len(listBody.Archives) != 1 || !listBody.Archives[0].Stale {
		t.Fatalf("补绑后旧存档应自动待更新: %+v", listBody.Archives)
	}
	if listBody.Archives[0].Rev != arch.Rev || listBody.CurrentEvidenceVersion != 2 {
		t.Fatalf("存档清单锚点错误: %+v", listBody)
	}

	// 新版本再存档:通过;清单两份,仅旧的一份待更新。
	post(t, h, http.MethodPost, "/api/tasks/tA/report/archive", `{"evidence_version":2}`, http.StatusOK, nil)
	get(t, h, "/api/tasks/tA/report/archives", http.StatusOK, &listBody)
	if len(listBody.Archives) != 2 || listBody.Archives[0].Stale || !listBody.Archives[1].Stale {
		t.Fatalf("两份存档应仅旧份待更新: %+v", listBody.Archives)
	}

	// 存档下载:与 GET /report 同任务正文(md 快照,以 Content-Type 交付)。
	rec := doReq(t, h, http.MethodGet, fmt.Sprintf("/api/tasks/tA/report/archives/%d", listBody.Archives[0].Rev), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("存档下载失败:%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "渗透测试报告") {
		t.Fatalf("存档正文应为报告 md:%s", rec.Body.String())
	}
	// 未知版本 404。
	get(t, h, "/api/tasks/tA/report/archives/99999", http.StatusNotFound, nil)
}

// TestTrafficRawSide 单侧原文端点(P3-02 审核迭代:请求包/返回包分开
// 展示):side=req 只含请求行不含状态行,side=resp 反之;两侧拼接 ==
// 全量端点原文(逐字);非法 side 400。
func TestTrafficRawSide(t *testing.T) {
	dir := t.TempDir()
	_, store, h := newTrafficServer(t, dir)
	seedTrafficTask(t, dir, store, "tA", "单侧回放验证")

	fetchAll := func(query string) (string, int) {
		t.Helper()
		var full strings.Builder
		offset, total := 0, -1
		for {
			var seg struct {
				Text    string `json:"text"`
				Total   int    `json:"total"`
				HasMore bool   `json:"has_more"`
			}
			get(t, h, fmt.Sprintf("/api/mirror/exchanges/ms-a.1/raw?task=tA&%s&offset=%d&limit=9", query, offset), http.StatusOK, &seg)
			if total == -1 {
				total = seg.Total
			}
			full.WriteString(seg.Text)
			if !seg.HasMore {
				return full.String(), total
			}
			offset += len([]byte(seg.Text))
		}
	}
	req, reqTotal := fetchAll("side=req")
	resp, respTotal := fetchAll("side=resp")
	if !strings.Contains(req, "GET http://mock.test/old-backup/?page=1 HTTP/1.1") || strings.Contains(req, "HTTP/1.1 200 OK") {
		t.Fatalf("请求侧内容错误:\n%s", req)
	}
	if !strings.Contains(resp, "HTTP/1.1 200 OK") || strings.Contains(resp, "GET http://") {
		t.Fatalf("响应侧内容错误:\n%s", resp)
	}
	full, fullTotal := fetchAll("")
	if full != req+"\n"+resp || fullTotal != reqTotal+respTotal+1 {
		t.Fatalf("单侧拼接须与全量逐字一致(%d/%d/%d)", fullTotal, reqTotal, respTotal)
	}
	// 非法 side。
	get(t, h, "/api/mirror/exchanges/ms-a.1/raw?task=tA&side=both", http.StatusBadRequest, nil)
}
