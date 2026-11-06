package failover

// P4-42 ① 流式读超时验收(问题清单 #69):挂死假端点(连接建立后
// 停发数据)在读超时阈值内判死、进既有重试链(退避重放/切备可观测);
// 正常慢流(慢速持续出字 + 长思考首包延迟)不误杀;非流式挂死同治。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// sseStub 可编程 OpenAI Chat Completions SSE 端点:按脚本逐帧输出。
// 帧间节奏由脚本自带(服务器侧等待),测试据此构造挂死(停发)/
// 慢流(持续出字)/长思考首包(首帧延迟)三种形态。
type sseStub struct {
	srv  *httptest.Server
	hits atomic.Int64
}

// streamChunk 一段输出:先等 wait 再送出。
type streamChunk struct {
	wait time.Duration
	text string
}

func newSSEStub(chunks func(hit int64) []streamChunk) *sseStub {
	s := &sseStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fl.Flush() // 响应头已到、数据未发:#69 挂死形态的起点
		for _, c := range chunks(n) {
			select {
			case <-time.After(c.wait):
			case <-r.Context().Done():
				return // 客户端判死掐断:连接随请求结束关闭
			}
			if c.text == "" {
				continue
			}
			frame, _ := json.Marshal(map[string]any{
				"id":      "x",
				"object":  "chat.completion.chunk",
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": c.text}, "finish_reason": nil}},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			fl.Flush()
		}
		frame, _ := json.Marshal(map[string]any{
			"id": "x", "object": "chat.completion.chunk",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
		})
		fmt.Fprintf(w, "data: %s\n\n", frame)
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	return s
}

// hangStub 挂死假端点:响应头送达后永停发(#69 实测形态:外连
// ESTABLISHED 不动),直到客户端掐断。
func hangStub() *sseStub {
	return newSSEStub(func(int64) []streamChunk {
		return []streamChunk{{wait: time.Hour}} // 一小时内不出一个字节
	})
}

// namedLeg 测试链的一条腿。
type namedLeg struct {
	name string
	p    llm.Provider
}

// rtChainAt 指定读超时三限与冷却/退避节奏的两腿内链:首腿为主用、
// 次腿为备。腿经 BuildProvider 真实构建 + readTimeoutWrap 装饰(与
// buildProvider 同路径)。冷却梯度须 ≫ 退避间隔(与生产 60s ≫ 500ms
// 同构):梯度与退避同量级时 routeChanged 评估瞬间腿是开闸的、重放
// 时已冷却期满,switched 永真、重放计数永不耗尽——生产配比不会发生。
func rtChainAt(t *testing.T, first, stall, overall, cooldown, backoff time.Duration, legs ...namedLeg) *Chain {
	t.Helper()
	c := NewChain(nil, EnvConfig{})
	c.mu.Lock()
	defer c.mu.Unlock()
	// 三限先设后包:readTimeoutWrap 在包腿时刻读取字段取值。
	c.ReadFirst, c.ReadStall, c.ReadOverall = first, stall, overall
	c.lead = &leg{name: legs[0].name, model: "m-" + legs[0].name,
		prov: c.readTimeoutWrap(legs[0].p), brk: breakerState{State: "closed"}}
	for _, l := range legs[1:] {
		c.members = append(c.members, &leg{name: l.name, model: "m-" + l.name,
			prov: c.readTimeoutWrap(l.p), priority: 1, brk: breakerState{State: "closed"}})
	}
	c.FailThreshold, c.ReplayLimit = 2, 2
	c.Backoff = backoff
	c.CooldownLadder = []time.Duration{cooldown, cooldown, cooldown}
	return c
}

func provOf(t *testing.T, srv *httptest.Server) llm.Provider {
	t.Helper()
	p, err := BuildProvider(ModelConfig{
		Name: "t", Format: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m",
	}, nil)
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	return p
}

// drainText 走链消费一次 Stream,返回累计文本与收尾错误。
func drainText(ctx context.Context, c *Chain) (string, error) {
	acc := llm.NewAccumulator()
	var final error
	for ev, err := range c.Stream(ctx, llm.CompletionRequest{MaxTokens: 64}) {
		if err != nil {
			final = err
			break
		}
		acc.Add(ev)
	}
	return acc.Message().Text(), final
}

// TestReadTimeoutHangFailsWithinThreshold 挂死假端点(头到数据永不来,
// 单腿链):调用在首包宽限附近判死,错误为瞬时类(进既有重试链),
// 链内零交付重放可观测(挂死腿至少命中两次),重试耗尽后错误如实上抛。
func TestReadTimeoutHangFailsWithinThreshold(t *testing.T) {
	hang := hangStub()
	t.Cleanup(hang.srv.Close)
	// 冷却 5ms + 退避 10ms:退避睡完冷却已过,挂死腿作探测再吃一次命中
	// (两次命中 = 链内零交付重放可观测),随后重试耗尽如实上抛。
	c := rtChainAt(t, 200*time.Millisecond, 150*time.Millisecond, 5*time.Second, 5*time.Millisecond, 10*time.Millisecond,
		namedLeg{"hang", provOf(t, hang.srv)})
	start := time.Now()
	_, err := drainText(context.Background(), c)
	if err == nil {
		t.Fatal("挂死端点(重试耗尽后)必须失败")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("应在读超时阈值附近失败,实际 %v(错误: %v)", elapsed, err)
	}
	if !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("错误应为读超时判死: %v", err)
	}
	if cls := Classify(context.Background(), err); cls != ClassTransient {
		t.Fatalf("判死错误应为瞬时类(走既有重试链),实际 %s", cls)
	}
	if hang.hits.Load() < 2 {
		t.Fatalf("链内零交付重放应可观测(挂死腿至少命中 2 次),实际 %d", hang.hits.Load())
	}
}

// TestReadTimeoutHangSwitchesToBackup 挂死主用 → 链按既有错误链切备:
// 备腿回包完整送达,主用熔断开闸可观测。
func TestReadTimeoutHangSwitchesToBackup(t *testing.T) {
	hang := hangStub()
	t.Cleanup(hang.srv.Close)
	ok := newSSEStub(func(int64) []streamChunk {
		return []streamChunk{{wait: time.Millisecond, text: "来自备腿"}}
	})
	t.Cleanup(ok.srv.Close)
	// 冷却 500ms ≫ 退避 1ms(生产 60s ≫ 500ms 同构):挂死腿开闸后整个
	// 测试期内冷却,第 3 次请求必然落备腿。
	c := rtChainAt(t, 200*time.Millisecond, 150*time.Millisecond, 5*time.Second, 500*time.Millisecond, time.Millisecond,
		namedLeg{"hang", provOf(t, hang.srv)}, namedLeg{"ok", provOf(t, ok.srv)})
	text, err := drainText(context.Background(), c)
	if err != nil {
		t.Fatalf("切备后应成功: %v", err)
	}
	if !strings.Contains(text, "来自备腿") {
		t.Fatalf("应收到备腿回包,实际 %q", text)
	}
	if st := c.State(); st.State != "open" && st.State != "probing" {
		t.Fatalf("挂死主用应熔断开闸,实际 %s", st.State)
	}
	if hang.hits.Load() < 2 || ok.hits.Load() < 1 {
		t.Fatalf("主用应至少命中 2 次、备腿接手 1 次,实际 %d/%d", hang.hits.Load(), ok.hits.Load())
	}
}

// TestReadTimeoutMidStreamStall 中途停发:首包已到、出过字后停发——
// 间隔阈值判死;已交付事件保真(半截输出不重放,由链上抛)。
func TestReadTimeoutMidStreamStall(t *testing.T) {
	stub := newSSEStub(func(int64) []streamChunk {
		return []streamChunk{
			{wait: time.Millisecond, text: "前半段"},
			{wait: time.Hour, text: "永远到不了"}, // 中途停发
		}
	})
	t.Cleanup(stub.srv.Close)
	c := rtChainAt(t, 400*time.Millisecond, 150*time.Millisecond, 5*time.Second, 500*time.Millisecond, time.Millisecond,
		namedLeg{"stall", provOf(t, stub.srv)})
	text, err := drainText(context.Background(), c)
	if err == nil {
		t.Fatal("中途停发必须判死")
	}
	if !strings.Contains(err.Error(), "数据间隔") {
		t.Fatalf("应为间隔判死: %v", err)
	}
	if !strings.Contains(text, "前半段") {
		t.Fatalf("已交付事件应保真,实际 %q", text)
	}
}

// TestReadTimeoutOverallWallCap 整体墙钟兜底:持续慢速出字(间隔远小于
// 阈值,间隔判不死)+ 首包及时(首包判不死)——但整请求超过墙钟上限,
// 整体判死。
func TestReadTimeoutOverallWallCap(t *testing.T) {
	stub := newSSEStub(func(int64) []streamChunk {
		var cs []streamChunk
		for i := 0; i < 100; i++ {
			cs = append(cs, streamChunk{wait: 30 * time.Millisecond, text: "字"})
		}
		return cs // 总时长 3 秒 >> 墙钟 400ms;间隔 30ms << 阈值
	})
	t.Cleanup(stub.srv.Close)
	c := rtChainAt(t, 5*time.Second, 5*time.Second, 400*time.Millisecond, 500*time.Millisecond, time.Millisecond,
		namedLeg{"slow", provOf(t, stub.srv)})
	start := time.Now()
	_, err := drainText(context.Background(), c)
	if err == nil {
		t.Fatal("整体墙钟必须兜底判死")
	}
	if !strings.Contains(err.Error(), "整体墙钟") {
		t.Fatalf("应为整体墙钟判死: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("应在墙钟上限附近判死,实际 %v", elapsed)
	}
}

// TestReadTimeoutSlowStreamNotKilled 慢流不误杀:长思考首包延迟 +
// 慢速持续出字(间隔远小于阈值)——完整成功。
func TestReadTimeoutSlowStreamNotKilled(t *testing.T) {
	stub := newSSEStub(func(int64) []streamChunk {
		return []streamChunk{
			{wait: 150 * time.Millisecond, text: "长思考"}, // 首包延迟 < 400ms 宽限
			{wait: 50 * time.Millisecond, text: "慢"},       // 出字间隔 50ms << 250ms 阈值
			{wait: 50 * time.Millisecond, text: "速"},
			{wait: 50 * time.Millisecond, text: "流"},
		}
	})
	t.Cleanup(stub.srv.Close)
	c := rtChainAt(t, 400*time.Millisecond, 250*time.Millisecond, 5*time.Second, 500*time.Millisecond, time.Millisecond,
		namedLeg{"slow", provOf(t, stub.srv)})
	text, err := drainText(context.Background(), c)
	if err != nil {
		t.Fatalf("慢流不得误杀: %v", err)
	}
	if text != "长思考慢速流" {
		t.Fatalf("文本应完整: %q", text)
	}
}

// TestReadTimeoutCompleteHang 非流式挂死同治:请求送达后服务端永不回包,
// Complete 在上限内判死为瞬时类。
func TestReadTimeoutCompleteHang(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 响应头先到、响应体永不到(非流式挂死形态)。先 Flush 再挂:
		// net/http 在响应首字节后布防后台读,客户端掐断才能送达 ctx。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := NewChain(nil, EnvConfig{})
	c.mu.Lock()
	c.ReadFirst, c.ReadStall, c.ReadOverall = 150*time.Millisecond, 100*time.Millisecond, 5*time.Second
	prov := c.readTimeoutWrap(provOf(t, srv))
	c.mu.Unlock()

	start := time.Now()
	_, _, _, err := prov.Complete(context.Background(), llm.CompletionRequest{MaxTokens: 8})
	if err == nil {
		t.Fatal("非流式挂死必须判死")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("应在上限附近判死,实际 %v", elapsed)
	}
	if !strings.Contains(err.Error(), "停滞") {
		t.Fatalf("应为读超时判死: %v", err)
	}
	if cls := Classify(context.Background(), err); cls != ClassTransient {
		t.Fatalf("判死错误应为瞬时类,实际 %s", cls)
	}
	if hits.Load() < 1 {
		t.Fatal("请求应已送达服务端(连接建立)")
	}
}

// TestReadTimeoutDefaults 零值归一:链上三限零值取默认(5m/10m/30m,
// 设计定稿口径)。
func TestReadTimeoutDefaults(t *testing.T) {
	if DefaultReadStall != 5*time.Minute || DefaultReadFirst != 10*time.Minute || DefaultReadOverall != 30*time.Minute {
		t.Fatalf("默认值应为间隔 5m/首包 10m/整体 30m")
	}
	c := NewChain(nil, EnvConfig{})
	wrapped := c.readTimeoutWrap(&stubProvider{})
	rt, ok := wrapped.(readTimeoutProvider)
	if !ok {
		t.Fatal("应装饰为读超时 provider")
	}
	if rt.first != DefaultReadFirst || rt.stall != DefaultReadStall || rt.overall != DefaultReadOverall {
		t.Fatalf("零值应归一默认,实际 %v/%v/%v", rt.first, rt.stall, rt.overall)
	}
	// buildProvider 产物自带装饰(链内全部会话同链覆盖的结构保证)。
	cfg := ModelConfig{Name: "t", Format: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"}
	if bp, err := c.buildProvider(cfg); err != nil {
		t.Fatalf("构建: %v", err)
	} else if _, ok := bp.(readTimeoutProvider); !ok {
		t.Fatal("buildProvider 产物应带读超时装饰")
	}
}
