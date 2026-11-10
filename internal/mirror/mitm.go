package mirror

// CONNECT 解密接管(P3-01,P2-01 隧道透传的升级):
//
// 客户端侧:以镜 CA 为目标域签发的叶证书起 TLS(SNI 缺席退 CONNECT
// 目标,IP 目标走 IP SAN),接管对工具透明——工具只当自己在跟目标握手。
// 内层按 HTTP/1.1 逐交换全量留痕(请求行/头/体 + 状态行/头/体,与
// http 类型同粒度),交换文档入 blob(R18),账本摘要带目标证书校验结论。
//
// 目标侧(P4-62 C(a) 起):一律不校验目标证书(定死,不做配置项)——
// 目标证书无效是渗透常态,镜是留痕设施不是验证者;校验事实仍由
// classifyConnState 如实分类入档(trusted/self_signed/expired/
// hostname_mismatch/untrusted/invalid,留痕口径不变)。目标侧握手在
// 接管前先试(mirror.serveTunnel 经 mitmUp.ensure 预建):协议层拒签
// 等握手失败时整条隧道退透传(工具与目标端到端直连,镜只留隧道元
// 数据)——镜不因目标证书/TLS 形态阻断流量,内容原料由工具调用记录
// 组包补(问题清单 #83)。
//
// 兜底:首字节非 TLS ClientHello 的 CONNECT 照旧透传(tunnel 元数据
// 留痕);客户端拒绝镜 CA(TLS 握手失败)如实记 tunnel 错误,不冒充、
// 不静默。

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ghostreaper/internal/rings"
)

// tlsFirstByte TLS ClientHello 首字节(记录层 handshake 类型)。
const tlsFirstByte = 0x16

// tlsHandshakeTimeout 客户端/目标侧 TLS 握手超时。
const tlsHandshakeTimeout = 15 * time.Second

// peekTimeout CONNECT 建立后等首字节判型的时限。
const peekTimeout = 10 * time.Second

// serveMITM 对已判型为 TLS 的隧道做解密接管。client 为原始连接,
// br 为已窥探的读缓冲(可能已缓冲 ClientHello 后续字节,连同已消费的
// 首字节一并经 peekedConn 回放给 TLS 层);up 为目标侧已就绪的 mitmUp
// (serveTunnel 预建,含握手与证书分类;后续重拨由 mitmUp 自理)。
// started 为隧道起点(留痕计时);first 为窥探到的 ClientHello 首字节。
func (s *Session) serveMITM(client net.Conn, br *bufio.Reader, up *mitmUp, started time.Time, first byte) {
	tlsConn := tls.Server(&peekedConn{Conn: client, r: io.MultiReader(bytes.NewReader([]byte{first}), br)}, &tls.Config{
		NextProtos: []string{"http/1.1"}, // 镜只解 HTTP/1.1;ALPN 依此协商
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			h := chi.ServerName
			if h == "" {
				h = hostOf(up.target) // IP 目标等无 SNI 场景退 CONNECT 目标
			}
			return s.m.leafFor(h)
		},
	})
	_ = tlsConn.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
	if err := tlsConn.Handshake(); err != nil {
		// 客户端不信任镜 CA 等:接管失败如实入账(元数据级)。
		ex := s.newTunnelExchange(up.target, started)
		ex.Error = "解密接管: 客户端 TLS 握手失败: " + err.Error()
		s.record(ex)
		_ = tlsConn.Close()
		up.close()
		return
	}
	_ = tlsConn.SetDeadline(time.Time{})

	untrack := s.trackTunnel(client)
	defer untrack()
	defer up.close()
	var exch atomic.Int64
	deadline := time.AfterFunc(tunnelMaxLife, func() { _ = client.Close(); up.close() })
	defer deadline.Stop()

	inner := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			exch.Add(1)
			s.serveTLSExchange(w, r, up)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
	}
	// 单连接服务:Accept 阻塞到连接生命周期结束再放行 Serve 返回,
	// 免得 Serve 抢先返回触发收尾、掐死仍在途的请求处理。
	ln := &singleConnListener{conn: tlsConn, dead: make(chan struct{})}
	inner.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			_ = ln.Close()
		}
	}
	err := inner.Serve(ln)
	if exch.Load() == 0 {
		// 握手成功但零交换(空连/内层非 HTTP/寿命到顶):元数据级留痕。
		ex := s.newTunnelExchange(up.target, started)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			ex.Error = "解密接管: " + err.Error()
		}
		s.record(ex)
	}
	_ = tlsConn.Close()
}

// serveTLSExchange 一条解密后的 HTTP 交换:捕获原文 → 经目标侧 TLS
// 连接转发 → 捕获响应原样写回 → 全量留痕(与 http 类型同粒度)。
func (s *Session) serveTLSExchange(w http.ResponseWriter, r *http.Request, up *mitmUp) {
	url := "https://" + r.Host + r.URL.RequestURI()
	ex := &Exchange{
		ID: s.nextID(), Type: rings.MirrorRecHTTPS,
		TaskID: s.meta.TaskID, StepID: s.meta.StepID,
		UnitID: s.meta.UnitID, TranscriptID: s.meta.TranscriptID,
		StartedAt: time.Now(),
		Request: &Request{
			Method: r.Method, URL: url, Proto: r.Proto, Host: r.Host,
			Headers: flatHeaders(r.Header),
		},
	}
	reqBody, trunc, err := readCapped(r.Body)
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return // 尚无完整请求可留痕
	}
	ex.Request.Body, ex.Request.BodyTruncated = reqBody, trunc

	if err := up.ensure(); err != nil {
		ex.Error = err.Error()
		ex.ElapsedMS = msSince(ex.StartedAt)
		s.record(ex)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	info := up.infoSnapshot()
	ex.TLS = &info

	fwd, err := http.NewRequest(r.Method, url, bytes.NewReader(reqBody))
	if err != nil {
		ex.Error = "构造转发请求: " + err.Error()
		ex.ElapsedMS = msSince(ex.StartedAt)
		s.record(ex)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	fwd.ContentLength = int64(len(reqBody))
	fwd.Host = r.Host
	copyHeaders(fwd.Header, r.Header)
	fwd.Header.Del("Expect") // 代理形态重组:100-continue 由内层服务端已应答,不透传目标

	conn, br := up.connPair()
	if err := fwd.Write(conn); err != nil {
		up.markDead()
		ex.Error = "目标请求写出失败: " + err.Error()
		ex.ElapsedMS = msSince(ex.StartedAt)
		s.record(ex)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(br, fwd)
	if err != nil {
		up.markDead()
		ex.Error = "目标响应读取失败: " + err.Error()
		ex.ElapsedMS = msSince(ex.StartedAt)
		s.record(ex)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	respBody, rtrunc, _ := readCapped(resp.Body)
	_ = resp.Body.Close()
	if rtrunc || resp.Close {
		up.markDead() // 截断/对端要求断:连接不可复用
	}
	statusText := resp.Status
	if code := fmt.Sprintf("%d ", resp.StatusCode); strings.HasPrefix(statusText, code) {
		statusText = strings.TrimPrefix(statusText, code)
	}
	ex.Response = &Response{
		Proto: resp.Proto, Status: resp.StatusCode, StatusText: statusText,
		Headers: flatHeaders(resp.Header), Body: respBody, BodyTruncated: rtrunc,
	}
	ex.ReqBytes, ex.RespBytes = int64(len(reqBody)), int64(len(respBody))

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	ex.ElapsedMS = msSince(ex.StartedAt)
	s.record(ex)
}

// mitmUp 目标侧 TLS 连接(惰性建连、按需重拨、逐请求复用)。
type mitmUp struct {
	s      *Session
	target string // CONNECT authority(host:port)

	mu   sync.Mutex
	raw  net.Conn // 预拨连接(首次用)
	conn *tls.Conn
	br   *bufio.Reader
	info TLSInfo
	dead bool
}

// ensure 目标侧 TLS 就绪(拨号+握手+校验分类);连接已活则复用。
// 预拨的 raw 首次领养,不在此关闭。
func (u *mitmUp) ensure() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil && !u.dead {
		return nil
	}
	if u.conn != nil { // 旧 TLS 连接已死:收掉再重拨
		_ = u.conn.Close()
		u.conn, u.br = nil, nil
	}
	raw := u.raw
	u.raw = nil
	if raw == nil {
		var err error
		raw, err = net.DialTimeout("tcp", u.target, tunnelDial)
		if err != nil {
			return fmt.Errorf("目标拨号失败: %w", err)
		}
	}
	host := hostOf(u.target)
	// 解密审计接管:标准校验由镜分类记录,不据此拒绝(镜非信任闸)。
	tc := tls.Client(raw, &tls.Config{
		ServerName:         host,
		NextProtos:         []string{"http/1.1"},
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // nolint:gosec // 校验在 classifyConnState 如实入档
	})
	_ = tc.SetDeadline(time.Now().Add(tlsHandshakeTimeout))
	if err := tc.Handshake(); err != nil {
		_ = tc.Close()
		return fmt.Errorf("目标 TLS 握手失败: %w", err)
	}
	_ = tc.SetDeadline(time.Time{})
	u.conn, u.br = tc, bufio.NewReader(tc)
	u.info = classifyConnState(tc.ConnectionState(), host, nil)
	u.dead = false
	return nil
}

func (u *mitmUp) markDead() {
	u.mu.Lock()
	u.dead = true
	u.mu.Unlock()
}

// close 收拢全部连接(TLS 与未领养的预拨)。
func (u *mitmUp) close() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn != nil {
		_ = u.conn.Close()
		u.conn, u.br = nil, nil
	}
	if u.raw != nil {
		_ = u.raw.Close()
		u.raw = nil
	}
	u.dead = true
}

func (u *mitmUp) infoSnapshot() TLSInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.info
}

// connPair 取当前连接与读缓冲(单隧道请求串行,连接由 ensure 保证就绪)。
func (u *mitmUp) connPair() (net.Conn, *bufio.Reader) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.conn, u.br
}

// peekedConn 把已窥探的读缓冲交还上层(Read 走缓冲,其余透传原连接,
// 保留 deadline 语义——TLS 握手超时依赖它)。
type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// singleConnListener 一次 Accept 的单连接监听器:把既有连接交给
// http.Server.Serve;第二次 Accept 阻塞至 Close(Serve 派发连接协程后
// 立即回头 Accept,若即刻放行会抢先返回、误判隧道已终)。
type singleConnListener struct {
	conn net.Conn
	dead chan struct{}
	once sync.Once
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	c := l.conn
	if c == nil {
		<-l.dead
		return nil, http.ErrServerClosed
	}
	l.conn = nil
	return c, nil
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.dead) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.RemoteAddr() }

// newTunnelExchange 隧道元数据级留痕(MITM 失败/零交换兜底用)。
func (s *Session) newTunnelExchange(target string, started time.Time) *Exchange {
	ex := &Exchange{
		ID: s.nextID(), Type: rings.MirrorRecTunnel,
		TaskID: s.meta.TaskID, StepID: s.meta.StepID,
		UnitID: s.meta.UnitID, TranscriptID: s.meta.TranscriptID,
		StartedAt: started, URL: target,
	}
	ex.ElapsedMS = msSince(started)
	return ex
}
