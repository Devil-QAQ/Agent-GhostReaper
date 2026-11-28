package websearch

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAssetProviderRequests(t *testing.T) {
	for _, p := range AssetProviders() {
		t.Run(p.Kind, func(t *testing.T) {
			key := `test-private-"key`
			query := `domain="example.com" && title="测试"`
			store := openStore(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				params := map[string]any{}
				if r.Method == "GET" {
					for k, v := range r.URL.Query() {
						params[k] = v[0]
					}
				} else {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("missing json content type")
					}
					if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
						t.Error(err)
					}
				}
				wantPath := ""
				var auth, gotQuery string
				switch p.Kind {
				case "fofa":
					wantPath = "/api/v1/search/all"
					auth, _ = params["key"].(string)
					b, _ := base64.StdEncoding.DecodeString(params["qbase64"].(string))
					gotQuery = string(b)
					if params["page"] != "2" || params["size"] != "10" || params["fields"] != "ip,port" {
						t.Error("fofa parameters")
					}
				case "hunter":
					wantPath = "/openApi/search"
					auth, _ = params["api-key"].(string)
					b, _ := base64.URLEncoding.DecodeString(params["search"].(string))
					gotQuery = string(b)
					if params["page_size"] != "10" || params["page"] != "2" {
						t.Error("hunter pagination")
					}
				case "quake":
					wantPath = "/api/v3/search/quake_service"
					auth = r.Header.Get("X-QuakeToken")
					gotQuery, _ = params["query"].(string)
					if params["start"] != float64(10) || params["size"] != float64(10) {
						t.Error("quake offset")
					}
				case "zoomeye":
					wantPath = "/v2/search"
					auth = r.Header.Get("API-KEY")
					b, _ := base64.StdEncoding.DecodeString(params["qbase64"].(string))
					gotQuery = string(b)
					if params["pagesize"] != float64(10) {
						t.Error("zoomeye size")
					}
				case "shodan":
					wantPath = "/shodan/host/search"
					auth, _ = params["key"].(string)
					gotQuery, _ = params["query"].(string)
					if _, exists := params["size"]; exists {
						t.Error("invented shodan size parameter")
					}
				case "daydaymap":
					wantPath = "/api/v1/raymap/search/all"
					auth = r.Header.Get("api-key")
					b, _ := base64.StdEncoding.DecodeString(params["keyword"].(string))
					gotQuery = string(b)
				case "zerozone":
					wantPath = "/api/data/"
					auth, _ = params["zone_key_id"].(string)
					gotQuery, _ = params["query"].(string)
					if params["query_type"] != "site" {
						t.Error("zero query type")
					}
				}
				if r.URL.Path != wantPath || gotQuery != query || auth != key {
					t.Error("request path, original query or credentials mismatch")
				}
				data := map[string]any{"echo": key, "url_echo": url.QueryEscape(key), "custom_field": "native data retained"}
				switch p.Kind {
				case "fofa":
					data["error"] = false
					data["results"] = []any{[]any{"https://example.com", "192.0.2.1", 443}}
					data["consumed_fpoint"] = 2
				case "shodan":
					data["matches"] = []any{map[string]any{"ip_str": "192.0.2.1"}}
				case "hunter", "daydaymap":
					data["code"] = 200
					data["data"] = map[string]any{"total": 1}
				case "zoomeye":
					data["code"] = 60000
					data["data"] = []any{}
				default:
					data["code"] = 0
					data["data"] = []any{}
				}
				json.NewEncoder(w).Encode(data)
			}))
			defer srv.Close()
			cfg := Config{Kind: p.Kind, Endpoint: srv.URL, APIKey: key}
			q := AssetQuery{Query: query, Page: 2, Size: 10}
			if p.Kind == "fofa" {
				q.Options = map[string]any{"fields": "ip,port"}
			}
			result, err := searchAsset(t.Context(), store, cfg, q)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || result.Page != 2 || !strings.Contains(string(result.Data), "native data retained") || !json.Valid(result.Data) {
				t.Fatal("response changed or automatically paginated")
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "test-private") || strings.Contains(result.URL, "?") {
				t.Fatal("credential exposed")
			}
			if p.Kind == "shodan" && result.Size != 100 {
				t.Fatal("wrong native shodan page size")
			}
			if result.Quota.DayUsed != 1 {
				t.Fatal("query not counted")
			}
		})
	}
}

func TestAssetCatalogAvailabilityAndSecrets(t *testing.T) {
	store := openStore(t)
	if list, _ := Services(t.Context(), store); len(list) != 0 {
		t.Fatal("configured sites shipped by default")
	}
	for _, p := range AssetProviders() {
		list, err := SaveService(t.Context(), store, ServiceChange{Name: p.Name, Enabled: true, Config: Config{Kind: p.Kind}}, "test")
		if err != nil {
			t.Fatal(err)
		}
		id := list[len(list)-1].ID
		if Available(list[len(list)-1]) {
			t.Fatal("unconfigured site available")
		}
		if _, err := ResolveService(t.Context(), store, id); err == nil {
			t.Fatal("unconfigured key accepted")
		}
		list, err = SaveService(t.Context(), store, ServiceChange{ID: id, Name: p.Name, Enabled: true, Config: Config{Kind: p.Kind}, APIKey: pointer("private-key")}, "test")
		if err != nil {
			t.Fatal(err)
		}
		s, _ := ResolveService(t.Context(), store, id)
		if !Available(s) {
			t.Fatal("configured service unavailable")
		}
		if _, err := SaveService(t.Context(), store, ServiceChange{Name: "duplicate", Config: Config{Kind: p.Kind}}, "test"); err == nil {
			t.Fatal("duplicate site accepted")
		}
		if _, err := SaveService(t.Context(), store, ServiceChange{ID: id, Name: "rename", Enabled: true, Config: s.Config}, "test"); err != nil {
			t.Fatal(err)
		}
		s, _ = ResolveService(t.Context(), store, id)
		if s.APIKey != "private-key" {
			t.Fatal("omitted key erased")
		}
		if _, err := SaveService(t.Context(), store, ServiceChange{ID: id, Name: p.Name, Enabled: true, Config: s.Config, APIKey: pointer("")}, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveService(t.Context(), store, id); err == nil {
			t.Fatal("cleared key still available")
		}
	}
	if _, err := ResolveService(t.Context(), store, ""); err != ErrNotConfigured {
		t.Fatal("unconfigured services selected")
	}
}

func TestAssetSharedBudgetAndAccountWait(t *testing.T) {
	store := openStore(t)
	cfg, _ := normalizeAsset(Config{Kind: "fofa", APIKey: "key", PerMinute: 3, PerDay: 5})
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reserveAsset(t.Context(), store, cfg) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatal("concurrent limit bypassed", accepted.Load())
	}
	v, err := AssetUsage(t.Context(), store, cfg)
	if err != nil || v.DayUsed != 3 || v.RetryAt.IsZero() {
		t.Fatal("shared count missing", err)
	}
	cfg.PerMinute = 10
	cfg.PerDay = 3
	if err := reserveAsset(t.Context(), store, cfg); err == nil {
		t.Fatal("24h limit bypassed")
	}
	if err := blockAsset(store, cfg, http.Header{"Retry-After": []string{"120"}}); err != nil {
		t.Fatal(err)
	}
	cfg.PerDay = 100
	v, _ = AssetUsage(t.Context(), store, cfg)
	if !v.RetryAt.After(time.Now().Add(100 * time.Second)) {
		t.Fatal("official pause lost")
	}
	// Readding the same key keeps counts, while a different account's wait does
	// not contaminate a newly configured identity.
	if err := reserveAsset(t.Context(), store, cfg); err == nil {
		t.Fatal("saved wait bypassed")
	}
	cfg.APIKey = "another-account"
	if err := reserveAsset(t.Context(), store, cfg); err != nil {
		t.Fatal("different account inherited wait", err)
	}
}

func TestAssetErrorAndNativeOptions(t *testing.T) {
	store := openStore(t)
	cfg, _ := normalizeAsset(Config{Kind: "zerozone", APIKey: "secret"})
	r, _, err := assetRequest(t.Context(), cfg, AssetQuery{Query: "test", Options: map[string]any{"next": 12345, "query_type": "domain"}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	if _, ok := body["page"]; ok || body["query_type"] != "domain" {
		t.Fatal("native cursor/options lost")
	}
	for _, name := range []string{"zone_key_id", "page", "keyword", "API-KEY"} {
		if _, _, err := assetRequest(t.Context(), cfg, AssetQuery{Query: "x", Options: map[string]any{name: "override"}}); err == nil {
			t.Fatal("credential/pagination override", name)
		}
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
		io.WriteString(w, `{"message":"throttle"}`)
	}))
	defer srv.Close()
	cfg.Endpoint = srv.URL
	if _, err := searchAsset(t.Context(), store, cfg, AssetQuery{Query: "x"}); err == nil {
		t.Fatal("429 accepted")
	}
	if _, err := searchAsset(t.Context(), store, cfg, AssetQuery{Query: "x"}); err == nil || calls.Load() != 1 {
		t.Fatal("paused request reached provider")
	}
	for _, data := range []map[string]any{{"code": 60001}, {"message": "missing status"}, {"code": 200, "error": true}} {
		if assetSuccess("zoomeye", data) {
			t.Fatal("API error accepted")
		}
	}
	if _, err := searchAsset(t.Context(), store, Config{Kind: "fofa"}, AssetQuery{Query: "x"}); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestAssetResponseFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"permission", `{"message":"denied"}`, 403, "拒绝访问"},
		{"business", `{"error":true,"errmsg":"no credit secret"}`, 200, "查询未成功"},
		{"not-json", "<html>login</html>", 200, "JSON"},
		{"concatenated", `{"error":false,"results":[]}{"extra":true}`, 200, "JSON"},
		{"large", strings.Repeat("x", maxSearchBody+1), 200, "2 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer srv.Close()
			_, err := searchAsset(t.Context(), openStore(t), Config{Kind: "fofa", Endpoint: srv.URL, APIKey: "secret"}, AssetQuery{Query: "x"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe / missing error", err)
			}
		})
	}
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer origin.Close()
	_, err := searchAsset(t.Context(), openStore(t), Config{Kind: "fofa", Endpoint: origin.URL, APIKey: "secret"}, AssetQuery{Query: "x"})
	if err == nil || redirected.Load() != 0 {
		t.Fatal("credentials followed redirect")
	}
}
