package swarm

// P3-13c 流量去特征实测:① UA 策略三模式对 http_probe / web_fetch
// 生效且配置可切(工具每次现读);② 出站请求零自有标识——请求行与
// 全部头的值里不得出现项目名与任务/单位/会话标识;③ bash 里模型自拼
// curl 的 UA 不受策略干预(完全开放原则,哲学红线)。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/uapolicy"
)

// capturedReq 泄漏检查用的一份到达请求(代理形态下 URL 为绝对形态)。
type capturedReq struct {
	URL    string
	Host   string
	Header http.Header
}

// newCaptureServer 记录每个到达请求的本地靶站。
func newCaptureServer() (*httptest.Server, *[]capturedReq) {
	var mu sync.Mutex
	got := []capturedReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, capturedReq{URL: r.URL.String(), Host: r.Host, Header: r.Header.Clone()})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	return srv, &got
}

// assertNoOwnMarkers 自有标识零泄漏断言:URL/Host 与全部头(名+值)
// 里不得出现项目名与本环境的具体标识(大小写不敏感)。
func assertNoOwnMarkers(t *testing.T, reqs []capturedReq, env *unitEnv) {
	t.Helper()
	for _, req := range reqs {
		var b strings.Builder
		b.WriteString(req.URL)
		b.WriteString(" " + req.Host)
		for name, vals := range req.Header {
			b.WriteString(" " + name + " " + strings.Join(vals, " "))
		}
		lower := strings.ToLower(b.String())
		for _, marker := range []string{
			"ghostreaper",                     // 项目名
			strings.ToLower(env.TaskID),       // 任务 ID
			strings.ToLower(env.TranscriptID), // 会话标识(含单位 ID)
			"ms-",                             // 镜会话 ID 前缀
		} {
			if marker != "" && strings.Contains(lower, marker) {
				t.Fatalf("出站请求泄漏自有标识 %q: url=%s host=%s header=%v", marker, req.URL, req.Host, req.Header)
			}
		}
	}
}

// isBrowserUA / isToolUA 策略 UA 的形态判定(跨包只认形态,精确池
// 成员断言在 uapolicy 包内测试)。
func isBrowserUA(ua string) bool {
	return strings.HasPrefix(ua, "Mozilla/5.0 (") &&
		(strings.Contains(ua, "Gecko") || strings.Contains(ua, "AppleWebKit"))
}

func isToolUA(ua string) bool {
	for _, p := range []string{"curl/", "Wget/", "python-requests/", "Go-http-client/", "axios/", "httpie/"} {
		if strings.HasPrefix(ua, p) {
			return true
		}
	}
	return false
}

// TestUAStrategyAppliedAndSwitchable 完成标准:默认随机池生效、固定
// 自定义生效、配置可切(http_probe 直连与 web_fetch 经镜两条路径)。
func TestUAStrategyAppliedAndSwitchable(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "t-313cua", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	srv, gotP := newCaptureServer()
	defer srv.Close()

	// probeOnce:发一次 http_probe,返回本次到达的 UA。
	probeOnce := func() string {
		n := len(*gotP)
		res := callTool(t, env.httpProbeTool(), fmt.Sprintf(`{"url":%q}`, srv.URL+"/x"))
		if res.IsError {
			t.Fatalf("http_probe 失败: %s", resultText(res))
		}
		reqs := *gotP
		if len(reqs) != n+1 {
			t.Fatalf("捕获服务器应收到一次请求(收 %d 次)", len(reqs)-n)
		}
		return reqs[n].Header.Get("User-Agent")
	}

	// 默认(无配置):随机浏览器池。
	for i := 0; i < 3; i++ {
		if ua := probeOnce(); !isBrowserUA(ua) {
			t.Fatalf("默认策略 UA 应为浏览器形态: %q", ua)
		}
	}

	// 切 tools:模拟常见工具。
	if err := uapolicy.Save(ctx, store, uapolicy.Config{Mode: uapolicy.ModeTools}, "creator"); err != nil {
		t.Fatalf("写 UA 配置失败: %v", err)
	}
	if ua := probeOnce(); !isToolUA(ua) {
		t.Fatalf("tools 策略 UA 应为常见工具形态: %q", ua)
	}

	// 切 fixed:固定自定义,精确原样。
	const mine = "UnitTest-Agent/3.13 (custom)"
	if err := uapolicy.Save(ctx, store, uapolicy.Config{Mode: uapolicy.ModeFixed, Fixed: mine}, "creator"); err != nil {
		t.Fatalf("写 UA 配置失败: %v", err)
	}
	if ua := probeOnce(); ua != mine {
		t.Fatalf("fixed 策略 UA 应精确等于固定值: %q", ua)
	}

	// web_fetch(经镜)同一策略:fixed 值到达靶站。
	m := mirror.New(store, env.Executor.Blobs, t.TempDir())
	sess, err := m.Session(mirror.Meta{TaskID: env.TaskID, StepID: env.StepID,
		UnitID: env.UnitID, TranscriptID: env.TranscriptID})
	if err != nil {
		t.Fatalf("起镜会话失败: %v", err)
	}
	defer sess.Close()
	env.mirror = sess

	n := len(*gotP)
	res := callTool(t, env.webFetchTool(), fmt.Sprintf(`{"url":%q}`, srv.URL+"/doc"))
	if res.IsError {
		t.Fatalf("web_fetch 失败: %s", resultText(res))
	}
	reqs := *gotP
	if len(reqs) != n+1 {
		t.Fatalf("捕获服务器应收到一次 web_fetch 请求")
	}
	if ua := reqs[n].Header.Get("User-Agent"); ua != mine {
		t.Fatalf("web_fetch(经镜)应带 fixed 策略 UA %q,实际 %q", mine, ua)
	}

	// 泄漏断言:本测试全部出站请求(http_probe + web_fetch 经镜转发)
	// 零自有标识。
	assertNoOwnMarkers(t, *gotP, env)
}

// TestBashUAUntouched 完成标准:bash 自拼命令的 UA 不受策略干预——
// fixed 策略配置成自定义值,bash 里模型自拼 curl(显式 -A)的 UA 原样
// 到达,系统不覆盖(完全开放原则)。
func TestBashUAUntouched(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "t-313cbash", StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}

	// 策略配置为 fixed(若 bash 被干预,UA 会被改成它)。
	if err := uapolicy.Save(ctx, store, uapolicy.Config{Mode: uapolicy.ModeFixed, Fixed: "PolicyUA/0 (不应出现)"}, "creator"); err != nil {
		t.Fatalf("写 UA 配置失败: %v", err)
	}

	srv, gotP := newCaptureServer()
	defer srv.Close()

	const modelUA = "ModelChoice/7.7"
	res, err := env.bashTool().Call(ctx, json.RawMessage(
		fmt.Sprintf(`{"command":"curl -s -A '%s' %s/bash"}`, modelUA, srv.URL)), nil)
	if err != nil {
		t.Fatalf("bash 调用失败: %v", err)
	}
	if res.IsError {
		t.Fatalf("bash 应成功: %s", resultText(res))
	}
	reqs := *gotP
	if len(reqs) != 1 {
		t.Fatalf("捕获服务器应收到的请求数不符: %d", len(reqs))
	}
	if ua := reqs[0].Header.Get("User-Agent"); ua != modelUA {
		t.Fatalf("bash 自拼 UA 应原样到达(不干预): %q", ua)
	}
	// bash 流量同样零自有标识。
	assertNoOwnMarkers(t, reqs, env)
}

// TestOutboundHeadersNoOwnMarkers 出站头全量抽查:除 UA 外的其余头
// 也不携带自有信息(无 X-* 自定义头、无任务标识参数)。
func TestOutboundHeadersNoOwnMarkers(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "t-313cleak", StepID: "s1", UnitID: "u3", TranscriptID: "swarm-u3"}

	srv, gotP := newCaptureServer()
	defer srv.Close()

	res := callTool(t, env.httpProbeTool(), fmt.Sprintf(`{"url":%q}`, srv.URL+"/probe?a=1"))
	if res.IsError {
		t.Fatalf("http_probe 失败: %s", resultText(res))
	}
	reqs := *gotP
	if len(reqs) != 1 {
		t.Fatalf("捕获服务器应收到的请求数不符: %d", len(reqs))
	}
	for name := range reqs[0].Header {
		if strings.HasPrefix(strings.ToLower(name), "x-") {
			t.Fatalf("出站请求不应携带自定义 X-* 头: %s -> %v", name, reqs[0].Header[name])
		}
	}
	assertNoOwnMarkers(t, reqs, env)
}
