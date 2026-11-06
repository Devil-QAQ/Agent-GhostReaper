package failover

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

// stubProvider 测试用可编程 Provider:按脚本顺序出事件/错误,计请求数。
type stubProvider struct {
	model string
	hits  atomic.Int64
	// script 每次请求的行为:返回 (events, err);err 非 nil 时 events
	// 先全部交付再以 err 收尾(delivered 模拟)。
	script func(hit int64) ([]llm.StreamEvent, error)
}

func (p *stubProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		n := p.hits.Add(1)
		evs, err := p.script(n)
		for _, ev := range evs {
			if !yield(ev, nil) {
				return
			}
		}
		if err != nil {
			yield(llm.StreamEvent{}, err)
		}
	}
}

func (p *stubProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	n := p.hits.Add(1)
	_, err := p.script(n)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok")}}, "stop", llm.Usage{InputTokens: 1}, nil
}

func textEv(s string) llm.StreamEvent {
	return llm.StreamEvent{Type: llm.SETextDelta, Text: s}
}

func errTransient(n int64) error {
	return errors.New("openai: status 503: upstream unavailable")
}

func newTestChain(t *testing.T) (*Chain, *stubProvider, *stubProvider, *rings.Store) {
	t.Helper()
	store, err := rings.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("开测试库: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	primary := &stubProvider{model: "model-a"}
	backup := &stubProvider{model: "model-b"}
	c := NewChain(store, EnvConfig{})
	c.mu.Lock()
	c.lead = &leg{name: "lib-a", model: primary.model, prov: primary, brk: breakerState{State: "closed"}}
	c.members = []*leg{{name: "lib-b", model: backup.model, prov: backup,
		priority: 1, brk: breakerState{State: "closed"}}}
	c.FailThreshold, c.ReplayLimit, c.Backoff = 3, 2, time.Millisecond
	c.CooldownLadder = []time.Duration{40 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}
	c.mu.Unlock()
	return c, primary, backup, store
}

// drain 消费一次 Stream,返回交付的事件数与收尾错误。
func drain(ctx context.Context, c *Chain) (int, error) {
	n, rerr := 0, error(nil)
	for _, err := range c.Stream(ctx, llm.CompletionRequest{MaxTokens: 8}) {
		if err != nil {
			rerr = err
			break
		}
		n++
	}
	return n, rerr
}

// TestChainSwitchOnConsecutiveFailures 主模型连续瞬时失败达阈值 → 软熔断
// 开冷却,当前请求零交付换腿在备用上重放成功(同请求接续),ActiveModel
// 指向备用;状态持久化在键值表。
func TestChainSwitchOnConsecutiveFailures(t *testing.T) {
	c, primary, backup, store := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	backup.script = func(int64) ([]llm.StreamEvent, error) {
		return []llm.StreamEvent{textEv("from-backup")}, nil
	}
	n, err := drain(context.Background(), c)
	if err != nil || n != 1 {
		t.Fatalf("首次请求应经备用成功: events=%d err=%v", n, err)
	}
	if got := primary.hits.Load(); got != 3 {
		t.Fatalf("主模型应恰试 3 次(阈值),实试 %d", got)
	}
	if got := backup.hits.Load(); got != 1 {
		t.Fatalf("备用应恰服务 1 次(换腿重放),实服务 %d", got)
	}
	st := c.State()
	if st.State != "open" || !strings.Contains(st.Reason, "连续瞬时失败 3 次") {
		t.Fatalf("熔断应 open 且带连续失败原因: %+v", st)
	}
	if c.ActiveModel() != "model-b" {
		t.Fatalf("ActiveModel 应为备用: %s", c.ActiveModel())
	}
	// 持久化可读(failover.breakers,逐成员表)。
	e, ok, err := store.Setting(context.Background(), keyBreakers)
	if err != nil || !ok || !strings.Contains(e.Value, "\"open\"") {
		t.Fatalf("熔断状态应持久化: ok=%v err=%v value=%s", ok, err, e.Value)
	}
	if strings.Contains(e.Value, "api_key") {
		t.Fatalf("持久化状态不得含 key 字段")
	}
}

// TestChainQuotaSwitch 额度耗尽(402/配额文案)立即切备用,不等连续阈值。
func TestChainQuotaSwitch(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		return nil, errors.New("openai: status 402: insufficient_quota")
	}
	backup.script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("b")}, nil }
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("额度耗尽应立即换腿备用成功: %v", err)
	}
	if got := primary.hits.Load(); got != 1 {
		t.Fatalf("额度耗尽不重试同腿,主模型应恰试 1 次,实试 %d", got)
	}
	if got := backup.hits.Load(); got != 1 {
		t.Fatalf("备用应恰服务 1 次,实服务 %d", got)
	}
	st := c.State()
	if st.State != "open" || !strings.Contains(st.Reason, "额度耗尽") {
		t.Fatalf("额度耗尽应开熔断带原因: %+v", st)
	}
}

// TestChainCooldownProbeSwitchBack 冷却期满自动探测回切:冷却内走备用,
// 期满首个请求作探测走主模型,成功即回切 closed,ActiveModel 回主模型。
func TestChainCooldownProbeSwitchBack(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	healthy := atomic.Bool{}
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		if !healthy.Load() {
			return nil, errTransient(0)
		}
		return []llm.StreamEvent{textEv("primary-back")}, nil
	}
	backup.script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("b")}, nil }
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("熔断接续失败: %v", err)
	}
	if c.State().State != "open" {
		t.Fatal("应已开熔断")
	}
	// 冷却内:请求走备用(主不再被路由)。
	bHits := backup.hits.Load()
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("冷却内应走备用: %v", err)
	}
	if backup.hits.Load() != bHits+1 {
		t.Fatal("冷却内应由备用服务")
	}
	time.Sleep(50 * time.Millisecond) // 过冷却(demo 阈 40ms)
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("探测请求应成功: %v", err)
	}
	healthy.Store(true) // 主恢复,但探测发生在置位前——上面这次已换腿?修正时序:
	// 上面请求探测时主仍不健康 → 重开冷却走备用;再等冷却后主已健康。
	time.Sleep(50 * time.Millisecond)
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("回切请求应成功: %v", err)
	}
	st := c.State()
	if st.State != "closed" {
		t.Fatalf("探测成功应回切 closed: %+v", st)
	}
	if c.ActiveModel() != "model-a" {
		t.Fatalf("ActiveModel 应回主模型: %s", c.ActiveModel())
	}
}

// TestChainZeroDeliveryReplay 零交付安全重放:主模型失败一次后恢复,
// 调用方只见一次成功流(不重复交付),主模型被调两次。
func TestChainZeroDeliveryReplay(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	primary.script = func(hit int64) ([]llm.StreamEvent, error) {
		if hit == 1 {
			return nil, errors.New("openai: status 500: boom")
		}
		return []llm.StreamEvent{textEv("recovered")}, nil
	}
	backup.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	n, err := drain(context.Background(), c)
	if err != nil || n != 1 {
		t.Fatalf("重放后应成功交付一次: events=%d err=%v", n, err)
	}
	if got := primary.hits.Load(); got != 2 {
		t.Fatalf("主模型应被调 2 次(首发+重放),实 %d", got)
	}
	if got := backup.hits.Load(); got != 0 {
		t.Fatalf("未达熔断阈值不应动备用,实 %d", got)
	}
	st := c.State()
	if st.State != "closed" {
		t.Fatalf("单次失败不应开熔断: %+v", st)
	}
}

// TestChainNoReplayAfterDelivery 已交付事件后流断:只上抛不重放(不重复
// 半截输出)。
func TestChainNoReplayAfterDelivery(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		return []llm.StreamEvent{textEv("half")}, errors.New("openai: stream reset by peer")
	}
	n, err := drain(context.Background(), c)
	if err == nil {
		t.Fatal("已交付后失败应上抛")
	}
	if n != 1 {
		t.Fatalf("应只交付已到的 1 个事件: %d", n)
	}
	if got := primary.hits.Load(); got != 1 {
		t.Fatalf("不应重放: 主模型实调 %d 次", got)
	}
	_ = backup
}

// TestChainNoRetryDeterministic 4xx 确定性拒绝不重试、不计熔断:上抛且
// 状态保持 closed。
func TestChainNoRetryDeterministic(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		return nil, errors.New("openai: status 401: invalid api key")
	}
	backup.script = func(int64) ([]llm.StreamEvent, error) { return []llm.StreamEvent{textEv("b")}, nil }
	if _, err := drain(context.Background(), c); err == nil {
		t.Fatal("401 应原样上抛")
	}
	if got := primary.hits.Load(); got != 1 {
		t.Fatalf("确定性拒绝不重试: 实调 %d 次", got)
	}
	if got := backup.hits.Load(); got != 0 {
		t.Fatalf("确定性拒绝不切备用: 实调 %d 次", got)
	}
	if st := c.State(); st.State != "closed" {
		t.Fatalf("确定性拒绝不开熔断: %+v", st)
	}
}

// TestChainNoBackupFastFail 熔断冷却中且无备用:快速失败,不再打主模型
// (冷却期不再路由)。
func TestChainNoBackupFastFail(t *testing.T) {
	c, primary, _, _ := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	c.mu.Lock()
	c.members = nil
	c.mu.Unlock()
	// 第一发:3 次失败(2 次重放)后上抛,熔断开。
	if _, err := drain(context.Background(), c); err == nil {
		t.Fatal("无备用应上抛")
	}
	if got := primary.hits.Load(); got != 3 {
		t.Fatalf("首发应试满阈值 3 次,实 %d", got)
	}
	// 冷却内再请求:快速失败,主模型零新增调用。
	if _, err := drain(context.Background(), c); err == nil || !strings.Contains(err.Error(), "熔断冷却中") {
		t.Fatalf("冷却内应快速失败带熔断说明: %v", err)
	}
	if got := primary.hits.Load(); got != 3 {
		t.Fatalf("冷却期主模型不得被路由: 实 %d", got)
	}
}

// TestChainStatePersistAcrossRestart 状态持久化跨链重建:open 且冷却未满
// 的链,同库重建(库激活项同名)后仍 open(继续冷却)。
func TestChainStatePersistAcrossRestart(t *testing.T) {
	c, primary, backup, store := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	backup.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(0) }
	if _, err := drain(context.Background(), c); err == nil {
		t.Fatal("应失败开熔断")
	}
	// 取证(重建前):持久化状态为 open(逐成员表)。
	e, ok, err := store.Setting(context.Background(), keyBreakers)
	if err != nil || !ok || !strings.Contains(e.Value, "\"open\"") {
		t.Fatalf("熔断状态应已持久化为 open: ok=%v err=%v", ok, err)
	}
	// 同库激活同名主模型(stub 腿换真实构建,不真调用),重建链应延续 open。
	ctx := context.Background()
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "lib-a", Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m-a"}, "creator")
	_ = SetActive(ctx, store, RolePrimary, "lib-a", "creator")
	c2 := NewChain(store, EnvConfig{})
	if st := c2.State(); st.State != "open" {
		t.Fatalf("重建后应延续 open: %+v", st)
	}
	if !strings.Contains(c2.State().Reason, "连续瞬时失败") {
		t.Fatalf("故障原因应随状态留存: %+v", c2.State())
	}
}

// TestChainCompleteRouting Complete 同语义:额度耗尽换腿成功。
func TestChainCompleteRouting(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		return nil, errors.New("anthropic: status 429: rate limited then quota exceeded balance")
	}
	backup.script = func(int64) ([]llm.StreamEvent, error) { return nil, nil }
	msg, _, _, err := c.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil || msg.Content[0].Text != "ok" {
		t.Fatalf("Complete 应经备用成功: %v", err)
	}
	if got := backup.hits.Load(); got != 1 {
		t.Fatalf("备用应服务 Complete: %d", got)
	}
}

// TestClassify 分类判定面。
func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want ErrClass
	}{
		{errors.New("openai: status 402: payment required"), ClassQuota},
		{errors.New("llm: insufficient_quota for model"), ClassQuota},
		{errors.New("openai: status 401: bad key"), ClassDeterminant},
		{errors.New("openai: status 400: bad request"), ClassDeterminant},
		{errors.New("openai: status 503: down"), ClassTransient},
		{errors.New("openai: status 429: slow down"), ClassTransient},
		{errors.New("connection refused"), ClassTransient},
		{errors.New("anthropic: maximum context length exceeded"), ClassTooLong},
		{errors.New("prompt is too long"), ClassTooLong},
	}
	for _, tc := range cases {
		if got := Classify(context.Background(), tc.err); got != tc.want {
			t.Fatalf("Classify(%v)=%s want %s", tc.err, got, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Classify(ctx, errors.New("whatever")); got != ClassCanceled {
		t.Fatalf("取消 ctx 应判 canceled: %s", got)
	}
}

// ---- 模型库 ----

func TestLibraryCRUDAndMask(t *testing.T) {
	store, err := rings.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := SaveModelConfig(ctx, store, ModelConfig{
		Name: "main", Format: "OpenAI", BaseURL: "http://x", APIKey: "sk-abcdef123456", Model: "gpt-x",
	}, "creator"); err != nil {
		t.Fatalf("保存: %v", err)
	}
	cfg, ok, err := ModelConfigByName(ctx, store, "main")
	if err != nil || !ok || cfg.APIKey != "sk-abcdef123456" {
		t.Fatalf("读回: ok=%v err=%v cfg=%+v", ok, err, cfg)
	}
	if cfg.Format != "openai" {
		t.Fatalf("格式应归一: %s", cfg.Format)
	}
	// 空 key 更新保留旧 key。
	if err := SaveModelConfig(ctx, store, ModelConfig{
		Name: "main", Format: "openai", BaseURL: "http://y", APIKey: "", Model: "gpt-y",
	}, "creator"); err != nil {
		t.Fatalf("空 key 更新: %v", err)
	}
	cfg, _, _ = ModelConfigByName(ctx, store, "main")
	if cfg.APIKey != "sk-abcdef123456" || cfg.BaseURL != "http://y" {
		t.Fatalf("空 key 应保旧 key 改其余: %+v", cfg)
	}
	// 打码。
	if m := MaskKey("sk-abcdef123456"); m != "sk-…3456" {
		t.Fatalf("MaskKey: %s", m)
	}
	if m := MaskKey("short"); m != "…rt" {
		t.Fatalf("短 key 打码: %s", m)
	}
	if MaskKey("") != "" {
		t.Fatal("空 key 打码为空")
	}
	// 激活与清单。
	if err := SetActive(ctx, store, RolePrimary, "main", "creator"); err != nil {
		t.Fatalf("激活主: %v", err)
	}
	if err := SetActive(ctx, store, RoleBackup, "ghost", "creator"); err == nil {
		t.Fatal("激活库外模型应拒")
	}
	list, err := ListModelConfigs(ctx, store)
	if err != nil || len(list) != 1 {
		t.Fatalf("清单: %v %d", err, len(list))
	}
	// 删除被激活引用的配置:激活项一并清除。
	if err := DeleteModelConfig(ctx, store, "main", "creator"); err != nil {
		t.Fatalf("删除: %v", err)
	}
	if _, ok, _ := ActiveName(ctx, store, RolePrimary); ok {
		t.Fatal("删除后激活项应清除")
	}
	// 非法输入受控拒绝(P3-05c ②:名称放开自定义——空格/点号/中文
	// 皆可;仍拒空名、超长与含 /(按名称寻址的端点路由约束))。
	if err := SaveModelConfig(ctx, store, ModelConfig{Name: "good name.v2 中文", Format: "openai", BaseURL: "x", APIKey: "k", Model: "m"}, "creator"); err != nil {
		t.Fatalf("名称放开后空格/点号/中文应可存: %v", err)
	}
	if err := SaveModelConfig(ctx, store, ModelConfig{Name: "bad/name", Format: "openai", BaseURL: "x", APIKey: "k", Model: "m"}, "creator"); err == nil {
		t.Fatal("含 / 的名应拒(端点路由约束)")
	}
	if err := SaveModelConfig(ctx, store, ModelConfig{Name: "", Format: "openai", BaseURL: "x", APIKey: "k", Model: "m"}, "creator"); err == nil {
		t.Fatal("空名应拒")
	}
	if err := SaveModelConfig(ctx, store, ModelConfig{Name: "ok", Format: "grpc", BaseURL: "x", APIKey: "k", Model: "m"}, "creator"); err == nil {
		t.Fatal("非法格式应拒")
	}
}

// TestLibraryReloadAndEnvFallback 链 Reload 消费模型库激活项;未激活时
// 回退环境变量腿。
func TestLibraryReloadAndEnvFallback(t *testing.T) {
	store, err := rings.Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// 环境变量兜底:库空时 Ready 由 env 决定。
	c := NewChain(store, EnvConfig{Configured: false})
	if c.Ready() {
		t.Fatal("无 env 无库应不 Ready")
	}
	cEnv := NewChain(store, EnvConfig{Configured: true, Format: "openai", BaseURL: "http://env", APIKey: "ek", Model: "env-model"})
	if !cEnv.Ready() {
		t.Fatal("env 注入应 Ready")
	}
	if st := cEnv.State(); st.Primary != EnvName || st.State != "closed" {
		t.Fatalf("env 腿状态: %+v", st)
	}
	// 库激活后 Reload:主备按库构建(以 httptest 服务端点验证真实构建成功)。
	// 此处以合法 URL 形式写入;构建成功即可(不真调用)。
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "a", Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k1", Model: "m-a"}, "creator")
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "b", Format: "openai", BaseURL: "http://127.0.0.1:2", APIKey: "k2", Model: "m-b"}, "creator")
	_ = SetActive(ctx, store, RolePrimary, "a", "creator")
	_ = SetActive(ctx, store, RoleBackup, "b", "creator")
	cEnv.Reload(ctx)
	st := cEnv.State()
	if st.Primary != "a" || st.Backup != "b" {
		t.Fatalf("库激活后 Reload 应换腿: %+v", st)
	}
}

// TestTaskOverrideRouting 任务级覆盖:带任务标记的请求走覆盖腿,无标记
// 走全局。
func TestTaskOverrideRouting(t *testing.T) {
	store, err := rings.Open(filepath.Join(t.TempDir(), "ovr.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "g", Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "global-m"}, "creator")
	_ = SaveModelConfig(ctx, store, ModelConfig{Name: "t", Format: "openai", BaseURL: "http://127.0.0.1:2", APIKey: "k", Model: "task-m"}, "creator")
	_ = SetActive(ctx, store, RolePrimary, "g", "creator")
	_ = SetTaskOverride(ctx, store, "tk1", RolePrimary, "t", "creator")

	c := NewChain(store, EnvConfig{})
	c.mu.Lock()
	routeGlobal := c.routeLocked(context.Background(), llm.CompletionRequest{}, true)
	routeTask := c.routeLocked(WithTask(context.Background(), "tk1"), llm.CompletionRequest{}, true)
	routeOther := c.routeLocked(WithTask(context.Background(), "tk2"), llm.CompletionRequest{}, true)
	c.mu.Unlock()
	if routeGlobal.currentModel != "global-m" {
		t.Fatalf("无标记走全局: %s", routeGlobal.currentModel)
	}
	if routeTask.currentModel != "task-m" {
		t.Fatalf("任务标记走覆盖: %s", routeTask.currentModel)
	}
	if routeOther.currentModel != "global-m" {
		t.Fatalf("未覆盖任务走全局: %s", routeOther.currentModel)
	}
}

// TestActiveModelFollowsSwitch ActiveModel 逐次读当前路由(P4-41 ②④):
// 主腿成功后主开闸 → 立即解析为备腿模型(不等备首次成功,主备切换同治);
// 换主(新腿未成功过)→ 立即解析为新主模型(粘滞修复)。
func TestActiveModelFollowsSwitch(t *testing.T) {
	c, primary, _, _ := newTestChain(t)
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		return []llm.StreamEvent{textEv("a")}, nil
	}
	if _, err := drain(context.Background(), c); err != nil {
		t.Fatalf("主腿首请求应成功: %v", err)
	}
	if c.ActiveModel() != "model-a" {
		t.Fatalf("主腿成功后 ActiveModel 应为主模型: %s", c.ActiveModel())
	}
	// 主开闸(备尚未服务过任何请求)——旧实现的粘滞点即在此。
	c.mu.Lock()
	c.lead.brk = breakerState{State: "open", OpenUntil: c.Now().Add(time.Hour)}
	c.mu.Unlock()
	if got := c.ActiveModel(); got != "model-b" {
		t.Fatalf("主开闸后 ActiveModel 应为备腿模型(不等备首次成功): %s", got)
	}
	// 切主用(换腿重载,新主未成功过)。
	c.mu.Lock()
	c.lead.brk = breakerState{State: "closed", UpdatedAt: c.Now()}
	c.lead, c.members[0] = c.members[0], c.lead
	c.mu.Unlock()
	if got := c.ActiveModel(); got != "model-b" {
		t.Fatalf("切主用后 ActiveModel 应立即为新主模型(新主即原备): %s", got)
	}
}

// TestServedModelPerSession 会话级记账精确归属(P4-41):轮转/换腿下,
// 记账解析(ActiveModelFor 带会话键)返回该会话最近**实际服务**的模型,
// 与轮转游标的当前位置无关;新会话无记录退当前路由;Reload 清记录
//(切换后下一请求起记新配置的腿)。
func TestServedModelPerSession(t *testing.T) {
	c, primary, backup, _ := newTestChain(t)
	// 同优先级第二备,构造轮转组(b1/b2 轮流打头)。
	b2 := &stubProvider{model: "model-b2"}
	b2.script = func(int64) ([]llm.StreamEvent, error) {
		return []llm.StreamEvent{textEv("b2")}, nil
	}
	c.mu.Lock()
	c.members = append(c.members, &leg{name: "lib-b2", model: b2.model, prov: b2,
		priority: 1, brk: breakerState{State: "closed"}})
	c.mu.Unlock()
	primary.script = func(int64) ([]llm.StreamEvent, error) {
		return []llm.StreamEvent{textEv("a")}, nil
	}
	backup.script = func(int64) ([]llm.StreamEvent, error) {
		return []llm.StreamEvent{textEv("b")}, nil
	}

	s1 := WithLLMSession(context.Background(), "sess-1")
	// 会话 s1 在主腿成功 → served 记主模型。
	if _, err := drain(s1, c); err != nil {
		t.Fatalf("主腿请求应成功: %v", err)
	}
	if got := c.ActiveModelFor(s1); got != "model-a" {
		t.Fatalf("s1 应记实际服务的主模型: %s", got)
	}
	// 主开闸 → s1 下一请求轮转到首个备(b)成功 → served 记 b,即使
	// 轮转游标已推进到 b2(记账时刻的当前路由 ≠ 实际服务腿)。
	c.mu.Lock()
	c.lead.brk = breakerState{State: "open", OpenUntil: c.Now().Add(time.Hour)}
	c.mu.Unlock()
	if _, err := drain(s1, c); err != nil {
		t.Fatalf("备腿请求应成功: %v", err)
	}
	c.mu.Lock()
	c.rr[1]++ // 人为推进轮转游标(模拟下一请求已轮到 b2)
	cur := c.rr[1]
	c.mu.Unlock()
	if got := c.ActiveModelFor(s1); got != "model-b" {
		t.Fatalf("s1 应记实际服务的备腿模型(与游标位置无关,游标=%d): %s", cur, got)
	}
	// 新会话无服务记录:退当前路由解析(将走的腿)。
	s2 := WithLLMSession(context.Background(), "sess-2")
	if got := c.ActiveModelFor(s2); got == "" {
		t.Fatal("新会话应退当前路由解析")
	}
	// Reload(切主用)清服务记录:下一请求起记新配置的腿。
	c.Reload(context.Background())
	c.mu.Lock()
	c.lead = &leg{name: "lib-a2", model: "model-a2", prov: primary,
		brk: breakerState{State: "closed", UpdatedAt: c.Now()}}
	c.mu.Unlock()
	if got := c.ActiveModelFor(s1); got != "model-a2" {
		t.Fatalf("Reload 后 served 应清空退当前路由(新主模型): %s", got)
	}
}

// TestActiveModelForTaskBinding 带任务标记的模型解析(P4-41 ③):显式
// 绑定任务呈现绑定腿的模型名,不随全局主用切换漂移;全局口径照常切换。
func TestActiveModelForTaskBinding(t *testing.T) {
	store, err := rings.Open(filepath.Join(t.TempDir(), "bind.db"))
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, cfg := range []ModelConfig{
		{Name: "g", Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "global-m"},
		{Name: "t", Format: "openai", BaseURL: "http://127.0.0.1:2", APIKey: "k", Model: "task-m"},
		{Name: "n", Format: "openai", BaseURL: "http://127.0.0.1:3", APIKey: "k", Model: "new-m"},
	} {
		if err := SaveModelConfig(ctx, store, cfg, "creator"); err != nil {
			t.Fatalf("存配置: %v", err)
		}
	}
	_ = SetActive(ctx, store, RolePrimary, "g", "creator")
	_ = SetTaskOverride(ctx, store, "tk1", RolePrimary, "t", "creator")

	c := NewChain(store, EnvConfig{})
	if got := c.ActiveModel(); got != "global-m" {
		t.Fatalf("全局应走主用: %s", got)
	}
	taskCtx := WithTask(ctx, "tk1")
	if got := c.ActiveModelFor(taskCtx); got != "task-m" {
		t.Fatalf("绑定任务应走绑定腿: %s", got)
	}
	// 全局切主用:全局口径立即跟随,绑定任务不动(strict 隔离)。
	_ = SetActive(ctx, store, RolePrimary, "n", "creator")
	c.Reload(ctx)
	if got := c.ActiveModel(); got != "new-m" {
		t.Fatalf("切主用后全局应立即为新主: %s", got)
	}
	if got := c.ActiveModelFor(taskCtx); got != "task-m" {
		t.Fatalf("切主用后绑定任务应仍走绑定腿: %s", got)
	}
}
