package failover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// P4-03 ①:anthropic SSE 用量输入侧补全。三种端点行为实测归一:
// GLM(message_start 全零、message_delta 带真实输入侧)、官方 Anthropic
// 与 DeepSeek(message_start 带真实输入侧)——前者须被补全,后者不得
// 双计。测试直接驱动 llm.NewProvider 经 BuildProvider 构建的 Provider
// Stream,覆盖传输层改写在真实解析栈上的效果。

// anthropicSSE 测试服务器:按事件序列原样吐 SSE。
func anthropicSSE(t *testing.T, frames string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(frames))
	}))
}

// foldUsage 折叠一个流的全部 usage 事件(与 Norma 累加器同规则:
// message_start/message_delta 的 Usage.Add)。
func foldUsage(t *testing.T, prov llm.Provider) llm.Usage {
	t.Helper()
	acc := llm.NewAccumulator()
	for ev, err := range prov.Stream(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{llm.UserText("hi")}, MaxTokens: 16,
	}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		acc.Add(ev)
	}
	return acc.Usage
}

// TestAnthropicUsageGLMPatternBackfilled GLM 模式:message_start 零用量,
// message_delta 携带真实输入侧(2026-10 实测形状)——输入/缓存须入账。
func TestAnthropicUsageGLMPatternBackfilled(t *testing.T) {
	srv := anthropicSSE(t, "event: message_start\n"+
		`data: {"type": "message_start", "message": {"id": "m1", "usage": {"input_tokens": 0, "output_tokens": 0}}}`+"\n\n"+
		"event: content_block_delta\n"+
		`data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "ok"}}`+"\n\n"+
		"event: message_delta\n"+
		`data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 13, "output_tokens": 16, "cache_read_input_tokens": 7, "cache_creation_input_tokens": 2}}`+"\n\n"+
		"event: message_stop\n"+
		`data: {"type": "message_stop"}`+"\n\n")
	defer srv.Close()

	prov, err := BuildProvider(ModelConfig{
		Format: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "test",
	}, nil)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	u := foldUsage(t, prov)
	// 输入侧 = input(13) + cache_read(7) + cache_creation(2),Norma norm()
	// 折叠为总输入 22(含缓存子集 7/2);输出 16 来自 message_delta。
	if u.InputTokens != 22 || u.OutputTokens != 16 {
		t.Fatalf("GLM 模式用量未补全: got input=%d output=%d, want input=22 output=16", u.InputTokens, u.OutputTokens)
	}
	if u.CacheReadTokens != 7 || u.CacheWriteTokens != 2 {
		t.Fatalf("GLM 模式缓存未入账: read=%d write=%d, want 7/2", u.CacheReadTokens, u.CacheWriteTokens)
	}
}

// TestAnthropicUsageOfficialPatternUntouched 官方模式:message_start 带
// 真实输入侧——不得改写、不得双计(DeepSeek anthropic 实测形状)。
func TestAnthropicUsageOfficialPatternUntouched(t *testing.T) {
	srv := anthropicSSE(t, "event: message_start\n"+
		`data: {"type": "message_start", "message": {"id": "m2", "usage": {"input_tokens": 31, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 0}}}`+"\n\n"+
		"event: message_delta\n"+
		`data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 31, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 24}}`+"\n\n"+
		"event: message_stop\n"+
		`data: {"type": "message_stop"}`+"\n\n")
	defer srv.Close()

	prov, err := BuildProvider(ModelConfig{
		Format: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "test",
	}, nil)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	u := foldUsage(t, prov)
	if u.InputTokens != 31 || u.OutputTokens != 24 {
		t.Fatalf("官方模式被改写或双计: got input=%d output=%d, want input=31 output=24", u.InputTokens, u.OutputTokens)
	}
}

// TestAnthropicUsageNonSSEPassthrough 非流式(JSON)与模型列表请求不经
// 改写(透传原样)。
func TestAnthropicUsageNonSSEPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"id":"m3","type":"message","role":"assistant","model":"t",` +
				`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,` +
				`"usage":{"input_tokens":9,"output_tokens":4,"cache_read_input_tokens":1}}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"test"}]}`))
		}
	}))
	defer srv.Close()

	prov, err := BuildProvider(ModelConfig{
		Format: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "test",
	}, nil)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	msg, _, usage, err := prov.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{llm.UserText("hi")}, MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if msg.Text() != "ok" || usage.InputTokens != 10 || usage.OutputTokens != 4 || usage.CacheReadTokens != 1 {
		t.Fatalf("非流式透传异常: text=%q usage=%+v", msg.Text(), usage)
	}
	ids, err := FetchModelList(context.Background(), ModelConfig{
		Format: "anthropic", BaseURL: srv.URL, APIKey: "test-key",
	})
	if err != nil || len(ids) != 1 || ids[0].ID != "test" {
		t.Fatalf("模型列表透传异常: %v %+v", err, ids)
	}
}

// TestAnthropicUsageNoStartEvent 兜底:message_start 整个缺失(部分网关
// 精简事件流),message_delta 是唯一用量来源——同样补全。
func TestAnthropicUsageNoStartEvent(t *testing.T) {
	srv := anthropicSSE(t, "event: content_block_delta\n"+
		`data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hi"}}`+"\n\n"+
		"event: message_delta\n"+
		`data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 5, "output_tokens": 2}}`+"\n\n"+
		"event: message_stop\n"+
		`data: {"type": "message_stop"}`+"\n\n")
	defer srv.Close()

	prov, err := BuildProvider(ModelConfig{
		Format: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "test",
	}, nil)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if u := foldUsage(t, prov); u.InputTokens != 5 || u.OutputTokens != 2 {
		t.Fatalf("无 message_start 流未补全: got input=%d output=%d, want 5/2", u.InputTokens, u.OutputTokens)
	}
}
