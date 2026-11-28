package websearch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const ddpocListFixture = `<table><tr><th>编号</th><th>漏洞名称</th><th>类型</th><th>威胁等级</th></tr><tr><td>DVB-2026-1</td><td><a href="DVB-2026-1.html">Example 漏洞</a></td><td>信息泄露</td><td>高危</td></tr><tr><td>DVB-2026-2</td><td><a href="https://offsite.invalid/DVB-2026-2.html">bad</a></td><td>bad</td></tr></table>`
const ddpocPageFixture = `<nav>导航资料</nav><div class="project-details-area"><h2>Example 漏洞</h2><p>漏洞描述：公开简介</p><p>漏洞详情：账号可见内容</p><p>剩余部分暂无权限</p><a href="https://example.com/reference">参考链接</a><a href="/download/paid">下载入口</a><div hidden>HIDDEN-CONTENT</div><script>SCRIPT-CONTENT</script><button>积分兑换</button></div>`

func TestDDPocAccountSearchAndRead(t *testing.T) {
	for _, input := range []string{"test-cookie", "web1=test-cookie", "Hm_lvt=stats; web1=test-cookie; HMACCOUNT=stats", "Cookie: web1=test-cookie"} {
		v, e := ddpocCookie(input)
		if e != nil || v != "test-cookie" {
			t.Fatal("cookie normalization", e)
		}
	}
	for _, input := range []string{"", "web1=", "abc\r\nInjected: x", "Hm_lvt=x; other=y"} {
		if _, e := ddpocCookie(input); e == nil {
			t.Fatal("invalid cookie accepted")
		}
	}
	store := openStore(t)
	var requests atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.Header.Get("Cookie") != "web1=test-cookie" {
			t.Error("wrong login or method")
		}
		switch r.URL.Path {
		case "/gateway/list.html":
			if r.URL.Query().Get("keyword") != "Apache" || r.URL.Query().Get("page") != "1" {
				t.Error("wrong search query")
			}
			w.Write([]byte(ddpocListFixture))
		case "/gateway/DVB-2026-1.html":
			w.Write([]byte(ddpocPageFixture))
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	change := ServiceChange{Name: "DDPoc", Enabled: true, Config: Config{Kind: "ddpoc", Endpoint: api.URL + "/gateway"}, APIKey: pointer("HMACCOUNT=stats; web1=test-cookie")}
	result, err := TestService(t.Context(), store, change)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if requests.Load() != 1 || !strings.Contains(string(raw), "高危") || !strings.Contains(string(raw), "DVB-2026-1") || strings.Contains(string(raw), "test-cookie") || strings.Contains(string(raw), "offsite") {
		t.Fatal("bad list result", string(raw))
	}
	list, _ := Services(t.Context(), store)
	if len(list) != 0 {
		t.Fatal("draft persisted")
	}
	list, err = SaveService(t.Context(), store, change, "test")
	if err != nil || list[0].APIKey != "test-cookie" {
		t.Fatal("secret not canonicalized", err)
	}
	for _, path := range []string{"DVB-2026-1", "/gateway/DVB-2026-1.html", api.URL + "/gateway/DVB-2026-1.html"} {
		result, err := AssetSearch(t.Context(), store, list[0].ID, AssetQuery{Action: "read", Path: path})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"公开简介", "账号可见内容", "暂无权限", "https://example.com/reference"} {
			if !strings.Contains(string(result.Data), want) {
				t.Fatal("visible content missing", want)
			}
		}
		for _, unwanted := range []string{"HIDDEN-CONTENT", "SCRIPT-CONTENT", "导航资料", "test-cookie"} {
			if strings.Contains(string(result.Data), unwanted) {
				t.Fatal("hidden or secret content returned", unwanted)
			}
		}
	}
	if requests.Load() != 4 {
		t.Fatal("unexpected download, unlock, enrichment or retry")
	}
	q, _ := AssetUsage(t.Context(), store, list[0].Config)
	if q.DayUsed != 4 {
		t.Fatal("HTTP requests not counted", q.DayUsed)
	}
	for _, path := range []string{"https://offsite.invalid/DVB-2026-1.html", "/download/paid", "/gateway/DVB-2026-1.html?unlock=1", "../DVB-2026-1.html"} {
		if _, e := AssetSearch(t.Context(), store, list[0].ID, AssetQuery{Action: "read", Path: path}); e == nil {
			t.Fatal("invalid read accepted", path)
		}
	}
	if requests.Load() != 4 {
		t.Fatal("credentials sent for invalid page")
	}
}

func TestDDPocLoginAndPageErrors(t *testing.T) {
	for _, tc := range []struct {
		status             int
		body, action, want string
	}{
		{302, "", "search", "登录已失效"},
		{403, "", "read", "账号权限"},
		{200, `<input type=password>`, "search", "登录已失效"},
		{200, `<input type=password>`, "read", "登录已失效"},
		{200, "changed layout", "search", "漏洞列表"},
		{200, "changed layout", "read", "漏洞页面"},
	} {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
		_, err := searchAsset(t.Context(), openStore(t), Config{Kind: "ddpoc", Endpoint: api.URL, APIKey: "cookie"}, AssetQuery{Action: tc.action, Query: "x", Path: "DVB-2026-1"})
		api.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatal("bad login/format result", err)
		}
	}
	base, _ := url.Parse("https://www.ddpoc.com/list.html?page=1")
	data, err := parseDDPocList([]byte(`<table><tr><th>编号</th><th>漏洞名称</th><th>类型</th></tr></table>`), base, 10)
	if err != nil || len(data["results"].([]ddpocClue)) != 0 {
		t.Fatal("valid empty list rejected", err)
	}
}
