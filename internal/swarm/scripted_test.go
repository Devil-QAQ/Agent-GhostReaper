package swarm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/Autumn-27/norma/llm"
)

// scriptedModel 进程内 OpenAI Chat Completions 兼容 SSE 模型服务
// (P1-02 同款做法的精简版):真实 HTTP、真实流式分块;唯一脚本是回复
// 内容,由 script 函数按观察到的对话状态决定——与真实模型"看到工具
// 结果后决定下一步"同构。虫群引擎链路(适配器/流式/工具广告)全真。
type scriptedModel struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recordedRequest
	bad    int
	script func(rr *recordedRequest) scriptedReply
}

type oaMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type recordedRequest struct {
	Index        int
	System       string
	Messages     []oaMessage
	ToolsOffered []string
	IsSummary    bool
	LastUser     string
	BodyChars    int // 请求体规模(prompt_tokens 据此回报,引擎 compaction 计数用)
}

type scriptedToolCall struct {
	ID   string
	Name string
	Args string
}

type scriptedReply struct {
	Text      string
	ToolCalls []scriptedToolCall
	Finish    string
}

func newScriptedModel(script func(rr *recordedRequest) scriptedReply) *scriptedModel {
	m := &scriptedModel{script: script}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *scriptedModel) Close() { m.srv.Close() }

func (m *scriptedModel) provider() (llm.Provider, error) {
	return llm.NewProvider(llm.Config{
		Format:  llm.FormatOpenAI,
		BaseURL: m.srv.URL,
		APIKey:  "test-key",
		Model:   "scripted-swarm-model",
	})
}

func (m *scriptedModel) requests() []recordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedRequest(nil), m.reqs...)
}

func (m *scriptedModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

func (m *scriptedModel) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		m.mu.Lock()
		m.bad++
		m.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body struct {
		Messages []oaMessage `json:"messages"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rr := recordedRequest{}
	for _, msg := range body.Messages {
		if msg.Role == "system" {
			rr.System += msg.Content
		} else {
			rr.Messages = append(rr.Messages, msg)
			if msg.Role == "user" {
				rr.LastUser = msg.Content
			}
		}
	}
	for _, t := range body.Tools {
		rr.ToolsOffered = append(rr.ToolsOffered, t.Function.Name)
	}
	rr.BodyChars = len(rr.System)
	for _, msg := range rr.Messages {
		rr.BodyChars += len(msg.Content) + len(msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			rr.BodyChars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	rr.IsSummary = strings.Contains(rr.System, "summarizing conversations")
	m.mu.Lock()
	rr.Index = len(m.reqs)
	m.reqs = append(m.reqs, rr)
	m.mu.Unlock()

	reply := m.script(&rr)
	m.writeSSE(w, reply, rr)
}

// writeSSE 按真实 OpenAI 流式线格式回写。prompt_tokens 按请求体规模
// 回报(≈4 字符/token)——引擎的 compaction 计数优先采用上一轮回报的
// 输入规模,固定小值会让压缩线永远够不着(P2-06d 组装测试前提)。
func (m *scriptedModel) writeSSE(w http.ResponseWriter, reply scriptedReply, rr recordedRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(payload any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if t := reply.Text; t != "" {
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": t}}}})
	}
	for i, tc := range reply.ToolCalls {
		args := tc.Args
		if args == "" {
			args = "{}"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": i, "id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": ""}}}}}}})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": i,
				"function": map[string]any{"arguments": args}}}}}}})
	}
	finish := reply.Finish
	if finish == "" {
		finish = "stop"
	}
	send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
	completion := len(reply.Text)
	for _, tc := range reply.ToolCalls {
		completion += len(tc.Args) + 32
	}
	send(map[string]any{"choices": []any{},
		"usage": map[string]any{"prompt_tokens": rr.BodyChars / 4, "completion_tokens": completion/4 + 8}})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// seenToolResult 报告某工具的结果内容是否包含 marker。
func seenToolResult(rr *recordedRequest, toolName, marker string) bool {
	id2name := map[string]string{}
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				id2name[tc.ID] = tc.Function.Name
			}
		}
	}
	for _, msg := range rr.Messages {
		if msg.Role == "tool" && id2name[msg.ToolCallID] == toolName && strings.Contains(msg.Content, marker) {
			return true
		}
	}
	return false
}
