package websearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ghostreaper/internal/rings"
)

type AssetProvider struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Docs      string `json:"docs"`
	MaxSize   int    `json:"max_size"`
	FixedSize int    `json:"fixed_size,omitempty"`
	NoKey     bool   `json:"no_key,omitempty"`
	Category  string `json:"category"`
}

// Only support definitions ship with the program; no configured service or key.
func AssetProviders() []AssetProvider {
	return []AssetProvider{
		{"fofa", "FOFA", "https://fofa.info", "https://fofa.info/api", 10000, 0, false, "asset"},
		{"hunter", "鹰图", "https://hunter.qianxin.com", "https://hunter.qianxin.com/home/helpCenter?r=5-1-1", 100, 0, false, "asset"},
		{"quake", "Quake", "https://quake.360.net", "https://quake.360.net/quake/#/help", 100, 0, false, "asset"},
		{"zoomeye", "ZoomEye", "https://api.zoomeye.ai", "https://www.zoomeye.ai/agent/doc", 10000, 0, false, "asset"},
		{"shodan", "Shodan", "https://api.shodan.io", "https://developer.shodan.io/api", 100, 100, false, "asset"},
		{"daydaymap", "DayDayMap", "https://www.daydaymap.com", "https://www.daydaymap.com/help/document?type=api-auth", 10000, 0, false, "asset"},
		{"zerozone", "0.zone", "https://0.zone", "https://0.zone/apply", 100, 0, false, "asset"},
	}
}

func assetProvider(kind string) (AssetProvider, bool) {
	for _, p := range AssetProviders() {
		if p.Kind == kind {
			return p, true
		}
	}
	return AssetProvider{}, false
}
func IsAsset(kind string) bool { _, ok := assetProvider(kind); return ok }
func Available(s Service) bool {
	return s.Enabled && (!IsNative(s.Kind) || !NeedsKey(s.Kind) || s.APIKey != "")
}

func normalizeAsset(cfg Config) (Config, error) {
	p, _ := siteProvider(cfg.Kind)
	if p.NoKey {
		cfg.APIKey = ""
	}
	if cfg.Kind == "ddpoc" && cfg.APIKey != "" {
		var e error
		cfg.APIKey, e = ddpocCookie(cfg.APIKey)
		if e != nil {
			return cfg, e
		}
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = p.URL
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return cfg, errors.New("填写站点的 HTTP 或 HTTPS 基础地址")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") || len(cfg.APIKey) > 4096 {
		return cfg, errors.New("API Key 格式无效")
	}
	if err := ValidateProxy(cfg.Proxy); err != nil {
		return cfg, err
	}
	if cfg.LimitsVersion == 0 && ((cfg.PerMinute != 0 && cfg.PerMinute != 6) || (cfg.PerDay != 0 && cfg.PerDay != 100) || cfg.QPS != 0) {
		cfg.CustomLimits = true
	}
	cfg.LimitsVersion = 1
	if !cfg.CustomLimits {
		profile := selectedLimit(cfg)
		cfg.LimitPlan = profile.ID
		cfg.QPS = profile.QPS * 0.9
		cfg.PerMinute = profile.PerMinute * 9 / 10
		cfg.PerDay = profile.PerDay * 9 / 10
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = 10
	}
	if p.FixedSize > 0 {
		cfg.PageSize = p.FixedSize
	}
	if cfg.QPS < 0 || cfg.QPS > 10000 || cfg.PerMinute < 0 || cfg.PerMinute > 10000 || cfg.PerDay < 0 || cfg.PerDay > 1000000 || cfg.PageSize < 1 || cfg.PageSize > p.MaxSize {
		return cfg, fmt.Errorf("每秒上限须为 0 至 10000，每分钟上限须为 0 至 10000，24 小时上限须为 0 至 1000000，每页条数须为 1 至 %d", p.MaxSize)
	}
	method := "POST"
	if cfg.Kind == "fofa" || cfg.Kind == "hunter" || cfg.Kind == "shodan" || cfg.Kind == "bing" || cfg.Kind == "brave" || cfg.Kind == "ddpoc" {
		method = "GET"
	}
	if cfg.Kind == "hunter" && cfg.PageSize != 10 && cfg.PageSize != 20 && cfg.PageSize != 50 && cfg.PageSize != 100 {
		return cfg, errors.New("鹰图每页条数须为 10、20、50 或 100")
	}
	return Config{Kind: cfg.Kind, Endpoint: u.String(), Method: method, ResponseMode: "text", APIKey: cfg.APIKey, Proxy: cfg.Proxy, PerMinute: cfg.PerMinute, PerDay: cfg.PerDay, PageSize: cfg.PageSize, QPS: cfg.QPS, LimitPlan: cfg.LimitPlan, LimitsVersion: 1, CustomLimits: cfg.CustomLimits, ServiceID: cfg.ServiceID}, nil
}

type AssetQuery struct {
	Action  string         `json:"action,omitempty"`
	Path    string         `json:"path,omitempty"`
	Query   string         `json:"query"`
	Page    int            `json:"page"`
	Size    int            `json:"size"`
	Options map[string]any `json:"options,omitempty"`
}
type AssetQuota struct {
	MinuteUsed int       `json:"minute_used"`
	DayUsed    int       `json:"day_used"`
	RetryAt    time.Time `json:"retry_at,omitzero"`
}
type AssetResponse struct {
	Provider string          `json:"provider"`
	URL      string          `json:"url"`
	Page     int             `json:"page"`
	Size     int             `json:"size"`
	Data     json.RawMessage `json:"data"`
	Quota    AssetQuota      `json:"quota"`
}
type assetUsage struct {
	Requests    []time.Time `json:"requests"`
	Blocked     time.Time   `json:"blocked,omitzero"`
	OfficialQPS float64     `json:"official_qps,omitempty"`
}

const assetQuotaKey = "search.asset.quota"

var assetMu sync.Mutex

func assetAccount(cfg Config) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.Kind+"\x00"+cfg.Endpoint+"\x00"+cfg.APIKey)))
}
func readAssetUsage(ctx context.Context, store *rings.Store) (map[string]assetUsage, error) {
	if store == nil {
		return nil, errors.New("资产查询额度存储未就绪")
	}
	m := map[string]assetUsage{}
	s, exists, err := store.Setting(ctx, assetQuotaKey)
	if err != nil {
		return nil, errors.New("资产查询额度无法读取")
	}
	if exists && json.Unmarshal([]byte(s.Value), &m) != nil {
		return nil, errors.New("资产查询额度记录无效")
	}
	if m == nil {
		m = map[string]assetUsage{}
	}
	return m, nil
}
func assetQuotaView(u assetUsage, cfg Config, now time.Time) AssetQuota {
	if u.OfficialQPS > 0 && (cfg.QPS == 0 || u.OfficialQPS < cfg.QPS) {
		cfg.QPS = u.OfficialQPS
	}
	v := AssetQuota{}
	if u.Blocked.After(now) {
		v.RetryAt = u.Blocked
	}
	for _, at := range u.Requests {
		if at.After(now.Add(-time.Minute)) {
			v.MinuteUsed++
		}
		if at.After(now.Add(-24 * time.Hour)) {
			v.DayUsed++
		}
	}
	for _, w := range []struct {
		duration    time.Duration
		used, limit int
	}{{time.Minute, v.MinuteUsed, cfg.PerMinute}, {24 * time.Hour, v.DayUsed, cfg.PerDay}} {
		if w.limit <= 0 || w.used < w.limit {
			continue
		}
		n := w.used - w.limit
		for _, at := range u.Requests {
			if at.After(now.Add(-w.duration)) {
				if n == 0 {
					v.RetryAt = maxTime(v.RetryAt, at.Add(w.duration))
					break
				}
				n--
			}
		}
	}
	if cfg.QPS > 0 && len(u.Requests) > 0 {
		at := u.Requests[len(u.Requests)-1].Add(time.Duration(float64(time.Second) / cfg.QPS))
		if at.After(now) {
			v.RetryAt = maxTime(v.RetryAt, at)
		}
	}

	return v
}
func AssetUsage(ctx context.Context, store *rings.Store, cfg Config) (AssetQuota, error) {
	assetMu.Lock()
	defer assetMu.Unlock()
	m, e := readAssetUsage(ctx, store)
	if e != nil {
		return AssetQuota{}, e
	}
	return assetQuotaView(m[assetAccount(cfg)], cfg, time.Now()), nil
}
func reserveAsset(ctx context.Context, store *rings.Store, cfg Config) error {
	assetMu.Lock()
	defer assetMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m, e := readAssetUsage(ctx, store)
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	key := assetAccount(cfg)
	u := m[key]
	v := assetQuotaView(u, cfg, now)
	if v.RetryAt.After(now) {
		return fmt.Errorf("请求额度暂不可用，%s 后可重试", v.RetryAt.Local().Format("2006-01-02 15:04:05"))
	}
	kept := u.Requests[:0]
	for _, at := range u.Requests {
		if at.After(now.Add(-24 * time.Hour)) {
			kept = append(kept, at)
		}
	}
	u.Requests = append(kept, now)
	m[key] = u
	return saveAssetUsage(ctx, store, m)
}
func saveAssetUsage(ctx context.Context, store *rings.Store, m map[string]assetUsage) error {
	data, _ := json.Marshal(m)
	if err := store.SetSetting(ctx, assetQuotaKey, string(data), "asset-search"); err != nil {
		return errors.New("资产查询额度无法保存，本次未继续发送")
	}
	return nil
}
func blockAsset(store *rings.Store, cfg Config, h http.Header) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	until := time.Now().Add(time.Minute)
	if seconds, e := strconv.Atoi(h.Get("Retry-After")); e == nil && seconds > 0 {
		until = time.Now().Add(time.Duration(seconds) * time.Second)
	} else if at, e := http.ParseTime(h.Get("Retry-After")); e == nil && at.After(until) {
		until = at
	}
	assetMu.Lock()
	defer assetMu.Unlock()
	m, e := readAssetUsage(ctx, store)
	if e != nil {
		return e
	}
	key := assetAccount(cfg)
	u := m[key]
	u.Blocked = maxTime(u.Blocked, until)
	m[key] = u
	return saveAssetUsage(ctx, store, m)
}

func assetRequest(ctx context.Context, cfg Config, q AssetQuery) (*http.Request, int, error) {
	p, _ := siteProvider(cfg.Kind)
	if strings.TrimSpace(q.Query) == "" || len(q.Query) > 16384 {
		return nil, 0, errors.New("填写 1 至 16384 字节的查询语句")
	}
	if q.Page < 0 || q.Page > 1000000 {
		return nil, 0, errors.New("页码须为 1 至 1000000")
	}
	q.Page = max(1, q.Page)
	size := q.Size
	if size == 0 {
		size = cfg.PageSize
	}
	if p.FixedSize > 0 {
		size = p.FixedSize
	}
	if size < 1 || size > p.MaxSize {
		return nil, 0, fmt.Errorf("每页条数须为 1 至 %d", p.MaxSize)
	}
	if cfg.Kind == "hunter" && size != 10 && size != 20 && size != 50 && size != 100 {
		return nil, 0, errors.New("鹰图每页条数须为 10、20、50 或 100")
	}
	// Native optional parameters remain available without exposing credentials or
	// allowing them to replace the query and pagination controlled by this call.
	params := map[string]any{}
	for key, v := range q.Options {
		switch strings.ToLower(key) {
		case "key", "api_key", "api-key", "apikey", "zone_key_id", "x-quaketoken", "authorization", "query", "qbase64", "keyword", "search", "page", "size", "pagesize", "page_size", "start":
			return nil, 0, fmt.Errorf("附加参数不能覆盖 %s", key)
		}
		params[key] = v
	}
	enc := base64.StdEncoding.EncodeToString([]byte(q.Query))
	method := "POST"
	path := ""
	headers := map[string]string{}
	switch cfg.Kind {
	case "fofa":
		method = "GET"
		path = "/api/v1/search/all"
		if _, ok := params["next"]; ok {
			path = "/api/v1/search/next"
		}
		params["key"] = cfg.APIKey
		params["qbase64"] = enc
		params["page"] = q.Page
		params["size"] = size
		if _, ok := params["fields"]; !ok {
			params["fields"] = "host,ip,port,protocol,domain,title,server"
		}
		if _, ok := params["full"]; !ok {
			params["full"] = false
		}
	case "hunter":
		method = "GET"
		path = "/openApi/search"
		params["api-key"] = cfg.APIKey
		params["search"] = base64.URLEncoding.EncodeToString([]byte(q.Query))
		params["page"] = q.Page
		params["page_size"] = size
		if _, ok := params["is_web"]; !ok {
			params["is_web"] = 3
		}
	case "quake":
		path = "/api/v3/search/quake_service"
		headers["X-QuakeToken"] = cfg.APIKey
		params["query"] = q.Query
		params["start"] = (q.Page - 1) * size
		params["size"] = size
	case "zoomeye":
		path = "/v2/search"
		headers["API-KEY"] = cfg.APIKey
		params["qbase64"] = enc
		params["page"] = q.Page
		params["pagesize"] = size
	case "shodan":
		method = "GET"
		path = "/shodan/host/search"
		params["key"] = cfg.APIKey
		params["query"] = q.Query
		params["page"] = q.Page
	case "daydaymap":
		path = "/api/v1/raymap/search/all"
		headers["api-key"] = cfg.APIKey
		params["keyword"] = enc
		params["page"] = q.Page
		params["page_size"] = size
	case "zerozone":
		path = "/api/data/"
		params["zone_key_id"] = cfg.APIKey
		params["query"] = q.Query
		params["page"] = q.Page
		params["pagesize"] = size
		if _, ok := params["query_type"]; !ok {
			params["query_type"] = "site"
		}
		if _, ok := params["next"]; ok {
			delete(params, "page")
		}
	default:
		return nil, 0, errors.New("资产查询站点类型无效")
	}
	address := cfg.Endpoint + path
	var body io.Reader
	if method == "GET" {
		v := url.Values{}
		for key, value := range params {
			switch value.(type) {
			case map[string]any, []any:
				return nil, 0, errors.New("查询参数须为字符串、数字或布尔值")
			}
			v.Set(key, fmt.Sprint(value))
		}
		address += "?" + v.Encode()
	} else {
		b, e := json.Marshal(params)
		if e != nil {
			return nil, 0, errors.New("附加参数格式无效")
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, address, body)
	if e != nil {
		return nil, 0, errors.New("站点请求参数无效")
	}
	req.Header.Set("User-Agent", "GhostReaper")
	req.Header.Set("Accept", "application/json")
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, size, nil
}

func AssetSearch(ctx context.Context, store *rings.Store, id string, q AssetQuery) (AssetResponse, error) {
	s, e := ResolveService(ctx, store, id)
	if e != nil {
		return AssetResponse{}, e
	}
	if !IsNative(s.Kind) {
		return AssetResponse{}, errors.New("该卡片不是资产查询站点")
	}
	return searchAsset(ctx, store, s.Config, q)
}
func searchAsset(ctx context.Context, store *rings.Store, cfg Config, q AssetQuery) (AssetResponse, error) {
	var result AssetResponse
	cfg, e := normalizeAsset(cfg)
	if e != nil {
		return result, e
	}
	if NeedsKey(cfg.Kind) && cfg.APIKey == "" {
		if cfg.Kind == "ddpoc" {
			return result, errors.New("填写 DDPoc 登录 Cookie 中的 web1")
		}
		return result, errors.New("尚未配置 API Key")
	}
	var req *http.Request
	var size int
	if IsWeb(cfg.Kind) {
		req, size, e = webRequest(ctx, cfg, q)
	} else {
		req, size, e = assetRequest(ctx, cfg, q)
	}
	if e != nil {
		return result, e
	}
	c, e := clientFor(cfg.Proxy)
	if e != nil {
		return result, e
	}
	defer c.CloseIdleConnections()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cfg.Kind == "bing" || cfg.Kind == "ddgs" {
		c.CheckRedirect = func(next *http.Request, via []*http.Request) error {
			domain := "bing.com"
			if cfg.Kind == "ddgs" {
				domain = "duckduckgo.com"
			}
			host := next.URL.Hostname()
			if len(via) >= 3 || next.URL.Scheme != "https" || (host != domain && !strings.HasSuffix(host, "."+domain)) {
				return http.ErrUseLastResponse
			}
			return nil
		}
	}
	if e = reserveAsset(ctx, store, cfg); e != nil {
		return result, e
	}
	resp, e := c.Do(req)
	if e != nil {
		return result, errors.New("站点连接失败，检查网络或出口代理")
	}
	defer resp.Body.Close()
	if cfg.Kind == "ddpoc" && (resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 401) {
		return result, errors.New("DDPoc 登录已失效，更新登录 Cookie 后重试")
	}
	if e := recordSitePolicy(store, cfg, resp.Header); e != nil {
		return result, e
	}
	if cfg.Kind == "ddgs" && resp.StatusCode == 202 {
		return result, errors.New("DuckDuckGo 要求验证，本次未取得结果，可稍后重试或选择其他搜索服务")
	}
	if resp.StatusCode == 429 {
		if e := blockAsset(store, cfg, resp.Header); e != nil {
			return result, e
		}
		return result, errors.New("站点限制了请求频率，已暂停调用，查看额度后重试")
	}
	if resp.StatusCode != 200 {
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			if cfg.Kind == "ddpoc" {
				return result, errors.New("DDPoc 拒绝访问，检查登录 Cookie 或账号权限")
			}
			if !NeedsKey(cfg.Kind) {
				return result, errors.New("搜索站点拒绝访问，可能需要验证，检查网络或出口代理")
			}
			return result, errors.New("站点拒绝访问，检查 API Key、账号权限和剩余额度")
		}
		return result, fmt.Errorf("站点返回 HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, maxSearchBody+1))
	if e != nil {
		return result, errors.New("站点响应未读取完整")
	}
	if len(b) > maxSearchBody {
		return result, errors.New("站点响应超过 2 MiB，减少每页条数或返回字段后重试")
	}
	var data map[string]any
	if cfg.Kind == "ddpoc" {
		if q.Action == "read" {
			data, e = parseDDPocPage(b, req.URL)
		} else {
			data, e = parseDDPocList(b, req.URL, size)
		}
		if e != nil {
			return result, e
		}
		b, _ = json.Marshal(data)
	}
	if cfg.Kind == "bing" || cfg.Kind == "ddgs" {
		data, e = parseWebHTML(b, req.URL, cfg.Kind, size)
		if e != nil {
			return result, e
		}
		b, _ = json.Marshal(data)
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil || data == nil {
		return result, errors.New("站点未返回 JSON 对象")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return result, errors.New("站点未返回完整 JSON 对象")
	}
	if !(IsWeb(cfg.Kind) && webSuccess(cfg.Kind, data)) && !(IsAsset(cfg.Kind) && assetSuccess(cfg.Kind, data)) {
		message, _ := data["message"].(string)
		if message == "" {
			message, _ = data["errmsg"].(string)
		}
		if message == "" {
			message, _ = data["error"].(string)
		}
		message = redactAsset(message, cfg.APIKey)
		if len(message) > 400 {
			message = message[:400]
		}
		if message == "" {
			message = "检查查询语法、API Key、账号权限和剩余额度"
		}
		return result, fmt.Errorf("站点查询未成功：%s", message)
	}
	// Decode then re-encode to remove secrets even if the source escaped them.
	b, _ = json.Marshal(redactAssetData(data, cfg.APIKey))
	quota, e := AssetUsage(ctx, store, cfg)
	if e != nil {
		return result, e
	}
	publicURL := *req.URL
	publicURL.RawQuery = ""
	return AssetResponse{Provider: cfg.Kind, URL: publicURL.String(), Page: max(q.Page, 1), Size: size, Data: b, Quota: quota}, nil
}
func redactAsset(s, key string) string {
	for _, v := range []string{key, url.QueryEscape(key)} {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[密钥已隐藏]")
		}
	}
	return s
}
func redactAssetData(v any, key string) any {
	switch x := v.(type) {
	case string:
		return redactAsset(x, key)
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			m[redactAsset(k, key)] = redactAssetData(v, key)
		}
		return m
	case []any:
		for i := range x {
			x[i] = redactAssetData(x[i], key)
		}
		return x
	default:
		return v
	}
}
func assetSuccess(kind string, data map[string]any) bool {
	if kind == "fofa" {
		v, ok := data["error"].(bool)
		return ok && !v
	}
	if kind == "shodan" {
		_, bad := data["error"]
		_, ok := data["matches"].([]any)
		return !bad && ok
	}
	v, exists := data["code"]
	if !exists {
		return false
	}
	code := fmt.Sprint(v)
	switch kind {
	case "hunter", "daydaymap":
		return code == "200"
	case "zoomeye":
		return code == "60000"
	case "quake", "zerozone":
		return code == "0"
	}
	return false
}
