package mirror

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 留痕容量与超时(最小链路口径)。
const (
	maxBodyBytes    = 16 << 20 // 单方向流量体捕获上限,超出截断并如实标记
	outboundTimeout = 30 * time.Second
	tunnelDial      = 10 * time.Second
	tunnelMaxLife   = 5 * time.Minute // 隧道寿命上限(Close 不被长隧道挂死)
)

// BlobSink 内容寻址写入侧(swarm.BlobStore 满足;镜只依赖该窄接口,
// 避免与 swarm 包互相导入)。
type BlobSink interface {
	Write(data []byte) (ref string, err error)
}

// BlobSource 内容寻址读回侧(回放查证用)。
type BlobSource interface {
	Read(ref string) ([]byte, error)
}

// Meta 会话归因:每条留痕盖章,流量钉回图上的执行记录。
type Meta struct {
	TaskID       string
	StepID       string
	UnitID       string
	TranscriptID string
}

// Mirror 镜核:持账本写入与 blob 存储的进程级对象(第七章"Manager 持
// 全局共享:镜"),出站直连(镜自身不走镜)。零值不可用,New 构建。
// P3-01 起持 CA 与信任池:HTTPS CONNECT 解密接管(CA 生成/持久化与
// 目标证书分类见 ca.go,接管流程见 mitm.go)。
type Mirror struct {
	store *rings.Store
	blobs BlobSink
	out   *http.Client

	// CA 与信任(P3-01)。pool=系统根+镜 CA(工具客户端 RootCAs);
	// bundlePath=信任包文件(子进程 SSL_CERT_FILE 指向);空则子进程
	// 不注入(降级可见,见 writeTrustBundle)。
	ca         tls.Certificate
	caCert     *x509.Certificate
	pool       *x509.CertPool
	bundlePath string
	leafMu     sync.Mutex
	leaves     map[string]*tls.Certificate
}

// New 构建镜核。blobs 须与虫群执行器共用同一内容寻址存储(引用互查);
// dir 为数据目录(信任包文件落点,如任务根目录)。CA 经设置表持久化,
// 重启不轮换。
func New(store *rings.Store, blobs BlobSink, dir string) *Mirror {
	m := &Mirror{
		store: store,
		blobs: blobs,
		// 出站直连:显式 Proxy=nil,进程环境里的代理变量一律不吃
		//(否则镜自身出站可能绕去别的代理);重定向不跟随——每一跳
		//由工具侧客户端决定是否追,追则再次经镜,逐跳留痕。
		out: &http.Client{
			Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: tunnelDial}).DialContext},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: outboundTimeout,
		},
	}
	m.initCA(dir)
	return m
}

// Session 每虫群单位一个会话前端:本地随机端口 + 归因元数据。
// 会话存活期即单位执行期,Close 等待在飞交换留痕落账后再关。
func (m *Mirror) Session(meta Meta) (*Session, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("mirror: 起会话监听: %w", err)
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("mirror: 生成会话 ID: %w", err)
	}
	s := &Session{
		m: m, meta: meta,
		id:      "ms-" + hex.EncodeToString(b[:]),
		proxy:   &url.URL{Scheme: "http", Host: ln.Addr().String()},
		tunnels: map[net.Conn]struct{}{},
	}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.serve),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Session 一个虫群单位的镜会话前端。零值不可用,由 Mirror.Session 构建。
type Session struct {
	m     *Mirror
	meta  Meta
	id    string
	proxy *url.URL
	srv   *http.Server
	seq   atomic.Int64
	wg    sync.WaitGroup
	lost  atomic.Int64 // blob/账本写入失败次数(诊断;失败不阻断代理服务)

	tunMu   sync.Mutex
	tunnels map[net.Conn]struct{} // 劫持中的隧道连接(Close 时强制收拢)
}

// Addr 会话监听地址(127.0.0.1:port)。
func (s *Session) Addr() string { return s.proxy.Host }

// HTTPClient 经镜的 HTTP 客户端:传输层显式 Proxy 恒指本会话地址;
// TLS 信任池含镜 CA(HTTPS 解密接管对工具透明——工具拿到的就是标准
// *http.Client,证书按系统根+镜 CA 验证)。工具侧零镜知识;镜会话
// 关闭后该客户端的请求将失败(强制经镜:无直连旁路)。
func (s *Session) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(s.proxy),
			TLSClientConfig: &tls.Config{RootCAs: s.m.pool},
		},
	}
}

// ProxyEnv 注入子进程的代理环境变量(bash 里的 curl/wget 等守规矩工具
// 的接管通道)。大小写双份:curl 系读小写 http_proxy,通用读大写。
// P3-01 起随发 CA 信任安装:SSL_CERT_FILE/REQUESTS_CA_BUNDLE 指向
// 信任包(系统根+镜 CA)——curl/python 经镜的 HTTPS 才信任镜的解密
// 叶证书,且公开 CA 目标不受影响。
func (s *Session) ProxyEnv() []string {
	a := "http://" + s.Addr()
	env := []string{
		"HTTP_PROXY=" + a, "http_proxy=" + a,
		"HTTPS_PROXY=" + a, "https_proxy=" + a,
	}
	if s.m.bundlePath != "" {
		env = append(env, "SSL_CERT_FILE="+s.m.bundlePath, "REQUESTS_CA_BUNDLE="+s.m.bundlePath)
	}
	return env
}

// Close 关闭会话:停监听,强制收拢劫持中的隧道连接(其字节计数在
// 隧道关闭时定案),等待在飞交换完成留痕。
func (s *Session) Close() error {
	err := s.srv.Close()
	s.tunMu.Lock()
	for c := range s.tunnels {
		_ = c.Close()
	}
	s.tunMu.Unlock()
	s.wg.Wait()
	return err
}

// trackTunnel 登记劫持的隧道连接;返回结束登记函数。
// 关闭通知双保险:会话 Close 广播 + 连接强制关闭。
func (s *Session) trackTunnel(c net.Conn) func() {
	s.tunMu.Lock()
	s.tunnels[c] = struct{}{}
	s.tunMu.Unlock()
	return func() {
		s.tunMu.Lock()
		delete(s.tunnels, c)
		s.tunMu.Unlock()
	}
}

// Lost 留痕失败计数(blob/账本写入失败;审计侧应关注非零会话)。
func (s *Session) Lost() int64 { return s.lost.Load() }

func (s *Session) nextID() string {
	return fmt.Sprintf("%s.%d", s.id, s.seq.Add(1))
}

func (s *Session) serve(w http.ResponseWriter, r *http.Request) {
	s.wg.Add(1)
	defer s.wg.Done()
	if r.Method == http.MethodConnect {
		s.serveTunnel(w, r)
		return
	}
	s.serveExchange(w, r)
}

// ---- HTTP 交换:全量留痕 ----

// serveExchange 处理绝对形式代理请求:捕获原始请求(保真,含代理形态
// 头)→ 直连转发 → 捕获响应 → 原样写回 → 完整交换文档入 blob、摘要入
// 账本。转发失败也留痕(有失败记录才算全量可审计)。
func (s *Session) serveExchange(w http.ResponseWriter, r *http.Request) {
	ex := &Exchange{
		ID: s.nextID(), Type: rings.MirrorRecHTTP,
		TaskID: s.meta.TaskID, StepID: s.meta.StepID,
		UnitID: s.meta.UnitID, TranscriptID: s.meta.TranscriptID,
		StartedAt: time.Now(),
		Request: &Request{
			Method: r.Method, URL: r.URL.String(), Proto: r.Proto, Host: r.Host,
			Headers: flatHeaders(r.Header),
		},
	}
	reqBody, trunc, err := readCapped(r.Body)
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return // 尚无完整请求可留痕
	}
	ex.Request.Body, ex.Request.BodyTruncated = reqBody, trunc

	fwd, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), bytes.NewReader(reqBody))
	if err != nil {
		ex.Error = "构造转发请求: " + err.Error()
		s.record(ex)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	fwd.ContentLength = int64(len(reqBody))
	copyHeaders(fwd.Header, r.Header)
	fwd.Host = r.Host

	resp, err := s.m.out.Do(fwd)
	if err != nil {
		ex.Error = "转发失败: " + err.Error()
		ex.ElapsedMS = msSince(ex.StartedAt)
		s.record(ex)
		// 代理惯例:上游不可达回 502(通用文案,不暴露镜的存在;
		//详细原因在账本留痕的 Error 字段)。
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	respBody, rtrunc, _ := readCapped(resp.Body)
	_ = resp.Body.Close()
	statusText := resp.Status
	if code := fmt.Sprintf("%d ", resp.StatusCode); strings.HasPrefix(statusText, code) {
		statusText = strings.TrimPrefix(statusText, code)
	}
	ex.Response = &Response{
		Proto: resp.Proto, Status: resp.StatusCode, StatusText: statusText,
		Headers: flatHeaders(resp.Header), Body: respBody, BodyTruncated: rtrunc,
	}
	ex.ReqBytes, ex.RespBytes = int64(len(reqBody)), int64(len(respBody))

	// 原样写回(剥 hop-by-hop 头;Transfer-Encoding 由服务端重新组帧)。
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	ex.ElapsedMS = msSince(ex.StartedAt)
	s.record(ex)
}

// ---- CONNECT 隧道:解密接管(P3-01)/ 透传兜底 ----

// serveTunnel 处理 CONNECT:首字节判型——TLS ClientHello 走解密接管
// (逐交换全量留痕,与 http 同粒度);其余照旧双向透传(非 TLS 协议
// 兜底,元数据留痕,tunnel 类型如实区分)。
//
// P4-62 C(a)(问题清单 #83):TLS 隧道先与目标侧握手(镜上游不校验
// 目标证书,定死;见 mitm.go)——目标侧握手失败(协议层拒签等目标
// TLS 形态问题)时整条隧道退透传:已窥探的 ClientHello 原样回放给
// 目标(重新拨号,握手失败的连接状态不可信),工具与目标端到端直连,
// 镜只留隧道元数据。镜是留痕设施不是验证者,不因目标证书/TLS 形态
// 阻断流量;解密内容缺失时的复现原料由工具调用记录组包补(overmind)。
func (s *Session) serveTunnel(w http.ResponseWriter, r *http.Request) {
	target := tunnelTarget(r)
	started := time.Now()
	hj, ok := w.(http.Hijacker)
	if !ok {
		ex := s.newTunnelExchange(target, started)
		ex.Error = "服务端不支持 Hijack"
		s.record(ex)
		http.Error(w, "tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		ex := s.newTunnelExchange(target, started)
		ex.Error = "劫持客户端连接: " + err.Error()
		s.record(ex)
		return
	}
	targetConn, err := net.DialTimeout("tcp", target, tunnelDial)
	if err != nil {
		ex := s.newTunnelExchange(target, started)
		ex.Error = "隧道拨号失败: " + err.Error()
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		_ = client.Close()
		s.record(ex)
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		ex := s.newTunnelExchange(target, started)
		ex.Error = "回隧道建立应答: " + err.Error()
		_ = client.Close()
		_ = targetConn.Close()
		s.record(ex)
		return
	}

	// 判型:窥探首字节(可能连 ClientHello 后续一并缓冲)。
	br := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(peekTimeout))
	first, err := br.ReadByte()
	_ = client.SetReadDeadline(time.Time{})
	if err != nil {
		ex := s.newTunnelExchange(target, started)
		ex.Error = "隧道首字节读取失败: " + err.Error()
		_ = client.Close()
		_ = targetConn.Close()
		s.record(ex)
		return
	}
	if first == tlsFirstByte {
		up := &mitmUp{s: s, target: target, raw: targetConn}
		if err := up.ensure(); err != nil {
			// 目标侧握手失败:退透传(重拨,失败的连接状态不可信);
			// 原因随透传留痕一并入账(单条 tunnel 记录)。
			fresh, derr := net.DialTimeout("tcp", target, tunnelDial)
			if derr != nil {
				ex := s.newTunnelExchange(target, started)
				ex.Error = "解密接管不可行(" + err.Error() + ");透传重拨失败: " + derr.Error()
				_ = client.Close()
				s.record(ex)
				return
			}
			s.servePassthroughNote(client, br, fresh, target, started, first,
				"解密接管不可行,退透传: "+err.Error())
			return
		}
		s.serveMITM(client, br, up, started, first)
		return
	}
	s.servePassthrough(client, br, targetConn, target, started, first)
}

// servePassthrough 非 TLS 隧道透传(P2-01 行为):双向字节搬运,不解读
// 内容;隧道元数据(目标、双向字节、时长、错误)入账。首字节经
// MultiReader 回放,窥探缓冲一并交还。
func (s *Session) servePassthrough(client net.Conn, br *bufio.Reader, target net.Conn, url string, started time.Time, first byte) {
	s.servePassthroughNote(client, br, target, url, started, first, "")
}

// servePassthroughNote 透传(可携带注记:P4-62 C(a) 上游握手失败退
// 透传时,原因随本条 tunnel 记录一并如实入账)。
func (s *Session) servePassthroughNote(client net.Conn, br *bufio.Reader, target net.Conn, url string, started time.Time, first byte, note string) {
	ex := s.newTunnelExchange(url, started)
	ex.Error = note
	untrack := s.trackTunnel(client)
	defer untrack()
	// 寿命上限防长隧道挂死;会话 Close 亦强制收拢(trackTunnel)。
	deadline := time.AfterFunc(tunnelMaxLife, func() { _ = client.Close(); _ = target.Close() })
	var up, down atomic.Int64
	done := make(chan struct{}, 2)
	cp := func(dst io.WriteCloser, src io.Reader, n *atomic.Int64) {
		written, _ := io.Copy(dst, src)
		n.Add(written)
		done <- struct{}{}
	}
	go cp(target, io.MultiReader(bytes.NewReader([]byte{first}), br), &up)
	go cp(client, target, &down)
	<-done
	<-done
	deadline.Stop()
	_ = client.Close()
	_ = target.Close()

	ex.ReqBytes, ex.RespBytes = up.Load(), down.Load()
	ex.ElapsedMS = msSince(started)
	s.record(ex)
}

// tunnelTarget CONNECT 的 authority 形式目标(host:port)。
func tunnelTarget(r *http.Request) string {
	if r.URL != nil && r.URL.Host != "" {
		return r.URL.Host
	}
	return r.Host
}

// ---- 留痕落账 ----

// record 完整交换文档入 blob(R18)、摘要事件入账本。http/https 全量
// 留痕(文档入 blob);tunnel 仅元数据。账本写入用独立 ctx:留痕不因
// 任务熔断而断链(同虫群 ledgerCtx 惯例)。blob/账本失败不阻断代理
// 服务,但计入 Lost 计数并打日志——审计侧可据此发现缺口。
func (s *Session) record(ex *Exchange) {
	if ex.Type == rings.MirrorRecHTTP || ex.Type == rings.MirrorRecHTTPS {
		doc, err := json.Marshal(ex)
		if err != nil {
			s.lost.Add(1)
			runlog.Warnf("镜留痕降级: 序列化交换 %s 失败: %v", ex.ID, err)
			return
		}
		ref, err := s.m.blobs.Write(doc)
		if err != nil {
			s.lost.Add(1)
			runlog.Errorf("镜留痕入 blob 失败: 交换 %s: %v", ex.ID, err)
			return
		}
		ex.BlobRef = ref
	}
	method, url, status := ex.summary()
	rec := rings.MirrorRecord{
		ExchangeID: ex.ID, Type: ex.Type,
		StepID: ex.StepID, UnitID: ex.UnitID, TranscriptID: ex.TranscriptID,
		Method: method, URL: url, Status: status,
		ReqBytes: ex.ReqBytes, RespBytes: ex.RespBytes,
		BlobRef: ex.BlobRef, ElapsedMS: ex.ElapsedMS, Error: ex.Error,
	}
	if ex.TLS != nil {
		rec.TLS = ex.TLS.Validity
	}
	if _, err := s.m.store.Append(context.Background(), ex.TaskID, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror",
		OccurredAt: ex.StartedAt, Payload: rec,
	}); err != nil {
		s.lost.Add(1)
		runlog.Errorf("账本写入失败: 镜交换 %s: %v", ex.ID, err)
	}
}

// ---- 交换文档(blob 里的完整原文,回放查证的载体) ----

// Header 名值对(保留重复值;名经 Go 规范化,顺序按名排序保证文档确定性)。
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Request 留痕侧请求原文(工具发给镜的代理形态原样保真)。
type Request struct {
	Method        string   `json:"method"`
	URL           string   `json:"url"`
	Proto         string   `json:"proto,omitempty"`
	Host          string   `json:"host,omitempty"` // 代理形态下 Host 在请求行外独立携带(不在 Header 集)
	Headers       []Header `json:"headers,omitempty"`
	Body          []byte   `json:"body,omitempty"` // encoding/json 自动 base64
	BodyTruncated bool     `json:"body_truncated,omitempty"`
}

// Response 留痕侧响应原文(镜从目标收到的原样保真)。
type Response struct {
	Proto         string   `json:"proto,omitempty"`
	Status        int      `json:"status"`
	StatusText    string   `json:"status_text,omitempty"`
	Headers       []Header `json:"headers,omitempty"`
	Body          []byte   `json:"body,omitempty"`
	BodyTruncated bool     `json:"body_truncated,omitempty"`
}

// Exchange 一次流量交换的完整留痕文档,入内容寻址存储(R18)。
// 字段为确定顺序的结构体,同内容字节级一致。
type Exchange struct {
	ID   string `json:"id"`
	Type string `json:"type"` // rings.MirrorRecHTTP | MirrorRecHTTPS | MirrorRecTunnel

	TaskID       string `json:"task_id"`
	StepID       string `json:"step_id"`
	UnitID       string `json:"unit_id"`
	TranscriptID string `json:"transcript_id,omitempty"`

	Request  *Request  `json:"request,omitempty"`  // http / https
	Response *Response `json:"response,omitempty"` // http / https

	// URL 仅 tunnel 用(CONNECT 目标);http/https 的 URL 在 Request.URL。
	URL string `json:"url,omitempty"`

	// TLS 仅 https 用(P3-01 解密留痕):目标侧证书校验事实。
	TLS *TLSInfo `json:"tls,omitempty"`

	// 隧道方向计数(tunnel);http/https 侧同字段表请求/响应字节数。
	ReqBytes  int64 `json:"req_bytes,omitempty"`
	RespBytes int64 `json:"resp_bytes,omitempty"`

	BlobRef   string    `json:"blob_ref,omitempty"` // 本文档自身的引用(账本事件与文档互指)
	StartedAt time.Time `json:"started_at"`
	ElapsedMS int64     `json:"elapsed_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// summary 摘要三元组(记账用):方法/URL/状态。
func (ex *Exchange) summary() (method, url string, status int) {
	if ex.Request != nil {
		method, url = ex.Request.Method, ex.Request.URL
	} else {
		url = ex.URL
	}
	if ex.Response != nil {
		status = ex.Response.Status
	}
	return
}

// Exchanges 回放查证:按账本事件流顺序还原任务的全部镜留痕。
// http/https 记录经 blob 引用读回完整交换文档;tunnel 记录以账本字段
// 呈现(无内容文档)。审计时从 Step 一路下钻到 HTTP 原文即走此函数。
func Exchanges(ctx context.Context, store *rings.Store, blobs BlobSource, taskID string) ([]Exchange, error) {
	evs, err := store.Events(ctx, taskID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("mirror: 查事件流: %w", err)
	}
	var out []Exchange
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var rec rings.MirrorRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			return nil, fmt.Errorf("mirror: 解码 mirror.record: %w", err)
		}
		if (rec.Type != rings.MirrorRecHTTP && rec.Type != rings.MirrorRecHTTPS) || rec.BlobRef == "" {
			out = append(out, Exchange{
				ID: rec.ExchangeID, Type: rec.Type, TaskID: taskID,
				StepID: rec.StepID, UnitID: rec.UnitID, TranscriptID: rec.TranscriptID,
				URL: rec.URL, ReqBytes: rec.ReqBytes, RespBytes: rec.RespBytes,
				StartedAt: ev.OccurredAt, ElapsedMS: rec.ElapsedMS, Error: rec.Error,
			})
			continue
		}
		doc, err := blobs.Read(rec.BlobRef)
		if err != nil {
			return nil, fmt.Errorf("mirror: 读交换文档 %s: %w", rec.BlobRef, err)
		}
		var ex Exchange
		if err := json.Unmarshal(doc, &ex); err != nil {
			return nil, fmt.Errorf("mirror: 解码交换文档 %s: %w", rec.BlobRef, err)
		}
		out = append(out, ex)
	}
	return out, nil
}

// ---- 小件 ----

// flatHeaders 头展平为排序名值对(确定性;重复值保留)。
func flatHeaders(h http.Header) []Header {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for n := range h {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Header
	for _, n := range names {
		for _, v := range h[n] {
			out = append(out, Header{Name: n, Value: v})
		}
	}
	return out
}

// hopByHop 转发/写回时须剥除的逐跳头(留痕侧保留原样)。
var hopByHop = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Proxy-Connection":    true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// IsHopByHopHeader 名称是否属逐跳头(重放侧按留痕原文重建请求时须
// 剥除;名称经规范形比对,P3-07)。
func IsHopByHopHeader(name string) bool {
	return hopByHop[http.CanonicalHeaderKey(name)]
}

func copyHeaders(dst, src http.Header) {
	for n, vs := range src {
		if hopByHop[n] {
			continue
		}
		for _, v := range vs {
			dst.Add(n, v)
		}
	}
}

// readCapped 读全量但封顶 maxBodyBytes,超出截断并如实标记。
func readCapped(r io.Reader) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxBodyBytes {
		return data[:maxBodyBytes], true, nil
	}
	return data, false, nil
}

func msSince(t time.Time) int64 { return time.Since(t).Milliseconds() }
