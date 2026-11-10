package overmind

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// gatedScriptedModel 进程内 OpenAI Chat Completions 兼容 SSE 模型服务
// (与 swarm/normapoc 的脚本模型同构,独立维护):真实 HTTP、真实流式
// 分块,回复内容由 script 按观察到的对话状态决定——与真实模型"看到
// 态势后决定派发什么"同构。额外两组测试观测器:
//  1. 一次性闸门 gate:虫群会话请求(带工具广告)在放行前全部挂起,
//     测试据此确定性地观测"恰 3 只单位在飞、第 4 只未起飞";
//  2. 在飞计数:虫群请求的并发峰值(maxInflight);
//  3. 按请求挂起 holdReq(P4-04):虫群会话请求按其内容返回一个挂起
//     通道(闸门之后),测试据此确定性控制"某只单位慢、另一只先落地"
//     的节奏——增量轮验证的观测器。须在首次唤醒前设置(与请求处理
//     goroutine 有 happens-before:唤醒链)。
//
// 拆解请求(无工具广告)不经闸门与挂起。
type gatedScriptedModel struct {
	srv    *httptest.Server
	gate   chan struct{}
	script func(rr *recordedRequest) scriptedReply
	// holdReq 按请求内容挂起虫群会话(返回 nil 即不挂)。
	holdReq     func(rr *recordedRequest) <-chan struct{}
	inflight    atomic.Int32
	maxInflight atomic.Int32

	mu   sync.Mutex
	reqs []recordedRequest
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
	LastUser     string
	MaxTokens    *int // 请求携带的输出上限(nil = 字段未发送,P3-14a a 观测点)
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

func newGatedScriptedModel(script func(rr *recordedRequest) scriptedReply) *gatedScriptedModel {
	m := &gatedScriptedModel{script: script, gate: make(chan struct{})}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *gatedScriptedModel) Close()   { m.srv.Close() }
func (m *gatedScriptedModel) release() { close(m.gate) }

func (m *gatedScriptedModel) provider() (llm.Provider, error) {
	return llm.NewProvider(llm.Config{
		Format:  llm.FormatOpenAI,
		BaseURL: m.srv.URL,
		APIKey:  "test-key",
		Model:   "scripted-overmind-model",
	})
}

func (m *gatedScriptedModel) requests() []recordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recordedRequest(nil), m.reqs...)
}

// decomposeRequests 拆解请求子集(P4-65 修正:按拆解会话特征过滤——
// system 段带"态势规划标准五问";研判校对/摘要族等生成会话同样零工具
// 广告,不能按"无工具"归入拆解计数)。
func (m *gatedScriptedModel) decomposeRequests() []recordedRequest {
	var out []recordedRequest
	for _, r := range m.requests() {
		if len(r.ToolsOffered) == 0 && strings.Contains(r.System, "态势规划标准五问") {
			out = append(out, r)
		}
	}
	return out
}

func (m *gatedScriptedModel) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
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
		MaxTokens *int `json:"max_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rr := recordedRequest{MaxTokens: body.MaxTokens}
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
	m.mu.Lock()
	rr.Index = len(m.reqs)
	m.reqs = append(m.reqs, rr)
	m.mu.Unlock()

	if len(rr.ToolsOffered) > 0 {
		// 虫群会话:计入在飞并发,放行前挂起(观测并发上限)。
		n := m.inflight.Add(1)
		for {
			cur := m.maxInflight.Load()
			if n <= cur || m.maxInflight.CompareAndSwap(cur, n) {
				break
			}
		}
		defer m.inflight.Add(-1)
		<-m.gate
		if m.holdReq != nil {
			if ch := m.holdReq(&rr); ch != nil {
				<-ch
			}
		}
	}
	m.writeSSE(w, m.script(&rr))
}

func (m *gatedScriptedModel) writeSSE(w http.ResponseWriter, reply scriptedReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(p any) {
		b, _ := json.Marshal(p)
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
	send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 40}})
	fmt.Fprint(w, "data: [DONE]\n\n")
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

// hasToolCallWithArg 报告历史里是否已发起过指定工具且参数含 marker
// (submit_test_claim 两次提交的结果文本相同,只能按参数区分)。
func hasToolCallWithArg(rr *recordedRequest, toolName, marker string) bool {
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == toolName && strings.Contains(tc.Function.Arguments, marker) {
				return true
			}
		}
	}
	return false
}

// seedTCPTarget 起一个本地 TCP 靶标:accept 后写 banner 再断开。
func seedTCPTarget(t *testing.T, banner string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地靶标失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(banner))
			_ = c.Close()
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}
