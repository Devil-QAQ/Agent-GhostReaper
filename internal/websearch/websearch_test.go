package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

func openStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// withEndpoints 三端点指向本地 mock,测毕还原。
func withEndpoints(t *testing.T, ddgs, brave, tavily string) {
	t.Helper()
	oldD, oldB, oldT := DDGSEndpoint, BraveEndpoint, TavilyEndpoint
	DDGSEndpoint, BraveEndpoint, TavilyEndpoint = ddgs, brave, tavily
	t.Cleanup(func() { DDGSEndpoint, BraveEndpoint, TavilyEndpoint = oldD, oldB, oldT })
}

// mockDDGS 仿 DuckDuckGo HTML 结果页(真实页面的结构形态:uddg 跳转
// 链接、<b> 高亮、result__snippet;属性序与真实页面一样 href 在前)。
func mockDDGS(t *testing.T, queries *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.FormValue("q")
		*queries = append(*queries, q)
		fmt.Fprintf(w, `<!DOCTYPE html><html><body>
<div class="result results_links">
 <h2 class="result__title"><a rel="nofollow" href="//duckduckgo.com/l/?uddg=%s&amp;rut=abc" class="result__a">Apache <b>log4j</b> RCE &#x27;CVE-2021-44228&#x27;</a></h2>
 <a class="result__snippet" href="//duckduckgo.com/l/?uddg=%s">Log4j2 &lt;=2.14.1 JNDI 注入,<b>影响面</b>极大</a>
</div>
<div class="result">
 <h2 class="result__title"><a rel="nofollow" href="https://example.org/advisory" class="result__a">直链通告</a></h2>
 <a class="result__snippet">官方通告正文</a>
</div>
<div class="nav-link"><a class="nav" href="/?q=x">下一页(非结果锚)</a></div>
</body></html>`,
			url.QueryEscape("https://logging.apache.org/log4j/2.x/security.html"),
			url.QueryEscape("https://logging.apache.org/log4j/2.x/security.html"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSearchDDGSDefaultKeyless ddgs 默认后端免 key 全链路:POST 表单、
// 跳转链接还原真实 URL、<b>/实体还原、非结果锚不混入。
func TestSearchDDGSDefaultKeyless(t *testing.T) {
	var gotQueries []string
	srv := mockDDGS(t, &gotQueries)
	withEndpoints(t, srv.URL, "http://127.0.0.1:1", "http://127.0.0.1:1")

	rs, err := Search(context.Background(), Config{}, "CVE-2021-44228 log4j")
	if err != nil {
		t.Fatalf("ddgs 搜索失败: %v", err)
	}
	if len(rs) != 2 {
		t.Fatalf("应解析出 2 条结果,得到 %d: %+v", len(rs), rs)
	}
	if rs[0].URL != "https://logging.apache.org/log4j/2.x/security.html" {
		t.Fatalf("uddg 跳转应还原为真实 URL: %q", rs[0].URL)
	}
	if rs[0].Title != "Apache log4j RCE 'CVE-2021-44228'" {
		t.Fatalf("标题应剥 <b> 并还原实体: %q", rs[0].Title)
	}
	if !strings.Contains(rs[0].Snippet, "JNDI 注入") || !strings.Contains(rs[0].Snippet, "影响面") {
		t.Fatalf("摘要应剥标签还原实体: %q", rs[0].Snippet)
	}
	if rs[1].URL != "https://example.org/advisory" || rs[1].Title != "直链通告" {
		t.Fatalf("直链结果不符: %+v", rs[1])
	}
	if len(gotQueries) != 1 || gotQueries[0] != "CVE-2021-44228 log4j" {
		t.Fatalf("查询词应经 POST 表单送达: %+v", gotQueries)
	}
}

// TestSearchBraveBackend Brave 后端:key 经 X-Subscription-Token 头送达、
// JSON 解析归一;401 报 key 问题。
func TestSearchBraveBackend(t *testing.T) {
	var gotToken string
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{"results": []map[string]string{
				{"title": "cve.org 记录", "url": "https://www.cve.org/CVERecord?id=CVE-2021-44228", "description": "JNDI 注入"},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	withEndpoints(t, "http://127.0.0.1:1", srv.URL, "http://127.0.0.1:1")

	rs, err := Search(context.Background(), Config{Backend: BackendBrave, BraveKey: "BSAkey123"}, "cve-2021-44228")
	if err != nil {
		t.Fatalf("Brave 搜索失败: %v", err)
	}
	if gotToken != "BSAkey123" {
		t.Fatalf("Brave key 应经 X-Subscription-Token 送达: %q", gotToken)
	}
	if gotQuery != "cve-2021-44228" {
		t.Fatalf("查询词不符: %q", gotQuery)
	}
	if len(rs) != 1 || rs[0].Title != "cve.org 记录" || rs[0].URL != "https://www.cve.org/CVERecord?id=CVE-2021-44228" {
		t.Fatalf("Brave 结果解析不符: %+v", rs)
	}

	// 401:key 无效的如实报错。
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid key"}`)
	}))
	t.Cleanup(srv401.Close)
	withEndpoints(t, "http://127.0.0.1:1", srv401.URL, "http://127.0.0.1:1")
	_, err = Search(context.Background(), Config{Backend: BackendBrave, BraveKey: "bad"}, "x")
	if err == nil || !strings.Contains(err.Error(), "key 无效或过期") {
		t.Fatalf("401 应报 key 问题: %v", err)
	}

	// 无 key:入口直接拒并指路。
	_, err = Search(context.Background(), Config{Backend: BackendBrave}, "x")
	if err == nil || !strings.Contains(err.Error(), "需要 key") {
		t.Fatalf("Brave 无 key 应拒并指路: %v", err)
	}
}

// TestSearchTavilyBackend Tavily 后端:Bearer 头 + POST JSON 解析。
func TestSearchTavilyBackend(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{
				{"title": "NVD 条目", "url": "https://nvd.nist.gov/vuln/detail/CVE-2021-44228", "content": "10.0 严重级"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	withEndpoints(t, "http://127.0.0.1:1", "http://127.0.0.1:1", srv.URL)

	rs, err := Search(context.Background(), Config{Backend: BackendTavily, TavilyKey: "tvly-xyz"}, "log4j shell")
	if err != nil {
		t.Fatalf("Tavily 搜索失败: %v", err)
	}
	if gotAuth != "Bearer tvly-xyz" {
		t.Fatalf("Tavily key 应经 Bearer 头送达: %q", gotAuth)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil || sent["query"] != "log4j shell" {
		t.Fatalf("查询词应经 POST JSON 送达: %q", gotBody)
	}
	if len(rs) != 1 || rs[0].Snippet != "10.0 严重级" {
		t.Fatalf("Tavily 结果解析不符: %+v", rs)
	}
}

// TestSearchEgressProxy 独立出口代理接线:配了代理(http 形态)时请求
// 经代理送达(代理收到绝对形式 URL),不配直连直达。ddgs 端点作载体
// (http:// mock,Go 传输层对 http 目标走 Proxy 的可见形态)。
func TestSearchEgressProxy(t *testing.T) {
	target := mockDDGS(t, &[]string{})
	var viaProxy string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxy = r.URL.String() // 代理形态:绝对 URL
		resp, err := http.Get(r.URL.String())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxySrv.Close)
	withEndpoints(t, target.URL, "http://127.0.0.1:1", "http://127.0.0.1:1")

	// 配代理:命中 mock 目标且请求经代理(绝对形式 URL)。
	if _, err := Search(context.Background(), Config{Proxy: proxySrv.URL}, "x"); err != nil {
		t.Fatalf("经代理搜索失败: %v", err)
	}
	if !strings.HasPrefix(viaProxy, target.URL) {
		t.Fatalf("请求应经独立出口代理送达(绝对形式指向目标),得到 %q", viaProxy)
	}

	// 坏代理协议:入口拒。
	if _, err := Search(context.Background(), Config{Proxy: "ftp://127.0.0.1:9"}, "x"); err == nil {
		t.Fatalf("坏代理协议应拒")
	}
}

// TestConfigLoadSaveSwitch 配置读写与后端切换生效:设置表存取往返、
// 未知后端归一 ddgs、切到 brave 后 Search 实际走 brave 端点。
func TestConfigLoadSaveSwitch(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	// 空库:全默认(ddgs、无 key、直连)。
	if cfg := Load(ctx, store); cfg.Backend != BackendDDGS || cfg.BraveKey != "" || cfg.Proxy != "" {
		t.Fatalf("空库应全默认: %+v", cfg)
	}

	// 写入 + 读回:后端与 key 原样,未知后端归一。
	if err := Save(ctx, store, Config{Backend: BackendBrave, BraveKey: "bk"}, "creator"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if cfg := Load(ctx, store); cfg.Backend != BackendBrave || cfg.BraveKey != "bk" {
		t.Fatalf("读回应与写入一致: %+v", cfg)
	}
	if err := Save(ctx, store, Config{Backend: "unknown"}, "creator"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if cfg := Load(ctx, store); cfg.Backend != BackendDDGS {
		t.Fatalf("未知后端应归一 ddgs: %+v", cfg)
	}

	// 切后端生效:切回 brave 后,Search 命中 brave mock 端点。
	var braveHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		braveHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"web":{"results":[{"title":"t","url":"https://a.b/c","description":"d"}]}}`)
	}))
	t.Cleanup(srv.Close)
	withEndpoints(t, "http://127.0.0.1:1", srv.URL, "http://127.0.0.1:1")
	if err := Save(ctx, store, Config{Backend: BackendBrave, BraveKey: "bk"}, "creator"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := Search(ctx, Load(ctx, store), "switch test"); err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if !braveHit {
		t.Fatalf("切换后端后应命中 Brave 端点")
	}
}

// TestValidateProxy 代理校验:三协议过、坏形态与坏协议拒、空合法。
func TestValidateProxy(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:7890", "https://p.example:8443", "socks5://u:p@127.0.0.1:1080"} {
		if err := ValidateProxy(ok); err != nil {
			t.Fatalf("%q 应合法: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://x", "127.0.0.1:7890", "http://"} {
		if err := ValidateProxy(bad); err == nil {
			t.Fatalf("%q 应拒绝", bad)
		}
	}
}
