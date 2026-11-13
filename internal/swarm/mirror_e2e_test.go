package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
)

// mirrorRecordsOf 捞任务的全部镜留痕。
func mirrorRecordsOf(t *testing.T, store *rings.Store, task string) []rings.MirrorRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.MirrorRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventMirrorRecord {
			continue
		}
		var rec rings.MirrorRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			t.Fatalf("解码 mirror.record 失败: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// TestMirrorForcedTakeoverE2E P2-01 完成标准主证:真实工具流量 100% 经镜
// 留痕且可回放查证;接管对工具透明。载体双份——Go 原生 http_probe 与
// bash 子进程里的 curl(代理环境变量通道)。引擎链路全真(脚本模型
// 经真实 HTTP/SSE 驱动 Norma 会话)。
func TestMirrorForcedTakeoverE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "mirror-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	defer target.Close()
	main := target.MainURL()
	intent := "HTTP 探测靶站暴露面:版本接口、备份目录与 robots 策略"
	seedGraph(t, store, task, intent)

	// 脚本模型:三次真实工具调用(http_probe ×2 + bash/curl ×1)后收口。
	// bash 命令是普通 curl——不带任何代理参数,接管只能来自环境注入。
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "http_probe", "gr-mock-main"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "http_probe",
				Args: fmt.Sprintf(`{"url":%q}`, main+"/api/version"),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "http_probe", "mock backup archive listing"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "http_probe",
				Args: fmt.Sprintf(`{"url":%q}`, main+"/old-backup/"),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "Disallow: /old-backup"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c3", Name: "bash",
				Args: fmt.Sprintf(`{"command":"curl -s %s/robots.txt"}`, main),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "意图执行完成:版本、备份目录与 robots 策略均已探测,证据完整。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	root := filepath.Join(dir, "tasks")
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

	// ---- 100% 经镜:逐路径对账(靶站命中数 == 镜留痕数) ----
	recs := mirrorRecordsOf(t, store, task)
	if len(recs) != 3 {
		t.Fatalf("应恰 3 条镜留痕(http_probe×2 + curl×1),得到 %d: %+v", len(recs), recs)
	}
	byPath := map[string]int{}
	for _, rec := range recs {
		if rec.Type != rings.MirrorRecHTTP || rec.BlobRef == "" {
			t.Fatalf("交换应全量留痕(带 blob 引用): %+v", rec)
		}
		if rec.StepID != "s1" || rec.UnitID != "u1" || rec.TranscriptID != "swarm-u1" {
			t.Fatalf("留痕归因不符(流量未钉回执行记录): %+v", rec)
		}
		if rec.Status != 200 {
			t.Fatalf("交换状态不符: %+v", rec)
		}
		byPath[pathOf(rec.URL)]++
	}
	for path, want := range map[string]int{"/api/version": 1, "/old-backup/": 1, "/robots.txt": 1} {
		if hits := target.Hits("main", path); hits != want {
			t.Fatalf("靶站 %s 命中 %d 次", path, hits)
		}
		if byPath[path] != want {
			t.Fatalf("镜留痕 %s %d 条 ≠ 靶站命中 %d 次(非 100%% 经镜)", path, byPath[path], want)
		}
	}

	// ---- 可回放查证:账本引用 → blob 原文,与靶站真实内容逐字一致 ----
	exs, err := mirror.Exchanges(ctx, store, ex.Blobs, task)
	if err != nil {
		t.Fatalf("回放失败: %v", err)
	}
	if len(exs) != 3 {
		t.Fatalf("回放应 3 条,得到 %d", len(exs))
	}
	for _, ex := range exs {
		switch pathOf(ex.Request.URL) {
		case "/api/version":
			if string(ex.Response.Body) != `{"service":"gr-mock-main","version":"2.4.1"}` {
				t.Fatalf("版本交换回放失真: %q", ex.Response.Body)
			}
		case "/old-backup/":
			if !strings.Contains(string(ex.Response.Body), "db-2026-09-01.sql.gz") {
				t.Fatalf("备份交换回放失真: %q", ex.Response.Body)
			}
		case "/robots.txt":
			// curl 走代理的直接证据:请求为绝对形式(URL 带主机)且
			//携带 curl 的 User-Agent——命令本身没写任何代理参数。
			if !strings.HasPrefix(ex.Request.URL, "http://") {
				t.Fatalf("curl 交换应为代理绝对形式: %q", ex.Request.URL)
			}
			ua := headerValue(ex.Request.Headers, "User-Agent")
			if !strings.HasPrefix(ua, "curl/") {
				t.Fatalf("curl 交换应带 curl UA: %q", ua)
			}
			if string(ex.Response.Body) != "User-agent: *\nDisallow: /old-backup\n" {
				t.Fatalf("robots 交换回放失真: %q", ex.Response.Body)
			}
			if v := headerValue(ex.Response.Headers, "Server"); v != "gr-mock-main/2.4.1" {
				t.Fatalf("响应头留痕失真: Server=%q", v)
			}
		default:
			t.Fatalf("意外交换: %s", ex.Request.URL)
		}
	}

	// ---- 工具透明 + 执行侧记录不受影响:三次工具调用的 swarm.record
	// 照常入账(模型看到的是普通工具结果,镜在其下)。
	srecs := swarmRecords(t, store, task)
	toolCalls := 0
	for _, r := range srecs {
		if r.Type == rings.SwarmRecToolCall && !r.IsError {
			toolCalls++
		}
	}
	if toolCalls != 3 {
		t.Fatalf("三次工具调用应各有执行记录,得到 %d", toolCalls)
	}
	reqs := model.requests()
	if len(reqs) < 4 {
		t.Fatalf("模型应经 4 轮(3 次工具 + 收口),得到 %d 轮", len(reqs))
	}
	if !strings.Contains(res.FinalText, "意图执行完成") {
		t.Fatalf("模型收口文本不符(工具结果未受接管影响): %q", res.FinalText)
	}

	// ---- P4-73 ① 交换编号可见性:http_probe 结果带 exchange= 行,编号
	// 与镜留痕逐字一致(反证结论据此引用证明交换;curl 结果无此行,引用
	// 走 URL 回查)。
	probeExIDs := map[string]bool{}
	for _, rr := range reqs {
		for _, msg := range rr.Messages {
			if msg.Role != "tool" {
				continue
			}
			for _, line := range strings.Split(msg.Content, "\n") {
				if strings.HasPrefix(line, "exchange=") {
					probeExIDs[strings.TrimPrefix(line, "exchange=")] = true
				}
			}
		}
	}
	if len(probeExIDs) != 2 {
		t.Fatalf("两次 http_probe 各应标注 1 个交换编号: %+v", probeExIDs)
	}
	for _, rec := range recs {
		shouldTag := pathOf(rec.URL) != "/robots.txt" // curl 无编号
		if _, tagged := probeExIDs[rec.ExchangeID]; tagged != shouldTag {
			t.Fatalf("交换编号标注与镜留痕不一致: %s 标注=%v 应标注=%v", rec.ExchangeID, tagged, shouldTag)
		}
	}
}

// TestMirrorNilKeepsP1Behavior 无镜装配时:工具照常工作(直连)、零镜
// 留痕——P1 行为零回归,且透明性反证(http_probe 代码无镜分支)。
func TestMirrorNilKeepsP1Behavior(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "mirror-nil"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	defer target.Close()
	main := target.MainURL()
	seedGraph(t, store, task, "HTTP 探测版本接口")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "http_probe", "gr-mock-main") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "http_probe",
				Args: fmt.Sprintf(`{"url":%q}`, main+"/api/version"),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取任务包失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil || res.StepStatus != "done" {
		t.Fatalf("无镜执行应照常完成: %v %+v", err, res)
	}
	if target.Hits("main", "/api/version") != 1 {
		t.Fatalf("靶站应被真实命中 1 次")
	}
	if recs := mirrorRecordsOf(t, store, task); len(recs) != 0 {
		t.Fatalf("无镜装配不应产生镜留痕: %+v", recs)
	}
}

// ---- 小件 ----

func pathOf(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j:]
		}
		return "/"
	}
	return rawURL
}

func headerValue(hs []mirror.Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
