package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ghostreaper/internal/rings"
)

func openStore(t *testing.T) *rings.Store {
	t.Helper()
	store, err := rings.Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func testConfig(endpoint string) Config {
	return Config{Endpoint: endpoint, Method: "GET", Encoding: "json", Headers: map[string]string{}, Params: map[string]any{"q": "{{query}}"}, ResponseMode: "json", ResultsPath: "results", TitlePath: "title", URLPath: "url", SnippetPath: "content"}
}

func TestCustomSearchGetAndResponseMapping(t *testing.T) {
	query := "雪 & \"quoted\" {{api_key}}"
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		if r.Header.Get("X-Custom-Token") != "secret-fixture" {
			t.Error("configured header missing")
		}
		fmt.Fprint(w, `{"payload":{"entries":[{"label":"result","link":"/doc","text":"match"}]}}`)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL + "/search?keep=1")
	cfg.APIKey = "secret-fixture"
	cfg.Headers = map[string]string{"X-Custom-Token": "{{api_key}}"}
	cfg.ResultsPath = "payload.entries"
	cfg.TitlePath = "label"
	cfg.URLPath = "link"
	cfg.SnippetPath = "text"
	cfg.Params = map[string]any{"keywords": "{{query}}", "limit": 3, "tags": []any{"a", "b"}}
	results, err := Search(t.Context(), cfg, query)
	if err != nil || len(results) != 1 || results[0].URL != srv.URL+"/doc" || results[0].Title != "result" {
		t.Fatalf("response %+v %v", results, err)
	}
	if got.Get("keywords") != query || got.Get("keep") != "1" || len(got["tags"]) != 2 || got.Get("limit") != "3" {
		t.Fatalf("parameters %v", got)
	}
}

func TestCustomSearchPostJSONAndForm(t *testing.T) {
	for _, encoding := range []string{"json", "form"} {
		t.Run(encoding, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Error("method was not POST")
				}
				var query, key string
				if encoding == "json" {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					query, _ = body["query"].(string)
					key, _ = body["token"].(string)
				} else {
					r.ParseForm()
					query = r.PostForm.Get("query")
					key = r.PostForm.Get("token")
				}
				if query != "a\"b & 雪" || key != "api-secret" {
					t.Errorf("query/key mismatch %q", query)
				}
				fmt.Fprint(w, `{"results":[]}`)
			}))
			defer srv.Close()
			cfg := testConfig(srv.URL)
			cfg.Method = "POST"
			cfg.Encoding = encoding
			cfg.APIKey = "api-secret"
			cfg.Params = map[string]any{"query": "{{query}}", "token": "{{api_key}}"}
			rs, err := Search(t.Context(), cfg, "a\"b & 雪")
			if err != nil || len(rs) != 0 {
				t.Fatalf("post %+v %v", rs, err)
			}
		})
	}
}

func TestRawSearchTextIsBoundedAndKeyRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "raw-api-secret "+strings.Repeat("x", maxRawText)+"雪")
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.ResponseMode = "text"
	cfg.APIKey = "raw-api-secret"
	rs, err := Search(t.Context(), cfg, "query")
	if err != nil || len(rs) != 1 || !rs[0].Raw || !rs[0].Truncated || strings.Contains(rs[0].Snippet, cfg.APIKey) {
		t.Fatalf("raw response leaked key or lost truncation: %v", err)
	}
}

func TestSearchMissingConfigurationAndMalformedResponses(t *testing.T) {
	var calls atomic.Int32
	var mode atomic.Value
	mode.Store("missing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch mode.Load().(string) {
		case "missing":
			fmt.Fprint(w, `{}`)
		case "bad-link":
			fmt.Fprint(w, `{"results":[{"title":"no link"}]}`)
		case "empty":
			fmt.Fprint(w, `{"results":[]}`)
		case "status":
			w.WriteHeader(403)
			fmt.Fprint(w, "PRIVATE-RESPONSE-KEY")
		case "huge":
			fmt.Fprint(w, strings.Repeat("x", maxSearchBody+1))
		}
	}))
	defer srv.Close()
	if _, err := Search(t.Context(), Config{}, "query"); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("empty configuration used a default")
	}
	cfg := testConfig(srv.URL)
	for _, value := range []string{"missing", "bad-link", "status", "huge"} {
		mode.Store(value)
		_, err := Search(t.Context(), cfg, "q")
		var unavailable *serviceError
		if !errors.As(err, &unavailable) || strings.Contains(err.Error(), "PRIVATE-RESPONSE-KEY") {
			t.Fatalf("invalid response accepted or leaked: %s %v", value, err)
		}
	}
	mode.Store("empty")
	rs, err := Search(t.Context(), cfg, "q")
	if err != nil || len(rs) != 0 {
		t.Fatalf("empty result %+v %v", rs, err)
	}
	cfg.Headers = map[string]string{"Authorization": "Bearer {{api_key}}"}
	before := calls.Load()
	if _, err := Search(t.Context(), cfg, "q"); err == nil || calls.Load() != before {
		t.Fatal("missing referenced key went outbound")
	}
}

func TestCustomSearchEgressProxyAndRedirectIsolation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"results":[]}`) }))
	defer target.Close()
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			http.Error(w, "proxy", 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	cfg := testConfig(target.URL)
	cfg.Proxy = proxy.URL
	if _, err := Search(context.Background(), cfg, "q"); err != nil || hits.Load() != 1 {
		t.Fatalf("proxy %v", err)
	}
	cfg.Proxy = ""
	if _, err := Search(t.Context(), cfg, "q"); err != nil || hits.Load() != 1 {
		t.Fatal("direct service inherited another proxy")
	}
	sinkHits := atomic.Int32{}
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sinkHits.Add(1) }))
	defer sink.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, 302) }))
	defer redirect.Close()
	cfg = testConfig(redirect.URL)
	cfg.APIKey = "secret"
	cfg.Headers = map[string]string{"X-Token": "{{api_key}}"}
	if _, err := Search(t.Context(), cfg, "q"); err == nil || sinkHits.Load() != 0 {
		t.Fatal("cross-origin redirect received configured credentials")
	}
}

func TestJSONPointerResultFields(t *testing.T) {
	value := map[string]any{"literal.dot": []any{map[string]any{"a/b": "value"}}}
	got, ok := valueAt(value, "/literal.dot/0/a~1b")
	if !ok || got != "value" {
		t.Fatal("JSON pointer mapping failed")
	}
	if _, ok := valueAt(value, "/literal.dot/8"); ok {
		t.Fatal("invalid index accepted")
	}
}
