package websearch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func WebProviders() []AssetProvider {
	return []AssetProvider{
		{"ddpoc", "DDPoc", "https://www.ddpoc.com", "https://www.ddpoc.com/list.html", 10, 0, false, "vulnerability"},
		{"bing", "Bing", "https://www.bing.com", "https://www.bing.com/", 50, 0, true, "web"},
		{"ddgs", "DuckDuckGo", "https://html.duckduckgo.com", "https://duckduckgo.com/duckduckgo-help-pages/results/", 30, 0, true, "web"},
		{"brave", "Brave", "https://api.search.brave.com", "https://api-dashboard.search.brave.com/api-reference/web/search/get", 20, 0, false, "web"},
		{"tavily", "Tavily", "https://api.tavily.com", "https://docs.tavily.com/documentation/api-reference/endpoint/search", 20, 0, false, "web"},
		{"deepseek", "DeepSeek", "https://api.deepseek.com", "https://api-docs.deepseek.com/api/search/", 50, 0, false, "web"},
	}
}
func SiteProviders() []AssetProvider { return append(AssetProviders(), WebProviders()...) }
func siteProvider(kind string) (AssetProvider, bool) {
	for _, p := range SiteProviders() {
		if p.Kind == kind {
			return p, true
		}
	}
	return AssetProvider{}, false
}
func IsWeb(kind string) bool {
	for _, p := range WebProviders() {
		if p.Kind == kind {
			return true
		}
	}
	return false
}
func IsNative(kind string) bool { _, ok := siteProvider(kind); return ok }
func NeedsKey(kind string) bool { p, ok := siteProvider(kind); return ok && !p.NoKey }

func webRequest(ctx context.Context, cfg Config, q AssetQuery) (*http.Request, int, error) {
	p, _ := siteProvider(cfg.Kind)
	size := q.Size
	if size == 0 {
		size = cfg.PageSize
	}
	page := max(q.Page, 1)
	if (strings.TrimSpace(q.Query) == "" && !(cfg.Kind == "ddpoc" && q.Action == "read")) || len(q.Query) > 16384 {
		return nil, 0, errors.New("填写有效搜索词")
	}
	if q.Page < 0 || size < 1 || size > p.MaxSize {
		return nil, 0, fmt.Errorf("每页条数须为 1 至 %d，页码从 1 起", p.MaxSize)
	}
	if (cfg.Kind == "tavily" || cfg.Kind == "deepseek" || cfg.Kind == "ddgs") && page != 1 {
		return nil, 0, errors.New("该搜索入口不支持页码翻页，可调整查询词或返回条数继续搜索")
	}
	if cfg.Kind == "brave" && (page > 10 || utf8.RuneCountInString(q.Query) > 600) {
		return nil, 0, errors.New("Brave 支持 1 至 10 页，搜索词最多 600 字符")
	}
	if cfg.Kind == "deepseek" && utf8.RuneCountInString(q.Query) > 128 {
		return nil, 0, errors.New("DeepSeek 搜索词最多 128 字符")
	}
	params := map[string]any{}
	for k, v := range q.Options {
		switch strings.ToLower(k) {
		case "q", "query", "count", "max_results", "first", "offset", "page", "size", "api_key", "api-key", "key", "authorization", "x-subscription-token", "keyword", "cookie", "web1":
			return nil, 0, fmt.Errorf("附加参数不能覆盖 %s", k)
		}
		params[k] = v
	}
	method, path := "GET", ""
	h := map[string]string{}
	switch cfg.Kind {
	case "ddpoc":
		if q.Action != "" && q.Action != "search" && q.Action != "read" {
			return nil, 0, errors.New("DDPoc 支持 search 搜索和 read 阅读")
		}
		path = "/list.html"
		if q.Action == "read" {
			if len(params) != 0 {
				return nil, 0, errors.New("DDPoc 阅读不使用附加查询参数")
			}
			target := strings.TrimSpace(q.Path)
			if target == "" {
				target = q.Query
			}
			if !strings.Contains(target, "/") {
				target = strings.TrimSuffix(target, ".html") + ".html"
			}
			root, _ := url.Parse(cfg.Endpoint + "/")
			u, e := url.Parse(target)
			if e != nil {
				return nil, 0, errors.New("DDPoc 页面地址无效")
			}
			u = root.ResolveReference(u)
			file := strings.TrimPrefix(u.Path, root.Path)
			if u.Host != root.Host || u.Scheme != root.Scheme || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !ddpocFile.MatchString(file) {
				return nil, 0, errors.New("DDPoc 阅读仅支持本站漏洞页面")
			}
			path = "/" + file
		} else {
			params["keyword"] = q.Query
			params["page"] = page
		}
		h["Cookie"] = "web1=" + cfg.APIKey
		h["Referer"] = cfg.Endpoint + "/list.html"
	case "bing":
		path = "/search"
		params["q"] = q.Query
		params["count"] = size
		params["first"] = (page-1)*size + 1
	case "ddgs":
		method = "POST"
		path = "/html/"
		params["q"] = q.Query
		if _, ok := params["kl"]; !ok {
			params["kl"] = "wt-wt"
		}
	case "brave":
		path = "/res/v1/web/search"
		h["X-Subscription-Token"] = cfg.APIKey
		params["q"] = q.Query
		params["count"] = size
		params["offset"] = page - 1
	case "tavily":
		method = "POST"
		path = "/search"
		h["Authorization"] = "Bearer " + cfg.APIKey
		params["query"] = q.Query
		params["max_results"] = size
		for k, v := range map[string]any{"search_depth": "basic", "auto_parameters": false, "include_answer": false, "include_raw_content": false} {
			if _, ok := params[k]; !ok {
				params[k] = v
			}
		}
	case "deepseek":
		method = "POST"
		path = "/v1/tools/search"
		h["Authorization"] = "Bearer " + cfg.APIKey
		params["query"] = q.Query
		params["count"] = size
		if _, ok := params["need_content"]; !ok {
			params["need_content"] = false
		}
	}
	address := cfg.Endpoint + path
	var body *bytes.Reader
	if method == "GET" {
		values := url.Values{}
		for k, v := range params {
			switch v.(type) {
			case map[string]any:
				return nil, 0, errors.New("查询参数格式无效")
			}
			switch a := v.(type) {
			case []any:
				for _, x := range a {
					values.Add(k, fmt.Sprint(x))
				}
			default:
				values.Set(k, fmt.Sprint(v))
			}
		}
		if len(values) != 0 {
			address += "?" + values.Encode()
		}
		body = bytes.NewReader(nil)
	} else if cfg.Kind == "ddgs" {
		values := url.Values{}
		for k, v := range params {
			values.Set(k, fmt.Sprint(v))
		}
		body = bytes.NewReader([]byte(values.Encode()))
	} else {
		b, e := json.Marshal(params)
		if e != nil {
			return nil, 0, errors.New("附加参数格式无效")
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, address, body)
	if e != nil {
		return nil, 0, errors.New("搜索请求参数无效")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GhostReaper)")
	if p.NoKey || cfg.Kind == "ddpoc" {
		req.Header.Set("Accept", "text/html")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
		if cfg.Kind == "ddgs" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Referer", "https://html.duckduckgo.com/")
		}
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	return req, size, nil
}

func webSuccess(kind string, d map[string]any) bool {
	if _, exists := d["error"]; exists {
		return false
	}
	if kind == "ddpoc" {
		_, ok := d["text"].(string)
		if ok {
			return true
		}
	}
	if kind == "brave" {
		if web, ok := d["web"].(map[string]any); ok {
			_, ok = web["results"].([]any)
			return ok
		}
		_, ok := d["query"].(map[string]any)
		return ok
	}
	_, ok := d["results"].([]any)
	return ok
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasClass(n *html.Node, c string) bool {
	for _, v := range strings.Fields(attr(n, "class")) {
		if v == c {
			return true
		}
	}
	return false
}
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hit := findNode(c, match); hit != nil {
			return hit
		}
	}
	return nil
}
func resultLink(raw string, base *url.URL) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	u = base.ResolveReference(u)
	if target := u.Query().Get("uddg"); target != "" {
		u, e = url.Parse(target)
		if e != nil {
			return ""
		}
	}
	if strings.HasSuffix(u.Hostname(), "bing.com") && u.Path == "/ck/a" {
		value := u.Query().Get("u")
		if strings.HasPrefix(value, "a1") {
			b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value[2:], "="))
			if err != nil {
				return ""
			}
			u, e = url.Parse(string(b))
			if e != nil {
				return ""
			}
		}
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}
func parseWebHTML(b []byte, base *url.URL, kind string, size int) (map[string]any, error) {
	root, e := html.Parse(bytes.NewReader(b))
	if e != nil {
		return nil, errors.New("搜索页面无法解析")
	}
	lower := strings.ToLower(string(b))
	if strings.Contains(lower, "anomaly.js") || strings.Contains(lower, "b_captcha") || strings.Contains(lower, "cf-chl-") {
		return nil, errors.New("搜索站点要求验证，本次未取得结果，可稍后重试或选择其他搜索服务")
	}
	results := []map[string]string{}
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		bing := kind == "bing" && hasClass(n, "b_algo")
		duck := kind == "ddgs" && hasClass(n, "result")
		if bing || duck {
			var a *html.Node
			if bing {
				if h := findNode(n, func(x *html.Node) bool { return x.Data == "h2" }); h != nil {
					a = findNode(h, func(x *html.Node) bool { return x.Data == "a" })
				}
			} else {
				a = findNode(n, func(x *html.Node) bool { return x.Data == "a" && hasClass(x, "result__a") })
			}
			if a != nil && len(results) < size {
				link := resultLink(attr(a, "href"), base)
				if link != "" && !seen[link] {
					seen[link] = true
					snippet := ""
					s := findNode(n, func(x *html.Node) bool { return (bing && x.Data == "p") || (duck && hasClass(x, "result__snippet")) })
					if s != nil {
						snippet = nodeText(s)
					}
					results = append(results, map[string]string{"title": nodeText(a), "url": link, "snippet": snippet})
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if len(results) == 0 {
		// Recognized no-results markers are valid; an unknown page is not success.
		marker := findNode(root, func(n *html.Node) bool {
			return hasClass(n, "result--no-result") || hasClass(n, "no-results") || hasClass(n, "b_no")
		})
		if marker == nil {
			return nil, errors.New("搜索页面未包含可识别的结果，可能需要验证或页面格式已变化")
		}
	}
	return map[string]any{"results": results}, nil
}
