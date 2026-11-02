package failover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

// ---- P2-08c 备链版测试:梯度冷却 / 同优先级轮转 / 窗口跳过 / 显式
// 绑定不换 / 重试覆盖三态 / 立即恢复 / 关流式垫片 / 限速 / 拉列表 ----

// v2Chain 备链测试链:lead + 若干备(同 stubProvider),梯度可设。
func v2Chain(t *testing.T, leadCfg ModelConfig, memberCfgs ...ModelConfig) (*Chain, *stubProvider, []*stubProvider, *rings.Store) {
	t.Helper()
	store, err := rings.Open(filepath.Join(t.TempDir(), "v2.db"))
	if err != nil {
		t.Fatalf("开测试库: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lead := &stubProvider{model: leadCfg.Model}
	c := NewChain(store, EnvConfig{})
	memberStubs := make([]*stubProvider, 0, len(memberCfgs))
	members := make([]*leg, 0, len(memberCfgs))
	for _, cfg := range memberCfgs {
		sp := &stubProvider{model: cfg.Model}
		members = append(members, &leg{name: cfg.Name, model: cfg.Model, cfg: cfg,
			priority: cfg.pollPriority(), prov: sp, brk: breakerState{State: "closed"}})
		memberStubs = append(memberStubs, sp)
	}
	c.mu.Lock()
	c.lead = &leg{name: leadCfg.Name, model: leadCfg.Model, cfg: leadCfg,
		prov: lead, brk: breakerState{State: "closed"}}
	c.members = members
	c.FailThreshold, c.ReplayLimit, c.Backoff = 3, 2, time.Millisecond
	c.mu.Unlock()
	return c, lead, memberStubs, store
}

func mkCfg(name, model string) ModelConfig { return ModelConfig{Name: name, Model: model} }

// TestChainGradientCooldown 冷却梯度 60s→5min→30min:按累计开闸取档,
// 顶档封顶;探测成功归零重走首档。
func TestChainGradientCooldown(t *testing.T) {
	c, lead, _, _ := v2Chain(t, mkCfg("a", "m-a"))
	c.mu.Lock()
	c.CooldownLadder = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	c.mu.Unlock()
	lead.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }

	// 单锁快照:剩余冷却与累计开闸同锁读取(avoid 时序抖动)。
	snap := func() (time.Duration, int) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.lead.brk.OpenUntil.Sub(c.Now()), c.lead.brk.Opens
	}
	// 三轮全失败开闸:1→1s,2→5s,3→30s,4→30s(封顶)。
	for i, want := range []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 30 * time.Second} {
		c.mu.Lock()
		c.lead.brk = breakerState{State: "closed", Opens: i} // 从 closed 起步,累计开闸预置为 i
		c.onFailure(c.lead, ClassQuota, errors.New("x"))
		c.mu.Unlock()
		got, opens := snap()
		if got <= want-200*time.Millisecond || got > want {
			t.Fatalf("第 %d 次开闸冷却应 %v,实 %v", i+1, want, got)
		}
		if opens != i+1 {
			t.Fatalf("累计开闸应 %d,实 %d", i+1, opens)
		}
	}
	// 探测成功归零:下一次开闸回首档。
	c.mu.Lock()
	c.lead.brk.State = "open"
	c.onSuccess(c.lead)
	c.onFailure(c.lead, ClassQuota, errors.New("x"))
	c.mu.Unlock()
	got, opens := snap()
	if opens != 1 {
		t.Fatalf("探测成功后梯度应归零(下次开闸=1),实 %d", opens)
	}
	if got <= time.Second-200*time.Millisecond || got > time.Second {
		t.Fatalf("归零后应回首档 1s,实 %v", got)
	}
}

// TestChainRoundRobinSamePriority 同优先级备轮流打头:主开冷却后,两个
// 同优先级备交替服务。
func TestChainRoundRobinSamePriority(t *testing.T) {
	c, lead, members, _ := v2Chain(t, mkCfg("a", "m-a"), mkCfg("b1", "m-b1"), mkCfg("b2", "m-b2"))
	lead.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	members[0].script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("1")}, nil }
	members[1].script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("2")}, nil }
	// 第一发:主 3 次失败开闸 → 备1 服务;随后 4 发应 b1/b2 交替。
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("首发送达: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := drain(context.Background(), c); err != nil {
			t.Fatalf("第 %d 发: %v", i+1, err)
		}
	}
	h1, h2 := members[0].hits.Load(), members[1].hits.Load()
	// 首发轮转起点计入:共 5 发备服务,b1 先 3 次 b2 2 次(或均衡 ±1)。
	if d := h1 - h2; d < 0 || d > 1 {
		t.Fatalf("同优先级应轮流分摊: b1=%d b2=%d", h1, h2)
	}
	if got := lead.hits.Load(); got != 3 {
		t.Fatalf("主应恰试 3 次后开闸,实 %d", got)
	}
}

// TestChainContextWindowSkip 上下文装不下跳过:主窗口小被跳过、备接手;
// 全部装不下时不跳过(按序直发,让端点报真实超长)。
func TestChainContextWindowSkip(t *testing.T) {
	small := mkCfg("a", "m-a")
	small.ContextWindow = 100 // 空请求估计 ~64 token 装得下;250 ASCII 字符 ≈126 装不下
	c, lead, members, _ := v2Chain(t, small, mkCfg("b", "m-b"))
	lead.script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("lead")}, nil }
	members[0].script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("b")}, nil }
	big := llm.CompletionRequest{System: []string{stringsN(250)}}

	// 小请求照走主。
	c.mu.Lock()
	r0 := c.routeLocked(context.Background(), llm.CompletionRequest{}, true)
	c.mu.Unlock()
	if r0.current == nil || r0.current.name != "a" {
		t.Fatalf("小请求应走主: %+v", r0)
	}
	// 大请求:主窗口装不下 → 跳到备。
	c.mu.Lock()
	r1 := c.routeLocked(context.Background(), big, true)
	c.mu.Unlock()
	if r1.current == nil || r1.current.name != "b" {
		t.Fatalf("主窗口装不下应跳到备: %+v", r1)
	}
	// 全部装不下:按序直发主,带说明。
	smallB := mkCfg("b", "m-b")
	smallB.ContextWindow = 5
	c2, _, _, _ := v2Chain(t, small, smallB)
	c2.mu.Lock()
	r2 := c2.routeLocked(context.Background(), big, true)
	c2.mu.Unlock()
	if r2.current == nil || r2.current.name != "a" || r2.windowNote == "" {
		t.Fatalf("全部装不下应按序直发主并带说明: %+v", r2)
	}
}

func stringsN(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

// TestChainTaskBindingStrict 任务显式绑定不悄悄换:绑定的腿失败如实报错
// (不落到全局备链);绑定主+备时只在绑定集内接续。
func TestChainTaskBindingStrict(t *testing.T) {
	c, lead, members, store := v2Chain(t, mkCfg("a", "m-a"), mkCfg("b", "m-b"))
	lead.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	members[0].script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("b")}, nil }
	ctx := context.Background()
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "a", Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m-a"}, "creator")
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "b", Format: "openai", BaseURL: "http://127.0.0.1:2", APIKey: "k", Model: "m-b"}, "creator")
	_ = SetActive(ctx, store, RolePrimary, "a", "creator")
	_ = SetTaskOverride(ctx, store, "tk", RolePrimary, "a", "creator")

	// 绑定腿(主)持续失败:重放耗尽后如实上抛原始错误;再请求时如实
	// 报"绑定…冷却中"——两种失败都不落全局备。
	tctx := WithTask(ctx, "tk")
	bHits := members[0].hits.Load()
	_, err := drain(tctx, c)
	if err == nil {
		t.Fatal("绑定腿失败应如实上抛")
	}
	if _, err := drain(tctx, c); err == nil || !strings.Contains(err.Error(), "任务绑定的模型全部熔断冷却中") {
		t.Fatalf("冷却期应报绑定语义: %v", err)
	}
	if members[0].hits.Load() != bHits {
		t.Fatal("显式绑定失败不得悄悄换到全局备")
	}
	// 绑定主+备:失败只在绑定集内接续(备服务)。
	_ = SetTaskOverride(ctx, store, "tk", RoleBackup, "b", "creator")
	c.Reload(ctx) // 清 taskCache,重建绑定集
	c.mu.Lock()
	c.lead = &leg{name: "a", model: "m-a", prov: lead, brk: breakerState{State: "closed"}}
	c.members = []*leg{{name: "b", model: "m-b", prov: members[0], priority: 1, brk: breakerState{State: "closed"}}}
	c.mu.Unlock()
	if _, err := drain(tctx, c); err != nil {
		t.Fatalf("绑定集内应接续到绑定备: %v", err)
	}
}

// TestRetryReplayOverride 重试覆盖③:同一腿零交付重放 -1=关闭(首发失败
// 即上抛)、>0=值(按覆盖次数重放)。
func TestRetryReplayOverride(t *testing.T) {
	// -1:关闭。
	c, lead, _, _ := v2Chain(t, mkCfg("a", "m-a"))
	c.lead.cfg.RetryReplay = -1
	lead.script = func(hit int64) ([]llm.StreamEvent, error) {
		if hit == 1 {
			return nil, errors.New("openai: status 500: boom")
		}
		return []llm.StreamEvent{textEv("ok")}, nil
	}
	if _, err := drain(context.Background(), c); err == nil {
		t.Fatal("覆盖③=-1 时零交付失败应立即上抛")
	}
	if got := lead.hits.Load(); got != 1 {
		t.Fatalf("关闭重放应恰 1 次,实 %d", got)
	}
	// >0:值=1(默认 2)。
	c2, lead2, _, _ := v2Chain(t, mkCfg("a", "m-a"))
	c2.lead.cfg.RetryReplay = 1
	lead2.script = func(hit int64) ([]llm.StreamEvent, error) {
		if hit <= 2 {
			return nil, errors.New("openai: status 500: boom")
		}
		return []llm.StreamEvent{textEv("ok")}, nil
	}
	if _, err := drain(context.Background(), c2); err == nil {
		t.Fatal("1 次重放后仍失败应上抛(第 3 发不会发出)")
	}
	if got := lead2.hits.Load(); got != 2 {
		t.Fatalf("覆盖③=1 应恰 1 次重放共 2 次,实 %d", got)
	}
}

// TestRetryBreakerOverride 重试覆盖④:熔断阈值 >0=值(2 次即开)、
// -1=永不开闸(一直路由主)。
func TestRetryBreakerOverride(t *testing.T) {
	// >0:2 次开闸。
	c, lead, members, _ := v2Chain(t, mkCfg("a", "m-a"), mkCfg("b", "m-b"))
	c.lead.cfg.RetryBreaker = 2
	lead.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	members[0].script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("b")}, nil }
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("应经备成功: %v", err)
	}
	if got := lead.hits.Load(); got != 2 {
		t.Fatalf("覆盖④=2 应 2 次即开闸,实 %d", got)
	}
	// -1:永不开闸(无备时也持续路由主,靠重放预算收口)。
	c2, lead2, _, _ := v2Chain(t, mkCfg("a", "m-a"))
	c2.mu.Lock()
	c2.members = nil
	c2.lead.cfg.RetryBreaker = -1
	c2.mu.Unlock()
	lead2.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	if _, err := drain(context.Background(), c2); err == nil {
		t.Fatal("无备应最终上抛")
	}
	if st := c2.State(); st.State != "closed" {
		t.Fatalf("覆盖④=-1 不得开闸: %+v", st)
	}
}

// TestResetBreaker 立即恢复:熔断关闭、梯度归零;未命中名如实报 false。
func TestResetBreaker(t *testing.T) {
	c, lead, _, _ := v2Chain(t, mkCfg("a", "m-a"))
	lead.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	if _, err := drain(context.Background(), c); err == nil {
		t.Fatal("应失败开闸")
	}
	if c.State().State != "open" {
		t.Fatal("应已开闸")
	}
	if !c.ResetBreaker("a") {
		t.Fatal("命中库内成员应恢复")
	}
	if st := c.State(); st.State != "closed" || st.Opens != 0 {
		t.Fatalf("立即恢复应关闭且梯度归零: %+v", st)
	}
	if c.ResetBreaker("nope") {
		t.Fatal("未命中名应 false")
	}
}

// openaiStub 最小 openai 端点:记录请求体,回非流式 completion。
func openaiStub(t *testing.T, respond func(body map[string]any) (int, string)) (*httptest.Server, *sync.Map) {
	t.Helper()
	var bodies sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies.Store(r.Method+" "+r.URL.Path, body)
		status, resp := respond(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func completionJSON(content string) string {
	return fmt.Sprintf(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`, content)
}

// TestNonStreamShim 关流式:Stream 走非流式请求(stream:false),事件面
// 仍组装出完整消息与用量。
func TestNonStreamShim(t *testing.T) {
	srv, bodies := openaiStub(t, func(map[string]any) (int, string) {
		return 200, completionJSON("你好,连通")
	})
	prov, err := BuildProvider(ModelConfig{
		Name: "t", Format: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
		StreamDisabled: true,
	}, nil)
	if err != nil {
		t.Fatalf("构建: %v", err)
	}
	acc := llm.NewAccumulator()
	for ev, err := range prov.Stream(context.Background(), llm.CompletionRequest{MaxTokens: 8}) {
		if err != nil {
			t.Fatalf("流: %v", err)
		}
		acc.Add(ev)
	}
	msg := acc.Message()
	if msg.Text() != "你好,连通" {
		t.Fatalf("文本应经垫片原样组装: %q", msg.Text())
	}
	if acc.Usage.OutputTokens != 2 || acc.Usage.InputTokens != 3 {
		t.Fatalf("用量应只计一次: %+v", acc.Usage)
	}
	body, _ := bodies.Load("POST /chat/completions")
	b := body.(map[string]any)
	if b["stream"] != false {
		t.Fatalf("关流式请求应为 stream:false: %v", b["stream"])
	}
}

// TestBuildProviderThinkingAndField 输出与思考段落到线格式:思考开关/
// 强度/上限字段名随请求发出。
func TestBuildProviderThinkingAndField(t *testing.T) {
	srv, bodies := openaiStub(t, func(map[string]any) (int, string) {
		return 200, completionJSON("ok")
	})
	prov, err := BuildProvider(ModelConfig{
		Name: "t", Format: "openai", BaseURL: srv.URL, APIKey: "k", Model: "o-x",
		Thinking: "enabled", ThinkingEffort: "high",
		MaxTokens: 777, MaxTokensField: llm.MaxTokensFieldCompletion,
	}, nil)
	if err != nil {
		t.Fatalf("构建: %v", err)
	}
	if _, _, _, err := prov.Complete(context.Background(), llm.CompletionRequest{MaxTokens: 9999}); err != nil {
		t.Fatalf("完成: %v", err)
	}
	body, _ := bodies.Load("POST /chat/completions")
	b := body.(map[string]any)
	if th, ok := b["thinking"].(map[string]any); !ok || th["type"] != "enabled" {
		t.Fatalf("思考开关应落线: %v", b["thinking"])
	}
	if b["reasoning_effort"] != "high" {
		t.Fatalf("思考强度应落线: %v", b["reasoning_effort"])
	}
	if v, ok := b["max_completion_tokens"].(float64); !ok || int(v) != 777 {
		t.Fatalf("上限应压到配置值且走新字段名: %v", b["max_completion_tokens"])
	}
	if _, has := b["max_tokens"]; has {
		t.Fatal("选了新字段名不得再发 max_tokens")
	}
}

// TestRetryConnectOverride 重试覆盖①:建连重试 -1=恰 1 发、0=默认 4 发、
// 2=3 发(恒 500 端点计数;退避压到 5ms 提速)。
func TestRetryConnectOverride(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	fastBackoff := func(c *llm.Config) { c.RetryInterval = 5 * time.Millisecond }
	for _, tc := range []struct {
		override int
		want     int64
	}{
		{-1, 1}, {0, 4}, {2, 3},
	} {
		prov, err := BuildProvider(ModelConfig{
			Name: "t", Format: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
			RetryConnect: tc.override,
		}, fastBackoff)
		if err != nil {
			t.Fatalf("构建: %v", err)
		}
		before := hits.Load()
		_, _, _, _ = prov.Complete(context.Background(), llm.CompletionRequest{MaxTokens: 8})
		if got := hits.Load() - before; got != tc.want {
			t.Fatalf("覆盖①=%d 应恰 %d 发,实 %d", tc.override, tc.want, got)
		}
	}
}

// TestRateLimitThroughProvider 限速:每秒 2 请求下 4 发至少铺开 1 秒
// (同配置全部调用方共享限速器)。
func TestRateLimitThroughProvider(t *testing.T) {
	srv, _ := openaiStub(t, func(map[string]any) (int, string) {
		return 200, completionJSON("ok")
	})
	prov, err := BuildProvider(ModelConfig{
		Name: "t", Format: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
		RatePerSecond: 2,
	}, nil)
	if err != nil {
		t.Fatalf("构建: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	for range 4 {
		if _, _, _, err := prov.Complete(ctx, llm.CompletionRequest{MaxTokens: 8}); err != nil {
			t.Fatalf("完成: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("每秒 2 发 4 连发应至少约 1.5s,实 %v", elapsed)
	}
}

// TestFetchModelList 从 API 拉模型列表:openai 形状 + 排序;anthropic 走
// /v1/models 带 x-api-key;代理定向出站(请求只到代理)。
func TestFetchModelList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("openai 应走 /models: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-x" {
			t.Errorf("应带 Bearer key")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"zzz"},{"id":"aaa","context_window":262144},{"id":"mmm","max_model_len":32768}]}`))
	}))
	t.Cleanup(srv.Close)
	ids, err := FetchModelList(context.Background(), ModelConfig{
		Format: "openai", BaseURL: srv.URL, APIKey: "sk-x", Model: "irrelevant",
	})
	if err != nil || len(ids) != 3 || ids[0].ID != "aaa" || ids[2].ID != "zzz" {
		t.Fatalf("openai 拉列表: %+v err=%v", ids, err)
	}
	if ids[0].ContextWindow != 262144 || ids[1].ContextWindow != 32768 {
		t.Fatalf("上下文窗口应随模型带回(多字段名兼容): %+v", ids)
	}

	// anthropic:/v1/models + x-api-key。
	var gotPath, gotKey string
	aSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-x"}]}`))
	}))
	t.Cleanup(aSrv.Close)
	ids, err = FetchModelList(context.Background(), ModelConfig{
		Format: "anthropic", BaseURL: aSrv.URL, APIKey: "ak", Model: "m",
	})
	if err != nil || len(ids) != 1 || ids[0].ID != "claude-x" {
		t.Fatalf("anthropic 拉列表: %+v err=%v", ids, err)
	}
	if gotPath != "/v1/models" || gotKey != "ak" {
		t.Fatalf("anthropic 形状: path=%s key=%q", gotPath, gotKey)
	}

	// 代理定向:请求只到代理(代理自己回清单即证出站走它)。
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"via-proxy"}]}`))
	}))
	t.Cleanup(proxy.Close)
	ids, err = FetchModelList(context.Background(), ModelConfig{
		Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m",
		Proxy: proxy.URL,
	})
	if err != nil || len(ids) != 1 || ids[0].ID != "via-proxy" {
		t.Fatalf("代理出站拉列表: %+v err=%v", ids, err)
	}
}

// TestMaskProxyAndValidateV2 代理打码与 v2 校验面。
func TestMaskProxyAndValidateV2(t *testing.T) {
	if m := MaskProxy("http://user:pass@127.0.0.1:7890"); m != "http://***@127.0.0.1:7890" {
		t.Fatalf("代理应去凭据: %s", m)
	}
	if m := MaskProxy("socks5://127.0.0.1:1080"); m != "socks5://127.0.0.1:1080" {
		t.Fatalf("无凭据代理原样: %s", m)
	}
	if MaskProxy("") != "" {
		t.Fatal("空代理空串")
	}
	base := ModelConfig{Name: "ok", Format: "openai", BaseURL: "http://x", APIKey: "k", Model: "m"}
	for name, mutate := range map[string]func(*ModelConfig){
		"代理协议":   func(c *ModelConfig) { c.Proxy = "grpc://x:1" },
		"上限字段名":  func(c *ModelConfig) { c.MaxTokensField = "output_tokens" },
		"思考开关":   func(c *ModelConfig) { c.Thinking = "maybe" },
		"思考强度":   func(c *ModelConfig) { c.ThinkingEffort = "extreme" },
		"重试覆盖越界": func(c *ModelConfig) { c.RetryBreaker = -2 },
	} {
		cfg := base
		mutate(&cfg)
		if err := validateV2(cfg); err == nil {
			t.Fatalf("%s 非法值应拒", name)
		}
	}
	if err := validateV2(base); err != nil {
		t.Fatalf("合法 v2 配置应过: %v", err)
	}
}

// TestPollDisabledAndPriorityOrdering 备链成员=参与轮询的其余配置,按
// (优先级,名称)排序;不参与的不入链;主用恒第 1。
func TestPollDisabledAndPriorityOrdering(t *testing.T) {
	store, err := rings.Open(filepath.Join(t.TempDir(), "poll.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	save := func(name string, prio int, disabled bool) {
		cfg := ModelConfig{Name: name, Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m-" + name}
		cfg.PollPriority, cfg.PollDisabled = prio, disabled
		if err := SaveModelConfig(ctx, store, cfg, "creator"); err != nil {
			t.Fatalf("保存 %s: %v", name, err)
		}
	}
	save("p", 0, false) // 将激活为主用
	save("low", 9, false)
	save("hi2", 2, false)
	save("hi1", 2, false)
	save("off", 1, true) // 不参与
	_ = SetActive(ctx, store, RolePrimary, "p", "creator")
	c := NewChain(store, EnvConfig{})
	var order []string
	for _, m := range c.Members() {
		order = append(order, m.Name)
	}
	want := []string{"p", "hi1", "hi2", "low"}
	if len(order) != len(want) {
		t.Fatalf("链成员序: %v (want %v)", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("链成员序: %v (want %v)", order, want)
		}
	}
}
