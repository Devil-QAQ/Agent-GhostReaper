package websearch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWebSitesRequestsAndDrafts(t *testing.T) {
	for _, p := range WebProviders() {
		if p.Category != "web" {
			continue
		} // Vulnerability-list adapters have their own end-to-end tests.
		t.Run(p.Kind, func(t *testing.T) {
			store := openStore(t)
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				params := map[string]any{}
				if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
					r.ParseForm()
					for k, v := range r.Form {
						params[k] = v[0]
					}
				} else if r.Method == "GET" {
					for k, v := range r.URL.Query() {
						params[k] = v[0]
					}
				} else {
					json.NewDecoder(r.Body).Decode(&params)
				}
				switch p.Kind {
				case "bing":
					if r.URL.Path != "/search" || params["q"] != "example.com" || params["count"] != "1" {
						t.Error("bing request")
					}
					w.Write([]byte(`<li class="b_algo"><h2><a href="https://example.com">Example</a></h2><p>reference</p></li>`))
				case "ddgs":
					if r.URL.Path != "/html/" || params["q"] != "example.com" {
						t.Error("duck request")
					}
					w.Write([]byte(`<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com">Example</a><a class="result__snippet">reference</a></div>`))
				case "brave":
					if r.Header.Get("X-Subscription-Token") != "private-key" || params["count"] != "1" || params["offset"] != "0" {
						t.Error("brave credentials/paging")
					}
					w.Write([]byte(`{"web":{"results":[{"title":"Example","url":"https://example.com","description":"reference"}]}}`))
				case "tavily":
					if r.Header.Get("Authorization") != "Bearer private-key" || params["max_results"] != float64(1) || params["auto_parameters"] != false || params["search_depth"] != "basic" {
						t.Error("tavily parameters")
					}
					w.Write([]byte(`{"results":[{"title":"Example","url":"https://example.com","content":"reference"}],"usage":{"credits":1}}`))
				case "deepseek":
					if r.Header.Get("Authorization") != "Bearer private-key" || params["count"] != float64(1) || params["need_content"] != false || r.URL.Path != "/v1/tools/search" {
						t.Error("deepseek parameters")
					}
					w.Write([]byte(`{"results":[{"title":"Example","url":"https://example.com","snippet":"reference","summary":"long reference"}]}`))
				}
			}))
			defer api.Close()
			change := ServiceChange{Name: p.Name, Enabled: true, Config: Config{Kind: p.Kind, Endpoint: api.URL}}
			if !p.NoKey {
				if _, err := TestService(t.Context(), store, change); err == nil {
					t.Fatal("missing key accepted")
				}
				if calls != 0 {
					t.Fatal("sent without key")
				}
				change.APIKey = pointer("private-key")
			}
			result, err := TestService(t.Context(), store, change)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "private-key") || !strings.Contains(string(b), "https://example.com") {
				t.Fatal("bad output")
			}
			if calls != 1 {
				t.Fatal("automatic retries")
			}
			list, _ := Services(t.Context(), store)
			if len(list) != 0 {
				t.Fatal("draft saved")
			}
			list, err = SaveService(t.Context(), store, change, "test")
			if err != nil || !Available(list[0]) {
				t.Fatal("not available", err)
			}
			if _, err := SaveService(t.Context(), store, change, "test"); err == nil {
				t.Fatal("duplicate built-in card")
			}
		})
	}
}

func TestWebPageParsingAndErrors(t *testing.T) {
	base, _ := url.Parse("https://www.bing.com/search")
	rows, err := parseWebHTML([]byte(`<li class="b_algo"><h2><a href="https://example.com">Example</a></h2><p>body</p></li><li class="b_algo"><h2><a href="javascript:alert(1)">bad</a></h2></li>`), base, "bing", 1)
	if err != nil || len(rows["results"].([]map[string]string)) != 1 {
		t.Fatal("parser result", err)
	}
	for _, page := range []string{`<form action="anomaly.js">captcha</form>`, `<html><style>.b_no{color:red}</style>login</html>`} {
		if _, err := parseWebHTML([]byte(page), base, "bing", 1); err == nil {
			t.Fatal("unknown page accepted")
		}
	}
	if _, err := parseWebHTML([]byte(`<div class="b_no">No results</div>`), base, "bing", 1); err != nil {
		t.Fatal("valid empty result rejected")
	}
	for _, kind := range []string{"tavily", "deepseek", "ddgs"} {
		cfg, _ := normalizeAsset(Config{Kind: kind})
		if _, _, err := webRequest(t.Context(), cfg, AssetQuery{Query: "x", Page: 2}); err == nil {
			t.Fatal("invented pagination", kind)
		}
	}
	cfg, _ := normalizeAsset(Config{Kind: "brave"})
	if _, _, err := webRequest(t.Context(), cfg, AssetQuery{Query: "x", Page: 11}); err == nil {
		t.Fatal("brave page maximum ignored")
	}
	cfg, _ = normalizeAsset(Config{Kind: "deepseek"})
	if _, _, err := webRequest(t.Context(), cfg, AssetQuery{Query: strings.Repeat("中", 129)}); err == nil {
		t.Fatal("unicode length ignored")
	}
	cfg, _ = normalizeAsset(Config{Kind: "tavily"})
	if _, _, err := webRequest(t.Context(), cfg, AssetQuery{Query: "x", Options: map[string]any{"api_key": "override"}}); err == nil {
		t.Fatal("credential override allowed")
	}
}
