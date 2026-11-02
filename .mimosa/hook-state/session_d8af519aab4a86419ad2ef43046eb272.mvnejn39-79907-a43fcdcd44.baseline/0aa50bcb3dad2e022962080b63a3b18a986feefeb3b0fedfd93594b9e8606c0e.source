package swarm

// P3-03 工具扩充引擎全链路 e2e:真实 Norma 会话(脚本模型经真实
// HTTP/SSE 驱动)调 read / web_search / web_fetch,验证三工具进会话、
// 结果实时入账、web_fetch 流量 100% 经镜留痕可回放。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/websearch"
)

func TestWebToolsE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "webtools-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	defer target.Close()

	// 搜索后端指向本地 mock(ddgs 免 key 裸跑的确定性替身;真实端点的
	// 实跑验证在完成记录单独呈现)。
	var ddgsHit bool
	ddgs := newMockDDGS(t, &ddgsHit)
	oldD := websearch.DDGSEndpoint
	websearch.DDGSEndpoint = ddgs.URL
	t.Cleanup(func() { websearch.DDGSEndpoint = oldD })

	intent := "分析下载物并查证公开信息"
	seedGraph(t, store, task, intent)

	// 步骤工作目录预置一份"下载物"(渗透落盘产物的替身)。
	root := filepath.Join(dir, "tasks")
	stepDir := filepath.Join(root, task, "is1")
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		t.Fatalf("建步骤目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "response.dump"),
		[]byte("HTTP/1.1 200 OK\nServer: gr-mock-main/2.4.1\n\n{\"leak\":\"admin:1234\"}\n"), 0o644); err != nil {
		t.Fatalf("写下载物失败: %v", err)
	}

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "read", "admin:1234"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "read", Args: `{"path":"response.dump"}`,
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "web_search", "CVE-2021-44228"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "web_search", Args: `{"query":"CVE-2021-44228 log4j"}`,
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "web_fetch", "Disallow: /old-backup"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c3", Name: "web_fetch",
				Args: fmt.Sprintf(`{"url":%q}`, target.MainURL()+"/robots.txt"),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "意图完成:下载物已读、公开信息已查证、原文已取。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	ex := NewExecutor(store, prov, root, Budget{})
	ex.Mirror = mirror.New(store, ex.Blobs, t.TempDir())

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取任务包失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.StepStatus != "done" || res.Reason != "completed" {
		t.Fatalf("执行结果不符: %+v", res)
	}

	// 三次工具调用照常入账(引擎侧记录不受影响)。
	srecs := swarmRecords(t, store, task)
	calls := map[string]int{}
	for _, r := range srecs {
		if r.Type == rings.SwarmRecToolCall && !r.IsError {
			calls[r.ToolName]++
		}
	}
	for _, name := range []string{"read", "web_search", "web_fetch"} {
		if calls[name] != 1 {
			t.Fatalf("%s 应恰 1 次成功调用记录,得到 %d(全集 %v)", name, calls[name], calls)
		}
	}

	// read:相对路径按步骤工作目录解析,内容带行号返回。
	if !strings.Contains(res.FinalText, "意图完成") {
		t.Fatalf("收口文本不符: %q", res.FinalText)
	}

	// web_search:默认 ddgs(免 key)命中 mock,结果回到模型(脚本第
	//三分支能等到 CVE 标记即已证明,这里再核后端命中)。
	if !ddgsHit {
		t.Fatalf("默认后端应命中 ddgs mock")
	}

	// web_fetch:流量 100% 经镜留痕 + 回放逐字一致。
	if target.Hits("main", "/robots.txt") != 1 {
		t.Fatalf("靶站应被真实命中 1 次")
	}
	recs := mirrorRecordsOf(t, store, task)
	if len(recs) != 1 || recs[0].BlobRef == "" {
		t.Fatalf("web_fetch 流量应恰 1 条带 blob 的镜留痕: %+v", recs)
	}
	if recs[0].StepID != "s1" || recs[0].UnitID != "u1" {
		t.Fatalf("留痕归因不符: %+v", recs[0])
	}
	exs, err := mirror.Exchanges(ctx, store, ex.Blobs, task)
	if err != nil || len(exs) != 1 {
		t.Fatalf("回放应恰 1 条: %v %d", err, len(exs))
	}
	if string(exs[0].Response.Body) != "User-agent: *\nDisallow: /old-backup\n" {
		t.Fatalf("回放原文失真: %q", exs[0].Response.Body)
	}
}

// newMockDDGS ddgs 后端的本地确定性替身(真实页面结构的最小形态)。
func newMockDDGS(t *testing.T, hit *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hit = true
		_, _ = io.WriteString(w, `<html><body><div class="result">
<h2><a class="result__a" href="https://nvd.example/CVE-2021-44228">NVD:CVE-2021-44228</a></h2>
<a class="result__snippet">log4j RCE</a></div></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}
