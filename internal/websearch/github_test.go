package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubConfiguration(t *testing.T) {
	store := openStore(t)
	list, err := SaveService(t.Context(), store, ServiceChange{Name: "GitHub", Enabled: true, Config: Config{Kind: "github"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	id := list[0].ID
	if list[0].Endpoint != githubAPI || list[0].PerMinute != 9 || list[0].PerHour != 54 {
		t.Fatalf("bad defaults: %+v", list[0])
	}
	list, err = SaveService(t.Context(), store, ServiceChange{ID: id, Name: "GitHub", Enabled: true, APIKey: pointer("test-private-token"), Config: Config{Kind: "github", Endpoint: "https://untrusted.invalid", Headers: map[string]string{"X-Extra": "ignore"}}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if list[0].PerMinute != 27 || list[0].PerHour != 4500 || list[0].Endpoint != githubAPI || len(list[0].Headers) != 0 {
		t.Fatal("authenticated defaults or fixed endpoint incorrect")
	}
	list, err = SaveService(t.Context(), store, ServiceChange{ID: id, Name: "rename", Enabled: true, Config: Config{Kind: "github", CustomLimits: true, PerMinute: 2, PerHour: 3}}, "test")
	if err != nil || list[0].APIKey != "test-private-token" || list[0].PerMinute != 2 {
		t.Fatalf("custom limits / preserve: %v %+v", err, list)
	}
	list, err = SaveService(t.Context(), store, ServiceChange{ID: id, Name: "GitHub", Enabled: true, APIKey: pointer(""), Config: Config{Kind: "github"}}, "test")
	if err != nil || list[0].PerMinute != 9 || list[0].PerHour != 54 {
		t.Fatal("anonymous defaults not restored")
	}
	if _, err = SaveService(t.Context(), store, ServiceChange{Name: "duplicate", Enabled: true, Config: Config{Kind: "github"}}, "test"); err == nil {
		t.Fatal("duplicate site allowed")
	}
	if _, err = normalizeGitHub(Config{CustomLimits: true}); err == nil {
		t.Fatal("zero custom limit accepted")
	}
	if _, err = normalizeGitHub(Config{APIKey: "bad\nheader"}); err == nil {
		t.Fatal("invalid header accepted")
	}
}

func TestGitHubWaitDistinguishesOfficialAndLocal(t *testing.T) {
	now := time.Now()
	cfg := Config{PerMinute: 9, PerHour: 54}
	v := quotaView(githubUsage{ReadBlocked: now.Add(time.Hour)}, cfg, now)
	if !v.ReadOfficial || v.SearchOfficial || !v.SearchRetryAt.IsZero() {
		t.Fatal("official read wait affected search", v)
	}
	err := githubWaitError(v, githubCore)
	if !strings.Contains(err.Error(), "目录和文件读取") || !strings.Contains(err.Error(), "官方限流") {
		t.Fatal("missing operation / official cause", err)
	}
	v = quotaView(githubUsage{Requests: []githubReservation{{At: now, Search: true}}}, Config{PerMinute: 1, PerHour: 54}, now)
	if v.SearchOfficial || v.SearchRetryAt.IsZero() || !v.ReadRetryAt.IsZero() {
		t.Fatal("local search wait affected reads", v)
	}
	if err := githubWaitError(v, githubRepositories); !strings.Contains(err.Error(), "仓库搜索，本地上限") {
		t.Fatal("wrong local wait reason", err)
	}
}

func TestGitHubRequestsAndSeparateBudgets(t *testing.T) {
	store := openStore(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || r.Header.Get("User-Agent") == "" {
			t.Error("missing authentication or API headers")
		}
		switch r.URL.Path {
		case "/search/repositories":
			if r.URL.Query().Get("q") != "CVE-2021-41773" || r.URL.Query().Get("page") != "2" {
				t.Error("wrong repository query")
			}
			ioJSON(w, `{"total_count":0,"items":[]}`)
		case "/repos/owner/repo/contents/":
			ioJSON(w, `[{"name":"README.md","type":"file"}]`)
		case "/repos/owner/repo/contents/资料.md":
			if r.URL.Query().Get("ref") != "release/test" || r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				t.Error("wrong raw read")
			}
			w.Write([]byte("中文源码 test-secret"))
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := Config{APIKey: "test-secret", CustomLimits: true, PerMinute: 1, PerHour: 2}
	search := GitHubQuery{Query: "CVE-2021-41773", Page: 2}
	if _, err := githubRequest(t.Context(), store, cfg, search, server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := githubRequest(t.Context(), store, cfg, search, server.URL); err == nil {
		t.Fatal("search limit ignored")
	}
	if _, err := githubRequest(t.Context(), store, cfg, GitHubQuery{Action: "browse", Repo: "owner/repo"}, server.URL); err != nil {
		t.Fatal("search blocked directory", err)
	}
	r, err := githubRequest(t.Context(), store, cfg, GitHubQuery{Action: "read", Repo: "owner/repo", Path: "资料.md", Ref: "release/test"}, server.URL)
	if err != nil || strings.Contains(string(r.Data), "test-secret") || !strings.Contains(string(r.Data), "中文源码") {
		t.Fatalf("read/redaction: %s %v", r.Data, err)
	}
	if _, err = githubRequest(t.Context(), store, cfg, GitHubQuery{Action: "browse", Repo: "owner/repo"}, server.URL); err == nil {
		t.Fatal("read hourly limit ignored")
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected outbounds %d", calls.Load())
	}
	q, _ := GitHubUsage(t.Context(), store, cfg)
	if q.MinuteUsed != 1 || q.HourUsed != 2 || q.SearchRetryAt.IsZero() || q.ReadRetryAt.IsZero() {
		t.Fatalf("quota %+v", q)
	}
	// Editing authentication/configuration does not clear durable usage.
	other := cfg
	other.APIKey = ""
	if err = reserveGitHub(t.Context(), store, other, githubRepositories); err == nil {
		t.Fatal("key change reset counters")
	}
	if _, err = githubRequest(t.Context(), store, cfg, GitHubQuery{Action: "read", Repo: "owner/repo", Path: "../secret"}, server.URL); err == nil {
		t.Fatal("invalid path accepted")
	}
}

func ioJSON(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(s))
}

func TestGitHubSharedDurableLimit(t *testing.T) {
	store := openStore(t)
	cfg := Config{CustomLimits: true, PerMinute: 3, PerHour: 10}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if reserveGitHub(t.Context(), store, cfg, githubRepositories) == nil {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("concurrent budget bypass %d", accepted.Load())
	}
	entry, ok, err := store.Setting(t.Context(), githubQuotaKey)
	if err != nil || !ok {
		t.Fatal("usage not durable")
	}
	var state githubUsage
	if json.Unmarshal([]byte(entry.Value), &state) != nil || len(state.Requests) != 3 {
		t.Fatal("usage missing")
	}
	now := time.Now()
	q := quotaView(state, cfg, now.Add(time.Minute+time.Second))
	if q.MinuteUsed != 0 || !q.SearchRetryAt.IsZero() {
		t.Fatal("minute did not recover")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if reserveGitHub(cancelled, store, cfg, githubCore) == nil {
		t.Fatal("canceled request reserved")
	}
}

func TestGitHubServerLimitPersistenceAndErrors(t *testing.T) {
	for _, status := range []int{200, 401, 403, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			store := openStore(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "" {
					t.Error("anonymous request authenticated")
				}
				if status == 200 {
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
				}
				if status == 429 {
					w.Header().Set("Retry-After", "3600")
				}
				w.WriteHeader(status)
				w.Write([]byte(`{"items":[]}`))
			}))
			defer server.Close()
			cfg := Config{}
			_, err := githubRequest(t.Context(), store, cfg, GitHubQuery{Query: "poc"}, server.URL)
			if (err == nil) != (status == 200) {
				t.Fatalf("status %d err %v", status, err)
			}
			if status == 200 || status == 403 || status == 429 {
				_, err = githubRequest(t.Context(), store, cfg, GitHubQuery{Query: "other"}, server.URL)
				if err == nil || calls.Load() != 1 {
					t.Fatal("server wait ignored")
				}
			}
		})
	}
}

func TestGitHubRedirectDoesNotForwardToken(t *testing.T) {
	store := openStore(t)
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer server.Close()
	_, err := githubRequest(t.Context(), store, Config{APIKey: "secret"}, GitHubQuery{Query: "poc"}, server.URL)
	if err == nil || leaked.Load() {
		t.Fatal("redirect followed")
	}
}

func TestGitHubServerResourceIsAuthoritative(t *testing.T) {
	store := openStore(t)
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Resource", "core")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	if err := recordGitHubLimit(t.Context(), store, h, 200, githubRepositories); err != nil {
		t.Fatal(err)
	}
	cfg, _ := normalizeGitHub(Config{})
	q, err := GitHubUsage(t.Context(), store, cfg)
	if err != nil || q.ReadRetryAt.IsZero() || !q.SearchRetryAt.IsZero() {
		t.Fatalf("wrong resource wait: %+v %v", q, err)
	}
	if reserveGitHub(t.Context(), store, cfg, githubCore) == nil {
		t.Fatal("server core wait bypassed")
	}
	if err = reserveGitHub(t.Context(), store, cfg, githubRepositories); err != nil {
		t.Fatal("wrong bucket blocked", err)
	}
}

func TestGitHubCodeSearchAndResourceIsolation(t *testing.T) {
	store := openStore(t)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing token")
		}
		switch r.URL.Path {
		case "/search/code":
			if r.URL.Query().Get("q") != `example repo:owner/private language:go` || r.URL.Query().Get("page") != "2" || r.Header.Get("Accept") != "application/vnd.github.text-match+json" {
				t.Error("code query changed", r.URL)
			}
			w.Header().Set("X-RateLimit-Resource", "code_search")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			ioJSON(w, `{"items":[{"path":"src/example.go","repository":{"full_name":"owner/private","private":true},"text_matches":[{"fragment":"example fixture-token"}]}]}`)
		case "/search/repositories":
			if r.URL.Query().Get("q") != "org:owner is:private" {
				t.Error("repository query changed", r.URL)
			}
			ioJSON(w, `{"items":[{"full_name":"owner/private","private":true}]}`)
		case "/repos/owner/private/contents/src/example.go":
			w.Write([]byte("package example"))
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	cfg := Config{APIKey: "fixture-token"}
	code := GitHubQuery{Action: "search_code", Query: `example repo:owner/private language:go`, Page: 2}
	anonymous := cfg
	anonymous.APIKey = ""
	if _, e := githubRequest(t.Context(), store, anonymous, code, api.URL); e == nil || !strings.Contains(e.Error(), "需要 Token") || calls.Load() != 0 {
		t.Fatal("missing code authentication not reported before sending", e)
	}
	result, e := githubRequest(t.Context(), store, cfg, code, api.URL)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(result.Data), "fixture-token") || !strings.Contains(string(result.Data), "owner/private") || result.Quota.CodeMinuteUsed != 1 || result.Quota.MinuteUsed != 0 || result.Quota.HourUsed != 0 || result.Quota.CodePerMinute != 9 || !result.Quota.CodeOfficial {
		t.Fatal("code result/quota incorrect", result)
	}
	if _, e := githubRequest(t.Context(), store, cfg, code, api.URL); e == nil || !strings.Contains(e.Error(), "代码搜索") || calls.Load() != 1 {
		t.Fatal("official code limit ignored", e)
	}
	if _, e := githubRequest(t.Context(), store, cfg, GitHubQuery{Query: "org:owner is:private"}, api.URL); e != nil {
		t.Fatal("code limit blocked repositories", e)
	}
	result, e = githubRequest(t.Context(), store, cfg, GitHubQuery{Action: "read", Repo: "owner/private", Path: "src/example.go"}, api.URL)
	if e != nil || !strings.Contains(string(result.Data), "package example") || calls.Load() != 3 {
		t.Fatal("code search could not continue to original file", e)
	}
	if result.Quota.CodeMinuteUsed != 1 || result.Quota.MinuteUsed != 1 || result.Quota.HourUsed != 1 {
		t.Fatal("resource counts mixed", result.Quota)
	}
}

func TestGitHubCodeSearchDurableBudgetAndLegacyUsage(t *testing.T) {
	store := openStore(t)
	cfg, err := normalizeGitHub(Config{APIKey: "token", CustomLimits: true, PerMinute: 27, CodePerMinute: 3, PerHour: 54})
	if err != nil {
		t.Fatal(err)
	}
	// Existing persisted searches remain repository searches after this update.
	legacy := `{"requests":[{"at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","search":true}]}`
	if err = store.SetSetting(t.Context(), githubQuotaKey, legacy, "test"); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 15 {
		wg.Go(func() {
			if reserveGitHub(t.Context(), store, cfg, githubCode) == nil {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatal("concurrent code budget bypass", accepted.Load())
	}
	q, err := GitHubUsage(t.Context(), store, cfg)
	if err != nil || q.CodeMinuteUsed != 3 || q.MinuteUsed != 1 || q.CodeRetryAt.IsZero() || !q.SearchRetryAt.IsZero() {
		t.Fatal("legacy usage/isolation", q, err)
	}
	entry, _, _ := store.Setting(t.Context(), githubQuotaKey)
	var state githubUsage
	if json.Unmarshal([]byte(entry.Value), &state) != nil {
		t.Fatal("usage not persisted")
	}
	q = quotaView(state, cfg, time.Now().Add(time.Minute+time.Second))
	if q.CodeMinuteUsed != 0 || !q.CodeRetryAt.IsZero() {
		t.Fatal("code window did not recover", q)
	}
	if _, err = normalizeGitHub(Config{CustomLimits: true, PerMinute: 1, CodePerMinute: -1, PerHour: 1}); err == nil {
		t.Fatal("invalid code budget accepted")
	}
}
