// Package server 的面板认证(P3-09,总计划 P3「面板认证 + 凭据管理」):
// 本机单用户,密码登录换发 token——token 走 HttpOnly Cookie(浏览器
// 文档导航与 fetch 同源自动携带)、Authorization: Bearer(程序调用)、
// ?token= 查询参数(WebSocket 握手,浏览器 WS 不能设请求头)三通道。
// WrapAuth 是生产装配的总门:除认证端点外,面板与 API 未登录一律不可
// 访问(API 回 401,页面回登录页——登录页内联自足,未登录时不发面板
// 静态资源)。
//
// 存储:密码散列(PBKDF2-HMAC-SHA256,随机盐,记录含参数)与 token
// 摘要(sha256)都落通用配置键值表;两者均为单向散列,不在静态加密
// 敏感键清单(见 internal/secrets)。
package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"ghostreaper/internal/runlog"
	"ghostreaper/internal/secrets"
)

// 认证键空间与 Cookie 名。
const (
	keyAuthPassword   = "auth.password" // 密码散列记录(secrets.PasswordHash JSON)
	keyAuthTokenPre   = "auth.token."   // + hex(sha256(token)) → {created_at}
	tokenCookieName   = "gr_token"
	tokenCookieMaxAge = 30 * 24 * 3600 // 秒:Cookie 生命周期(服务端可随时吊销)
	loginFailedDelay  = 300 * time.Millisecond
)

// ---- 密码 ----

// authPasswordSet 密码是否已设置(状态端点与登录/设置分流依据)。
func (s *Server) authPasswordSet(r *http.Request) bool {
	_, ok, err := s.Store.Setting(r.Context(), keyAuthPassword)
	return err == nil && ok
}

// savePassword 保存密码散列(新随机盐,参数随记录)。
func (s *Server) savePassword(r *http.Request, password string) error {
	h, err := secrets.NewPasswordHash(password)
	if err != nil {
		return err
	}
	h.UpdatedBy = "creator"
	h.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	raw, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("编码密码散列: %w", err)
	}
	return s.Store.SetSetting(r.Context(), keyAuthPassword, string(raw), "creator")
}

// verifyPassword 校验密码;密码已设置且相符为 true。
func (s *Server) verifyPassword(r *http.Request, password string) (bool, error) {
	e, ok, err := s.Store.Setting(r.Context(), keyAuthPassword)
	if err != nil || !ok {
		return false, err
	}
	var h secrets.PasswordHash
	if err := json.Unmarshal([]byte(e.Value), &h); err != nil {
		return false, fmt.Errorf("密码散列记录损坏: %w", err)
	}
	return h.Verify(password), nil
}

// ---- token ----

// issueToken 签发一枚登录 token:随机 32 字节,库里存 sha256 摘要
// (明文只在签发响应出现一次),同时下发 HttpOnly Cookie。
func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成 token: %w", err)
	}
	token := hex.EncodeToString(raw)
	rec, _ := json.Marshal(map[string]string{"created_at": time.Now().Format(time.RFC3339)})
	if err := s.Store.SetSetting(r.Context(), tokenKeyOf(token), string(rec), "creator"); err != nil {
		return "", fmt.Errorf("登记 token: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name: tokenCookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: tokenCookieMaxAge,
	})
	return token, nil
}

// tokenKeyOf token 的库内键(sha256 摘要——库泄露不暴露可用 token)。
func tokenKeyOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return keyAuthTokenPre + hex.EncodeToString(sum[:])
}

// tokenFromRequest 三通道取 token:Bearer 头 > ?token= 查询参数(WS
// 握手)> Cookie(浏览器导航与 fetch)。
func tokenFromRequest(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	if q := r.URL.Query().Get("token"); q != "" {
		return q
	}
	if c, err := r.Cookie(tokenCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// checkToken 请求携带的 token 是否有效(库里在册即有效,可吊销)。
func (s *Server) checkToken(r *http.Request) bool {
	token := tokenFromRequest(r)
	if token == "" {
		return false
	}
	_, ok, err := s.Store.Setting(r.Context(), tokenKeyOf(token))
	return err == nil && ok
}

// revokeToken 吊销一枚 token(登出);幂等。
func (s *Server) revokeToken(r *http.Request, token string) {
	if token == "" {
		return
	}
	_ = s.Store.DeleteSetting(r.Context(), tokenKeyOf(token))
}

// revokeOtherTokens 吊销除 keep 外的全部 token(改密后其他登录即失效)。
func (s *Server) revokeOtherTokens(r *http.Request, keep string) {
	entries, err := s.Store.SettingsWithPrefix(r.Context(), keyAuthTokenPre)
	if err != nil {
		return // 吊销尽力而为:读失败不影响改密主流程
	}
	keepKey := tokenKeyOf(keep)
	for _, e := range entries {
		if e.Key != keepKey {
			_ = s.Store.DeleteSetting(r.Context(), e.Key)
		}
	}
}

// ---- 认证端点 ----

// handleAuthStatus 认证状态(公开):密码是否已设置(登录/首设分流)、
// 本次请求是否已登录。
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"password_set":  s.authPasswordSet(r),
		"authenticated": s.checkToken(r),
	})
}

// handleAuthSetup 首次设置密码(公开,仅当未设置;已设置即 409)。
// 成功即视为登录(签发 token)。
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.authPasswordSet(r) {
		writeErr(w, http.StatusConflict, errors.New("密码已设置,请直接登录"))
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := secrets.ValidatePasswordPolicy(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.savePassword(r, req.Password); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	token, err := s.issueToken(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

// handleAuthLogin 密码登录(公开):相符即签发 token;不符 401(附恒定
// 小延迟,不区分失败细节)。
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authPasswordSet(r) {
		writeErr(w, http.StatusConflict, errors.New("未设置密码,请先设置"))
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ok, err := s.verifyPassword(r, req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		time.Sleep(loginFailedDelay)
		runlog.Warnf("面板登录失败: 密码不正确(%s)", r.RemoteAddr)
		writeErr(w, http.StatusUnauthorized, errors.New("密码不正确"))
		return
	}
	token, err := s.issueToken(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	runlog.Infof("面板登录成功: %s", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

// handleAuthLogout 登出:吊销本次 token 并清 Cookie。
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	runlog.Infof("面板登出: %s", r.RemoteAddr)
	s.revokeToken(r, tokenFromRequest(r))
	http.SetCookie(w, &http.Cookie{
		Name: tokenCookieName, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAuthPassword 修改密码:当前密码相符才放行;改后吊销其他全部
// token(本次会话保持)。
func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ok, err := s.verifyPassword(r, req.Current)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		time.Sleep(loginFailedDelay)
		writeErr(w, http.StatusUnauthorized, errors.New("当前密码不正确"))
		return
	}
	if err := secrets.ValidatePasswordPolicy(req.Next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.savePassword(r, req.Next); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	runlog.Infof("面板密码已修改: %s(其他登录已吊销)", r.RemoteAddr)
	s.revokeOtherTokens(r, tokenFromRequest(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- 生产装配总门 ----

// authPublicPaths 未登录可访问的路径(认证自身的入口:方法 + 路径)。
var authPublicPaths = map[string]bool{
	"GET /api/auth/status": true,
	"POST /api/auth/setup": true,
	"POST /api/auth/login": true,
}

// WrapAuth 面板认证总门(P3-09 完成标准:未登录不可访问面板与 API)。
// main.go 装配时包在全部路由(API + 前端静态资源)之外;测试与内嵌
// 装配可不经此门直挂 Handler()。未登录:API 回 401,页面回登录页
// (200,内联自足,不发面板静态资源);WebSocket 经 ?token= 或 Cookie
// 过门。
func (s *Server) WrapAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// panic 兜底(P3-13b ③):单请求崩溃不带走连接与进程,运行日志
		// 留 ERROR 含堆栈;WriteHeader 前的崩溃回 500(已开始写则静默)。
		defer func() {
			if rec := recover(); rec != nil {
				runlog.Errorf("面板 API 崩溃: %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				writeErr(w, http.StatusInternalServerError, errors.New("服务内部错误"))
			}
		}()
		if authPublicPaths[r.Method+" "+r.URL.Path] {
			h.ServeHTTP(w, r)
			return
		}
		if s.checkToken(r) {
			h.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusUnauthorized, errors.New("未登录"))
			return
		}
		serveLoginPage(w)
	})
}

// serveLoginPage 登录页(P3-09):未登录时页面请求的统一应答。内联
// 样式与脚本自足(不依赖面板静态资源),配色沿用 8.4 token 且随主题
// 三态切换(P3-10:head 内联 boot script 与面板同键 gr.theme,亮色
// 首帧前落定);状态端点区分首设(设置密码)与常规登录,成功后整页
// 重载进面板。
func serveLoginPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(loginPageHTML))
}

const loginPageHTML = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>GhostReaper</title>
<script>(function(){try{var t=localStorage.getItem("gr.theme");var l=t==="light"||(t==="system"&&window.matchMedia("(prefers-color-scheme: light)").matches);document.documentElement.classList.toggle("light",l);}catch(e){}})();</script>
<style>
:root{--bg:#0b0e14;--card:#111827;--fg:#e5e7eb;--muted:#9ca3af;--border:#1f2937;--input:#141b2a;--inputb:#243044;--primary:#3b82f6;--danger:#ef4444;--shadow:0 8px 32px rgba(0,0,0,.45);color-scheme:dark}
.light{--bg:#f8fafc;--card:#ffffff;--fg:#0f172a;--muted:#64748b;--border:#e2e8f0;--input:#f1f5f9;--inputb:#cbd5e1;--primary:#2563eb;--danger:#dc2626;--shadow:0 8px 32px rgba(15,23,42,.14);color-scheme:light}
*{box-sizing:border-box;margin:0;padding:0}
body{background:var(--bg);color:var(--fg);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;min-height:100vh;display:flex;align-items:center;justify-content:center}
main{width:360px;max-width:92vw;background:var(--card);border:1px solid var(--border);border-radius:14px;padding:32px 28px;display:flex;flex-direction:column;gap:14px;box-shadow:var(--shadow)}
h1{font-size:18px;font-weight:600;letter-spacing:.01em}
p{color:var(--muted);font-size:12px}
form{display:flex;flex-direction:column;gap:10px}
input{width:100%;background:var(--input);border:1px solid var(--inputb);border-radius:8px;color:var(--fg);padding:9px 12px;font-size:14px;outline:none;transition:border-color .15s}
input:focus{border-color:var(--primary)}
button{width:100%;background:var(--primary);color:#fff;border:0;border-radius:8px;padding:9px 0;font-size:14px;cursor:pointer;margin-top:4px}
button:disabled{opacity:.6;cursor:default}
.err{color:var(--danger);font-size:12px;min-height:18px;margin:0}
</style>
</head>
<body>
<main>
  <h1>GhostReaper</h1>
  <p id="mode">输入面板密码</p>
  <form id="f">
    <input id="pw" type="password" placeholder="面板密码" autocomplete="current-password" required>
    <input id="pw2" type="password" placeholder="再次输入" autocomplete="new-password" style="display:none">
    <button id="btn" type="submit">登录</button>
  </form>
  <p class="err" id="err"></p>
</main>
<script>
(function(){
  var mode = "login";
  fetch("/api/auth/status").then(function(r){return r.json()}).then(function(s){
    if (s.password_set) return;
    mode = "setup";
    document.getElementById("mode").textContent = "首次使用,设置面板密码";
    document.getElementById("pw").setAttribute("autocomplete", "new-password");
    document.getElementById("pw").placeholder = "面板密码(至少 8 位)";
    document.getElementById("pw2").style.display = "";
    document.getElementById("btn").textContent = "设置密码";
  }).catch(function(){
    document.getElementById("err").textContent = "后端未连通";
  });
  document.getElementById("f").addEventListener("submit", function(e){
    e.preventDefault();
    var err = document.getElementById("err");
    var btn = document.getElementById("btn");
    err.textContent = "";
    var pw = document.getElementById("pw").value;
    if (mode === "setup" && pw !== document.getElementById("pw2").value) {
      err.textContent = "两次输入不一致";
      return;
    }
    btn.disabled = true;
    fetch(mode === "setup" ? "/api/auth/setup" : "/api/auth/login", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({password: pw})
    }).then(function(r){
      if (r.ok) { location.reload(); return; }
      r.json().catch(function(){ return {} }).then(function(b){
        err.textContent = b.error || "登录失败";
        btn.disabled = false;
      });
    }).catch(function(){
      err.textContent = "网络未连通";
      btn.disabled = false;
    });
  });
})();
</script>
</body>
</html>`
