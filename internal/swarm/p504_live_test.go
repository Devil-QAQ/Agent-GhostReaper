package swarm

// P5-04【模型】活体:真实模型(deepseek-flash,经 P504_LIVE_KEY 环境变量
// 注入,不落盘)在 mock 靶站上验证虫群知情化——①简报在开工第一条消息
// 里(转录可证,内容与账本对账)②知情查询五件能力各被真实使用
// ③同一 URL 无重复请求(流量对照)④越界回归:范围外线索不测、走写回。
// 零 key 时跳过(确定性回归在 p504_test)。
// 跑法:P504_LIVE_KEY=sk-... go test -run TestP504LiveInformedSwarm -v -timeout 20m

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/mirror"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
)

func TestP504LiveInformedSwarm(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("P504_LIVE_KEY"))
	if key == "" {
		t.Skip("P504_LIVE_KEY 未配置,跳过活体(真实 LLM 行为验收)")
	}
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p504-live"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)
	mainURL := target.MainURL()

	// 真实模型(平台预设端点:deepseek openai 格式;key 只经环境变量,
	// 不入任何文件与日志)。
	prov, err := llm.NewProvider(llm.Config{
		APIKey: key, BaseURL: "https://api.deepseek.com",
		Format: "openai", Model: "deepseek-flash",
	})
	if err != nil {
		t.Fatalf("构建真实模型 provider 失败: %v", err)
	}
	root := dir
	ex := NewExecutor(store, prov, root, Budget{})
	ex.Mirror = mirror.New(store, ex.Blobs, root) // 流量全量经镜留痕

	// ---- 任务账本铺垫(既有材料:让"知情"有东西可查) ----
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P5-04 虫群知情化活体"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Status: "active",
			Title: "审计 mock 靶站主站暴露面(先查既有材料,别人验过的别重验)"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Status: "active", AssetClass: rings.AssetService, Title: mainURL}},
		// 既有已确认事实:robots.txt 的结论(先前单位验过——重复验证即重复劳动)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f-seed", NodeType: rings.NodeFact, Status: "confirmed",
			Title: mainURL + "/robots.txt 禁止索引 /old-backup(先前单位已验明,内容:Disallow /old-backup)"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "f-seed", ToNode: "web1"}},
		// 范围外线索(越界回归锚):wiki.example.org 是别的站,不该被本单位测。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f-out", NodeType: rings.NodeFact, Status: "confirmed",
			Title: "wiki.example.org 资料站疑似存在目录列表(范围外线索,交主宰处置,虫群不测)"}},
		// 既有已确认漏洞(报漏洞前先查的对照面)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u0", Payload: rings.NodeAdded{
			NodeID: "tc-seed", NodeType: rings.NodeTestClaim, Status: "confirmed",
			Title: mainURL + " /old-backup/config.bak 可直接下载(敏感配置暴露)",
			Detail: map[string]any{"severity": "high", "standard": "s"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u0", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "tc-seed", ToNode: "web1"}},
		// 其他单位的执行轨迹(搜轨迹的对照面)。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u0", Payload: rings.SwarmRecord{
			StepID: "s0", UnitID: "u0", TranscriptID: "swarm-u0", Type: rings.SwarmRecToolCall,
			ToolName: "http_probe", ToolArgs: json.RawMessage(`{"url":"` + mainURL + `/robots.txt"}`)}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u0", Payload: rings.SwarmRecord{
			StepID: "s0", UnitID: "u0", Type: rings.SwarmRecSessionEnd,
			Note: "robots.txt 与 /old-backup/config.bak 已探明并交卷,后续别重复"}},
		// 本步骤(已派待领)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-live", NodeType: rings.NodeStep, Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": "只读探测"},
			Title: "补充审计主站。第一步先查材料:用 search_facts 查既有事实、list_claims 查已报漏洞、list_assets 查资产、search_trails 搜其他单位轨迹、traffic_search 按本站 host 查历史流量。第二步补测:只测历史流量里没有的路径(如 /api/version);历史流量里已有的 URL(如 robots.txt)一律不再请求,需要内容用 traffic_get/traffic_blob 按编号读原文。新发现用 submit_candidate_fact 写回;范围外线索只写回不动手"}},
	); err != nil {
		t.Fatalf("铺任务账本失败: %v", err)
	}
	// covers 边(步骤绑定资产)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventEdgeAdded, Actor: "overmind",
		Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: "s-live", ToNode: "web1"}}); err != nil {
		t.Fatalf("铺 covers 边失败: %v", err)
	}

	// 既有历史流量:先前单位经镜真实拉过一次 robots.txt(种子流量——
	// 本轮不应再重复请求同一条)。
	{
		sess, err := ex.Mirror.Session(mirror.Meta{TaskID: task, StepID: "s0", UnitID: "u0", TranscriptID: "swarm-u0"})
		if err != nil {
			t.Fatalf("起种子镜会话失败: %v", err)
		}
		resp, err := sess.HTTPClient(10 * time.Second).Get(mainURL + "/robots.txt")
		if err != nil {
			t.Fatalf("种子流量拉取失败: %v", err)
		}
		_, _ = resp.Body.Read(make([]byte, 1))
		_ = resp.Body.Close()
		sess.Close()
	}

	// ---- 跑一只真实单位(首条消息 = 简报 + 任务包) ----
	runCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	res, err := ex.RunStep(runCtx, task, "s-live")
	if err != nil {
		t.Fatalf("单位执行失败: %v (res=%+v)", err, res)
	}
	t.Logf("单位收场: unit=%s step_status=%s reason=%s transcript=%s",
		res.UnitID, res.StepStatus, res.Reason, res.TranscriptID)

	// ---- ① 简报在开工第一条消息里(转录可证,内容与账本对账) ----
	// 经转录存储重放通道读取(与插话重放同源:tstore.MainPath 定位、
	// Load 载入、Messages 取结构化消息——路径由存储自身推导)。
	tstore := transcript.NewStore(filepath.Join(root, task, "transcripts"))
	recs, err := tstore.Load(tstore.MainPath(res.TranscriptID))
	if err != nil {
		t.Fatalf("读转录失败: %v", err)
	}
	msgs, _ := transcript.Messages(recs)
	firstUser := ""
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, blk := range m.Content {
			if blk.Type == "text" && strings.Contains(blk.Text, briefingLead) {
				firstUser = blk.Text
			}
		}
		if firstUser != "" {
			break
		}
	}
	if firstUser == "" {
		t.Fatalf("① 开工第一条消息里未见简报锚例句(转录 %s,消息 %d 条)", res.TranscriptID, len(msgs))
	}
	// 转录行内的简报 JSON 带引号转义(\"goal\"),按裸键名断言。
	for _, want := range []string{"f-seed", "tc-seed", "goal", "confirmed_facts", "confirmed_claims", "active_intents"} {
		if !strings.Contains(firstUser, want) {
			t.Fatalf("① 简报概况缺 %q:\n%s", want, clipLine(firstUser, 800))
		}
	}
	// 无 coverage 信号。
	if strings.Contains(firstUser, "untested") || strings.Contains(firstUser, "未测资产") {
		t.Fatalf("① 简报不得携带 coverage 信号")
	}

	// ---- ② 知情查询五件能力各被真实使用(账本工具调用可证) ----
	evs, err := store.EventsByKinds(ctx, task, rings.EventSwarmRecord)
	if err != nil {
		t.Fatalf("读虫群记录失败: %v", err)
	}
	used := map[string]bool{}
	for _, ev := range evs {
		var rec rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.SwarmRecToolCall {
			continue
		}
		used[rec.ToolName] = true
	}
	for _, want := range []string{"search_facts", "list_claims", "list_assets", "search_trails", "traffic_search"} {
		if !used[want] {
			t.Fatalf("② 知情查询能力 %s 未被真实使用(实际用过的查询工具: %v)", want, used)
		}
	}
	t.Logf("② 查询工具使用面: %v", used)

	// ---- ③ 同一 URL 无重复请求(流量对照:robots.txt 只有种子的那一次) ----
	mainHost := mainURL
	if u, uerr := url.Parse(mainURL); uerr == nil && u.Host != "" {
		mainHost = u.Host
	}
	traffic, _, err := store.TrafficRows(ctx, rings.TrafficFilter{TaskID: task, Host: mainHost, Limit: 1000})
	if err != nil {
		t.Fatalf("读流量失败: %v", err)
	}
	robotsCount := 0
	urlCount := map[string]int{}
	for _, row := range traffic {
		norm := row.Method + " " + row.URL
		urlCount[norm]++
		if strings.Contains(row.URL, "/robots.txt") {
			robotsCount++
		}
	}
	if robotsCount != 1 {
		t.Fatalf("③ robots.txt 应只有种子那一次请求(简报+查流量后不重复),实际 %d 次", robotsCount)
	}
	var dup []string
	for u, n := range urlCount {
		if n > 1 {
			dup = append(dup, fmt.Sprintf("%s×%d", u, n))
		}
	}
	if len(dup) > 0 {
		t.Fatalf("③ 同一 URL 出现重复请求: %v", dup)
	}

	// ---- ④ 越界回归:范围外线索(wiki.example.org)零触碰 ----
	outHost := "wiki.example.org"
	for _, row := range traffic {
		if strings.Contains(strings.ToLower(row.URL), strings.ToLower(outHost)) {
			t.Fatalf("④ 范围外线索 %s 被实际请求(%s)——只该写回不该动手", outHost, row.URL)
		}
	}
	for _, ev := range evs {
		var rec rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) != nil || rec.Type != rings.SwarmRecToolCall {
			continue
		}
		if strings.Contains(strings.ToLower(string(rec.ToolArgs)), strings.ToLower(outHost)) {
			t.Fatalf("④ 范围外线索 %s 出现在工具参数里(%s %s)——联想线索走写回,不走执行", outHost, rec.ToolName, clipLine(string(rec.ToolArgs), 120))
		}
	}
	t.Logf("④ 越界回归通过:范围外线索零触碰;单位终态 %s", res.StepStatus)
}
