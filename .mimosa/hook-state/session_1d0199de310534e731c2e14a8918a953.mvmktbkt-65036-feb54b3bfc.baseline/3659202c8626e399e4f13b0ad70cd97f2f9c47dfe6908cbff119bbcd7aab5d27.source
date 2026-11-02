package mirror

// P3-01 验收套件:HTTPS 强制接管(本地自签 mock 靶站先行验证)。
//   - 解密留痕与逐字回放(请求行/头/体、状态行/头/体,与 HTTP 同粒度);
//   - 自签/过期/未知签发目标照常处理并如实标注;
//   - 真实 curl 子进程经 ProxyEnv + SSL_CERT_FILE 信任安装实测;
//   - 非 TLS CONNECT 透传兜底、客户端拒信镜 CA 的如实失败留痕;
//   - 对账:目标命中数与留痕数一致(100% 经镜)、R18(原文入 blob 不入账本)。

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// customTLSServer 以指定证书起 HTTPS mock 靶站。
func customTLSServer(t *testing.T, cert tls.Certificate, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// selfSignedCert 自签靶站证书(IP SAN 127.0.0.1)。
func selfSignedCert(t *testing.T, cn string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成靶站密钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: cn},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("自签靶站证书失败: %v", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func waitRecords(t *testing.T, store *rings.Store, want int) []rings.MirrorRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs := mirrorRecords(t, store)
		if len(recs) >= want {
			return recs
		}
		if time.Now().After(deadline) {
			t.Fatalf("留痕 %d 条,期望 %d(等待超时): %+v", len(recs), want, recs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHTTPSDecryptFullCaptureAndReplay 核心验收:解密留痕 + 逐字回放。
func TestHTTPSDecryptFullCaptureAndReplay(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)

	const respBody = `{"service":"gr-mock-https","version":"3.0.0"}`
	var hits atomic.Int64
	var seenBody atomic.Value // string:目标收到的请求体
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/api/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(respBody))
			return
		}
		b, _ := io.ReadAll(r.Body)
		seenBody.Store(string(b))
		w.WriteHeader(201)
		_, _ = w.Write([]byte("created"))
	}))
	defer origin.Close()

	// 工具视角:标准客户端请求自签 HTTPS 靶站——只信任镜 CA(信任安装),
	// 响应正常,接管透明。
	client := proxiedClient(sess)
	resp, err := client.Get(origin.URL + "/api/version")
	if err != nil {
		t.Fatalf("经镜 HTTPS 请求失败: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != respBody {
		t.Fatalf("工具侧响应不符: %d %q", resp.StatusCode, got)
	}
	const reqBody = `{"cred":"post-body-逐字保真"}`
	resp, err = client.Post(origin.URL+"/api/login", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("经镜 HTTPS POST 失败: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	recs := waitRecords(t, store, 2)
	if hits.Load() != 2 {
		t.Fatalf("目标命中 %d 次,期望 2", hits.Load())
	}
	if len(recs) != 2 || recs[0].Type != rings.MirrorRecHTTPS || recs[1].Type != rings.MirrorRecHTTPS {
		t.Fatalf("应恰 2 条 https 记录: %+v", recs)
	}
	for _, rec := range recs {
		if rec.StepID != "s1" || rec.UnitID != "u1" || rec.TranscriptID != "swarm-u1" || rec.BlobRef == "" {
			t.Fatalf("归因/引用不符: %+v", rec)
		}
		if rec.TLS != TLSValidSelfSigned {
			t.Fatalf("自签靶站应如实标注 self_signed: %+v", rec)
		}
	}
	get := recs[0]
	if get.Method != "GET" || get.Status != 200 || !strings.HasPrefix(get.URL, "https://127.0.0.1:") || !strings.HasSuffix(get.URL, "/api/version") {
		t.Fatalf("GET 摘要不符: %+v", get)
	}
	if recs[1].Method != "POST" || recs[1].Status != 201 || recs[1].RespBytes != int64(len("created")) {
		t.Fatalf("POST 摘要不符: %+v", recs[1])
	}

	// 逐字回放:交换文档与真实交换逐字一致(方法/URL/请求体/响应体/关键头)。
	var exG, exP Exchange
	if err := unmarshalBlob(blobs, get.BlobRef, &exG); err != nil {
		t.Fatalf("解码 GET 交换文档失败: %v", err)
	}
	if err := unmarshalBlob(blobs, recs[1].BlobRef, &exP); err != nil {
		t.Fatalf("解码 POST 交换文档失败: %v", err)
	}
	originHost := mustURL(t, origin.URL).Host
	if exG.ID != get.ExchangeID || exG.Request.Method != "GET" ||
		exG.Request.URL != get.URL || exG.Request.Host != originHost ||
		exG.Response.Status != 200 || string(exG.Response.Body) != respBody {
		t.Fatalf("GET 回放不符: %+v %+v", exG, exG.Request)
	}
	if !hasHeader(exG.Response.Headers, "Content-Type", "application/json") {
		t.Fatalf("GET 回放应含 Content-Type: %+v", exG.Response.Headers)
	}
	if string(exP.Request.Body) != reqBody || seenBody.Load().(string) != reqBody {
		t.Fatalf("POST 请求体应逐字一致: 留痕 %q / 目标收到 %q", exP.Request.Body, seenBody.Load())
	}
	if string(exP.Response.Body) != "created" {
		t.Fatalf("POST 响应体不符: %q", exP.Response.Body)
	}
	if exG.TLS == nil || exG.TLS.Host != "127.0.0.1" || exG.TLS.Validity != TLSValidSelfSigned ||
		exG.TLS.PeerSubject == "" || exG.TLS.PeerIssuer == "" || exG.TLS.VerifyError == "" {
		t.Fatalf("TLS 事实应入档(主体/签发方/原始校验错误): %+v", exG.TLS)
	}

	// R18:账本负载只存引用与摘要,不含双向流量体原文。
	for _, pl := range [][]byte{lastMirrorPayload(t, store), recPayload(t, store, get.ExchangeID), recPayload(t, store, recs[1].ExchangeID)} {
		if strings.Contains(string(pl), respBody) || strings.Contains(string(pl), reqBody) {
			t.Fatal("账本负载不得内嵌流量体原文(R18)")
		}
	}
	// 回放全集(审计入口)。
	exs, err := Exchanges(context.Background(), store, blobs, "t1")
	if err != nil || len(exs) != 2 || exs[1].TLS == nil || exs[1].TLS.Validity != TLSValidSelfSigned {
		t.Fatalf("回放全集不符: %v %+v", err, exs)
	}
	if sess.Lost() != 0 {
		t.Fatalf("留痕失败计数应 0: %d", sess.Lost())
	}
}

// recPayload 取指定交换的账本负载(单条对账用)。
func recPayload(t *testing.T, store *rings.Store, exchangeID string) []byte {
	t.Helper()
	for _, rec := range mirrorRecords(t, store) {
		if rec.ExchangeID == exchangeID {
			evs, err := store.Events(context.Background(), "t1", 0, 0)
			if err != nil {
				t.Fatalf("查事件流失败: %v", err)
			}
			for _, ev := range evs {
				if ev.Kind == rings.EventMirrorRecord && strings.Contains(string(ev.Payload), exchangeID) {
					return ev.Payload
				}
			}
		}
	}
	return nil
}

func unmarshalBlob(blobs *memBlobs, ref string, v any) error {
	data := blobs.get(ref)
	if data == nil {
		return fmt.Errorf("blob %s 不存在", ref)
	}
	return json.Unmarshal(data, v)
}

// TestHTTPSExpiredTargetProceedsAnnotated 过期靶站:照常处理 + 如实标注。
func TestHTTPSExpiredTargetProceedsAnnotated(t *testing.T) {
	_, sess, store, _ := newTestMirror(t)
	now := time.Now()
	cert := selfSignedCert(t, "expired.target", now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	origin := customTLSServer(t, cert, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("expired-but-alive"))
	})

	client := proxiedClient(sess)
	resp, err := client.Get(origin.URL + "/x")
	if err != nil {
		t.Fatalf("过期靶站经镜应正常处理: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "expired-but-alive" {
		t.Fatalf("响应不符: %q", body)
	}
	recs := waitRecords(t, store, 1)
	if recs[0].Type != rings.MirrorRecHTTPS || recs[0].TLS != TLSValidExpired {
		t.Fatalf("过期靶站应如实标注 expired: %+v", recs[0])
	}
}

// TestHTTPSUntrustedIssuerAnnotated 未知签发方(非自签)与自签区分标注。
func TestHTTPSUntrustedIssuerAnnotated(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)
	ca, caKey := testCA(t, "Mock Issuer CA")
	leaf, leafKey := testLeaf(t, ca, caKey, "127.0.0.1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	origin := customTLSServer(t,
		tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey, Leaf: leaf},
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("issued")) })

	client := proxiedClient(sess)
	resp, err := client.Get(origin.URL + "/x")
	if err != nil {
		t.Fatalf("未知签发靶站经镜应正常处理: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	recs := waitRecords(t, store, 1)
	if recs[0].TLS != TLSValidUntrusted {
		var dbg Exchange
		_ = unmarshalBlob(blobs, recs[0].BlobRef, &dbg)
		t.Fatalf("未知签发应标 untrusted(与自签区分): %+v tls=%+v", recs[0], dbg.TLS)
	}
}

// TestHTTPSTargetNotTLSRecorded 目标端口非 TLS:请求失败如实留痕。
func TestHTTPSTargetNotTLSRecorded(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain-http"))
	}))
	defer origin.Close()

	client := proxiedClient(sess)
	resp, err := client.Get(strings.Replace(origin.URL, "http://", "https://", 1) + "/x")
	if err != nil {
		t.Fatalf("代理应回 502 响应而非传输错误: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("工具侧应见 502: %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	recs := waitRecords(t, store, 1)
	if recs[0].Type != rings.MirrorRecHTTPS || recs[0].Error == "" || recs[0].Status != 0 {
		t.Fatalf("目标非 TLS 应留 https 失败记录: %+v", recs[0])
	}
	if !strings.Contains(recs[0].Error, "目标 TLS 握手失败") {
		t.Fatalf("失败原因应如实: %q", recs[0].Error)
	}
	if recs[0].BlobRef == "" || !blobs.has(recs[0].BlobRef) {
		t.Fatalf("失败交换也应有 blob 文档: %+v", recs[0])
	}
}

// TestHTTPSClientRejectsMirrorCA 客户端拒信镜 CA:接管失败如实入账(tunnel)。
func TestHTTPSClientRejectsMirrorCA(t *testing.T) {
	_, sess, store, _ := newTestMirror(t)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unreachable"))
	}))
	defer origin.Close()

	// 不信任镜 CA 的客户端(系统默认信任池):TLS 握手被客户端拒绝。
	untrusting := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyURL(mustURL(t, "http://"+sess.Addr())),
	}}
	resp, err := untrusting.Get(origin.URL + "/x")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("不信任镜 CA 的客户端不应成功")
	}
	recs := waitRecords(t, store, 1)
	if recs[0].Type != rings.MirrorRecTunnel || !strings.Contains(recs[0].Error, "解密接管") {
		t.Fatalf("接管失败应记 tunnel 错误(不冒充 https): %+v", recs[0])
	}
}

// TestHTTPSPassthroughFallbackNonTLS 非 TLS 的 CONNECT:透传兜底 + 元数据留痕。
func TestHTTPSPassthroughFallbackNonTLS(t *testing.T) {
	_, sess, store, _ := newTestMirror(t)
	// 透传层经 MultiReader 回放窥探首字节,首字节与后续载荷可能分两次
	// 写达——echo 必须收满整段载荷再回显(单次 Read 会在两次写之间只
	// 读到首字节即回显关闭,测试端 ReadFull 永等到隧道寿命兜底,全量
	// 聚载下间歇复现;透传转发本身行为正确)。
	const payload = "raw-bytes-not-tls"
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 TCP echo 失败: %v", err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { // 单问单答:收满载荷即回显,模拟简单 TCP 协议
				defer func() { _ = c.Close() }()
				buf := make([]byte, len(payload))
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write(buf)
			}(c)
		}
	}()

	conn, err := net.DialTimeout("tcp", sess.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("连镜失败: %v", err)
	}
	defer func() { _ = conn.Close() }()
	target := echo.Addr().String()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		t.Fatalf("隧道应建立: %q %v", status, err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("写透传数据失败: %v", err)
	}
	echoed := make([]byte, len(payload))
	if _, err := io.ReadFull(br, echoed); err != nil {
		t.Fatalf("读透传回显失败: %v", err)
	}
	if string(echoed) != payload {
		t.Fatalf("透传回显不符: %q", echoed)
	}
	_ = conn.Close()

	recs := waitRecords(t, store, 1)
	rec := recs[0]
	if rec.Type != rings.MirrorRecTunnel || rec.URL != target ||
		rec.ReqBytes < int64(len(payload)) || rec.RespBytes < int64(len(payload)) || rec.BlobRef != "" {
		t.Fatalf("透传应记 tunnel 元数据: %+v", rec)
	}
}

// TestCurlThroughMirrorHTTPS 真实 curl 子进程:ProxyEnv + SSL_CERT_FILE 信任安装实测。
func TestCurlThroughMirrorHTTPS(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("环境无 curl,跳过真实子进程接管验证")
	}
	_, sess, store, blobs := newTestMirror(t)
	const body = "curl-secret-body"
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()

	// 信任安装自证:ProxyEnv 带信任包且文件在、含镜 CA。
	env := sess.ProxyEnv()
	bundle := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "SSL_CERT_FILE=") {
			bundle = strings.TrimPrefix(kv, "SSL_CERT_FILE=")
		}
	}
	if bundle == "" {
		t.Fatal("ProxyEnv 应注入 SSL_CERT_FILE(信任安装)")
	}
	pemBytes, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatalf("信任包应存在: %v", err)
	}
	if !strings.Contains(string(pemBytes), "BEGIN CERTIFICATE") {
		t.Fatal("信任包应含证书")
	}

	cmd := exec.Command(curl, "-sS", "--max-time", "30", origin.URL+"/secret")
	cmd.Env = append(cleanProxyEnv(os.Environ()), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("curl 经镜失败: %v\n%s", err, out)
	}
	if string(out) != body {
		t.Fatalf("curl 响应不符: %q", out)
	}
	recs := waitRecords(t, store, 1)
	if recs[0].Type != rings.MirrorRecHTTPS || recs[0].TLS != TLSValidSelfSigned ||
		recs[0].Method != "GET" || recs[0].Status != 200 || recs[0].BlobRef == "" {
		t.Fatalf("curl 流量应解密留痕: %+v", recs[0])
	}
	var ex Exchange
	if err := unmarshalBlob(blobs, recs[0].BlobRef, &ex); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if string(ex.Response.Body) != body || !strings.Contains(ex.Request.URL, "/secret") {
		t.Fatalf("curl 交换回放不符: %+v", ex)
	}
	uaOK := false
	for _, h := range ex.Request.Headers {
		if h.Name == "User-Agent" && strings.HasPrefix(h.Value, "curl/") {
			uaOK = true
		}
	}
	if !uaOK {
		t.Fatalf("应捕获 curl 原始 User-Agent: %+v", ex.Request.Headers)
	}
}

// cleanProxyEnv 剔除外层代理/CA 环境变量(防测试环境污染接管)。
func cleanProxyEnv(in []string) []string {
	var out []string
	for _, kv := range in {
		k := strings.SplitN(kv, "=", 2)[0]
		lk := strings.ToLower(k)
		if strings.HasSuffix(lk, "_proxy") || k == "SSL_CERT_FILE" || k == "REQUESTS_CA_BUNDLE" || k == "CURL_CA_BUNDLE" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// TestHTTPSRedirectPerHopRecorded 重定向逐跳留痕(内层连接复用)。
func TestHTTPSRedirectPerHopRecorded(t *testing.T) {
	_, sess, store, _ := newTestMirror(t)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loop" {
			w.Header().Set("Location", "/landed")
			w.WriteHeader(302)
			return
		}
		w.WriteHeader(200)
	}))
	defer origin.Close()

	client := proxiedClient(sess)
	resp, err := client.Get(origin.URL + "/loop")
	if err != nil {
		t.Fatalf("经镜 HTTPS 重定向失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Request.URL.Path != "/landed" {
		t.Fatalf("应正常落到 /landed: %d", resp.StatusCode)
	}
	recs := waitRecords(t, store, 2)
	if len(recs) != 2 || recs[0].Status != 302 || recs[1].Status != 200 {
		t.Fatalf("重定向两跳应各留一痕: %+v", recs)
	}
	if !strings.HasSuffix(recs[0].URL, "/loop") || !strings.HasSuffix(recs[1].URL, "/landed") {
		t.Fatalf("两跳 URL 不符: %+v", recs)
	}
}

// TestHTTPSConcurrentAllCaptured 对账:并发 40 请求 100% 留痕,回放全集保真。
func TestHTTPSConcurrentAllCaptured(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok:" + r.URL.Path))
	}))
	defer origin.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := proxiedClient(sess)
			for j := 0; j < 5; j++ {
				resp, err := c.Get(fmt.Sprintf("%s/c%d-%d", origin.URL, i, j))
				if err != nil {
					t.Errorf("并发请求失败: %v", err)
					return
				}
				_ = resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()
	client := proxiedClient(sess) // 收尾再打一条确保会话内已有记录可对账
	resp, err := client.Get(origin.URL + "/final")
	if err != nil {
		t.Fatalf("收尾请求失败: %v", err)
	}
	_ = resp.Body.Close()

	recs := waitRecords(t, store, 41)
	if len(recs) != 41 {
		t.Fatalf("41 次命中应留 41 痕(非 100%% 经镜),得到 %d", len(recs))
	}
	for _, rec := range recs {
		if rec.Type != rings.MirrorRecHTTPS || rec.BlobRef == "" || !blobs.has(rec.BlobRef) {
			t.Fatalf("每痕须 https 且带 blob: %+v", rec)
		}
	}
	if sess.Lost() != 0 {
		t.Fatalf("留痕失败计数应 0: %d", sess.Lost())
	}
	exs, err := Exchanges(context.Background(), store, blobs, "t1")
	if err != nil || len(exs) != 41 {
		t.Fatalf("回放 41 条: %v %d", err, len(exs))
	}
	for _, ex := range exs {
		if string(ex.Response.Body) != "ok:"+mustURL(t, ex.Request.URL).Path {
			t.Fatalf("回放内容失真: %q", ex.Response.Body)
		}
	}
}

// TestHTTPSBigBodyGoesBlobNotLedger 大正文:原文入 blob,账本零内嵌(R18)。
func TestHTTPSBigBodyGoesBlobNotLedger(t *testing.T) {
	_, sess, store, blobs := newTestMirror(t)
	big := strings.Repeat("x", 512<<10)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer origin.Close()

	client := proxiedClient(sess)
	resp, err := client.Get(origin.URL + "/big")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	recs := waitRecords(t, store, 1)
	if recs[0].RespBytes != int64(len(big)) {
		t.Fatalf("响应字节不符: %d", recs[0].RespBytes)
	}
	if pl := lastMirrorPayload(t, store); len(pl) >= len(big) {
		t.Fatalf("账本负载 %d 字节,不得内嵌大流量体(R18)", len(pl))
	}
	var ex Exchange
	if err := unmarshalBlob(blobs, recs[0].BlobRef, &ex); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(ex.Response.Body) != len(big) {
		t.Fatalf("blob 应保真全文: %d", len(ex.Response.Body))
	}
}
