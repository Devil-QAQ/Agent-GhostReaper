// Package mocktarget 提供本地 mock 靶站(P1-06):Go 内嵌测试 web 服务。
// 供 P1-04~P1-10 各阶段 E2E 在真实 HTTP 目标上跑全链路——P1-07 完成
// 前外部授权目标不得接入,一切验证一律打本地 mock 靶站。
// 靶站内容确定性、端口随机(127.0.0.1:0),自带命中计数器,供测试
// 断言"靶站确实被真实探测过"(而非只看账本自说自话)。
package mocktarget

import (
	"fmt"
	"net"
	"net/http"
	"sync"
)

// Server 一个 mock 靶站:主站 + 独立端口的管理后台(两类服务,对应
// 两个可种子化的资产)。零值不可用,Start 构建,用毕 Close。
type Server struct {
	mainLn  net.Listener
	adminLn net.Listener

	mu   sync.Mutex
	hits map[string]int // "main GET /" / "admin GET /" → 次数
}

// Start 在本地随机端口起靶站(主站 + 管理后台两个 HTTP 服务)。
func Start() (*Server, error) {
	s := &Server{hits: map[string]int{}}
	var err error
	if s.mainLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		return nil, fmt.Errorf("mocktarget: 起主站: %w", err)
	}
	if s.adminLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		_ = s.mainLn.Close()
		return nil, fmt.Errorf("mocktarget: 起管理后台: %w", err)
	}
	go func() { _ = http.Serve(s.mainLn, http.HandlerFunc(s.serveMain)) }()
	go func() { _ = http.Serve(s.adminLn, http.HandlerFunc(s.serveAdmin)) }()
	return s, nil
}

// MainURL 主站地址(http://127.0.0.1:port)。
func (s *Server) MainURL() string { return "http://" + s.mainLn.Addr().String() }

// AdminURL 管理后台地址(独立端口)。
func (s *Server) AdminURL() string { return "http://" + s.adminLn.Addr().String() }

// Hits 某服务某路径被请求次数(site: "main"/"admin")。命中计数是
// 测试侧观测器:断言虫群真实发起了 HTTP 探测。
func (s *Server) Hits(site, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[site+" "+path]
}

// Close 关闭靶站。
func (s *Server) Close() {
	_ = s.mainLn.Close()
	_ = s.adminLn.Close()
}

func (s *Server) count(site, path string) {
	s.mu.Lock()
	s.hits[site+" "+path]++
	s.mu.Unlock()
}

// 主站内容指纹:gr-mock-main / 2.4.1(探测工具与脚本模型据此识别)。
// 攻击链素材(P2-06 组合发现):备份目录暴露 config.bak,内容含管理
// 后台明文凭据;管理后台校验该组凭据(Basic admin:s3cret → 200)。
// 三段各自影响有限(备份可读/凭据存在/后台需认证),组合成未授权
// 管理访问——mock 场景的攻击链级发现。
const (
	mainServer   = "gr-mock-main/2.4.1"
	mainHomepage = `<!DOCTYPE html>
<html><head><title>MockTarget Main</title></head>
<body><h1>gr-mock-main</h1>
<p>Internal service portal. See <a href="/api/version">/api/version</a>.</p>
<!-- ops note: do not index, see /robots.txt -->
</body></html>`
	mainRobots    = "User-agent: *\nDisallow: /old-backup\n"
	mainBackup    = "mock backup archive listing: db-2026-09-01.sql.gz, config.bak\n"
	configBak     = "db_host=127.0.0.1\ndb_user=svc\nadmin_user=admin\nadmin_pass=s3cret\n"
	adminUser     = "admin"
	adminPass     = "s3cret"
	adminBody     = "mock admin console: authorization required\n"
	adminLoggedIn = "mock admin console: logged in as admin\n"
)

func (s *Server) serveMain(w http.ResponseWriter, r *http.Request) {
	s.count("main", r.URL.Path)
	w.Header().Set("Server", mainServer)
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mainHomepage))
	case "/robots.txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(mainRobots))
	case "/old-backup/":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(mainBackup))
	case "/old-backup/config.bak":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(configBak))
	case "/api/version":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"gr-mock-main","version":"2.4.1"}`))
	default:
		http.NotFound(w, r)
	}
}

// 管理后台:无凭据一律 401(带 Basic 质询)——"端口开放但需授权"的
// 真实形态;携带备份泄露的凭据(Basic admin:s3cret,P2-06 攻击链的
// 组合效果)则 200 登录态。内容指纹:mock admin console。
func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) {
	s.count("admin", r.URL.Path)
	if user, pass, ok := r.BasicAuth(); ok && user == adminUser && pass == adminPass {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(adminLoggedIn))
		return
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="mock-admin"`)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(adminBody))
}
