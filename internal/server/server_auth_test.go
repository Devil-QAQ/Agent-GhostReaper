package server

// P3-09 面板认证 + 凭据管理测试:密码设置/登录/改密/token 生命周期、
// WrapAuth 总门(未登录不可访问面板与 API)、凭据 API 打码。

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testPanelPassword = "panel-pass-8char"

// authSetup 设置面板密码并返回首个 token(经 setup 端点)。
func authSetup(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	code, body := authPost(t, ts.URL+"/api/auth/setup", map[string]string{"password": testPanelPassword}, "")
	if code != 200 {
		t.Fatalf("设置密码失败 %d: %s", code, body)
	}
	var r struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || r.Token == "" {
		t.Fatalf("setup 响应应含 token: %s", body)
	}
	return r.Token
}

// authLogin 用密码登录返回 token。
func authLogin(t *testing.T, ts *httptest.Server, password string) (int, string) {
	t.Helper()
	return authPost(t, ts.URL+"/api/auth/login", map[string]string{"password": password}, "")
}

// post JSON POST 辅助;auth 非空时带 Bearer。
func authPost(t *testing.T, url string, payload any, auth string) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

// get GET 辅助;auth 非空时带 Bearer。
func authGet(t *testing.T, url, auth string) (int, string, *http.Response) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String(), resp
}

// TestAuthSetupLoginFlow 密码设置与登录:未设密码时 login 指引设置、
// setup 弱密码拒、设好后 setup 409、login 对错分流、状态端点如实。
func TestAuthSetupLoginFlow(t *testing.T) {
	srv, _ := newSettingsServer(t)
	ts := httptest.NewServer(srv.WrapAuth(srv.Handler()))
	defer ts.Close()

	// 初始状态:未设密码、未登录(状态端点公开)。
	code, body, _ := authGet(t, ts.URL+"/api/auth/status", "")
	if code != 200 || !strings.Contains(body, `"password_set":false`) {
		t.Fatalf("初始状态不符 %d: %s", code, body)
	}
	// 未设密码时登录:409 指引设置。
	if code, _ := authLogin(t, ts, "whatever-8"); code != 409 {
		t.Fatalf("未设密码 login 应 409,得 %d", code)
	}
	// 弱密码设置:400。
	if code, _ := authPost(t, ts.URL+"/api/auth/setup", map[string]string{"password": "short"}, ""); code != 400 {
		t.Fatalf("弱密码应 400,得 %d", code)
	}
	// 正常设置:发 token。
	token := authSetup(t, ts)
	// 重复设置:409。
	if code, _ := authPost(t, ts.URL+"/api/auth/setup", map[string]string{"password": testPanelPassword}, ""); code != 409 {
		t.Fatalf("重复 setup 应 409,得 %d", code)
	}
	// 正确密码登录:200 + token。
	code, body = authLogin(t, ts, testPanelPassword)
	if code != 200 || !strings.Contains(body, `"token"`) {
		t.Fatalf("正确密码应登录成功 %d: %s", code, body)
	}
	// 错误密码:401。
	if code, _ := authLogin(t, ts, "wrong-password-8"); code != 401 {
		t.Fatalf("错误密码应 401,得 %d", code)
	}
	// 状态端点带 token:已登录。
	code, body, _ = authGet(t, ts.URL+"/api/auth/status", token)
	if code != 200 || !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("带 token 状态不符 %d: %s", code, body)
	}
}

// TestWrapAuthGate 总门(P3-09 完成标准:未登录不可访问面板与 API):
// API 401 / 页面回登录页 / healthz 同受门 / 三通道 token 过门 / 登出即吊销。
func TestWrapAuthGate(t *testing.T) {
	srv, _ := newSettingsServer(t)
	ts := httptest.NewServer(srv.WrapAuth(srv.Handler()))
	defer ts.Close()

	// 未登录:API 401(含 healthz);页面回登录页(不发面板静态资源)。
	for _, path := range []string{"/api/tasks", "/api/healthz", "/api/config", "/api/credentials"} {
		code, body, _ := authGet(t, ts.URL+path, "")
		if code != 401 || !strings.Contains(body, "未登录") {
			t.Fatalf("未登录 %s 应 401,得 %d: %s", path, code, body)
		}
	}
	code, body, resp := authGet(t, ts.URL+"/", "")
	if code != 200 || !strings.Contains(body, "面板密码") {
		t.Fatalf("未登录页面应回登录页 %d: %.80s", code, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("登录页 Content-Type = %q", ct)
	}
	if strings.Contains(body, "__next") {
		t.Fatal("未登录不得下发面板应用资源")
	}
	// 登录页(P4-26 审核迭代):title 与 favicon 声明与面板同值——
	// 隐私窗口首访标签页即显示图标。
	if !strings.Contains(body, "<title>Agent GhostReaper</title>") {
		t.Fatal("登录页 title 应为 Agent GhostReaper")
	}
	for _, icon := range []string{`/favicon.ico`, `/icon.svg`, `/favicon-64.png`} {
		if !strings.Contains(body, `href="`+icon+`"`) {
			t.Fatalf("登录页缺 favicon 声明 %s", icon)
		}
	}
	// favicon 三件套豁免认证门(P4-26 审核迭代):未登录不回登录页
	//(直通内层静态服务;测试装配无静态文件,404 即证未拦)。
	for _, p := range []string{"/favicon.ico", "/icon.svg", "/favicon-64.png"} {
		code, body, _ := authGet(t, ts.URL+p, "")
		if strings.Contains(body, "面板密码") {
			t.Fatalf("未登录 %s 不应回登录页", p)
		}
		if code != 200 && code != 404 {
			t.Fatalf("未登录 %s 放行后状态异常 %d", p, code)
		}
	}

	token := authSetup(t, ts)

	// Bearer 通道。
	if code, _, _ := authGet(t, ts.URL+"/api/tasks", token); code != 200 {
		t.Fatalf("Bearer 应过门,得 %d", code)
	}
	// Cookie 通道(浏览器文档导航与 fetch 的现实通道)。
	req, _ := http.NewRequest("GET", ts.URL+"/api/tasks", nil)
	req.AddCookie(&http.Cookie{Name: tokenCookieName, Value: token})
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("Cookie 应过门: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	// 查询参数通道(WebSocket 握手用)。
	if code, _, _ := authGet(t, ts.URL+"/api/tasks?token="+token, ""); code != 200 {
		t.Fatalf("查询参数 token 应过门,得 %d", code)
	}
	// 错 token:401。
	if code, _, _ := authGet(t, ts.URL+"/api/tasks", "forged-token"); code != 401 {
		t.Fatalf("伪 token 应 401,得 %d", code)
	}

	// 登出:吊销 + 清 Cookie;原 token 立即失效。
	code, body = authPost(t, ts.URL+"/api/auth/logout", map[string]string{}, token)
	if code != 200 {
		t.Fatalf("登出失败 %d: %s", code, body)
	}
	if code, _, _ := authGet(t, ts.URL+"/api/tasks", token); code != 401 {
		t.Fatalf("登出后原 token 应失效,得 %d", code)
	}
}

// TestAuthPasswordChange 改密:当前密码错拒;改对后旧密码不可登录、
// 其他 token 吊销、本次会话保持。
func TestAuthPasswordChange(t *testing.T) {
	srv, _ := newSettingsServer(t)
	ts := httptest.NewServer(srv.WrapAuth(srv.Handler()))
	defer ts.Close()

	tok1 := authSetup(t, ts)
	_, body := authLogin(t, ts, testPanelPassword)
	var r struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &r)
	tok2 := r.Token // 第二个会话

	// 当前密码错:401。
	if code, _ := authPost(t, ts.URL+"/api/auth/password",
		map[string]string{"current": "wrong-pass-8", "next": "new-pass-8char"}, tok1); code != 401 {
		t.Fatalf("当前密码错应 401,得 %d", code)
	}
	// 新密码弱:400。
	if code, _ := authPost(t, ts.URL+"/api/auth/password",
		map[string]string{"current": testPanelPassword, "next": "short"}, tok1); code != 400 {
		t.Fatalf("弱新密码应 400,得 %d", code)
	}
	// 改密成功:tok2(其他会话)吊销,tok1(本次)保持。
	if code, _ := authPost(t, ts.URL+"/api/auth/password",
		map[string]string{"current": testPanelPassword, "next": "new-pass-8char"}, tok1); code != 200 {
		t.Fatal("改密应成功")
	}
	if code, _, _ := authGet(t, ts.URL+"/api/tasks", tok2); code != 401 {
		t.Fatalf("其他会话应被吊销,得 %d", code)
	}
	if code, _, _ := authGet(t, ts.URL+"/api/tasks", tok1); code != 200 {
		t.Fatalf("本次会话应保持,得 %d", code)
	}
	if code, _ := authLogin(t, ts, testPanelPassword); code != 401 {
		t.Fatalf("旧密码应不可登录,得 %d", code)
	}
	if code, _ := authLogin(t, ts, "new-pass-8char"); code != 200 {
		t.Fatalf("新密码应可登录,得 %d", code)
	}
}

// TestCredentialsAPI 凭据存取(P3-09 完成标准:凭据可存取,界面打码):
// 保存 → 清单只回打码;编辑留空秘密保留;重名拒;删除。
func TestCredentialsAPI(t *testing.T) {
	srv, _ := newSettingsServer(t)
	ts := httptest.NewServer(srv.WrapAuth(srv.Handler()))
	defer ts.Close()
	token := authSetup(t, ts)

	// 未登录不可读写凭据。
	if code, _ := authPost(t, ts.URL+"/api/credentials",
		map[string]string{"name": "x", "secret": "y"}, ""); code != 401 {
		t.Fatalf("未登录写凭据应 401,得 %d", code)
	}

	// 保存。
	code, body := authPost(t, ts.URL+"/api/credentials", map[string]string{
		"name": "dvwa-admin", "username": "admin", "secret": "p4ss-secret", "note": "靶场管理员",
	}, token)
	if code != 200 {
		t.Fatalf("保存凭据失败 %d: %s", code, body)
	}
	if strings.Contains(body, "p4ss-secret") {
		t.Fatalf("响应不得回秘密明文: %s", body)
	}
	var list struct {
		Credentials []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Username   string `json:"username"`
			SecretMask string `json:"secret_mask"`
			EnvVar     string `json:"env_var"`
			HasSecret  bool   `json:"has_secret"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Credentials) != 1 {
		t.Fatalf("应回 1 条: %s", body)
	}
	c := list.Credentials[0]
	if c.Name != "dvwa-admin" || c.Username != "admin" || c.EnvVar != "GR_CRED_DVWA_ADMIN" ||
		!c.HasSecret || c.SecretMask == "" || c.SecretMask == "p4ss-secret" {
		t.Fatalf("打码视图不符: %+v", c)
	}

	// 重名:400。
	if code, _ := authPost(t, ts.URL+"/api/credentials",
		map[string]string{"name": "dvwa-admin", "secret": "other"}, token); code != 400 {
		t.Fatalf("重名应 400,得 %d", code)
	}

	// 编辑:秘密留空保留旧值(清单仍 HasSecret)。
	code, body = authPost(t, ts.URL+"/api/credentials", map[string]string{
		"id": c.ID, "name": "dvwa-admin", "username": "admin", "note": "改说明",
	}, token)
	if code != 200 || !strings.Contains(body, `"has_secret":true`) {
		t.Fatalf("编辑保留秘密不符 %d: %s", code, body)
	}

	// 清单读(打码)。
	code, body, _ = authGet(t, ts.URL+"/api/credentials", token)
	if code != 200 || strings.Contains(body, "p4ss-secret") || !strings.Contains(body, "GR_CRED_DVWA_ADMIN") {
		t.Fatalf("清单不符 %d: %s", code, body)
	}

	// 删除。
	req, _ := http.NewRequest("DELETE", ts.URL+"/api/credentials/"+c.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("删除失败: %v", resp)
	}
	resp.Body.Close()
	code, body, _ = authGet(t, ts.URL+"/api/credentials", token)
	if code != 200 || strings.Contains(body, "dvwa-admin") {
		t.Fatalf("删除后清单应为空: %s", body)
	}
}

// TestWSQueryTokenAuth WS 握手过门:未带 token 拒 401;?token= 过门并
// 收到 ready(P3-08 的连接建立在认证之内)。
func TestWSQueryTokenAuth(t *testing.T) {
	srv, _, _ := newLiveTestServer(t)
	ts := httptest.NewServer(srv.WrapAuth(srv.Handler()))
	defer ts.Close()
	token := authSetup(t, ts)

	// 未带 token:握手被门拒(非 101)。
	if conn := dialWSPath(t, ts.URL, "/api/ws", ""); conn != nil {
		conn.close()
		t.Fatal("未带 token 的 WS 握手应被拒")
	}
	// ?token= 过门:101 + ready 帧。
	c := dialWSPath(t, ts.URL, "/api/ws", "token="+token)
	if c == nil {
		t.Fatal("带 token 的 WS 握手应成功")
	}
	defer c.close()
	c.expectMsg(3*time.Second, map[string]any{"t": "ready"})
}

// dialWSPath 带路径与查询串的 WS 握手(返回 nil = 握手被拒);握手
// 成功后与 dialWS 同款构造(带读缓冲的 wsClient)。
func dialWSPath(t *testing.T, base, path, query string) *wsClient {
	t.Helper()
	var keyRaw [16]byte
	if _, err := rand.Read(keyRaw[:]); err != nil {
		t.Fatalf("生成握手随机数: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(keyRaw[:])
	host := strings.TrimPrefix(base, "http://")
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Fatalf("拨号 %s: %v", host, err)
	}
	target := path
	if query != "" {
		target += "?" + query
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", target, host, key)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "101") {
		_ = conn.Close()
		return nil
	}
	for { // 排空剩余握手头
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	return &wsClient{conn: conn, br: br, t: t}
}
