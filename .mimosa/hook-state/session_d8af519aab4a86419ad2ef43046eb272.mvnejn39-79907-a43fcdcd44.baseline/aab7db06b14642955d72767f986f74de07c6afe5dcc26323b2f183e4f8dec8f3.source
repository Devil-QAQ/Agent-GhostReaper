package failover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// P4-10 ①(问题清单 #22):opencode Go 订阅网关接入适配——网关判定、
// 注头传输层(稳定会话头 + 自报 UA)、Provider client 分层、拉列表
// 路径适配、接入方式双形态预设。

func TestOpenCodeGatewayDetect(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"https://opencode.ai/inference/go/openai/v1", true},
		{"https://opencode.ai/inference/go/anthropic/v1", true},
		{"https://opencode.ai/inference/go/openai/v1/", true}, // 尾斜杠归一
		{"https://opencode.ai/inference/go", true},
		{"https://OpenCode.ai/Inference/Go/OpenAI/v1", true}, // 大小写归一
		{"https://opencode.ai/zen", false},                   // API 按量:不注头
		{"https://opencode.ai/zen/v1", false},
		{"https://opencode.ai/inference", false}, // 网关根之外的 inference 前缀不算
		{"https://open.bigmodel.cn/api/paas/v4", false},
		{"https://api.deepseek.com", false},
		{"", false},
		{"not-a-url", false},
	}
	for _, c := range cases {
		if got := isOpenCodeGoURL(c.base); got != c.want {
			t.Errorf("isOpenCodeGoURL(%q) = %v, want %v", c.base, got, c.want)
		}
	}
}

// TestOpenCodeHeaderTransport 注头行为:UA 自报、会话头按标记稳定派生
// (Norma 会话标记优先,显式标记次之,无标记退实例兜底值)。
func TestOpenCodeHeaderTransport(t *testing.T) {
	var mu sync.Mutex
	got := []http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	do := func(client *http.Client, ctx context.Context) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	client := &http.Client{Transport: newOpenCodeHeaderTransport(http.DefaultTransport)}
	sessionOf := func(i int) string {
		mu.Lock()
		defer mu.Unlock()
		return got[i].Get(sessionHeader)
	}

	// Norma 会话标记(agentcore 会话 Prompt 时挂进 ctx):同会话两次请求
	// 同头、确定性派生;不同会话不同头。
	do(client, transcript.WithSessionID(context.Background(), "u-abc"))
	do(client, transcript.WithSessionID(context.Background(), "u-abc"))
	do(client, transcript.WithSessionID(context.Background(), "u-xyz"))
	if sessionOf(0) == "" || sessionOf(0) != sessionOf(1) || sessionOf(0) == sessionOf(2) {
		t.Fatalf("会话头稳定性: %q / %q / %q", sessionOf(0), sessionOf(1), sessionOf(2))
	}
	if want := opencodeSessionID("u-abc"); sessionOf(0) != want {
		t.Fatalf("会话头应为确定性派生: got %q want %q", sessionOf(0), want)
	}

	// 显式标记(非 agentcore 直连调用方,如插话线程)。
	do(client, WithLLMSession(context.Background(), "interject-s1"))
	if sessionOf(3) != opencodeSessionID("interject-s1") {
		t.Fatalf("显式标记派生: got %q", sessionOf(3))
	}

	// 无标记:实例兜底值(同实例稳定)。
	do(client, context.Background())
	do(client, context.Background())
	if sessionOf(4) == "" || sessionOf(4) != sessionOf(5) || sessionOf(4) == sessionOf(0) {
		t.Fatalf("兜底值应实例内稳定且与标记值不同: %q / %q", sessionOf(4), sessionOf(5))
	}
	// 不同实例兜底值不同(两条 Provider 腿不共享会话)。
	client2 := &http.Client{Transport: newOpenCodeHeaderTransport(http.DefaultTransport)}
	do(client2, context.Background())
	if sessionOf(6) == sessionOf(4) {
		t.Fatal("不同传输层实例的兜底值不应相同")
	}

	// UA 自报(未设时注入 ghostreaper/<版本>)。
	mu.Lock()
	ua := got[0].Get("User-Agent")
	mu.Unlock()
	if !strings.HasPrefix(ua, "ghostreaper/") {
		t.Fatalf("User-Agent 应自报 ghostreaper/<版本>: got %q", ua)
	}

	// 已设 UA 不覆盖(RoundTripper 不得改写调用方既有的标识)。
	capt := &captureRoundTripper{}
	rt := opencodeHeaderTransport{base: capt, fallbackSeed: "x"}
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.Header.Set("User-Agent", "custom-agent/9")
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if capt.last.Get("User-Agent") != "custom-agent/9" {
		t.Fatalf("已设 UA 不应被覆盖: got %q", capt.last.Get("User-Agent"))
	}
	if capt.last.Get(sessionHeader) == "" {
		t.Fatal("无 ctx 标记也应注入会话头(兜底值)")
	}
}

// captureRoundTripper 桩:记录出站前的最终请求头。
type captureRoundTripper struct {
	last http.Header
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.last = req.Header.Clone()
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
}

// TestLLMHTTPClientLayering BuildProvider 的 client 分层:go 网关注头
// (openai 族仅注头;anthropic 与用量补全叠两层);非 go 网关(含 glm /
// deepseek 与 zen 按量)零注入——平台适配为 OpenCode 分支,不触他平台
// 路径。
func TestLLMHTTPClientLayering(t *testing.T) {
	// openai + go:注头传输层,底座为普通 transport。
	c, err := llmHTTPClient(ModelConfig{Format: "openai",
		BaseURL: "https://opencode.ai/inference/go/openai/v1"})
	if err != nil || c == nil {
		t.Fatalf("openai+go 应有自定义 client: %v %v", c, err)
	}
	ht, ok := c.Transport.(opencodeHeaderTransport)
	if !ok {
		t.Fatalf("openai+go 传输层应为注头层: %T", c.Transport)
	}
	if _, ok := ht.base.(*http.Transport); !ok {
		t.Fatalf("openai+go 底座应为 *http.Transport: %T", ht.base)
	}

	// anthropic + go:注头层包用量补全层(P4-03 ① 与 P4-10 ① 叠加)。
	c, err = llmHTTPClient(ModelConfig{Format: "anthropic",
		BaseURL: "https://opencode.ai/inference/go/anthropic/v1"})
	if err != nil || c == nil {
		t.Fatalf("anthropic+go 应有自定义 client: %v %v", c, err)
	}
	ht, ok = c.Transport.(opencodeHeaderTransport)
	if !ok {
		t.Fatalf("anthropic+go 外层应为注头层: %T", c.Transport)
	}
	if _, ok := ht.base.(anthropicUsageTransport); !ok {
		t.Fatalf("anthropic+go 内层应为用量补全层: %T", ht.base)
	}

	// anthropic + 非 go:仅用量补全层,零注头(glm anthropic 回归无损)。
	c, err = llmHTTPClient(ModelConfig{Format: "anthropic",
		BaseURL: "https://open.bigmodel.cn/api/anthropic"})
	if err != nil || c == nil {
		t.Fatalf("anthropic+glm 应有用量补全 client: %v %v", c, err)
	}
	if _, ok := c.Transport.(anthropicUsageTransport); !ok {
		t.Fatalf("anthropic+glm 传输层应为用量补全层: %T", c.Transport)
	}

	// openai + 非 go(glm / deepseek / zen 按量):无自定义 client。
	for _, base := range []string{
		"https://open.bigmodel.cn/api/paas/v4",
		"https://api.deepseek.com",
		"https://opencode.ai/zen/v1",
	} {
		c, err = llmHTTPClient(ModelConfig{Format: "openai", BaseURL: base})
		if err != nil || c != nil {
			t.Fatalf("openai+%s 应无自定义 client: %v %v", base, c, err)
		}
	}
}

// TestModelListRequest 拉列表端点与请求头:go 网关固定路径 + Bearer +
// 自报 UA + 会话头;zen / glm / deepseek 走既有路径与头,零注头。
func TestModelListRequest(t *testing.T) {
	// go 网关两种格式同源。
	for _, format := range []string{"openai", "anthropic"} {
		endpoint, headers := modelListRequest(ModelConfig{Format: format,
			BaseURL: "https://opencode.ai/inference/go/" + format + "/v1", APIKey: "k"})
		if endpoint != opencodeModelListURL {
			t.Fatalf("%s+go 列表路径: got %q", format, endpoint)
		}
		if headers["Authorization"] != "Bearer k" || !strings.HasPrefix(headers["User-Agent"], "ghostreaper/") ||
			!strings.HasPrefix(headers[sessionHeader], "gr-") {
			t.Fatalf("%s+go 列表头: %+v", format, headers)
		}
	}
	// 既有路径回归:openai 族 {base}/models + Bearer;anthropic 补 /v1 与
	// x-api-key(已以 /v1 结尾不重复加);zen 按量不注头。
	cases := []struct{ format, base, wantEndpoint string }{
		{"openai", "https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1/models"},
		{"openai", "https://api.deepseek.com", "https://api.deepseek.com/models"},
		{"anthropic", "https://open.bigmodel.cn/api/anthropic", "https://open.bigmodel.cn/api/anthropic/v1/models"},
		{"anthropic", "https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1/models"},
	}
	for _, c := range cases {
		endpoint, headers := modelListRequest(ModelConfig{Format: c.format, BaseURL: c.base, APIKey: "k"})
		if endpoint != c.wantEndpoint {
			t.Fatalf("%s %s: got %q want %q", c.format, c.base, endpoint, c.wantEndpoint)
		}
		if headers["User-Agent"] != "" || headers[sessionHeader] != "" {
			t.Fatalf("%s %s 不应注头: %+v", c.format, c.base, headers)
		}
	}
}

// TestOpenCodeModePresets 接入方式双形态:go 与平台预设主表现同(API
// 预设即 go 形态),api 为 zen 按量;快照下发与查询一致。
func TestOpenCodeModePresets(t *testing.T) {
	if got := OpenCodeModeURL(OpenCodeModeGo, "openai"); got != "https://opencode.ai/inference/go/openai/v1" {
		t.Fatalf("go+openai: %q", got)
	}
	if got := OpenCodeModeURL(OpenCodeModeGo, "anthropic"); got != "https://opencode.ai/inference/go/anthropic" {
		t.Fatalf("go+anthropic: %q", got)
	}
	if got := OpenCodeModeURL(OpenCodeModeAPI, "openai"); got != "https://opencode.ai/zen/v1" {
		t.Fatalf("api+openai: %q", got)
	}
	if got := OpenCodeModeURL(OpenCodeModeAPI, "anthropic"); got != "https://opencode.ai/zen" {
		t.Fatalf("api+anthropic: %q", got)
	}
	if got := OpenCodeModeURL("bogus", "openai"); got != "" {
		t.Fatalf("非法方式应空串: %q", got)
	}
	// 平台预设主表现 = 默认形态(Go 订阅),两口径不漂移。
	for _, format := range []string{"openai", "anthropic"} {
		if a, b := PlatformDefaultURL(PlatformOpenCode, format), OpenCodeModeURL(OpenCodeModeGo, format); a != b {
			t.Fatalf("平台预设与 go 形态漂移: %q vs %q", a, b)
		}
	}
	// 快照与主表一致。
	snap := OpenCodeModePresetURLs()
	if len(snap) != len(opencodeModePresets) || len(snap[OpenCodeModeAPI]) != 2 {
		t.Fatalf("OpenCodeModePresetURLs: %+v", snap)
	}
}

// TestOpenCodeSessionHeaderE2E 全链注头直证:伪 go 网关(测试缝换源)+
// BuildProvider 真装配 + agentcore 真会话(转录在场,产线同款)——同一
// 会话多次请求同头(确定性派生自转录会话 ID),不同会话不同头,UA 自报。
func TestOpenCodeSessionHeaderE2E(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},` +
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	oldOrigin := opencodeGoOrigin
	opencodeGoOrigin = srv.URL
	defer func() { opencodeGoOrigin = oldOrigin }()

	cfg := ModelConfig{Format: "openai", BaseURL: srv.URL + "/openai/v1",
		APIKey: "k", Model: "fake-m", StreamDisabled: true}
	prov, err := BuildProvider(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	runSession := func(sessionID string) {
		t.Helper()
		opts := agentcore.Options{
			Provider: prov, SystemPrompt: []string{"连通性测试"}, Tools: []tool.CoreTool{},
			MaxTurns: 1, SessionID: sessionID, DisableBackgroundTasks: true,
			Transcript: transcript.NewStore(t.TempDir()), // 产线同款:转录在场,ctx 挂会话标记
		}
		sess := agentcore.NewSession(opts)
		defer sess.Close()
		for range 2 { // 同一会话两次提问 → 多次请求同头
			for _, err := range sess.Prompt(context.Background(), "reply ok") {
				if err != nil {
					t.Fatalf("会话 %s Prompt: %v", sessionID, err)
				}
			}
		}
	}
	runSession("oc-e2e-a")
	runSession("oc-e2e-b")

	mu.Lock()
	defer mu.Unlock()
	if len(paths) < 4 {
		t.Fatalf("请求数不足: %d (%v)", len(paths), paths)
	}
	for i, p := range paths {
		if p != "/openai/v1/chat/completions" {
			t.Fatalf("请求路径 %d: %q", i, p)
		}
	}
	sessA := opencodeSessionID("oc-e2e-a")
	sessB := opencodeSessionID("oc-e2e-b")
	for i, h := range headers[:2] {
		if h.Get(sessionHeader) != sessA {
			t.Fatalf("会话 A 请求 %d 头不稳定: %q", i, h.Get(sessionHeader))
		}
	}
	for i, h := range headers[2:4] {
		if h.Get(sessionHeader) != sessB || sessB == sessA {
			t.Fatalf("会话 B 请求 %d 头: %q", i, h.Get(sessionHeader))
		}
	}
	for i, h := range headers {
		if !strings.HasPrefix(h.Get("User-Agent"), "ghostreaper/") {
			t.Fatalf("请求 %d UA 未自报: %q", i, h.Get("User-Agent"))
		}
	}
}
