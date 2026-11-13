package overmind

// P4-62 验收套件(问题清单 #83,复现数据包质量):
// ① 池入口两道过滤类级测试集——error 交换多形态(TLS 握手失败/转发失败/
//    超时/连接拒绝/DNS 失败)与 host 游离多形态(裸 IP/兄弟子域/第三方域/
//    同 host 跨端口)各至少一例只进过程清单,本日实锤(GET / 裸 IP 与
//    TLS 握手失败)两例作回归样本;规则语义为契约,不按样本字符串特判;
// ② 工具调用记录确定性组包——http_probe 参数直接组包(origin-form＋Host,
//    Burp 就绪)、简单 bash curl 组包、含变量命令如实缺料;
// ③ 废除路径断言——模型空选/挑选失败后复现区"未生成"而非倒池;
//    账本受控入口物理拒绝"步骤与无复现包原因并存"。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/report"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// p462SeedAsset 铺资产节点。
func p462SeedAsset(t *testing.T, store *rings.Store, task, id, title string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset,
			AssetClass: rings.AssetService, Title: title, Status: "active"},
	}); err != nil {
		t.Fatalf("铺资产 %s 失败: %v", id, err)
	}
}

// p462SeedClaim 铺声明节点 + about 锚点边。
func p462SeedClaim(t *testing.T, store *rings.Store, task, claimID string, anchors ...string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "swarm:u1",
		Payload: rings.NodeAdded{NodeID: claimID, NodeType: rings.NodeTestClaim,
			Title: claimID + " 存在性陈述", Status: rings.TestClaimCandidate},
	}); err != nil {
		t.Fatalf("铺声明 %s 失败: %v", claimID, err)
	}
	for _, a := range anchors {
		if _, err := store.Append(context.Background(), task, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: a},
		}); err != nil {
			t.Fatalf("铺锚点边 %s→%s 失败: %v", claimID, a, err)
		}
	}
}

// p462SeedStep 铺反证步骤(声明绑定)。
func p462SeedStep(t *testing.T, store *rings.Store, task, stepID, claimID string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: ActorOvermind,
		Payload: rings.NodeAdded{NodeID: stepID, NodeType: rings.NodeStep, Status: "done",
			Detail: map[string]any{"testclaim_id": claimID, "round": 1, "kind": "falsify"}},
	}); err != nil {
		t.Fatalf("铺步骤 %s 失败: %v", stepID, err)
	}
}

// p462Mirror 铺一条镜留痕事件。
func p462Mirror(t *testing.T, store *rings.Store, task string, m rings.MirrorRecord) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror", Payload: m,
	}); err != nil {
		t.Fatalf("铺镜留痕 %s 失败: %v", m.ExchangeID, err)
	}
}

// p462Resolver 解析打桩:仅 anchor-dns.example 可解析(种子解析 IP 口径的
// 受控正例);其余解析失败(保守按范围外,与生产默认行为一致)。
func p462Resolver(host string) []string {
	if host == "anchor-dns.example" {
		return []string{"10.9.8.7"}
	}
	return nil
}

// TestP462PoolEntranceFilters ① 池入口两道过滤类级(规则语义为契约):
// error 交换 = 镜记录带错误的任意形态;host 锚定 = 任意非声明锚定目标的
// 成功交换。实锤回归样本:本日 GET / 裸 IP(223.13.237.164,任务第二种子
// 形态——任务范围放行、声明锚定拦截)与"目标 TLS 握手失败: certificate
// used with invalid signature algorithm"。
func TestP462PoolEntranceFilters(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p462-filter"
	mkTask(t, store, task)

	// 资产:URL 锚(实锤形态,URL 等同裸域名→全端口)、host:port 锚、
	// 裸 IP 种子(任务范围放行 GET / 用)、兄弟域与第三方域(任务内、锚外)、
	// 解析锚(种子解析 IP 正例)。
	p462SeedAsset(t, store, task, "a-url", "https://mcs.sxnx.com:8084/hm_services_c")
	p462SeedAsset(t, store, task, "a-port", "mcs.sxnx.com:8084")
	p462SeedAsset(t, store, task, "a-host", "mcs.sxnx.com") // 任务范围 host 级(跨端口用例过任务关、拦在锚定关)
	p462SeedAsset(t, store, task, "a-bareip", "http://223.13.237.164/")
	p462SeedAsset(t, store, task, "a-sib", "mcst.sxnx.com:8092")
	p462SeedAsset(t, store, task, "a-third", "ebank.ydnsh.com")
	p462SeedAsset(t, store, task, "a-dns", "anchor-dns.example")
	// 解析 IP 正例的 任务范围面:裸 IP 种子(实锤任务同款第二种子形态——
	// 任务范围放行、锚定关靠"锚定域名的当前解析 IP"放行)。
	p462SeedAsset(t, store, task, "a-ip9", "10.9.8.7")
	p462SeedClaim(t, store, task, "tc-url", "a-url")
	p462SeedClaim(t, store, task, "tc-port", "a-port")
	p462SeedClaim(t, store, task, "tc-dns", "a-dns")
	p462SeedStep(t, store, task, "s-url", "tc-url")
	p462SeedStep(t, store, task, "s-port", "tc-port")
	p462SeedStep(t, store, task, "s-dns", "tc-dns")

	// error 交换多形态(URL 均在任务范围与锚定内——只考 error 过滤一道)。
	errForms := []rings.MirrorRecord{
		// 实锤回归样本:本日 TLS 握手失败(certificate used with invalid
		// signature algorithm)。
		{ExchangeID: "e-tls", Type: rings.MirrorRecHTTPS, StepID: "s-url", UnitID: "u1",
			URL: "https://mcs.sxnx.com:8084/hm_mcec/wechatBank.html", BlobRef: "b-tls",
			Error: "目标 TLS 握手失败: tls: certificate used with invalid signature algorithm"},
		// 实锤回归样本:本日转发失败 EOF。
		{ExchangeID: "e-fwd", Type: rings.MirrorRecHTTP, StepID: "s-url", UnitID: "u1",
			URL: "http://mcs.sxnx.com:8084/hm_mcec/wechatBank.html", BlobRef: "b-fwd",
			Error: `转发失败: Get "http://mcs.sxnx.com:8084/hm_mcec/wechatBank.html": EOF`},
		{ExchangeID: "e-timeout", Type: rings.MirrorRecHTTP, StepID: "s-url", UnitID: "u1",
			URL: "https://mcs.sxnx.com:8084/hm_mcec/", BlobRef: "b-to",
			Error: "转发失败: context deadline exceeded (Client.Timeout exceeded while awaiting headers)"},
		{ExchangeID: "e-refused", Type: rings.MirrorRecHTTP, StepID: "s-url", UnitID: "u1",
			URL: "http://mcs.sxnx.com:8084/hm/", BlobRef: "b-rf",
			Error: "目标拨号失败: dial tcp 10.0.0.9:8084: connect: connection refused"},
		{ExchangeID: "e-dns", Type: rings.MirrorRecHTTP, StepID: "s-url", UnitID: "u1",
			URL: "https://mcs.sxnx.com:8084/nx", BlobRef: "b-dns",
			Error: "转发失败: dial tcp: lookup nx.sxnx.com: no such host"},
	}
	for _, m := range errForms {
		p462Mirror(t, store, task, m)
	}
	// host 游离多形态(成功交换,任务范围放行、声明锚定拦截)。
	offForms := []rings.MirrorRecord{
		// 实锤回归样本:反证步骤顺手 GET / 裸服务器 IP(任务第二种子,任务
		// 范围放行;声明锚定为 mcs.sxnx.com,裸 IP 非其解析 IP→游离)。
		{ExchangeID: "o-ip", Type: rings.MirrorRecHTTP, StepID: "s-url", UnitID: "u1",
			Method: "GET", URL: "http://223.13.237.164/", Status: 200, RespBytes: 139264, BlobRef: "b-ip"},
		{ExchangeID: "o-sib", Type: rings.MirrorRecHTTP, StepID: "s-url", UnitID: "u1",
			Method: "GET", URL: "http://mcst.sxnx.com:8092/panel", Status: 200, BlobRef: "b-sib"},
		{ExchangeID: "o-third", Type: rings.MirrorRecHTTPS, StepID: "s-url", UnitID: "u1",
			Method: "GET", URL: "https://ebank.ydnsh.com/login", Status: 200, BlobRef: "b-third"},
		// 同 host 跨端口(host:port 锚声明的战役):8085 不在锚内。
		{ExchangeID: "o-port", Type: rings.MirrorRecHTTP, StepID: "s-port", UnitID: "u1",
			Method: "GET", URL: "http://mcs.sxnx.com:8085/hm", Status: 200, BlobRef: "b-port"},
	}
	for _, m := range offForms {
		p462Mirror(t, store, task, m)
	}
	// 锚定内成功交换(应留候选)与种子解析 IP 正例。
	p462Mirror(t, store, task, rings.MirrorRecord{
		ExchangeID: "ok-url", Type: rings.MirrorRecHTTPS, StepID: "s-url", UnitID: "u1",
		Method: "POST", URL: "https://mcs.sxnx.com:8084/hm_services_c/getToken", Status: 200, BlobRef: "b-ok"})
	p462Mirror(t, store, task, rings.MirrorRecord{
		ExchangeID: "ok-port", Type: rings.MirrorRecHTTP, StepID: "s-port", UnitID: "u1",
		Method: "GET", URL: "http://mcs.sxnx.com:8084/hm", Status: 200, BlobRef: "b-okp"})
	p462Mirror(t, store, task, rings.MirrorRecord{
		ExchangeID: "ok-dns", Type: rings.MirrorRecHTTP, StepID: "s-dns", UnitID: "u1",
		Method: "GET", URL: "http://10.9.8.7:7777/x", Status: 200, BlobRef: "b-okd"})

	// URL 锚声明(实锤形态):5 条 error + 3 条游离只进过程清单,恰留 1 条候选。
	rec, diag, _, err := AssembleRepro(ctx, store, nil, p462Resolver, task, "tc-url", "high", nil)
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ok-url" {
		t.Fatalf("恰应留锚定内成功交换 1 条候选: %+v", rec.Steps)
	}
	if diag.ErrExchanges != 5 {
		t.Fatalf("error 交换应计 5 条(多形态全拦): %d", diag.ErrExchanges)
	}
	if diag.OffAnchor != 3 {
		t.Fatalf("host 游离应计 3 条(裸 IP/兄弟域/第三方域): %d", diag.OffAnchor)
	}
	if len(rec.Process) != 9 {
		t.Fatalf("被滤交换全量留过程清单(5 error + 3 游离 + 1 候选): %d", len(rec.Process))
	}
	// 实锤断言:失败包与 GET / 绝迹于候选池。
	for _, st := range rec.Steps {
		if strings.Contains(st.URL, "223.13.237.164") {
			t.Fatalf("GET / 裸 IP 不得进候选池: %+v", st)
		}
		if st.Status == 0 || st.Note != "" {
			t.Fatalf("候选池不得含失败交换: %+v", st)
		}
	}

	// host:port 锚声明:同 host 跨端口游离只进过程清单。
	recP, diagP, _, err := AssembleRepro(ctx, store, nil, p462Resolver, task, "tc-port", "high", nil)
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if len(recP.Steps) != 1 || recP.Steps[0].ExchangeID != "ok-port" {
		t.Fatalf("host:port 锚应恰留本端口候选: %+v", recP.Steps)
	}
	if diagP.OffAnchor != 1 {
		t.Fatalf("同 host 跨端口应计游离: %d", diagP.OffAnchor)
	}

	// 种子解析 IP(P4-50 ② 口径):锚定域名的当前解析 IP 直连在锚内。
	recD, _, _, err := AssembleRepro(ctx, store, nil, p462Resolver, task, "tc-dns", "low", nil)
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if len(recD.Steps) != 1 || recD.Steps[0].ExchangeID != "ok-dns" {
		t.Fatalf("解析 IP 直连应在锚内留候选: %+v", recD.Steps)
	}
}

// TestP462CombinedChainAnchorAnchors 组合声明的锚定集并集链上事实的资产
// (含组合链上资产口径)。
func TestP462CombinedChainAnchorAnchors(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p462-comb"
	mkTask(t, store, task)
	p462SeedAsset(t, store, task, "a-link", "comb.example:9000")
	p462SeedAsset(t, store, task, "a-other", "other.example:9000") // 任务范围内、链外(拦在锚定关)
	// 链上事实(about→a-link)+ 组合声明(derived_from→事实)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1",
			Payload: rings.NodeAdded{NodeID: "f-1", NodeType: rings.NodeFact, Title: "链环节", Status: "confirmed"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f-1", ToNode: "a-link"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind,
			Payload: rings.NodeAdded{NodeID: "tc-c", NodeType: rings.NodeTestClaim, Title: "组合声明",
				Status: rings.TestClaimCandidate, Detail: map[string]any{"combined": true}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: ActorOvermind,
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-c", ToNode: "f-1"}},
	); err != nil {
		t.Fatalf("铺组合链失败: %v", err)
	}
	p462SeedStep(t, store, task, "s-c", "tc-c")
	p462Mirror(t, store, task, rings.MirrorRecord{
		ExchangeID: "ok-c", Type: rings.MirrorRecHTTP, StepID: "s-c", UnitID: "u1",
		Method: "GET", URL: "http://comb.example:9000/x", Status: 200, BlobRef: "b-c"})
	p462Mirror(t, store, task, rings.MirrorRecord{
		ExchangeID: "off-c", Type: rings.MirrorRecHTTP, StepID: "s-c", UnitID: "u1",
		Method: "GET", URL: "http://other.example:9000/x", Status: 200, BlobRef: "b-oc"})

	rec, diag, _, err := AssembleRepro(ctx, store, nil, p462Resolver, task, "tc-c", "high", nil)
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if len(rec.Steps) != 1 || rec.Steps[0].ExchangeID != "ok-c" {
		t.Fatalf("组合链上资产的交换应留候选: %+v", rec.Steps)
	}
	if diag.OffAnchor != 1 {
		t.Fatalf("链外目标应计游离: %d", diag.OffAnchor)
	}
}

// TestP462ToolPacketAssembly ② 工具调用记录确定性组包:http_probe 参数
// 直接组包(origin-form＋Host,Burp 就绪)、简单 bash curl 组包(含 -i 响应
// 还原)、含变量命令如实缺料、非流量命令不计。
func TestP462ToolPacketAssembly(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p462-tool"
	mkTask(t, store, task)
	blobs := swarm.NewBlobStore(t.TempDir())
	p462SeedAsset(t, store, task, "a-t", "t.example:8080")
	p462SeedClaim(t, store, task, "tc-t", "a-t")
	p462SeedStep(t, store, task, "s-t", "tc-t")

	// 工具调用记录:结果全文经 ResultRef blob(R18 形态)。
	seedCall := func(tool string, args string, result string) {
		t.Helper()
		ref, err := blobs.Write([]byte(result))
		if err != nil {
			t.Fatalf("铺结果 blob 失败: %v", err)
		}
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s-t", UnitID: "u1", Type: rings.SwarmRecToolCall,
				ToolName: tool, ToolArgs: json.RawMessage(args), ResultRef: ref},
		}); err != nil {
			t.Fatalf("铺工具调用失败: %v", err)
		}
	}
	seedCall("http_probe", `{"url":"http://t.example:8080/api/x?token=1"}`,
		"url=http://t.example:8080/api/x?token=1 method=GET status=200\n"+
			"server=nginx content-type=application/json content-length=21\n"+
			"--- body(前 1536 字节) ---\n{\"ok\":true,\"list\":[]}")
	seedCall("bash", `{"command":"curl -s -k -i -X POST -H 'Content-Type: application/json' -d '{\"tenantId\":\"srcu\"}' 'https://t.example:8080/hm_services_c/getToken'"}`,
		"HTTP/1.1 200 OK\r\nServer: nginx\r\nContent-Type: application/json\r\n\r\n{\"token\":\"abc\"}")
	// 含变量命令:不可静态还原,如实缺料;非流量命令:不是复现原料。
	seedCall("bash", `{"command":"curl -H \"X-T: $TOKEN\" http://t.example:8080/x"}`, "ignored")
	seedCall("bash", `{"command":"ls -la"}`, "total 0")
	// 非锚定目标的简单 curl:直连流量同按锚定口径过滤。
	seedCall("bash", `{"command":"curl -s http://other.example:8080/"}`, "x")

	rec, diag, _, err := AssembleRepro(ctx, store, blobs, nil, task, "tc-t", "high", nil)
	if err != nil {
		t.Fatalf("定稿组装失败: %v", err)
	}
	if diag.ToolBuilt != 2 || diag.ToolSkipped != 1 {
		t.Fatalf("组包应恰 2 条(probe+curl),缺料恰 1 条(含变量): %+v", diag)
	}
	if len(rec.Steps) != 2 {
		t.Fatalf("候选池应恰 2 条组包步骤: %+v", rec.Steps)
	}
	byURL := map[string]rings.ReproStep{}
	for _, st := range rec.Steps {
		if st.Source != reproToolSource {
			t.Fatalf("组包步骤应带来源标记: %+v", st)
		}
		byURL[st.URL] = st
	}
	probe, okP := byURL["http://t.example:8080/api/x?token=1"]
	curlStep, okC := byURL["https://t.example:8080/hm_services_c/getToken"]
	if !okP || !okC {
		t.Fatalf("两条组包候选URL 不符: %+v", rec.Steps)
	}

	// http_probe 组包:请求包 origin-form＋Host、头终止空行(Burp 就绪);
	// 响应侧从结果标记行还原。
	var exP mirror.Exchange
	if doc, err := blobs.Read(probe.BlobRef); err != nil || json.Unmarshal(doc, &exP) != nil {
		t.Fatalf("读 probe 组包文档失败: %v", err)
	}
	reqP := report.ExchangeRawSide(&exP, "req")
	if !strings.HasPrefix(reqP, "GET /api/x?token=1 HTTP/1.1\r\nHost: t.example:8080\r\n") ||
		!strings.HasSuffix(reqP, "8080\r\n\r\n") {
		t.Fatalf("probe 请求包应 origin-form＋Host 且 Burp 就绪: %q", reqP)
	}
	if exP.Response == nil || exP.Response.Status != 200 || string(exP.Response.Body) != `{"ok":true,"list":[]}` {
		t.Fatalf("probe 响应侧应从工具结果还原: %+v", exP.Response)
	}
	if probe.Status != 200 {
		t.Fatalf("probe 步骤状态应为还原的状态码: %+v", probe)
	}

	// 简单 curl 组包:方法/头/体如实,响应侧(-i)还原。
	var exC mirror.Exchange
	if doc, err := blobs.Read(curlStep.BlobRef); err != nil || json.Unmarshal(doc, &exC) != nil {
		t.Fatalf("读 curl 组包文档失败: %v", err)
	}
	reqC := report.ExchangeRawSide(&exC, "req")
	wantC := "POST /hm_services_c/getToken HTTP/1.1\r\n" +
		"Host: t.example:8080\r\n" +
		"Content-Type: application/json\r\n" +
		"\r\n" +
		"{\"tenantId\":\"srcu\"}\r\n"
	if reqC != wantC {
		t.Fatalf("curl 请求包组包不符(Burp 就绪口径):\n got %q\nwant %q", reqC, wantC)
	}
	if exC.Response == nil || exC.Response.Status != 200 || string(exC.Response.Body) != "{\"token\":\"abc\"}" {
		t.Fatalf("curl 响应侧应按 -i 输出还原: %+v", exC.Response)
	}

	// 缺料如实标注:含变量命令计入原因句。
	reason := noReproReason(ReproPoolDiag{ErrExchanges: 2, ToolSkipped: 1}, "")
	if !strings.Contains(reason, "无可用复现包：") ||
		!strings.Contains(reason, "2 条交换未获响应") ||
		!strings.Contains(reason, "1 条直连命令无法还原请求包") {
		t.Fatalf("缺料标注应如实入原因句: %q", reason)
	}
}

// TestP462EmptySelectionNoDump ③ 废除路径断言:模型空选=无可用复现包+
// 原因(模型理由过闸门);理由含机器语时退确定性原因句;池空直接确定性
// 原因(零外呼);账本物理拒绝"步骤与原因并存"。
func TestP462EmptySelectionNoDump(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p462-empty"
	mkTask(t, store, task)
	p462SeedClaim(t, store, task, "tc-e") // 无锚点(退任务范围过滤,不影响本测)
	mkRec := func() *rings.TestClaimRecord {
		return &rings.TestClaimRecord{ClaimID: "tc-e", Type: rings.TestClaimRecRepro, Severity: "high",
			Steps: []rings.ReproStep{
				{Seq: 1, StepID: "s1", Short: "GET /", Desc: "请求 /,目标返回 200",
					Method: "GET", URL: "http://k9.local/", Status: 200, BlobRef: "b1", ExchangeID: "m1"},
			}}
	}

	t.Run("模型空选呈现无可用复现包", func(t *testing.T) {
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"steps":[],"skip_reason":"候选请求都是无关流量,不能证明该问题"}`, Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, Provider: prov}
		rec := mkRec()
		l.pickReproSteps(ctx, "tc-e", rec, ReproPoolDiag{}, nil)
		if len(rec.Steps) != 0 {
			t.Fatalf("空选不得倒池: %+v", rec.Steps)
		}
		if rec.NoReproReason != "无可用复现包：候选请求都是无关流量,不能证明该问题" {
			t.Fatalf("空选应呈现模型理由: %q", rec.NoReproReason)
		}
	})

	t.Run("空选理由含机器语退确定性原因句", func(t *testing.T) {
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"steps":[],"skip_reason":"tc-8f2a1b3c 无可用,verdict=confirmed"}`, Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, Provider: prov}
		rec := mkRec()
		l.pickReproSteps(ctx, "tc-e", rec, ReproPoolDiag{ErrExchanges: 3}, nil)
		if len(rec.Steps) != 0 || rec.NoReproReason == "" ||
			strings.Contains(rec.NoReproReason, "tc-8f2a1b3c") ||
			!strings.Contains(rec.NoReproReason, "3 条交换未获响应") {
			t.Fatalf("机器语理由应退确定性原因句: steps=%d reason=%q", len(rec.Steps), rec.NoReproReason)
		}
	})

	t.Run("池空零外呼直接确定性原因句", func(t *testing.T) {
		model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
			return scriptedReply{Text: `{"steps":[{"seq":1,"short":"x","desc":"y","purpose":"z"}]}`, Finish: "stop"}
		})
		defer model.Close()
		prov, err := model.provider()
		if err != nil {
			t.Fatalf("起 scripted 模型失败: %v", err)
		}
		l := &Loop{TaskID: task, Store: store, Provider: prov}
		rec := &rings.TestClaimRecord{ClaimID: "tc-e", Type: rings.TestClaimRecRepro}
		l.pickReproSteps(ctx, "tc-e", rec, ReproPoolDiag{ErrExchanges: 12, OffAnchor: 1}, nil)
		if len(model.requests()) != 0 {
			t.Fatalf("池空应零外呼: %d", len(model.requests()))
		}
		if !strings.Contains(rec.NoReproReason, "12 条交换未获响应") ||
			!strings.Contains(rec.NoReproReason, "1 条交换的目标不是判断锚定的资产") {
			t.Fatalf("池空原因句应如实: %q", rec.NoReproReason)
		}
	})

	t.Run("账本物理拒绝步骤与原因并存", func(t *testing.T) {
		bad := rings.TestClaimRecord{ClaimID: "tc-e", Type: rings.TestClaimRecRepro,
			NoReproReason: "无可用复现包：…",
			Steps:         []rings.ReproStep{{Seq: 1, Method: "GET", URL: "http://k9.local/", Status: 200}}}
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: bad,
		}); err == nil {
			t.Fatal("步骤与无复现包原因并存应被账本拒绝(倒池形态物理进不了账)")
		}
		good := rings.TestClaimRecord{ClaimID: "tc-e", Type: rings.TestClaimRecRepro, NoReproReason: "无可用复现包：…"}
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventTestClaimRecord, Actor: ActorOvermind, Payload: good,
		}); err != nil {
			t.Fatalf("纯原因记录应被接受: %v", err)
		}
	})
}
