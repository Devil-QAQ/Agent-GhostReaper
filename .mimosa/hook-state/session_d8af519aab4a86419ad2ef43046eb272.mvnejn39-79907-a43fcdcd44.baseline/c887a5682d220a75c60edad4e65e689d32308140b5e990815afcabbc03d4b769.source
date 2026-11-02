package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"ghostreaper/internal/rings"
)

// 后端标识(设置值,面板与工具同一份词汇)。
const (
	BackendDDGS   = "ddgs"
	BackendBrave  = "brave"
	BackendTavily = "tavily"
)

// 设置键(rings 通用配置键值表,命名空间 search.*;与 swarm.budget.* /
// mirror.ca.* 同款形态)。key 明文存库(P3-09 凭据静态加密时随 LLM key
// 一并收口),读侧打码由 server 视图承担——本包只给工具用的原文形态。
const (
	SettingKeyBackend   = "search.backend"
	SettingKeyBraveKey  = "search.brave.key"
	SettingKeyTavilyKey = "search.tavily.key"
	SettingKeyProxy     = "search.proxy"
)

// Config 一次搜索的生效配置(Load 从设置表拼出;零值 = 全默认)。
type Config struct {
	Backend   string // ddgs(默认)|brave|tavily
	BraveKey  string // Brave API key(免费档)
	TavilyKey string // Tavily API key(付费)
	Proxy     string // 独立出口代理(http/https/socks5,可带凭据;空=直连)
}

// NormalizeBackend 归一后端标识:已知三值原样,其余(含空)归 ddgs
// ——默认免 key 后端是设计决策(子计划 P3-03),未知值不报错按默认跑。
func NormalizeBackend(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case BackendBrave:
		return BackendBrave
	case BackendTavily:
		return BackendTavily
	default:
		return BackendDDGS
	}
}

// BackendLabel 后端的展示名(工具返回与面板共用,操作者语言)。
func BackendLabel(b string) string {
	switch NormalizeBackend(b) {
	case BackendBrave:
		return "Brave"
	case BackendTavily:
		return "Tavily"
	default:
		return "DuckDuckGo"
	}
}

// Load 从设置表拼出生效配置。fail-safe:读失败/缺键一律按默认(ddgs、
// 无 key、直连)——配置问题由设置页读路径呈现,不挡工具。
func Load(ctx context.Context, store *rings.Store) Config {
	cfg := Config{Backend: BackendDDGS}
	if store == nil {
		return cfg
	}
	get := func(key string) string {
		e, ok, err := store.Setting(ctx, key)
		if err != nil || !ok {
			return ""
		}
		return strings.TrimSpace(e.Value)
	}
	cfg.Backend = NormalizeBackend(get(SettingKeyBackend))
	cfg.BraveKey = get(SettingKeyBraveKey)
	cfg.TavilyKey = get(SettingKeyTavilyKey)
	cfg.Proxy = get(SettingKeyProxy)
	return cfg
}

// Save 整份写入设置表:空值 = 删键(键值表值非空约束下,删键即"恢复
// 装配默认"的既定语义)。by 为写者标记(面板路径恒 creator),随键值
// 表轻量审计。
func Save(ctx context.Context, store *rings.Store, cfg Config, by string) error {
	kv := map[string]string{
		SettingKeyBackend:   NormalizeBackend(cfg.Backend),
		SettingKeyBraveKey:  cfg.BraveKey,
		SettingKeyTavilyKey: cfg.TavilyKey,
		SettingKeyProxy:     cfg.Proxy,
	}
	for k, v := range kv {
		var err error
		if v == "" {
			err = store.DeleteSetting(ctx, k)
		} else {
			err = store.SetSetting(ctx, k, v, by)
		}
		if err != nil {
			return fmt.Errorf("websearch: 写配置 %s: %w", k, err)
		}
	}
	return nil
}

// ValidateProxy 出口代理合法性(与 LLM 代理同口径:http/https/socks5,
// 可带 user:pass@)。空串合法(=直连)。
func ValidateProxy(p string) error {
	if strings.TrimSpace(p) == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(p))
	if err != nil || u.Host == "" {
		return errors.New("代理地址解析失败(形如 http://127.0.0.1:7890 或 socks5://host:port)")
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		return nil
	default:
		return fmt.Errorf("代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
	}
}

// Result 一条搜索结果(三后端归一的形态)。
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// MaxResults 单次搜索返回上限(三家一致;多给无益于模型,少给换词重搜)。
const MaxResults = 8

// 搜索端点(包级变量:测试与自托管网关场景可覆写;生产值不动)。
var (
	DDGSEndpoint   = "https://html.duckduckgo.com/html/"
	BraveEndpoint  = "https://api.search.brave.com/res/v1/web/search"
	TavilyEndpoint = "https://api.tavily.com/search"
)

// searchTimeout 单次搜索的整段时限(出口代理慢时也别把单位拖死)。
const searchTimeout = 20 * time.Second

// ddgsUA DuckDuckGo HTML 端点对非浏览器 UA 有拦截倾向,按桌面浏览器
// 形态发(与 ddgs 生态工具同款做法)。
const ddgsUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Search 按配置执行一次外网搜索。错误面向模型(工具直接上抛),写清
// 原因与出路(配置在系统设置·搜索配置)。
func Search(ctx context.Context, cfg Config, query string) ([]Result, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, errors.New("搜索词为空:填入要查证的关键词(如 CVE 编号)后重发")
	}
	client, err := clientFor(cfg.Proxy)
	if err != nil {
		return nil, err
	}
	switch NormalizeBackend(cfg.Backend) {
	case BackendBrave:
		if cfg.BraveKey == "" {
			return nil, errors.New("Brave 后端需要 key:在系统设置·搜索配置填写 Brave key,或切回 DuckDuckGo(免 key)")
		}
		return searchBrave(ctx, client, cfg.BraveKey, q)
	case BackendTavily:
		if cfg.TavilyKey == "" {
			return nil, errors.New("Tavily 后端需要 key:在系统设置·搜索配置填写 Tavily key,或切回 DuckDuckGo(免 key)")
		}
		return searchTavily(ctx, client, cfg.TavilyKey, q)
	default:
		return searchDDGS(ctx, client, q)
	}
}

// clientFor 出口 HTTP 客户端:配了独立出口代理走代理(http/https 经
// ProxyURL,socks5 经 x/net/proxy 拨号),没配直连。每次搜索新建——
// 搜索是低频动作,不值得为代理热切换维护客户端池。
func clientFor(proxyURL string) (*http.Client, error) {
	if strings.TrimSpace(proxyURL) == "" {
		return &http.Client{Timeout: searchTimeout}, nil
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
	default:
		return nil, fmt.Errorf("出口代理协议 %q 不受支持(仅 http/https/socks5)", u.Scheme)
	}
}

// ---- DuckDuckGo(默认免 key 后端) ----

// searchDDGS DuckDuckGo HTML 端点:POST 表单取结果页,解析 result__a /
// result__snippet。零结果/异常页如实报错(可能被限流或网络受阻)。
func searchDDGS(ctx context.Context, client *http.Client, query string) ([]Result, error) {
	form := url.Values{"q": []string{query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DDGSEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ddgsUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DuckDuckGo 请求失败: %w(网络不通时可在系统设置·搜索配置配独立出口代理)", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("DuckDuckGo 响应读取失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo 返回 %d(可能被限流,稍后重试或切后端)", resp.StatusCode)
	}
	results := parseDDGS(body)
	if len(results) == 0 {
		return nil, errors.New("DuckDuckGo 返回 0 条结果(换关键词重试;持续为空可能被限流或网络受阻,可在系统设置·搜索配置切后端或配独立出口代理)")
	}
	return results, nil
}

// parseDDGS 解析 DuckDuckGo HTML 结果页:锚点 class 含 result__a 的是
// 结果链接、result__snippet 是摘要;链接多为 //duckduckgo.com/l/?uddg=
// 跳转形态,解出真实 URL。标题/摘要内的 <b> 高亮与实体照实还原。
func parseDDGS(body []byte) []Result {
	var out []Result
	for _, a := range anchorTags(string(body)) {
		class := attrOf(a.tag, "class")
		href := attrOf(a.tag, "href")
		switch {
		case strings.Contains(class, "result__a") && href != "":
			out = append(out, Result{URL: decodeDDGHref(href), Title: htmlText(a.inner)})
		case strings.Contains(class, "result__snippet") && len(out) > 0:
			if out[len(out)-1].Snippet == "" {
				out[len(out)-1].Snippet = htmlText(a.inner)
			}
		}
		if len(out) > MaxResults {
			break
		}
	}
	trimmed := out[:0]
	for _, r := range out {
		if r.URL != "" {
			trimmed = append(trimmed, r)
		}
	}
	if len(trimmed) > MaxResults {
		trimmed = trimmed[:MaxResults]
	}
	return trimmed
}

// anchor 一个锚点的整标签与内层 HTML(<a …> 到最近 </a>;DDG 结果锚
// 内无嵌套锚,就近配对足够)。
type anchor struct {
	tag   string
	inner string
}

// anchorTags 扫出页面全部锚点(属性序不定,逐标签解析而非整页正则)。
func anchorTags(s string) []anchor {
	var out []anchor
	for rest := s; ; {
		i := strings.Index(rest, "<a ")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, ">")
		if j < 0 {
			return out
		}
		tag := rest[:j]
		after := rest[j+1:]
		k := strings.Index(after, "</a>")
		if k < 0 {
			return out
		}
		out = append(out, anchor{tag: tag, inner: after[:k]})
		rest = after[k+4:]
	}
}

// attrOf 取标签属性值(双引号形态;DDG 页面稳定双引号)。
func attrOf(tag, name string) string {
	i := strings.Index(tag, name+"=\"")
	if i < 0 {
		return ""
	}
	rest := tag[i+len(name)+2:]
	if j := strings.Index(rest, "\""); j >= 0 {
		return rest[:j]
	}
	return ""
}

// decodeDDGHref 还原 DDG 跳转链接为真实 URL://duckduckgo.com/l/?uddg=
// <percent 编码> 解出;协议相对 //host/path 补 https:;其余(http/https
// 直链)原样。
func decodeDDGHref(href string) string {
	h := href
	if strings.HasPrefix(h, "//") {
		h = "https:" + h
	}
	u, err := url.Parse(h)
	if err != nil {
		return h
	}
	if strings.HasSuffix(u.Host, ".duckduckgo.com") || u.Host == "duckduckgo.com" {
		if u.Path == "/l/" {
			if raw := u.Query().Get("uddg"); raw != "" {
				if dec, derr := url.QueryUnescape(raw); derr == nil {
					return dec
				}
			}
		}
	}
	return u.String()
}

// ---- Brave / Tavily(JSON API 后端) ----

// searchBrave Brave Search API(免费 key):GET + X-Subscription-Token。
func searchBrave(ctx context.Context, client *http.Client, key, query string) ([]Result, error) {
	u := BraveEndpoint + "?" + url.Values{
		"q": []string{query}, "count": []string{fmt.Sprintf("%d", MaxResults)},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	body, err := doSearchRequest(ctx, client, req, "Brave")
	if err != nil {
		return nil, err
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("Brave 响应解码失败: %w", err)
	}
	rs := make([]Result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		rs = append(rs, Result{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return nonEmpty(rs, "Brave")
}

// searchTavily Tavily API(付费 key):POST JSON + Bearer。
func searchTavily(ctx context.Context, client *http.Client, key, query string) ([]Result, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "max_results": MaxResults})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TavilyEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	raw, err := doSearchRequest(ctx, client, req, "Tavily")
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("Tavily 响应解码失败: %w", err)
	}
	rs := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		rs = append(rs, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return nonEmpty(rs, "Tavily")
}

// doSearchRequest JSON 后端公共骨架:发请求、限长读体、状态码分级报错
// (401/403=key 问题;其余非 200 带状态与响应截头)。
func doSearchRequest(ctx context.Context, client *http.Client, req *http.Request, backend string) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s 请求失败: %w(网络不通时可在系统设置·搜索配置配独立出口代理)", backend, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s 响应读取失败: %w", backend, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%s 拒绝访问(%d):key 无效或过期,核对系统设置·搜索配置里的 key", backend, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 %d:%s", backend, resp.StatusCode, clipStr(string(body), 200))
	}
	return body, nil
}

// nonEmpty 空结果如实报(区别于解析失败;换词或切后端是出路)。
func nonEmpty(rs []Result, backend string) ([]Result, error) {
	if len(rs) == 0 {
		return nil, fmt.Errorf("%s 返回 0 条结果(换关键词重试或切后端)", backend)
	}
	return rs, nil
}

// ---- 小件 ----

// htmlText HTML 片段转纯文本:剥内层标签、还原实体、收空白。
func htmlText(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	text := strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
	return clipStr(text, 500)
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
