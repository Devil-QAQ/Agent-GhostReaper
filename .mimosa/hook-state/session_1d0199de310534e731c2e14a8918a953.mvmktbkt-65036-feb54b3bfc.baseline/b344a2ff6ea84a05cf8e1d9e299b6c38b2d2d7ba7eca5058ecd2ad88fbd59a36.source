package failover

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ---- anthropic SSE 用量输入侧补全(P4-03 ①) ----
//
// 真实端点行为差异(2026-10 实测):官方 Anthropic 与 DeepSeek 的
// anthropic 端点在 message_start 就携带真实输入侧用量(input_tokens /
// cache_read_input_tokens / cache_creation_input_tokens),message_delta
// 只补输出;GLM 的 anthropic 端点相反——message_start 的 usage 是全零,
// 真实输入侧只在 message_delta 里。Norma llm 包按官方行为解析:
// message_start 记输入侧、message_delta 只取 output_tokens——GLM 模式
// 下输入侧被整体丢弃,表现为会话信息条输入 / 缓存 / 上下文比例恒 0
// (问题清单 #6)。
//
// 修复层次:不动 Norma 依赖,在 GhostReaper 侧给 anthropic 格式的
// Provider 配自定义 HTTPClient,传输层对 text/event-stream 响应做一次
// 事件级改写——当 message_start 的输入侧为零而 message_delta 携带非零
// 输入侧时,在该 message_delta 之前合成一个只带输入侧的 message_start
// 事件。Norma 累加器对 message_start 用 Usage.Add 叠加,输入侧由此入账;
// 官方模式(message_start 已有真实输入)不触发改写,零双计。非 SSE
// 响应(非流式 / 模型列表)原样透传。

// newAnthropicUsageClient 构建 anthropic 格式专用 HTTP client:传输层包
// 输入侧补全改写,代理语义与 llm 包 defaultHTTPClient 一致(空=环境
// 变量;http/https/socks5=定向)。
func newAnthropicUsageClient(proxy string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy == "" {
		tr.Proxy = http.ProxyFromEnvironment
	} else {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("failover: 代理 %q 不可用: %v", proxy, err)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, fmt.Errorf("failover: 代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: anthropicUsageTransport{base: tr}}, nil
}

// anthropicUsageTransport RoundTripper 包装:SSE 响应体换补全读器,其余
// 原样透传。
type anthropicUsageTransport struct {
	base http.RoundTripper
}

func (t anthropicUsageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.Body == nil || !isEventStream(resp.Header.Get("Content-Type")) {
		return resp, nil
	}
	resp.Body = &sseUsageBackfill{src: bufio.NewReader(resp.Body)}
	return resp, nil
}

func isEventStream(contentType string) bool {
	for _, part := range strings.Split(contentType, ";") {
		if strings.TrimSpace(part) == "text/event-stream" {
			return true
		}
	}
	return false
}

// sseUsageBackfill SSE 事件级改写读器:逐事件(空行分隔)读源,按需在
// message_delta 前插入合成 message_start,其余字节原样透传。事件粒度
// 缓冲——单个事件(data 一行)体量小,流式延迟不受影响。
type sseUsageBackfill struct {
	src     *bufio.Reader
	out     bytes.Buffer // 已改写待读字节
	srcDone bool
	// startInputSeen message_start 是否已携带非零输入侧(官方模式)。
	startInputSeen bool
	// backfilled 本响应是否已补过一次(多 delta 场景防重复入账)。
	backfilled bool
}

// anthropicUsageShape SSE data JSON 的用量相关形状(只取关心的字段)。
type anthropicUsageShape struct {
	InputTokens              int `json:"input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

func (u anthropicUsageShape) inputSide() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// syntheticMessageStart 合成事件:只带输入侧(output 置零,Norma 在
// message_start 侧本就丢弃 output 占位值)。
func syntheticMessageStart(u anthropicUsageShape) string {
	payload := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"usage": map[string]int{
				"input_tokens":                u.InputTokens,
				"cache_read_input_tokens":     u.CacheReadInputTokens,
				"cache_creation_input_tokens": u.CacheCreationInputTokens,
				"output_tokens":               0,
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "" // 不可达:纯基础类型序列化
	}
	return "event: message_start\ndata: " + string(raw) + "\n\n"
}

// fill 读源事件改写进 out,直到 out 有可读字节或源耗尽。
func (b *sseUsageBackfill) fill() {
	for b.out.Len() == 0 && !b.srcDone {
		var event bytes.Buffer
		for {
			line, err := b.src.ReadString('\n')
			event.WriteString(line)
			if err != nil {
				b.srcDone = true
				break
			}
			if line == "\n" || line == "\r\n" {
				break // 事件结束(空行原样保留在事件尾部)
			}
		}
		if prefix := b.rewrite(event.String()); prefix != "" {
			b.out.WriteString(prefix)
		}
		b.out.Write(event.Bytes())
	}
}

// rewrite 分析一个事件:命中 GLM 模式(message_start 零输入侧 +
// message_delta 非零输入侧)时返回应插在事件前的合成 message_start,
// 其余返回空串。
func (b *sseUsageBackfill) rewrite(event string) string {
	data := sseDataOf(event)
	if data == "" {
		return ""
	}
	var shape struct {
		Type    string `json:"type"`
		Message struct {
			Usage *anthropicUsageShape `json:"usage"`
		} `json:"message"`
		Usage *anthropicUsageShape `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &shape) != nil {
		return ""
	}
	switch shape.Type {
	case "message_start":
		if shape.Message.Usage != nil && shape.Message.Usage.inputSide() > 0 {
			b.startInputSeen = true
		}
		return ""
	case "message_delta":
		if b.startInputSeen || b.backfilled || shape.Usage == nil || shape.Usage.inputSide() <= 0 {
			return ""
		}
		b.backfilled = true
		return syntheticMessageStart(*shape.Usage)
	}
	return ""
}

// Read 实现 io.Reader。
func (b *sseUsageBackfill) Read(p []byte) (int, error) {
	b.fill()
	if b.out.Len() == 0 {
		return 0, io.EOF
	}
	return b.out.Read(p)
}

// Close 关源。
func (b *sseUsageBackfill) Close() error { return nil }

// sseDataOf 从事件原文抽 data 行合并(多行 data 按 SSE 规范以 \n 连接)。
func sseDataOf(event string) string {
	var lines []string
	for _, line := range strings.Split(event, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			lines = append(lines, strings.TrimPrefix(rest, " "))
		}
	}
	return strings.Join(lines, "\n")
}
