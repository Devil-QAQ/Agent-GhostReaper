package failover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ---- 从 API 加载可用模型列表(P2-08c 基础段) ----
//
// 用配置里的端点与 key 拉模型清单:openai / openai-responses 走
// GET {base}/models(Authorization: Bearer);anthropic 走
// GET {base}/v1/models(x-api-key + anthropic-version)。代理照配置走
//(仅 LLM 出站,与模型请求同一出口)。key 只进请求头,不进错误文案。

// ModelInfo 拉取到的一个模型:ContextWindow 是三段式建议窗口(P4-03 ③
// 问题清单 #13)——端点带 context 字段取端点值(WindowSource=endpoint,
// 最准);不带查内置对照表(table,近似值请核对);都匹配不上保守默认
// 200K(default,请手动核对)。MaxOutput 为端点顺带回填的输出上限参考。
type ModelInfo struct {
	ID            string `json:"id"`
	ContextWindow int    `json:"context_window,omitempty"`
	WindowSource  string `json:"window_source,omitempty"` // endpoint|table|default
	MaxOutput     int    `json:"max_output_tokens,omitempty"`
}

// FetchModelList 拉取端点的可用模型列表(按 id 排序稳定)。draft 或库内
// 配置皆可(编辑中未保存的草稿也能拉)。
func FetchModelList(ctx context.Context, cfg ModelConfig) ([]ModelInfo, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("failover: 端点为空")
	}
	endpoint := cfg.BaseURL + "/models"
	reqHeaders := map[string]string{"Authorization": "Bearer " + cfg.APIKey}
	if NormalizeFormat(cfg.Format) == "anthropic" {
		if !strings.HasSuffix(cfg.BaseURL, "/v1") {
			endpoint = cfg.BaseURL + "/v1/models"
		}
		reqHeaders = map[string]string{
			"x-api-key":         cfg.APIKey,
			"anthropic-version": "2023-06-01",
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failover: 构建请求: %w", err)
	}
	for k, v := range reqHeaders {
		req.Header.Set(k, v)
	}
	client, err := proxyHTTPClient(cfg.Proxy)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failover: 请求 %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failover: %s: status %d: %.200s", endpoint, resp.StatusCode, clipSpace(body))
	}
	if err != nil {
		return nil, fmt.Errorf("failover: 读响应: %w", err)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
			// 各网关的上下文字段名兼容集(取到首个非零值)。
			ContextWindow    int `json:"context_window"`
			ContextLength    int `json:"context_length"`
			MaxContextTokens int `json:"max_context_tokens"`
			MaxModelLen      int `json:"max_model_len"`
			MaxOutputTokens  int `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("failover: 响应不是模型清单 JSON: %w", err)
	}
	out := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		win, src := suggestedContextWindow(m.ID,
			firstPositive(m.ContextWindow, m.ContextLength, m.MaxContextTokens, m.MaxModelLen))
		out = append(out, ModelInfo{ID: m.ID,
			ContextWindow: win, WindowSource: src,
			MaxOutput: m.MaxOutputTokens,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("failover: 端点返回空清单")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// suggestedContextWindow 三段式窗口决策(P4-03 ③):端点值 > 内置对照
// 表 > 保守默认 200K;返回值与来源标注(endpoint|table|default)。
func suggestedContextWindow(modelID string, endpointWindow int) (int, string) {
	switch {
	case endpointWindow > 0:
		return endpointWindow, "endpoint"
	case LookupContextWindow(modelID) > 0:
		return LookupContextWindow(modelID), "table"
	default:
		return DefaultContextWindow, "default"
	}
}

func firstPositive(vs ...int) int {
	for _, v := range vs {
		if v > 0 {
			return v
		}
	}
	return 0
}

func clipSpace(b []byte) string {
	return strings.TrimSpace(string(b))
}

// proxyHTTPClient 按配置代理出站(与 llm 包 defaultHTTPClient 同规则:
// 空 = 环境变量代理;http/https/socks5 = 定向代理)。
func proxyHTTPClient(proxy string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy == "" {
		tr.Proxy = http.ProxyFromEnvironment
		return &http.Client{Transport: tr, Timeout: 20 * time.Second}, nil
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, fmt.Errorf("failover: 代理 %q 不可用: %w", proxy, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("failover: 代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
	}
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}, nil
}
