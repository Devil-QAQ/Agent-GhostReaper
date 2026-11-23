package swarm

// P5-04 虫群知情化确定性类级测试(规则 15:多形态样本集):
//   - 简报装配(锚例句逐字/概况 JSON 各字段/截 20 与总数/无 coverage
//     信号/装配失败静默降级为首条消息照常);
//   - 只读工具行为(search_facts/list_assets/search_trails/traffic 三件,
//     防慢三规矩:host 必填/轻量摘要无正文/原文按编号分段);
//   - 注册面(knownToolScopes/ToolCatalog/toolset)与硬规则零误拦;
//   - traffic_blob 分段拼接 == 镜渲染全文(与面板同源)。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/rings"
)

// p504SeedBriefingLedger 铺一个有代表性的任务账本:目标/已确认事实
// (25 条+1 条未确认)/已确认与证据不足的漏洞各一/在跑+排队+已完成的
// 步骤各一。
func p504SeedBriefingLedger(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	evs := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "审计示例站敏感信息暴露", Status: "active"}},
	}
	for i := 1; i <= 25; i++ {
		evs = append(evs, rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: fmt.Sprintf("f%02d", i), NodeType: rings.NodeFact,
			Title:  fmt.Sprintf("示例事实 %02d 号:路径 /p%02d 返回 %d", i, i, 200+i%3),
			Status: "confirmed"}})
	}
	evs = append(evs,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f99", NodeType: rings.NodeFact, Title: "未确认事实不应进简报", Status: "candidate"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-ok", NodeType: rings.NodeTestClaim, Title: "示例站 /api/user 未授权返回全部手机号",
			Status: "confirmed",
			Detail: map[string]any{"severity": "high", "summary": "漏", "standard": "s"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "tc-bad", NodeType: rings.NodeTestClaim, Title: "证据不足的不进简报",
			Status: "insufficient"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-run", NodeType: rings.NodeStep, Title: "在跑的意图示例", Status: "running"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-q", NodeType: rings.NodeStep, Title: "排队的意图示例", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-done", NodeType: rings.NodeStep, Title: "完成的意图不进在跑摘要", Status: "done"}},
	)
	if _, err := store.Append(ctx, task, evs...); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}
}

// TestP504BriefingAssembly ① 简报装配:锚例句逐字开头 + 概况 JSON 与
// 账本对账(事实总数/最新 20 截断/最新在前、确认漏洞、在跑排队意图),
// 未确认事实/证据不足漏洞/完成步骤不得混入。
func TestP504BriefingAssembly(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p504-brief"
	p504SeedBriefingLedger(t, store, task)

	got := assembleBriefing(ctx, store, task)
	if got == "" {
		t.Fatalf("简报不应为空")
	}
	if !strings.HasPrefix(got, briefingLead) {
		t.Fatalf("简报必须以锚例句逐字开头,得到前缀 %q", clipLine(got, 60))
	}
	raw := strings.TrimPrefix(got, briefingLead+"\n")
	var b briefingJSON
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("简报概况应为合法 JSON: %v\n%s", err, raw)
	}
	if b.Goal != "审计示例站敏感信息暴露" {
		t.Fatalf("goal 应为最新目标,得到 %q", b.Goal)
	}
	if b.ConfirmedFacts.Total != 25 {
		t.Fatalf("已确认事实总数应 25,得到 %d", b.ConfirmedFacts.Total)
	}
	if len(b.ConfirmedFacts.Latest) != briefingFactLatest {
		t.Fatalf("最新事实应截 %d 条,得到 %d", briefingFactLatest, len(b.ConfirmedFacts.Latest))
	}
	// 最新在前:f25 是最后落的确认事实,应排第一;f06 是第 20 新。
	if b.ConfirmedFacts.Latest[0].ID != "f25" || b.ConfirmedFacts.Latest[len(b.ConfirmedFacts.Latest)-1].ID != "f06" {
		t.Fatalf("事实摘要应最新在前:首=%s 尾=%s",
			b.ConfirmedFacts.Latest[0].ID, b.ConfirmedFacts.Latest[len(b.ConfirmedFacts.Latest)-1].ID)
	}
	if len(b.ConfirmedClaims) != 1 || b.ConfirmedClaims[0].ID != "tc-ok" || b.ConfirmedClaims[0].Severity != "high" {
		t.Fatalf("确认漏洞清单应恰含 tc-ok(带级别),得到 %+v", b.ConfirmedClaims)
	}
	states := map[string]string{}
	for _, it := range b.ActiveIntents {
		states[it.Step] = it.State
	}
	if states["s-run"] != "在跑" || states["s-q"] != "排队中" {
		t.Fatalf("在跑/排队意图摘要不符: %+v", b.ActiveIntents)
	}
	if _, hit := states["s-done"]; hit {
		t.Fatalf("完成步骤不得进在跑/排队摘要")
	}
	// 无 coverage 信号:不含资产覆盖/未测清单类字段。
	for _, banned := range []string{"asset", "coverage", "未测", "untested"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(banned)) {
			t.Fatalf("简报不得携带 coverage 类信号,命中 %q:\n%s", banned, raw)
		}
	}
	// 未确认事实与证据不足漏洞不得混入。
	if strings.Contains(raw, "f99") || strings.Contains(raw, "tc-bad") {
		t.Fatalf("未确认事实/证据不足漏洞不得进简报:\n%s", raw)
	}
}

// TestP504BriefingSilentDegrade ⑤ 装配失败静默降级:读失败返回空串,
// 首条消息退为纯任务包(单位照常开工,不阻塞不报错)。
func TestP504BriefingSilentDegrade(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p504-degrade"
	if got := assembleBriefing(context.Background(), nil, task); got != "" {
		t.Fatalf("无存储应降级为空简报,得到 %q", got)
	}
	// 已关闭的存储(读失败)同样降级为空串,不 panic。
	store.Close()
	if got := assembleBriefing(context.Background(), store, task); got != "" {
		t.Fatalf("读失败应静默降级为空简报,得到 %q", got)
	}
	// 首条消息退化形态:空简报 = 纯任务包(有简报 = 简报 + 任务包)。
	pack := &TaskPack{TaskID: "t", StepID: "s", StepIntent: "i"}
	plain := pack.FirstMessage()
	if !strings.HasPrefix(plain, "【任务包 · 七要素】") {
		t.Fatalf("空简报时首条消息应为纯任务包,前缀 %q", clipLine(plain, 40))
	}
	pack.Briefing = briefingLead + "\n{}"
	withBrief := pack.FirstMessage()
	if !strings.HasPrefix(withBrief, briefingLead) || !strings.Contains(withBrief, "【任务包 · 七要素】") {
		t.Fatalf("有简报时首条消息应为 简报+任务包")
	}
}

// TestP504DriveWithEmptyBriefingUnitRuns ⑤ 降级活体形态(脚本模型):
// 简报为空的包照常开工到终态,首条消息只有任务包、零报错。
func TestP504DriveWithEmptyBriefingUnitRuns(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p504-drive"
	seedGraph(t, store, task, "探测本地靶标并记一条事实")

	sm := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) > 0 && rr.LastUser != "" && strings.Contains(rr.LastUser, "【任务包 · 七要素】") {
			// 开工首条消息:简报已降级为空——断言锚例句不在场,随即交卷收尾。
			if strings.Contains(rr.LastUser, briefingLead) {
				t.Errorf("降级形态下首条消息不得含简报锚例句")
			}
			return scriptedReply{Text: "任务包收到,无简报照常开工;直接收尾。", Finish: "stop"}
		}
		return scriptedReply{Text: "收尾完成。", Finish: "stop"}
	})
	t.Cleanup(sm.Close)
	prov, err := sm.provider()
	if err != nil {
		t.Fatalf("脚本模型失败: %v", err)
	}
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	pack.Briefing = "" // 模拟装配失败(静默降级形态)
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("降级简报下单位应照常执行零报错: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("单位应正常收场,得到 %s(%s)", res.StepStatus, res.Reason)
	}
}

// p504Env 造一个绑定任务环境的单位(工具测试用)。
func p504Env(t *testing.T, store *rings.Store, task string) *unitEnv {
	t.Helper()
	dir := t.TempDir()
	return &unitEnv{
		Executor: NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{}),
		TaskID:   task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
	}
}

// mustCall 调一个工具并返回文本(失败即 fatal)。
func mustCall(t *testing.T, c tool.CoreTool, args string) string {
	t.Helper()
	res, err := c.Call(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("调用 %s 异常: %v", c.Name(), err)
	}
	if res.IsError {
		t.Fatalf("调用 %s 返回错误: %s", c.Name(), resultText(res))
	}
	return resultText(res)
}

// TestP504SearchFacts 查事实:关键词/资产两形态+交集+大小写不敏感+
// 未确认事实不出现+空参拒绝。
func TestP504SearchFacts(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p504-facts"
	seedGraph(t, store, task, "任意")
	p504Append(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-1", NodeType: rings.NodeAsset, Title: "api.example.com", Status: "active", AssetClass: "domain"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "fa", NodeType: rings.NodeFact, Title: "api.example.com /api/user 未授权返回手机号", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "fb", NodeType: rings.NodeFact, Title: "admin.example.com 后台弱口令可登录", Status: "confirmed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "fc", NodeType: rings.NodeFact, Title: "api.example.com /api/version 暴露版本号(未确认)", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "fa", ToNode: "a-1"}},
	)
	env := p504Env(t, store, task)

	// 关键词形态(大小写不敏感)。
	if got := mustCall(t, env.searchFactsTool(), `{"keyword":"手机号"}`); !strings.Contains(got, "fa") {
		t.Fatalf("关键词应命中 fa:\n%s", got)
	}
	// 资产形态(about 边)。
	if got := mustCall(t, env.searchFactsTool(), `{"asset_id":"a-1"}`); !strings.Contains(got, "fa") || strings.Contains(got, "fb") {
		t.Fatalf("资产过滤应恰含 fa:\n%s", got)
	}
	// 交集。
	if got := mustCall(t, env.searchFactsTool(), `{"keyword":"弱口令","asset_id":"a-1"}`); strings.Contains(got, "fa") {
		t.Fatalf("交集为空应如实空:\n%s", got)
	}
	// 未确认事实不出现。
	if got := mustCall(t, env.searchFactsTool(), `{"keyword":"version"}`); strings.Contains(got, "fc") {
		t.Fatalf("未确认事实不得出现在查询结果:\n%s", got)
	}
	// 空参拒绝(防全量倾倒)。
	if res, _ := env.searchFactsTool().Call(ctx, json.RawMessage(`{}`), nil); !res.IsError {
		t.Fatalf("空参应拒绝")
	}
}

// TestP504ListAssets 查资产:清单/关键词过滤/截断计数/不含覆盖进度。
func TestP504ListAssets(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p504-assets"
	p504Append(t, store, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-1", NodeType: rings.NodeAsset, Title: "api.example.com", Status: "active", AssetClass: "domain"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-2", NodeType: rings.NodeAsset, Title: "10.0.0.8:8080", Status: "active", AssetClass: "service"}},
	)
	env := p504Env(t, store, task)
	got := mustCall(t, env.listAssetsTool(), `{}`)
	if !strings.Contains(got, "a-1 [domain] api.example.com") || !strings.Contains(got, "a-2 [service] 10.0.0.8:8080") {
		t.Fatalf("资产清单行不符:\n%s", got)
	}
	if strings.Contains(got, "覆盖") {
		t.Fatalf("资产查询不给覆盖进度(coverage 信号):\n%s", got)
	}
	if got := mustCall(t, env.listAssetsTool(), `{"keyword":"8080"}`); !strings.Contains(got, "a-2") || strings.Contains(got, "a-1") {
		t.Fatalf("关键词过滤不符:\n%s", got)
	}
}

// TestP504SearchTrails 搜轨迹:命中工具调用(名/参数)与收场结论、
// 排除自己、空关键词拒绝。
func TestP504SearchTrails(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p504-trails"
	p504Append(t, store, task,
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u7", Payload: rings.SwarmRecord{
			StepID: "s7", UnitID: "u7", Type: rings.SwarmRecToolCall,
			ToolName: "http_probe", ToolArgs: json.RawMessage(`{"url":"http://t.example.com/robots.txt"}`)}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u8", Payload: rings.SwarmRecord{
			StepID: "s8", UnitID: "u8", Type: rings.SwarmRecSessionEnd,
			Note: "探测完成:robots.txt 暴露 /backup 目录,已记事实"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1", Payload: rings.SwarmRecord{
			StepID: "s1", UnitID: "u1", Type: rings.SwarmRecToolCall,
			ToolName: "http_probe", ToolArgs: json.RawMessage(`{"url":"http://t.example.com/self.html"}`)}},
	)
	env := p504Env(t, store, task) // 自己 = u1

	// 命中他人工具调用的参数。
	if got := mustCall(t, env.searchTrailsTool(), `{"keyword":"robots.txt"}`); !strings.Contains(got, "u7") || strings.Contains(got, "self.html") {
		t.Fatalf("轨迹应命中 u7 的调用且排除自己:\n%s", got)
	}
	// 命中他人收场结论。
	if got := mustCall(t, env.searchTrailsTool(), `{"keyword":"backup"}`); !strings.Contains(got, "u8") || !strings.Contains(got, "收场结论") {
		t.Fatalf("轨迹应命中 u8 的收场结论:\n%s", got)
	}
	// 空关键词拒绝。
	if res, _ := env.searchTrailsTool().Call(context.Background(), json.RawMessage(`{"keyword":" "}`), nil); !res.IsError {
		t.Fatalf("空关键词应拒绝")
	}
}

// p504SeedTraffic 铺一条带原文的镜留痕(真实 blob),返回所铺交换文档
//(对账用——渲染全文必须与工具返回同源)。
func p504SeedTraffic(t *testing.T, store *rings.Store, blobs *BlobStore, task, exID, method, url string, status int, respBody string) *mirror.Exchange {
	t.Helper()
	ex := mirror.Exchange{
		ID: exID, Type: rings.MirrorRecHTTP, TaskID: task, StepID: "s9", UnitID: "u9",
		Request: &mirror.Request{Method: method, URL: url, Proto: "HTTP/1.1",
			Headers: []mirror.Header{{Name: "User-Agent", Value: "probe/1"}}},
		Response: &mirror.Response{Proto: "HTTP/1.1", Status: status, StatusText: "OK",
			Headers: []mirror.Header{{Name: "Content-Type", Value: "text/plain"}}, Body: []byte(respBody)},
	}
	raw, err := json.Marshal(ex)
	if err != nil {
		t.Fatalf("编码交换文档失败: %v", err)
	}
	ref, err := blobs.Write(raw)
	if err != nil {
		t.Fatalf("写 blob 失败: %v", err)
	}
	p504Append(t, store, task, rings.Event{
		Kind: rings.EventMirrorRecord, Actor: "mirror",
		Payload: rings.MirrorRecord{
			ExchangeID: exID, Type: rings.MirrorRecHTTP, StepID: "s9", UnitID: "u9",
			Method: method, URL: url, Status: status, BlobRef: ref,
		}})
	return &ex
}

// TestP504TrafficTools 流量三件:防慢三规矩(host 必填/轻量摘要无正文/
// 原文按编号分段)+ 元数据 + 分段拼接同源。
func TestP504TrafficTools(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p504-traffic"
	blobs := NewBlobStore(filepath.Join(dir, "tasks", "blobs"))
	exA1 := p504SeedTraffic(t, store, blobs, task, "ms-p504a.1", "GET", "http://t.example.com/api/user?id=7", 200, `{"phone":"13912345678"}`)
	p504SeedTraffic(t, store, blobs, task, "ms-p504a.2", "POST", "http://t.example.com/api/login", 403, "denied")
	p504SeedTraffic(t, store, blobs, task, "ms-p504b.1", "GET", "http://other.example.net/x", 500, "boom")

	// 防慢规矩一:不限定 host 的裸搜不给做。
	if res, _ := TrafficSearchTool(store, task).Call(context.Background(), json.RawMessage(`{}`), nil); !res.IsError {
		t.Fatalf("无 host 的裸搜必须拒绝")
	}

	// 轻量摘要:命中行含编号/方法/URL/状态码,不含响应正文。
	got := mustCall(t, TrafficSearchTool(store, task), `{"host":"t.example.com"}`)
	for _, want := range []string{"ms-p504a.1", "ms-p504a.2", "GET", "POST", "403"} {
		if !strings.Contains(got, want) {
			t.Fatalf("流量摘要缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "13912345678") || strings.Contains(got, "denied") {
		t.Fatalf("摘要不得带正文:\n%s", got)
	}

	// method/status 过滤。
	if got := mustCall(t, TrafficSearchTool(store, task), `{"host":"t.example.com","method":"POST"}`); !strings.Contains(got, "ms-p504a.2") || strings.Contains(got, "ms-p504a.1") {
		t.Fatalf("method 过滤不符:\n%s", got)
	}
	if got := mustCall(t, TrafficSearchTool(store, task), `{"host":"example","status":5}`); !strings.Contains(got, "ms-p504b.1") || strings.Contains(got, "ms-p504a.1") {
		t.Fatalf("status 过滤不符:\n%s", got)
	}

	// 元数据:无正文,带总长。
	got = mustCall(t, TrafficGetTool(store, blobs, task), `{"id":"ms-p504a.1"}`)
	for _, want := range []string{"GET http://t.example.com/api/user?id=7 status=200", "raw_total="} {
		if !strings.Contains(got, want) {
			t.Fatalf("元数据缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "13912345678") {
		t.Fatalf("元数据不得带正文:\n%s", got)
	}

	// 原文分段:按编号单取,分段拼接 == 渲染全文(与面板同源),side 单侧。
	full, _ := mirror.RenderExchange(exA1, 0)
	if got := mustCall(t, TrafficBlobTool(store, blobs, task), `{"id":"ms-p504a.1","offset":0,"limit":4096}`); !strings.Contains(got, full) {
		t.Fatalf("首段应覆盖全文(总长 %d):\n%s", len(full), got)
	}
	// 逐段拼接 == 全文。
	var joined strings.Builder
	off := 0
	for {
		seg := mustCall(t, TrafficBlobTool(store, blobs, task), fmt.Sprintf(`{"id":"ms-p504a.1","offset":%d,"limit":13}`, off))
		marker := fmt.Sprintf("--- ms-p504a.1 原文(offset=%d, 本段 ", off)
		if !strings.Contains(seg, marker) {
			t.Fatalf("段头不符:\n%s", seg)
		}
		body := seg[strings.Index(seg, "---\n")+4:]
		if i := strings.Index(body, "\n[未完:"); i >= 0 {
			body = body[:i]
		}
		joined.WriteString(body)
		if !strings.Contains(seg, "[未完:") {
			break
		}
		off += 13
		if off > len(full)+13 {
			t.Fatalf("分段读取未收敛")
		}
	}
	if joined.String() != full {
		t.Fatalf("分段拼接应与渲染全文逐字一致:\n拼接=%q\n全文=%q", joined.String(), full)
	}
	// side=resp 只含响应侧。
	if got := mustCall(t, TrafficBlobTool(store, blobs, task), `{"id":"ms-p504a.1","side":"resp"}`); strings.Contains(got, "GET /api/user") {
		t.Fatalf("resp 侧不得含请求行:\n%s", got)
	}
	// 查无编号如实报错(不 panic 不阻塞)。
	if res, _ := TrafficBlobTool(store, blobs, task).Call(context.Background(), json.RawMessage(`{"id":"ms-none.9"}`), nil); !res.IsError {
		t.Fatalf("查无编号应报错")
	}
}

// TestP504Registration 注册面:六个新工具名入 knownToolScopes(硬规则
// 误拦防线,P5-01③b 同款验证)与 ToolCatalog(插话快照目录),toolset
// 全集含五件能力;正常参数不命中硬规则。
func TestP504Registration(t *testing.T) {
	for _, name := range []string{"search_facts", "list_assets", "search_trails", "traffic_search", "traffic_get", "traffic_blob"} {
		if !knownToolScopes[name] {
			t.Fatalf("%s 必须登记受控入口清单,否则硬规则适用工具校验误拒", name)
		}
	}
	catalog := map[string]bool{}
	for _, e := range ToolCatalog() {
		catalog[e.Name] = true
	}
	for _, name := range []string{"search_facts", "list_assets", "search_trails", "traffic_search", "traffic_get", "traffic_blob", "list_claims"} {
		if !catalog[name] {
			t.Fatalf("工具目录缺 %s", name)
		}
	}
	// toolset 全集(非复测)含五件能力。
	dir := t.TempDir()
	store := openStore(t, dir)
	env := p504Env(t, store, "p504-reg")
	names := map[string]bool{}
	for _, c := range env.toolset() {
		names[c.Name()] = true
	}
	for _, name := range []string{"list_claims", "search_facts", "list_assets", "search_trails", "traffic_search", "traffic_get", "traffic_blob"} {
		if !names[name] {
			t.Fatalf("toolset 缺 %s", name)
		}
	}
	// 复测单位走受限集,不带知情查询(retestToolset 不受影响)。
	env.retest = &retestBinding{ClaimID: "tc-x", Round: 1}
	for _, c := range env.toolset() {
		for _, banned := range []string{"search_facts", "search_trails", "traffic_search"} {
			if c.Name() == banned {
				t.Fatalf("复测受限集不得带 %s", banned)
			}
		}
	}
	// 正常参数零命中硬规则(P5-01③b 教训:新工具被误拦的验证)。
	dir2 := t.TempDir()
	store2 := openStore(t, dir2)
	normalCalls := map[string]string{
		"search_facts":    `{"keyword":"admin"}`,
		"list_assets":     `{"keyword":"example.com"}`,
		"search_trails":   `{"keyword":"robots.txt"}`,
		"traffic_search":  `{"host":"t.example.com"}`,
		"traffic_get":     `{"id":"ms-x.1"}`,
		"traffic_blob":    `{"id":"ms-x.1","offset":0}`,
		"list_claims":     `{}`,
		"submit_claim":    `{"summary":"x","severity":"high"}`,
	}
	for name, args := range normalCalls {
		if r, m, hit := matchingHardRule(t, context.Background(), store2, name, json.RawMessage(args)); hit {
			t.Fatalf("正常参数不得命中硬规则: %s 命中 %s(%s)", name, r.Name, m)
		}
	}
}

// p504Append 测试铺账助手。
func p504Append(t *testing.T, store *rings.Store, task string, evs ...rings.Event) {
	t.Helper()
	if _, err := store.Append(context.Background(), task, evs...); err != nil {
		t.Fatalf("铺账失败: %v", err)
	}
}
