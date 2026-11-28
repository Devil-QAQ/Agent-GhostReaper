// Package websearch executes user-configured HTTP search APIs.
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/proxy"
)

const MaxResults = 8
const searchTimeout = 20 * time.Second
const maxSearchBody = 2 << 20
const maxRawText = 64 << 10

// Config describes HTTP request data and response fields, never provider code.
type Config struct {
	CustomLimits  bool              `json:"custom_limits,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	PerMinute     int               `json:"per_minute,omitempty"`
	CodePerMinute int               `json:"code_per_minute,omitempty"`
	PerHour       int               `json:"per_hour,omitempty"`
	PerDay        int               `json:"per_day,omitempty"`
	PageSize      int               `json:"page_size,omitempty"`
	QPS           float64           `json:"qps,omitempty"`
	LimitPlan     string            `json:"limit_plan,omitempty"`
	LimitsVersion int               `json:"limits_version,omitempty"`
	Endpoint      string            `json:"url"`
	Method        string            `json:"method"`
	Encoding      string            `json:"encoding"`
	Headers       map[string]string `json:"headers"`
	Params        map[string]any    `json:"params"`
	ResponseMode  string            `json:"response_mode"`
	ResultsPath   string            `json:"results_path"`
	TitlePath     string            `json:"title_path"`
	URLPath       string            `json:"url_path"`
	SnippetPath   string            `json:"snippet_path"`
	APIKey        string            `json:"api_key,omitempty"`
	Proxy         string            `json:"proxy,omitempty"`
	ServiceID     string            `json:"-"`
}

type Result struct {
	Title     string
	URL       string
	Snippet   string
	Raw       bool
	Truncated bool
}

func normalizeConfig(cfg Config) (Config, error) {
	if IsNative(cfg.Kind) {
		return normalizeAsset(cfg)
	}
	if cfg.Kind == "github" {
		return normalizeGitHub(cfg)
	}
	if cfg.Kind != "" && cfg.Kind != "custom" {
		return cfg, errors.New("搜索站点类型无效")
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return cfg, errors.New("填写搜索接口的 HTTP 或 HTTPS 地址，凭据单独配置")
	}
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	if cfg.Method != http.MethodGet && cfg.Method != http.MethodPost {
		return cfg, errors.New("搜索请求方式须为 GET 或 POST")
	}
	if cfg.Encoding == "" {
		cfg.Encoding = "json"
	}
	if cfg.Encoding != "json" && cfg.Encoding != "form" {
		return cfg, errors.New("请求正文须为 JSON 或表单")
	}
	if cfg.ResponseMode == "" {
		cfg.ResponseMode = "text"
	}
	if cfg.ResponseMode != "json" && cfg.ResponseMode != "text" {
		return cfg, errors.New("返回格式须为结构化结果或原始响应")
	}
	for name, value := range cfg.Headers {
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			return cfg, errors.New("请求头格式无效")
		}
	}
	if err := ValidateProxy(cfg.Proxy); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Search(ctx context.Context, cfg Config, query string) ([]Result, error) {
	if IsNative(cfg.Kind) {
		return nil, errors.New("资产查询使用站点入口，统一核对请求额度")
	}
	if cfg.Kind == "github" {
		return nil, errors.New("GitHub 使用站点查询入口，以便统一核对请求额度")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("搜索词为空，填写关键词后重发")
	}
	if cfg.Endpoint == "" {
		return nil, ErrNotConfigured
	}
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	render := func(value string) (string, error) {
		if strings.Contains(value, "{{api_key}}") && cfg.APIKey == "" {
			return "", errors.New("请求配置引用了密钥，但尚未配置密钥")
		}
		return strings.NewReplacer("{{api_key}}", cfg.APIKey, "{{query}}", query).Replace(value), nil
	}
	params, err := renderValue(cfg.Params, render)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	u, _ := url.Parse(cfg.Endpoint)
	if cfg.Method == http.MethodGet {
		values := u.Query()
		addParams(values, params)
		u.RawQuery = values.Encode()
	} else if cfg.Encoding == "form" {
		values := url.Values{}
		addParams(values, params)
		body = strings.NewReader(values.Encode())
	} else {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, errors.New("请求参数无法编码")
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, cfg.Method, u.String(), body)
	if err != nil {
		return nil, errors.New("搜索请求无法创建")
	}
	request.Header.Set("Accept", "application/json")
	if cfg.Method == http.MethodPost {
		if cfg.Encoding == "form" {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			request.Header.Set("Content-Type", "application/json")
		}
	}
	for name, value := range cfg.Headers {
		rendered, err := render(value)
		if err != nil {
			return nil, err
		}
		if !httpguts.ValidHeaderFieldValue(rendered) {
			return nil, errors.New("搜索词或密钥不适用于当前请求头，核对请求配置")
		}
		request.Header.Set(textproto.CanonicalMIMEHeaderKey(name), rendered)
	}
	client, err := clientFor(cfg.Proxy)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("搜索接口重定向次数过多")
		}
		if !strings.EqualFold(next.URL.Scheme, u.Scheme) || !strings.EqualFold(next.URL.Host, u.Host) {
			return errors.New("搜索接口跳转至其他站点，配置最终接口地址")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, unavailable("搜索请求失败，检查接口地址、网络或出口代理", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSearchBody+1))
	if err != nil {
		return nil, unavailable("搜索响应读取失败", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, searchStatusError("搜索服务", response.StatusCode)
	}
	if len(raw) > maxSearchBody {
		return nil, unavailable("搜索响应超过 2 MiB，调整服务的返回数量", nil)
	}
	redact := func(value string) string {
		if cfg.APIKey != "" {
			return strings.ReplaceAll(value, cfg.APIKey, "[密钥已隐藏]")
		}
		return value
	}
	if cfg.ResponseMode == "text" {
		truncated := len(raw) > maxRawText
		if truncated {
			raw = raw[:maxRawText]
			for n := 0; n < utf8.UTFMax-1 && len(raw) > 0 && !utf8.Valid(raw); n++ {
				raw = raw[:len(raw)-1]
			}
		}
		if !utf8.Valid(raw) {
			return nil, unavailable("原始响应不是 UTF-8 文本，核对接口返回格式", nil)
		}
		return []Result{{URL: cfg.Endpoint, Snippet: redact(string(raw)), Raw: true, Truncated: truncated}}, nil
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, unavailable("响应不是有效 JSON，可核对格式或选择原始响应", nil)
	}
	rows, ok := valueAt(decoded, cfg.ResultsPath)
	items, okArray := rows.([]any)
	if !ok || !okArray {
		return nil, unavailable("响应中的结果列表路径不匹配，不能判断为无搜索结果", nil)
	}
	results := make([]Result, 0, len(items))
	for _, item := range items {
		title, _ := valueAt(item, cfg.TitlePath)
		link, _ := valueAt(item, cfg.URLPath)
		snippet, _ := valueAt(item, cfg.SnippetPath)
		address, ok := link.(string)
		if !ok || address == "" {
			continue
		}
		addressURL, err := url.Parse(address)
		if err != nil {
			continue
		}
		addressURL = u.ResolveReference(addressURL)
		if addressURL.Scheme != "http" && addressURL.Scheme != "https" {
			continue
		}
		name, _ := title.(string)
		text, _ := snippet.(string)
		results = append(results, Result{Title: redact(name), URL: redact(addressURL.String()), Snippet: redact(text)})
		if len(results) == MaxResults {
			break
		}
	}
	if len(items) > 0 && len(results) == 0 {
		return nil, unavailable("结果链接字段不匹配，可核对字段或选择原始响应", nil)
	}
	return results, nil
}

func renderValue(value any, render func(string) (string, error)) (any, error) {
	switch v := value.(type) {
	case string:
		return render(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			next, err := renderValue(item, render)
			if err != nil {
				return nil, err
			}
			out[key] = next
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			next, err := renderValue(item, render)
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	default:
		return value, nil
	}
}

func addParams(values url.Values, params any) {
	entries, _ := params.(map[string]any)
	add := func(key string, value any) {
		if s, ok := value.(string); ok {
			values.Add(key, s)
		} else {
			encoded, _ := json.Marshal(value)
			values.Add(key, string(encoded))
		}
	}
	for key, value := range entries {
		if items, ok := value.([]any); ok {
			for _, item := range items {
				add(key, item)
			}
		} else {
			add(key, value)
		}
	}
}

// Dot paths cover common API fields; JSON pointers cover literal dots/slashes.
func valueAt(value any, path string) (any, bool) {
	if path == "" {
		return value, true
	}
	parts := strings.Split(path, ".")
	if strings.HasPrefix(path, "/") {
		parts = strings.Split(path[1:], "/")
		for i, part := range parts {
			parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		}
	}
	for _, part := range parts {
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[part]
			if !ok {
				return nil, false
			}
		case []any:
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n >= len(current) {
				return nil, false
			}
			value = current[n]
		default:
			return nil, false
		}
	}
	return value, true
}

func searchStatusError(label string, status int) error {
	message := fmt.Sprintf("%s返回 %d", label, status)
	if status == 401 || status == 403 {
		message += "，核对能力库中的搜索接口和密钥"
	}
	if status == 429 || status >= 500 || status == 401 || status == 403 {
		return unavailable(message, nil)
	}
	return errors.New(message + "，核对请求参数")
}
func clientFor(proxyURL string) (*http.Client, error) {
	if err := ValidateProxy(proxyURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(proxyURL) == "" {
		return &http.Client{Timeout: searchTimeout, Transport: &http.Transport{}}, nil
	}
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil {
		return nil, fmt.Errorf("出口代理地址非法: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		return &http.Client{
			Timeout:   searchTimeout,
			Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		}, nil
	case "socks5":
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "1080")
		}
		dialer, derr := proxy.FromURL(u, proxy.Direct)
		if derr != nil {
			return nil, fmt.Errorf("socks5 代理不可用: %w", derr)
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks5 代理不支持上下文拨号")
		}
		return &http.Client{
			Timeout:   searchTimeout,
			Transport: &http.Transport{DialContext: cd.DialContext},
		}, nil
	case "socks4a":
		return &http.Client{Timeout: searchTimeout, Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialSOCKS4a(ctx, u, network, address)
		}}}, nil
	default:
		return nil, errors.New("出口代理协议不受支持")
	}
}
