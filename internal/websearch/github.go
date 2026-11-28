package websearch

import (
	"context"
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
	"unicode/utf8"

	"ghostreaper/internal/rings"
)

const githubAPI = "https://api.github.com"
const githubQuotaKey = "search.github.quota"

const (
	githubRepositories = "search"
	githubCode         = "code_search"
	githubCore         = "core"
)

// The one GitHub site shares a durable request budget across tasks and callers.
// Requests are reserved before sending, including failures whose delivery is unknown.
var githubQuotaMu sync.Mutex

type GitHubQuery struct {
	perPage int
	Action  string `json:"action"`
	Query   string `json:"query"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Ref     string `json:"ref"`
	Page    int    `json:"page"`
}

type GitHubQuota struct {
	MinuteUsed     int       `json:"minute_used"`
	CodeMinuteUsed int       `json:"code_minute_used"`
	HourUsed       int       `json:"hour_used"`
	PerMinute      int       `json:"per_minute"`
	CodePerMinute  int       `json:"code_per_minute"`
	PerHour        int       `json:"per_hour"`
	SearchRetryAt  time.Time `json:"search_retry_at,omitzero"`
	CodeRetryAt    time.Time `json:"code_retry_at,omitzero"`
	ReadRetryAt    time.Time `json:"read_retry_at,omitzero"`
	SearchOfficial bool      `json:"search_official,omitempty"`
	CodeOfficial   bool      `json:"code_official,omitempty"`
	ReadOfficial   bool      `json:"read_official,omitempty"`
}
type GitHubResponse struct {
	URL   string          `json:"url"`
	Data  json.RawMessage `json:"data"`
	Quota GitHubQuota     `json:"quota"`
}
type githubReservation struct {
	At       time.Time `json:"at"`
	Search   bool      `json:"search"`
	Resource string    `json:"resource,omitempty"`
}
type githubUsage struct {
	Requests      []githubReservation `json:"requests"`
	SearchBlocked time.Time           `json:"search_blocked,omitzero"`
	CodeBlocked   time.Time           `json:"code_blocked,omitzero"`
	ReadBlocked   time.Time           `json:"read_blocked,omitzero"`
	Blocked       time.Time           `json:"blocked,omitzero"`
}

func normalizeGitHub(cfg Config) (Config, error) {
	if !cfg.CustomLimits {
		cfg.PerMinute, cfg.PerHour = 9, 54
		cfg.CodePerMinute = 9
		if cfg.APIKey != "" {
			cfg.PerMinute, cfg.PerHour = 27, 4500
		}
	}
	if cfg.CodePerMinute == 0 {
		cfg.CodePerMinute = min(9, cfg.PerMinute)
	}
	if cfg.PerMinute < 1 || cfg.PerMinute > 10000 || cfg.CodePerMinute < 1 || cfg.CodePerMinute > 10000 || cfg.PerHour < 1 || cfg.PerHour > 100000 {
		return cfg, errors.New("分钟上限须为 1 至 10000，小时上限须为 1 至 100000")
	}
	if err := ValidateProxy(cfg.Proxy); err != nil {
		return cfg, err
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return cfg, errors.New("GitHub Token 格式无效")
	}
	return Config{Kind: "github", Endpoint: githubAPI, Method: "GET", ResponseMode: "text", APIKey: cfg.APIKey, Proxy: cfg.Proxy, PerMinute: cfg.PerMinute, CodePerMinute: cfg.CodePerMinute, PerHour: cfg.PerHour, CustomLimits: cfg.CustomLimits, ServiceID: cfg.ServiceID}, nil
}

func readGitHubUsage(ctx context.Context, store *rings.Store) (githubUsage, error) {
	if store == nil {
		return githubUsage{}, errors.New("GitHub 请求额度存储未就绪")
	}
	entry, exists, err := store.Setting(ctx, githubQuotaKey)
	if err != nil {
		return githubUsage{}, errors.New("GitHub 请求额度无法读取")
	}
	var state githubUsage
	if exists && json.Unmarshal([]byte(entry.Value), &state) != nil {
		return state, errors.New("GitHub 请求额度记录无效")
	}
	return state, nil
}

// Older reservations used only Search; preserve their usage when reading them.
func (r githubReservation) resource() string {
	if r.Resource != "" {
		return r.Resource
	}
	if r.Search {
		return githubRepositories
	}
	return githubCore
}

func githubQueryResource(action string) string {
	switch action {
	case "", "search":
		return githubRepositories
	case "search_code":
		return githubCode
	default:
		return githubCore
	}
}

func quotaView(state githubUsage, cfg Config, now time.Time) GitHubQuota {
	codeLimit := cfg.CodePerMinute
	if codeLimit == 0 {
		codeLimit = min(9, cfg.PerMinute)
	}
	v := GitHubQuota{PerMinute: cfg.PerMinute, CodePerMinute: codeLimit, PerHour: cfg.PerHour}
	for _, w := range []struct {
		resource string
		duration time.Duration
		used     *int
		limit    int
		blocked  time.Time
		retry    *time.Time
		official *bool
	}{
		{githubRepositories, time.Minute, &v.MinuteUsed, v.PerMinute, state.SearchBlocked, &v.SearchRetryAt, &v.SearchOfficial},
		{githubCode, time.Minute, &v.CodeMinuteUsed, v.CodePerMinute, state.CodeBlocked, &v.CodeRetryAt, &v.CodeOfficial},
		{githubCore, time.Hour, &v.HourUsed, v.PerHour, state.ReadBlocked, &v.ReadRetryAt, &v.ReadOfficial},
	} {
		*w.retry = maxTime(w.blocked, state.Blocked)
		*w.official = w.retry.After(now)
		for _, r := range state.Requests {
			if r.resource() == w.resource && r.At.After(now.Add(-w.duration)) {
				*w.used += 1
			}
		}
		if *w.used >= w.limit && w.limit > 0 {
			n := *w.used - w.limit
			for _, r := range state.Requests {
				if r.resource() != w.resource || !r.At.After(now.Add(-w.duration)) {
					continue
				}
				if n == 0 {
					*w.retry = maxTime(*w.retry, r.At.Add(w.duration))
					break
				}
				n--
			}
		}
		if !w.retry.After(now) {
			*w.retry = time.Time{}
		}
	}
	return v
}

func githubQuotaStatus(v GitHubQuota, resource string) (string, time.Time, bool) {
	switch resource {
	case githubRepositories:
		return "仓库搜索", v.SearchRetryAt, v.SearchOfficial
	case githubCode:
		return "代码搜索", v.CodeRetryAt, v.CodeOfficial
	default:
		return "目录和文件读取", v.ReadRetryAt, v.ReadOfficial
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func GitHubUsage(ctx context.Context, store *rings.Store, cfg Config) (GitHubQuota, error) {
	githubQuotaMu.Lock()
	defer githubQuotaMu.Unlock()
	state, err := readGitHubUsage(ctx, store)
	return quotaView(state, cfg, time.Now()), err
}

func writeGitHubUsage(ctx context.Context, store *rings.Store, state githubUsage) error {
	b, _ := json.Marshal(state)
	if err := store.SetSetting(ctx, githubQuotaKey, string(b), "github"); err != nil {
		return errors.New("GitHub 请求额度无法保存，本次未继续发送")
	}
	return nil
}

func reserveGitHub(ctx context.Context, store *rings.Store, cfg Config, resource string) error {
	githubQuotaMu.Lock()
	defer githubQuotaMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := readGitHubUsage(ctx, store)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	v := quotaView(state, cfg, now)
	_, until, _ := githubQuotaStatus(v, resource)
	if until.After(now) {
		return githubWaitError(v, resource)
	}
	kept := state.Requests[:0]
	for _, t := range state.Requests {
		if t.At.After(now.Add(-time.Hour)) {
			kept = append(kept, t)
		}
	}
	state.Requests = append(kept, githubReservation{At: now, Search: resource != githubCore, Resource: resource})
	return writeGitHubUsage(ctx, store, state)
}

func githubWaitError(v GitHubQuota, resource string) error {
	action, until, official := githubQuotaStatus(v, resource)
	source := "本地上限"
	if official {
		source = "GitHub 官方限流"
	}
	return fmt.Errorf("GitHub 请求额度暂不可用：%s，%s，%s 后可重试；可继续其他工作", action, source, until.Local().Format("2006-01-02 15:04:05"))
}

func githubLimitResource(h http.Header, fallback string) string {
	switch resource := h.Get("X-RateLimit-Resource"); resource {
	case githubCore, githubRepositories, githubCode:
		return resource
	default:
		return fallback
	}
}

func recordGitHubLimit(ctx context.Context, store *rings.Store, h http.Header, status int, resource string) error {
	// Rejected requests can count against a different resource than a successful
	// search. The response headers are authoritative for the exhausted bucket.
	resource = githubLimitResource(h, resource)
	now := time.Now().UTC()
	var until time.Time
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			until = time.Unix(reset, 0)
		}
	}
	if value := h.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
			if at := now.Add(time.Duration(seconds) * time.Second); at.After(until) {
				until = at
			}
		} else if at, err := http.ParseTime(value); err == nil && at.After(until) {
			until = at
		}
	}
	if (status == 403 || status == 429) && !until.After(now) {
		until = now.Add(time.Minute)
	}
	if !until.After(now) {
		return nil
	}
	githubQuotaMu.Lock()
	defer githubQuotaMu.Unlock()
	state, err := readGitHubUsage(ctx, store)
	if err != nil {
		return err
	}
	if h.Get("X-RateLimit-Remaining") == "0" && h.Get("Retry-After") == "" {
		switch resource {
		case githubRepositories:
			state.SearchBlocked = maxTime(state.SearchBlocked, until)
		case githubCode:
			state.CodeBlocked = maxTime(state.CodeBlocked, until)
		default:
			state.ReadBlocked = maxTime(state.ReadBlocked, until)
		}
	} else {
		state.Blocked = maxTime(state.Blocked, until)
	}
	return writeGitHubUsage(ctx, store, state)
}

func githubPath(q GitHubQuery) (string, url.Values, error) {
	params := url.Values{}
	switch q.Action {
	case "", "search", "search_code":
		if strings.TrimSpace(q.Query) == "" || len(q.Query) > 2048 {
			return "", nil, errors.New("填写 1 至 2048 字节的搜索词")
		}
		if q.Page < 0 || q.Page > 100 {
			return "", nil, errors.New("搜索页码须为 1 至 100")
		}
		page := max(q.Page, 1)
		params.Set("q", q.Query)
		params.Set("page", strconv.Itoa(page))
		perPage := 10
		if q.perPage > 0 {
			perPage = q.perPage
		}
		params.Set("per_page", strconv.Itoa(perPage))
		if q.Action == "search_code" {
			return "/search/code", params, nil
		}
		return "/search/repositories", params, nil
	case "browse", "read":
		parts := strings.Split(q.Repo, "/")
		if len(parts) != 2 || !githubPart(parts[0]) || !githubPart(parts[1]) {
			return "", nil, errors.New("仓库填写 owner/repo，例如 octocat/Hello-World")
		}
		segments := []string{}
		if q.Path != "" {
			for _, part := range strings.Split(q.Path, "/") {
				if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
					return "", nil, errors.New("仓库文件路径无效")
				}
				segments = append(segments, url.PathEscape(part))
			}
		}
		if q.Action == "read" && q.Path == "" {
			return "", nil, errors.New("填写要读取的文件路径")
		}
		if q.Ref != "" {
			params.Set("ref", q.Ref)
		}
		return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/contents/" + strings.Join(segments, "/"), params, nil
	default:
		return "", nil, errors.New("GitHub 支持 search 搜仓库、search_code 搜代码、browse 列目录、read 读文件")
	}
}

func githubPart(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\?#%\x00\r\n ")
}

func GitHub(ctx context.Context, store *rings.Store, id string, q GitHubQuery) (GitHubResponse, error) {
	service, err := ResolveService(ctx, store, id)
	if err != nil {
		return GitHubResponse{}, err
	}
	if service.Kind != "github" {
		return GitHubResponse{}, errors.New("该卡片不是 GitHub 站点")
	}
	return githubRequest(ctx, store, service.Config, q, githubAPI)
}

func githubRequest(ctx context.Context, store *rings.Store, cfg Config, q GitHubQuery, base string) (GitHubResponse, error) {
	var result GitHubResponse
	cfg, err := normalizeGitHub(cfg)
	if err != nil {
		return result, err
	}
	if q.Action == "search_code" && cfg.APIKey == "" {
		return result, errors.New("GitHub 代码搜索需要 Token，可在搜索配置中填写")
	}
	resource := githubQueryResource(q.Action)
	path, params, err := githubPath(q)
	if err != nil {
		return result, err
	}
	address := base + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return result, errors.New("GitHub 请求参数无效")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if q.Action == "search_code" {
		req.Header.Set("Accept", "application/vnd.github.text-match+json")
	}
	if q.Action == "read" {
		req.Header.Set("Accept", "application/vnd.github.raw+json")
	}
	req.Header.Set("User-Agent", "GhostReaper")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client, err := clientFor(cfg.Proxy)
	if err != nil {
		return result, err
	}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if err := reserveGitHub(ctx, store, cfg, resource); err != nil {
		return result, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return result, errors.New("GitHub 连接失败，请检查网络或出口代理")
	}
	defer resp.Body.Close()
	// A canceled caller must not erase a server-provided wait interval.
	limitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recordGitHubLimit(limitCtx, store, resp.Header, resp.StatusCode, resource); err != nil {
		return result, err
	}
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case 401:
			return result, errors.New("GitHub Token 无效或已过期，修改配置后重试")
		case 403, 429:
			quota, err := GitHubUsage(limitCtx, store, cfg)
			if err != nil {
				return result, err
			}
			return result, githubWaitError(quota, githubLimitResource(resp.Header, resource))
		case 404:
			return result, errors.New("GitHub 仓库、路径或分支不存在，或当前凭证无权访问")
		case 301, 302, 307, 308:
			return result, errors.New("GitHub 地址已迁移，核对仓库名称；本次未跟随跳转")
		default:
			return result, fmt.Errorf("GitHub 返回 HTTP %d，本次未取得结果", resp.StatusCode)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchBody+1))
	if err != nil {
		return result, errors.New("GitHub 响应未读取完整")
	}
	if len(body) > maxSearchBody {
		return result, errors.New("GitHub 响应超过 2 MiB，可通过原文件链接使用现有工具获取")
	}
	if cfg.APIKey != "" {
		body = []byte(strings.ReplaceAll(string(body), cfg.APIKey, "[Token 已隐藏]"))
	}
	if q.Action == "read" {
		if !utf8.Valid(body) || strings.ContainsRune(string(body), 0) {
			return result, errors.New("此文件不支持文本读取，可通过原文件链接使用现有工具获取")
		}
		body, _ = json.Marshal(map[string]any{"repo": q.Repo, "path": q.Path, "ref": q.Ref, "text": string(body)})
	} else if !json.Valid(body) {
		return result, errors.New("GitHub 未返回有效 JSON")
	}
	quota, err := GitHubUsage(ctx, store, cfg)
	if err != nil {
		return result, err
	}
	return GitHubResponse{URL: address, Data: body, Quota: quota}, nil
}
