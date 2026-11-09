package failover

// P4-57 思考回传协议适配测试:①协议声明层(预设映射/生效解析)②出站
// 组装收口(剔除变换/逐腿真实线路 E2E——传回带原文、剔除剥掉、不干预
// 不动)④同类连环黑匣子(同端点同错误连续 ≥2 次)。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

// TestThinkingPassbackPresetMapping 平台 × 格式预设映射(造物主定案
// 2026-11-08:glm 两格式传回、deepseek×openai 剔除、deepseek×anthropic
// 实测确认取传回、openai 官方不干预;未列组合无预设)。
func TestThinkingPassbackPresetMapping(t *testing.T) {
	cases := []struct{ platform, format, want string }{
		{PlatformGLM, "openai", ThinkingPassback},
		{PlatformGLM, "anthropic", ThinkingPassback},
		{PlatformDeepSeek, "openai", ThinkingStrip},
		{PlatformDeepSeek, "anthropic", ThinkingPassback},
		{PlatformOpenAI, "openai", ThinkingNone},
		{PlatformOpenCode, "openai", ""},    // 无预设:剔除默认
		{"", "openai", ""},                  // 自定义
		{"GLM", "OpenAI", ThinkingPassback}, // 归一
	}
	for _, c := range cases {
		if got := ThinkingPassbackPreset(c.platform, c.format); got != c.want {
			t.Errorf("预设(%q,%q)=%q, want %q", c.platform, c.format, got, c.want)
		}
	}
}

// TestEffectiveThinkingPassback 生效解析:显式字段 > 端点推断 > 剔除默认。
func TestEffectiveThinkingPassback(t *testing.T) {
	cases := []struct {
		name string
		cfg  ModelConfig
		want string
	}{
		{"显式传回优先", ModelConfig{Format: "openai", BaseURL: "https://api.deepseek.com", ThinkingPassback: ThinkingPassback}, ThinkingPassback},
		{"glm openai 端点推断", ModelConfig{Format: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4"}, ThinkingPassback},
		{"glm anthropic 端点推断", ModelConfig{Format: "anthropic", BaseURL: "https://open.bigmodel.cn/api/anthropic"}, ThinkingPassback},
		{"deepseek openai 端点推断", ModelConfig{Format: "openai", BaseURL: "https://api.deepseek.com"}, ThinkingStrip},
		{"deepseek anthropic 端点推断", ModelConfig{Format: "anthropic", BaseURL: "https://api.deepseek.com/anthropic"}, ThinkingPassback},
		{"openai 官方推断", ModelConfig{Format: "openai", BaseURL: "https://api.openai.com/v1"}, ThinkingNone},
		{"自定义端点默认剔除", ModelConfig{Format: "openai", BaseURL: "https://llm.internal:8000/v1"}, ThinkingStrip},
		{"空端点默认剔除", ModelConfig{Format: "anthropic"}, ThinkingStrip},
	}
	for _, c := range cases {
		if got := c.cfg.EffectiveThinkingPassback(); got != c.want {
			t.Errorf("%s: 生效规则=%q, want %q", c.name, got, c.want)
		}
	}
}

// TestStripHistoryThinking 剔除变换:assistant 思考块剥掉、纯思考轮整条
// 剔除、user/tool 消息不动;无思考时请求原样返回。
func TestStripHistoryThinking(t *testing.T) {
	thinkA := llm.ContentBlock{Type: llm.BlockThinking, Thinking: "思考A", Signature: "sig-a"}
	thinkOnly := llm.ContentBlock{Type: llm.BlockThinking, Thinking: "纯思考"}
	toolUse := llm.ContentBlock{Type: llm.BlockToolUse, ID: "t1", Name: "probe"}
	msgs := []llm.Message{
		llm.UserText("问1"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{thinkA, llm.TextBlock("答1")}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1", Content: []llm.ContentBlock{llm.TextBlock("果1")}}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{thinkOnly}}, // 纯思考轮
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{thinkA, toolUse}},
		llm.UserText("问2"),
	}
	req := llm.CompletionRequest{Messages: msgs}
	out, removed := stripHistoryThinking(req)
	if removed != 3 {
		t.Fatalf("剥掉思考块应 3,实 %d", removed)
	}
	if len(out.Messages) != 5 { // 纯思考轮整条剔除:6-1
		t.Fatalf("剔除后消息数应 5,实 %d", len(out.Messages))
	}
	for _, m := range out.Messages {
		for _, b := range m.Content {
			if b.Type == llm.BlockThinking {
				t.Fatal("剔除后仍有思考块残留")
			}
		}
	}
	// user/tool 消息不动。
	if out.Messages[0].Content[0].Text != "问1" || out.Messages[2].Content[0].ToolUseID != "t1" {
		t.Fatal("user/tool 消息被误改")
	}
	// 无思考:原样返回。
	clean := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("q"), {Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("a")}}}}
	if out2, n := stripHistoryThinking(clean); n != 0 || len(out2.Messages) != len(clean.Messages) || out2.Messages[0].Content[0].Text != "q" {
		t.Fatal("无思考时请求应原样返回")
	}
}

// thinkingSrv 非流式 openai 兼容测试端点(P4-57 E2E):记录请求体,
// 回复带 reasoning_content 的 assistant;status 非 0 时固定该状态拒绝
// (v1.2.0 实锤 400 形态原文)。
type thinkingSrv struct {
	mu     sync.Mutex
	bodies []string
	status int
	srv    *httptest.Server
}

func newThinkingSrv(status int) *thinkingSrv {
	s := &thinkingSrv{status: status}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *thinkingSrv) Close() { s.srv.Close() }

func (s *thinkingSrv) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(raw))
	status := s.status
	s.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"The reasoning_content in the thinking mode must be passed back","code":"1210"}}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": "答", "reasoning_content": "思考原文"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
	})
}

// reasoningSeen 每次请求里历史 assistant 回传 reasoning_content 的条数。
func (s *thinkingSrv) reasoningSeen() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.bodies))
	for i, b := range s.bodies {
		var body struct {
			Messages []struct {
				Role             string `json:"role"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal([]byte(b), &body)
		n := 0
		for _, m := range body.Messages {
			if m.Role == "assistant" && strings.Contains(m.ReasoningContent, "思考") {
				n++
			}
		}
		out[i] = n
	}
	return out
}

// chainOfP457 构建挂真实测试端点的链(单腿,关流式走非流式 JSON)。
func chainOfP457(t *testing.T, cfg ModelConfig) (*Chain, *rings.Store) {
	t.Helper()
	store, err := rings.Open(filepath.Join(t.TempDir(), "p457.db"))
	if err != nil {
		t.Fatalf("开测试库: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := SaveModelConfig(context.Background(), store, cfg, "test"); err != nil {
		t.Fatalf("存配置: %v", err)
	}
	if err := SetActive(context.Background(), store, RolePrimary, cfg.Name, "test"); err != nil {
		t.Fatalf("激活: %v", err)
	}
	return NewChain(store, EnvConfig{}), store
}

// TestChainThinkingPassbackE2E 逐腿规则真实线路:三轮请求,每轮端点回
// 思考;下一轮历史随请求出站——传回腿带原文、剔除腿剥掉、不干预腿
// 引擎默认(openai 适配器带 reasoning_content)。
func TestChainThinkingPassbackE2E(t *testing.T) {
	for _, tc := range []struct {
		rule     string
		wantSeen bool // 第 2 轮起历史 assistant 是否带 reasoning_content
	}{
		{ThinkingPassback, true},
		{ThinkingStrip, false},
		{ThinkingNone, true}, // 不干预:引擎默认行为(openai 适配器回传)
	} {
		s := newThinkingSrv(0)
		cfg := ModelConfig{Name: "腿", Format: "openai", BaseURL: s.srv.URL,
			APIKey: "k", Model: "m", StreamDisabled: true, ThinkingPassback: tc.rule}
		c, _ := chainOfP457(t, cfg)
		ctx := WithTask(context.Background(), "t-e2e")
		var hist []llm.Message
		for i := 0; i < 3; i++ {
			hist = append(hist, llm.UserText("问"))
			msg, _, _, err := c.Complete(ctx, llm.CompletionRequest{Messages: hist})
			if err != nil {
				t.Fatalf("规则 %s 第 %d 轮失败: %v", tc.rule, i+1, err)
			}
			hist = append(hist, msg)
		}
		seen := s.reasoningSeen()
		if len(seen) != 3 {
			t.Fatalf("请求次数应 3,实 %d", len(seen))
		}
		for round := 1; round < 3; round++ { // 第 2、3 轮请求带历史
			got := seen[round] > 0
			if got != tc.wantSeen {
				t.Errorf("规则 %s 第 %d 轮历史思考在场=%v, want %v", tc.rule, round+1, got, tc.wantSeen)
			}
		}
		s.Close()
	}
}

// TestChainSameErrorStreakBlackbox 同类连环(P4-57 ④):同端点同错误
// 连续 2 次黑匣子记 llm_chain_streak;单次不记。
func TestChainSameErrorStreakBlackbox(t *testing.T) {
	s := newThinkingSrv(http.StatusBadRequest)
	cfg := ModelConfig{Name: "腿", Format: "openai", BaseURL: s.srv.URL,
		APIKey: "k", Model: "m", StreamDisabled: true, ThinkingPassback: ThinkingStrip}
	c, store := chainOfP457(t, cfg)
	ctx := WithTask(context.Background(), "t-streak")
	// 第 1 次失败:同类连环不应记(streak=1)。
	if _, _, _, err := c.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("问")}}); err == nil {
		t.Fatal("第 1 次应失败")
	}
	events, err := store.Events(context.Background(), "t-streak", 0, 0)
	if err != nil {
		t.Fatalf("读事件: %v", err)
	}
	if countStreak(events) != 0 {
		t.Fatal("单次失败不应记同类连环")
	}
	// 第 2 次同错误:记。
	if _, _, _, err := c.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("问")}}); err == nil {
		t.Fatal("第 2 次应失败")
	}
	events, err = store.Events(context.Background(), "t-streak", 0, 0)
	if err != nil {
		t.Fatalf("读事件: %v", err)
	}
	if n := countStreak(events); n != 1 {
		t.Fatalf("连续 2 次同错误应记 1 条同类连环,实 %d", n)
	}
}

// countStreak 事件流里同类连环黑匣子条数。
func countStreak(events []rings.RawEvent) int {
	n := 0
	for _, e := range events {
		if e.Kind != rings.EventBlackboxRecord {
			continue
		}
		var rec rings.BlackboxRecord
		if json.Unmarshal(e.Payload, &rec) == nil && rec.Type == rings.BlackboxLLMChainStreak {
			n++
		}
	}
	return n
}
