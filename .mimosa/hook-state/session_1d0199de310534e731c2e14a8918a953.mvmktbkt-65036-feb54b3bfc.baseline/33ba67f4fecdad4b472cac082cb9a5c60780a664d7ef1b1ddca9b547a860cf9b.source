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
	"testing"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/websearch"
)

// webEnv 带 store 的单位环境(直调工具用,完整 e2e 见 webtools_e2e_test)。
func webEnv(t *testing.T) *unitEnv {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	env := &unitEnv{Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID: "web-t", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	return env
}

// mockSearchEndpoints 三后端 mock 一次挂齐;返回还原闭包。
func mockSearchEndpoints(t *testing.T, ddgsHit, braveHit *bool) {
	t.Helper()
	ddgs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ddgsHit = true
		_, _ = io.WriteString(w, `<html><body>
<div class="result"><h2><a class="result__a" href="https://nvd.example/CVE-2021-44228">NVD:CVE-2021-44228</a></h2>
<a class="result__snippet">log4j RCE,10.0 严重级</a></div>
</body></html>`)
	}))
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*braveHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"web":{"results":[{"title":"Brave 结果","url":"https://brave.example/x","description":"描述"}]}}`)
	}))
	t.Cleanup(func() { ddgs.Close(); brave.Close() })
	oldD, oldB, oldT := websearch.DDGSEndpoint, websearch.BraveEndpoint, websearch.TavilyEndpoint
	websearch.DDGSEndpoint, websearch.BraveEndpoint, websearch.TavilyEndpoint = ddgs.URL, brave.URL, oldT
	t.Cleanup(func() {
		websearch.DDGSEndpoint, websearch.BraveEndpoint, websearch.TavilyEndpoint = oldD, oldB, oldT
	})
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

// TestWebSearchToolBackendSwitch 完成标准:默认 ddgs 免 key 裸跑(命中
// 后端);面板设置切后端后工具下一次调用即走新后端;缺 key 如实拒。
func TestWebSearchToolBackendSwitch(t *testing.T) {
	env := webEnv(t)
	var ddgsHit, braveHit bool
	mockSearchEndpoints(t, &ddgsHit, &braveHit)
	search := env.webSearchTool()

	// 默认(空设置):ddgs 免 key 裸跑。
	res := callTool(t, search, `{"query":"CVE-2021-44228"}`)
	if res.IsError {
		t.Fatalf("默认 ddgs 搜索应成功: %s", resultText(res))
	}
	if !ddgsHit {
		t.Fatalf("默认后端应命中 ddgs(免 key)")
	}
	text := resultText(res)
	if !strings.Contains(text, "后端 DuckDuckGo") || !strings.Contains(text, "https://nvd.example/CVE-2021-44228") {
		t.Fatalf("结果格式不符: %q", text)
	}

	// 切后端生效:设置表写 brave + key,下一次调用即走 brave。
	if err := websearch.Save(context.Background(), env.Executor.Store,
		websearch.Config{Backend: websearch.BackendBrave, BraveKey: "bk"}, "creator"); err != nil {
		t.Fatalf("写搜索配置失败: %v", err)
	}
	res = callTool(t, search, `{"query":"cve"}`)
	if res.IsError {
		t.Fatalf("brave 搜索应成功: %s", resultText(res))
	}
	if !braveHit {
		t.Fatalf("切后端后应命中 brave")
	}
	if !strings.Contains(resultText(res), "后端 Brave") {
		t.Fatalf("结果应标注后端 Brave: %q", resultText(res))
	}

	// 缺 key:拒并指路系统设置。
	if err := websearch.Save(context.Background(), env.Executor.Store,
		websearch.Config{Backend: websearch.BackendTavily}, "creator"); err != nil {
		t.Fatalf("写搜索配置失败: %v", err)
	}
	res = callTool(t, search, `{"query":"cve"}`)
	if !res.IsError || !strings.Contains(resultText(res), "系统设置·搜索配置") {
		t.Fatalf("缺 key 应拒并指路: %q", resultText(res))
	}

	// 空查询:入口拒。
	res = callTool(t, search, `{"query":"  "}`)
	if !res.IsError {
		t.Fatalf("空查询应拒")
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
		"web_search": {"DuckDuckGo", "系统设置·搜索配置"},
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
