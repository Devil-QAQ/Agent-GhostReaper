package swarm

// P3-03 工具扩充测试:read(只读本机文件)/ web_search(多后端经设置
// 切换)/ web_fetch(强制经镜留痕、无镜拒直连)。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/websearch"
)

func TestWebSearchUnitsShareOnlyUnavailableService(t *testing.T) {
	env := webEnv(t)
	other := &unitEnv{Executor: env.Executor, TaskID: env.TaskID, StepID: env.StepID, UnitID: "second-unit"}
	var calls atomic.Int64
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	cfg := searchFixtureConfig(srv.URL)
	service := saveSearchService(t, env.Executor.Store, "", "fixture", cfg, nil)
	first := callTool(t, env.webSearchTool(), `{"query":"first fixture"}`)
	second := callTool(t, other.webSearchTool(), `{"query":"different fixture"}`)
	if !first.IsError || !second.IsError || !strings.Contains(second.Flatten(), "不重复请求") || calls.Load() != 1 {
		t.Fatal("another unit repeated the same unavailable service")
	}
	healthy.Store(true)
	changedKey := "fixture-two"
	saveSearchService(t, env.Executor.Store, service.ID, service.Name, cfg, &changedKey)
	res := callTool(t, other.webSearchTool(), `{"query":"after configuration change"}`)
	if res.IsError || !strings.Contains(res.Flatten(), "0 条") || calls.Load() != 2 {
		t.Fatal("changed configuration or a valid empty result remained blocked")
	}
}

// webEnv 带 store 的单位环境(直调工具用,完整 e2e 见 webtools_e2e_test)。
func webEnv(t *testing.T) *unitEnv {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "web-t", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	return env
}

func searchFixtureConfig(endpoint string) websearch.Config {
	return websearch.Config{Endpoint: endpoint, Method: "GET", Params: map[string]any{"q": "{{query}}"}, ResponseMode: "json", ResultsPath: "results", TitlePath: "title", URLPath: "url", SnippetPath: "content"}
}

func saveSearchService(t *testing.T, store *rings.Store, id, name string, cfg websearch.Config, key *string) websearch.Service {
	t.Helper()
	list, err := websearch.SaveService(t.Context(), store, websearch.ServiceChange{ID: id, Name: name, Enabled: true, Config: cfg, APIKey: key}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		return list[len(list)-1]
	}
	for _, item := range list {
		if item.ID == id {
			return item
		}
	}
	t.Fatal("saved service missing")
	return websearch.Service{}
}

func callTool(t *testing.T, tt tool.CoreTool, args string) tool.Result {
	t.Helper()
	res, err := tt.Call(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("工具调用异常: %v", err)
	}
	return res
}

// TestReadToolLocalFile 完成标准:Read 读本机文件实测(只读;带行号与
// 分页;二进制/目录/越界如实拒并指路 bash;工具集无任何写文件工具)。
func TestReadToolLocalFile(t *testing.T) {
	env := webEnv(t)
	dir := env.Executor.stepDir(env.TaskID, env.StepID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建工作目录失败: %v", err)
	}
	content := "line-alpha\nline-beta\nline-gamma\nline-delta\n"
	if err := os.WriteFile(filepath.Join(dir, "loot.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	read := env.readTool()

	// 全量读:行号 + 内容逐行一致。
	res := callTool(t, read, `{"path":"loot.txt"}`)
	if res.IsError {
		t.Fatalf("read 应成功: %s", resultText(res))
	}
	text := resultText(res)
	for i, want := range []string{"1\tline-alpha", "2\tline-beta", "3\tline-gamma", "4\tline-delta"} {
		if !strings.Contains(text, want) {
			t.Fatalf("read 第 %d 行不符(应带行号): %q", i+1, text)
		}
	}

	// 分页读:offset/limit 取中段,头部标注起始行,尾部提示续读。
	res = callTool(t, read, `{"path":"loot.txt","offset":2,"limit":2}`)
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "第 2 行起") || !strings.Contains(text, "2\tline-beta") ||
		!strings.Contains(text, "3\tline-gamma") || strings.Contains(text, "line-delta") {
		t.Fatalf("分页读不符: %q", text)
	}
	if !strings.Contains(text, "未读完") || !strings.Contains(text, "offset=4") {
		t.Fatalf("未读完应提示续读 offset: %q", text)
	}

	// 绝对路径读。
	res = callTool(t, read, fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "loot.txt")))
	if res.IsError || !strings.Contains(resultText(res), "line-alpha") {
		t.Fatalf("绝对路径应可读: %q", resultText(res))
	}

	// 二进制:拒并指路 bash。
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatalf("写二进制失败: %v", err)
	}
	res = callTool(t, read, `{"path":"blob.bin"}`)
	if !res.IsError || !strings.Contains(resultText(res), "二进制") || !strings.Contains(resultText(res), "bash") {
		t.Fatalf("二进制应拒并指路 bash: %q", resultText(res))
	}

	// 目录与不存在:拒并指路。
	res = callTool(t, read, `{"path":"."}`)
	if !res.IsError || !strings.Contains(resultText(res), "目录") {
		t.Fatalf("目录应拒: %q", resultText(res))
	}
	res = callTool(t, read, `{"path":"no-such-file"}`)
	if !res.IsError || !strings.Contains(resultText(res), "不存在") {
		t.Fatalf("缺文件应拒: %q", resultText(res))
	}

	// 只读铁证:工具全集里不存在任何写文件工具;read 描述自证只读。
	names := map[string]bool{}
	for _, tt := range env.toolset() {
		names[tt.Name()] = true
		if strings.Contains(tt.Name(), "write") || strings.Contains(tt.Name(), "save") {
			t.Fatalf("工具集混入写工具 %q", tt.Name())
		}
	}
	if !names["read"] {
		t.Fatalf("工具集应含 read")
	}
	if d := read.Description(); !strings.Contains(d, "无任何写操作") {
		t.Fatalf("read 描述须自证只读: %q", d)
	}
	// 读过程零落盘副作用:工作目录只应有本测试写入的文件。
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("read 不应产生任何新文件: %v", ents)
	}
}

func TestWebSearchToolCustomServiceSelection(t *testing.T) {
	env := webEnv(t)
	search := env.webSearchTool()
	if result := callTool(t, search, `{"query":"reference"}`); !result.IsError || !strings.Contains(resultText(result), "尚未配置") {
		t.Fatal("empty search configuration used a default")
	}
	var firstHits, secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		io.WriteString(w, `{"results":[{"title":"first","url":"https://first.example/document","content":"first reference"}]}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		io.WriteString(w, `{"results":[{"title":"second","url":"https://second.example/document","content":"second reference"}]}`)
	}))
	defer second.Close()
	a := saveSearchService(t, env.Executor.Store, "", "first custom", searchFixtureConfig(first.URL), nil)
	b := saveSearchService(t, env.Executor.Store, "", "second custom", searchFixtureConfig(second.URL), nil)
	result := callTool(t, search, `{"query":"reference"}`)
	if result.IsError || !strings.Contains(resultText(result), a.ID) || !strings.Contains(resultText(result), b.ID) || firstHits.Load() != 0 || secondHits.Load() != 0 {
		t.Fatal("multiple services were automatically searched")
	}
	result = callTool(t, search, fmt.Sprintf(`{"query":"reference","service_id":%q}`, a.ID))
	if result.IsError || !strings.Contains(resultText(result), "https://first.example/document") || firstHits.Load() != 1 || secondHits.Load() != 0 {
		t.Fatal("model-selected service was ignored")
	}
	saveSearchService(t, env.Executor.Store, a.ID, a.Name, searchFixtureConfig(second.URL), nil)
	result = callTool(t, search, fmt.Sprintf(`{"query":"changed","service_id":%q}`, a.ID))
	if result.IsError || !strings.Contains(resultText(result), "second reference") {
		t.Fatal("edited API did not take effect")
	}
	if _, err := websearch.DeleteService(t.Context(), env.Executor.Store, a.ID, "test"); err != nil {
		t.Fatal(err)
	}
	result = callTool(t, search, fmt.Sprintf(`{"query":"reference","service_id":%q}`, a.ID))
	if !result.IsError {
		t.Fatal("deleted service remained available")
	}
	if _, err := websearch.DeleteService(t.Context(), env.Executor.Store, b.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if result := callTool(t, search, `{"query":"reference"}`); !result.IsError || !strings.Contains(resultText(result), "尚未配置") {
		t.Fatal("deleting all services restored a default")
	}
}

func TestWebSearchToolRawResponse(t *testing.T) {
	env := webEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"arbitrary":{"reference":"full raw response"}}`)
	}))
	defer srv.Close()
	cfg := searchFixtureConfig(srv.URL)
	cfg.ResponseMode = "text"
	saveSearchService(t, env.Executor.Store, "", "raw custom", cfg, nil)
	result := callTool(t, env.webSearchTool(), `{"query":"reference"}`)
	if result.IsError || !strings.Contains(resultText(result), "full raw response") || strings.Contains(resultText(result), "1 条") {
		t.Fatal("raw API response was lost or given a fabricated result count")
	}
}

func TestWebSearchGitHubCardDispatch(t *testing.T) {
	env := webEnv(t)
	service := saveSearchService(t, env.Executor.Store, "", "GitHub", websearch.Config{Kind: "github"}, nil)
	listing := callTool(t, env.webSearchTool(), `{}`)
	if listing.IsError || !strings.Contains(resultText(listing), "browse") || !strings.Contains(resultText(listing), service.ID) {
		t.Fatal("site capabilities not discoverable")
	}
	invalid := callTool(t, env.webSearchTool(), `{"action":"read","repo":"owner/repo","path":"../outside"}`)
	if !invalid.IsError || !strings.Contains(resultText(invalid), "路径无效") {
		t.Fatal("read action did not reach GitHub validation")
	}
	blocked, _ := json.Marshal(map[string]any{"blocked": time.Now().Add(time.Hour)})
	if err := env.Executor.Store.SetSetting(t.Context(), "search.github.quota", string(blocked), "test"); err != nil {
		t.Fatal(err)
	}
	other := &unitEnv{Executor: env.Executor, TaskID: "other-task", StepID: "s1", UnitID: "other"}
	for _, e := range []*unitEnv{env, other} {
		result := callTool(t, e.webSearchTool(), `{"query":"public poc"}`)
		if !result.IsError || !strings.Contains(resultText(result), "请求额度暂不可用") {
			t.Fatal("shared GitHub limit bypassed")
		}
	}
	if _, err := websearch.DeleteService(t.Context(), env.Executor.Store, service.ID, "test"); err != nil {
		t.Fatal(err)
	}
	deleted := callTool(t, env.webSearchTool(), `{"action":"browse","repo":"owner/repo"}`)
	if !deleted.IsError || !strings.Contains(resultText(deleted), "尚未配置") {
		t.Fatal("deleted site retained")
	}
}

func TestWebSearchAssetCardDispatch(t *testing.T) {
	env := webEnv(t)
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"error":false,"results":[["https://example.com","192.0.2.1",443]],"credit_note":"original data"}`)
	}))
	defer api.Close()
	missing := saveSearchService(t, env.Executor.Store, "", "鹰图", websearch.Config{Kind: "hunter"}, nil)
	key := "test-assets-key"
	fofa := saveSearchService(t, env.Executor.Store, "", "FOFA", websearch.Config{Kind: "fofa", Endpoint: api.URL, PerMinute: 1}, &key)
	listing := callTool(t, env.webSearchTool(), `{}`)
	if listing.IsError || !strings.Contains(resultText(listing), fofa.ID) || strings.Contains(resultText(listing), missing.ID) || calls.Load() != 0 {
		t.Fatal("unconfigured service listed or discovery sent a request")
	}
	result := callTool(t, env.webSearchTool(), fmt.Sprintf(`{"service_id":%q,"query":"domain=\"example.com\"","page":2,"size":10,"options":{"fields":"ip,port"}}`, fofa.ID))
	if result.IsError || !strings.Contains(resultText(result), "original data") || calls.Load() != 1 {
		t.Fatal("asset request did not reach native adapter", resultText(result))
	}
	other := &unitEnv{Executor: env.Executor, TaskID: "other-task", StepID: "s1", UnitID: "other"}
	result = callTool(t, other.webSearchTool(), fmt.Sprintf(`{"service_id":%q,"query":"another query"}`, fofa.ID))
	if !result.IsError || !strings.Contains(resultText(result), "请求额度") || calls.Load() != 1 {
		t.Fatal("agent budget differs from panel / task budget")
	}
	if _, err := websearch.DeleteService(t.Context(), env.Executor.Store, fofa.ID, "test"); err != nil {
		t.Fatal(err)
	}
	result = callTool(t, env.webSearchTool(), fmt.Sprintf(`{"service_id":%q,"query":"x"}`, fofa.ID))
	if !result.IsError || calls.Load() != 1 {
		t.Fatal("deleted card retained")
	}
}

// TestWebFetchToolMirrorEnforced 完成标准:WebFetch 强制经镜——无镜拒
// 直连(零旁路);有镜时流量 100% 留痕且可回放查证(逐字一致)。
func TestWebFetchToolMirrorEnforced(t *testing.T) {
	env := webEnv(t)
	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	defer target.Close()

	// 无镜:拒直连(fail-safe,不出旁路)。
	res := callTool(t, env.webFetchTool(), fmt.Sprintf(`{"url":%q}`, target.MainURL()+"/robots.txt"))
	if !res.IsError || !strings.Contains(resultText(res), "镜未装配") {
		t.Fatalf("无镜应拒绝直连: %q", resultText(res))
	}
	if target.Hits("main", "/robots.txt") != 0 {
		t.Fatalf("无镜时不应产生任何直连流量")
	}

	// 装镜:取文成功 + 留痕 + 回放逐字一致。
	m := mirror.New(env.Executor.Store, env.Executor.Blobs, t.TempDir())
	sess, err := m.Session(mirror.Meta{TaskID: env.TaskID, StepID: env.StepID,
		UnitID: env.UnitID, TranscriptID: env.TranscriptID})
	if err != nil {
		t.Fatalf("起镜会话失败: %v", err)
	}
	defer sess.Close()
	env.mirror = sess

	res = callTool(t, env.webFetchTool(), fmt.Sprintf(`{"url":%q}`, target.MainURL()+"/robots.txt"))
	if res.IsError {
		t.Fatalf("web_fetch 应成功: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "status=200") || !strings.Contains(text, "User-agent: *") ||
		!strings.Contains(text, "Disallow: /old-backup") {
		t.Fatalf("取文结果不符(状态行+正文): %q", text)
	}
	if target.Hits("main", "/robots.txt") != 1 {
		t.Fatalf("靶站应被真实命中 1 次")
	}
	recs := mirrorRecordsOf(t, env.Executor.Store, env.TaskID)
	if len(recs) != 1 || recs[0].Type != "http" || recs[0].BlobRef == "" {
		t.Fatalf("web_fetch 流量应经镜留痕: %+v", recs)
	}
	exs, err := mirror.Exchanges(context.Background(), env.Executor.Store, env.Executor.Blobs, env.TaskID)
	if err != nil || len(exs) != 1 {
		t.Fatalf("回放应恰 1 条: %v %d", err, len(exs))
	}
	if string(exs[0].Response.Body) != "User-agent: *\nDisallow: /old-backup\n" {
		t.Fatalf("回放原文失真: %q", exs[0].Response.Body)
	}
	// UA 如实留痕且按策略形态(P3-13c 起 web_fetch 吃 UA 策略,默认
	// 随机浏览器池——精确池成员断言见 tools_p313c_test.go)。
	if ua := headerValue(exs[0].Request.Headers, "User-Agent"); !strings.HasPrefix(ua, "Mozilla/5.0 (") {
		t.Fatalf("web_fetch 请求 UA 应按策略留痕: %q", ua)
	}

	// HTML 转正文:标签与 script 噪声不进模型正文(留痕侧仍是源码)。
	htmlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><script>var x=1;</script><style>a{}</style></head>
<body><h1>Advisory</h1><p>CVE-2021-44228 &amp; log4j</p></body></html>`)
	}))
	defer htmlSrv.Close()
	res = callTool(t, env.webFetchTool(), fmt.Sprintf(`{"url":%q}`, htmlSrv.URL))
	if res.IsError {
		t.Fatalf("HTML 取文应成功: %s", resultText(res))
	}
	if text := resultText(res); !strings.Contains(text, "CVE-2021-44228 & log4j") ||
		strings.Contains(text, "<script>") || strings.Contains(text, "<p>") {
		t.Fatalf("HTML 应转正文文本(实体还原、标签剥离): %q", text)
	}

	// 二进制:只报事实,不展开。
	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02})
	}))
	defer binSrv.Close()
	res = callTool(t, env.webFetchTool(), fmt.Sprintf(`{"url":%q}`, binSrv.URL))
	if res.IsError || !strings.Contains(resultText(res), "二进制内容不展开") {
		t.Fatalf("二进制应只报事实: %q", resultText(res))
	}

	// 坏 URL:入口拒。
	res = callTool(t, env.webFetchTool(), `{"url":"ftp://x"}`)
	if !res.IsError {
		t.Fatalf("坏 URL 应拒")
	}
}

// TestWebToolsCatalogAndDescriptions §11 描述即规则 + 目录单一来源:
// 三工具入目录(插话快照可见),描述写明边界与用途。
func TestWebToolsCatalogAndDescriptions(t *testing.T) {
	catalog := map[string]string{}
	for _, e := range ToolCatalog() {
		catalog[e.Name] = e.Desc
	}
	for name, want := range map[string][]string{
		"read":       {"offset/limit", "无任何写操作"},
		"web_search": {"service_id", "能力库·搜索配置"},
		"web_fetch":  {"经镜留痕", "二进制"},
	} {
		d, ok := catalog[name]
		if !ok {
			t.Fatalf("工具目录缺 %s", name)
		}
		for _, w := range want {
			if !strings.Contains(d, w) {
				t.Fatalf("%s 描述缺 %q: %q", name, w, d)
			}
		}
	}
}

func TestWebSearchDDPocAccountPages(t *testing.T) {
	env := webEnv(t)
	var calls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "web1=fixture-session" {
			t.Error("missing session")
		}
		switch r.URL.Path {
		case "/list.html":
			if r.URL.Query().Get("keyword") != "Example" {
				t.Error("query missing")
			}
			io.WriteString(w, `<table><tr><th>编号</th><th>漏洞名称</th><th>类型</th></tr><tr><td>DVB-2026-1</td><td><a href="DVB-2026-1.html">Example</a></td><td>信息泄露</td></tr></table>`)
		case "/DVB-2026-1.html":
			io.WriteString(w, `<div class="project-details-area"><h2>Example</h2><p>当前账号可见详情</p><p>权限不足的部分由网站提示</p></div>`)
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	key := "fixture-session"
	service := saveSearchService(t, env.Executor.Store, "", "DDPoc", websearch.Config{Kind: "ddpoc", Endpoint: api.URL}, &key)
	discovery := callTool(t, env.webSearchTool(), `{}`)
	if discovery.IsError || !strings.Contains(discovery.Flatten(), "action=read") {
		t.Fatal("read capability not discoverable", discovery.Flatten())
	}
	result := callTool(t, env.webSearchTool(), fmt.Sprintf(`{"service_id":%q,"query":"Example"}`, service.ID))
	if result.IsError || !strings.Contains(result.Flatten(), "DVB-2026-1") {
		t.Fatal("search failed", result.Flatten())
	}
	result = callTool(t, env.webSearchTool(), fmt.Sprintf(`{"service_id":%q,"action":"read","path":"DVB-2026-1"}`, service.ID))
	if result.IsError || !strings.Contains(result.Flatten(), "当前账号可见详情") || strings.Contains(result.Flatten(), key) {
		t.Fatal("account read failed", result.Flatten())
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected automatic requests", calls.Load())
	}
}

func TestWebSearchGitHubCodeEntry(t *testing.T) {
	env := webEnv(t)
	service := saveSearchService(t, env.Executor.Store, "", "GitHub", websearch.Config{Kind: "github"}, nil)
	result := callTool(t, env.webSearchTool(), `{}`)
	if result.IsError || !strings.Contains(result.Flatten(), "search_code") || !strings.Contains(result.Flatten(), "https://docs.github.com/en/rest") {
		t.Fatal("code search not discoverable", result.Flatten())
	}
	input, _ := json.Marshal(map[string]any{"service_id": service.ID, "action": "search_code", "query": "example language:go"})
	result = callTool(t, env.webSearchTool(), string(input))
	if !result.IsError || !strings.Contains(result.Flatten(), "代码搜索需要 Token") {
		t.Fatal("agent code request not routed to GitHub", result.Flatten())
	}
	usage, err := websearch.GitHubUsage(t.Context(), env.Executor.Store, service.Config)
	if err != nil || usage.MinuteUsed != 0 || usage.CodeMinuteUsed != 0 || usage.HourUsed != 0 {
		t.Fatal("unsent query consumed budget", usage, err)
	}
}
