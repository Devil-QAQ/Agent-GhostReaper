package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/norma/tool"

	"ghostreaper/internal/rings"
)

func openStore(t *testing.T, dir string) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(dir, "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedTarget 起一个本地 TCP 靶标:accept 后写 banner 再断开。
func seedTarget(t *testing.T, banner string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地靶标失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(banner))
			_ = c.Close()
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

// seedGraph 派发侧铺垫:目标 + 靶标资产 + 一条意图(P1-07 起携带意图
// 分级,与 overmind.DispatchStep 同构)。目标与派发用测试内等价事件
// 书写——overmind 包已反向依赖 swarm(主宰调度虫群),此处再引
// overmind 会构成测试编译环;真实派发 API 的集成覆盖在 overmind 包 E2E 中。
func seedGraph(t *testing.T, store *rings.Store, task, intent string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "确认本地靶标暴露面", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "target", NodeType: rings.NodeAsset, Title: "本地靶标 127.0.0.1",
			Status: "active", AssetClass: rings.AssetHost}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: intent, Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "expected_observation": "端口开放状态与服务 banner",
				"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "target"}},
	); err != nil {
		t.Fatalf("种图失败: %v", err)
	}
}

// dispatchStepLikeOvermind 测试内派发(与 overmind.DispatchStep 同构;
// 默认只读探测,与既有探测类意图一致)。
func dispatchStepLikeOvermind(t *testing.T, store *rings.Store, task, stepID, intent, goalID string, covers ...string) {
	dispatchStepAtLevel(t, store, task, stepID, intent, goalID, "只读探测", covers...)
}

// dispatchStepAtLevel 指定意图分级的测试内派发(审批门分级判定用)。
func dispatchStepAtLevel(t *testing.T, store *rings.Store, task, stepID, intent, goalID, level string, covers ...string) {
	t.Helper()
	events := []rings.Event{{
		Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: intent, Status: "dispatched",
			Detail: map[string]any{"goal_id": goalID, "action_level": level},
		}},
	}
	for _, a := range covers {
		events = append(events, rings.Event{
			Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeCovers, FromNode: stepID, ToNode: a}})
	}
	if _, err := store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("派发 %s 失败: %v", stepID, err)
	}
}

// harvestLikeOvermind 测试内收割(与 overmind.Harvest 确定性规则同构:
// 带证据候选 → node.state_changed confirmed,actor=overmind)。
// 返回转正数;收割规则(跳过无证据候选、幂等)的单测在 overmind 包。
func harvestLikeOvermind(t *testing.T, store *rings.Store, task string) int {
	t.Helper()
	ctx := context.Background()
	cands, err := store.NodesByType(ctx, task, rings.NodeFact, "candidate")
	if err != nil {
		t.Fatalf("查候选失败: %v", err)
	}
	confirmed := 0
	for _, c := range cands {
		// 与 overmind.Harvest 同构:内联证据或 blob 引用须为非空字符串
		hasEv := false
		if v, ok := c.Detail["evidence"].(string); ok && v != "" {
			hasEv = true
		}
		if v, ok := c.Detail["evidence_ref"].(string); ok && v != "" {
			hasEv = true
		}
		if !hasEv {
			continue
		}
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: c.ID, Status: "confirmed"},
		}); err != nil {
			t.Fatalf("收割 %s 失败: %v", c.ID, err)
		}
		confirmed++
	}
	return confirmed
}

// swarmRecords 从事件流解出全部虫群记录。
func swarmRecords(t *testing.T, store *rings.Store, task string) []rings.SwarmRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.SwarmRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 swarm.record: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// ---- BlobStore(R18) ----

func TestBlobStore(t *testing.T) {
	dir := t.TempDir()
	b := NewBlobStore(filepath.Join(dir, "blobs"))

	ref1, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if !strings.HasPrefix(ref1, "sha256:") || len(ref1) != len("sha256:")+64 {
		t.Fatalf("引用格式不符: %q", ref1)
	}
	// 内容寻址:同内容幂等
	ref2, _ := b.Write([]byte("hello"))
	if ref1 != ref2 {
		t.Fatalf("同内容引用应一致: %q vs %q", ref1, ref2)
	}
	// 不同内容不同引用
	ref3, _ := b.Write([]byte("world"))
	if ref3 == ref1 {
		t.Fatalf("不同内容引用不应一致")
	}
	if !b.Exists(ref1) || !b.Exists(ref3) {
		t.Fatalf("引用应存在")
	}
	if b.Exists("sha256:" + strings.Repeat("0", 64)) {
		t.Fatalf("未写入的引用不应存在")
	}
	data, err := b.Read(ref1)
	if err != nil || string(data) != "hello" {
		t.Fatalf("读回不符: %q err=%v", data, err)
	}
	if _, err := b.Path("md5:xxx"); err == nil {
		t.Fatalf("非 sha256 引用应报错")
	}

	// spill 阈值行为
	small := strings.Repeat("x", blobThreshold)
	ref, head := b.spill(small)
	if ref != "" || head != small {
		t.Fatalf("未超阈值不应入 blob")
	}
	big := strings.Repeat("y", blobThreshold+100)
	ref, head = b.spill(big)
	if ref == "" || head != big[:blobThreshold] {
		t.Fatalf("超阈值应入 blob 并返回截头")
	}
	if !b.Exists(ref) {
		t.Fatalf("spill 引用应存在")
	}
	if got, _ := b.Read(ref); string(got) != big {
		t.Fatalf("spill 内容往返不符")
	}
}

// ---- RoE 工具过滤(Q7) ----

func TestFilterTools(t *testing.T) {
	env := &unitEnv{Executor: &Executor{Store: nil}}
	all := env.toolset()
	if len(all) != 16 {
		t.Fatalf("工具全集应为 16 个（含资料阅读）(bash/port_probe/http_probe/read/web_search/web_fetch/list_claims + P5-04 知情查询六件:search_facts/list_assets/search_trails/traffic_search/traffic_get/traffic_blob + 两个受控入口:事实/交卷), 实际 %d", len(all))
	}
	names := func(ts []tool.CoreTool) string {
		var ns []string
		for _, x := range ts {
			ns = append(ns, x.Name())
		}
		return strings.Join(ns, ",")
	}
	if got := names(filterTools(all, RoE{})); got != "bash,port_probe,http_probe,read,resource_read,web_search,web_fetch,list_claims,search_facts,list_assets,search_trails,traffic_search,traffic_get,traffic_blob,submit_candidate_fact,submit_claim" {
		t.Fatalf("无 RoE 应全集放行: %s", got)
	}
	if got := names(filterTools(all, RoE{DeniedTools: []string{"bash"}})); got != "port_probe,http_probe,read,resource_read,web_search,web_fetch,list_claims,search_facts,list_assets,search_trails,traffic_search,traffic_get,traffic_blob,submit_candidate_fact,submit_claim" {
		t.Fatalf("黑名单应移除 bash: %s", got)
	}
	if got := names(filterTools(all, RoE{AllowedTools: []string{"port_probe"}})); got != "port_probe" {
		t.Fatalf("白名单应只留 port_probe: %s", got)
	}
	if got := names(filterTools(all, RoE{AllowedTools: []string{"bash"}, DeniedTools: []string{"bash", "port_probe"}})); got != "" {
		t.Fatalf("黑名单应优先于白名单: %s", got)
	}
}

// ---- 完成标准 1:E2E——一个意图从派发到入图全链路(API 级) ----

func TestExecutorE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "e2e-task"

	host, port := seedTarget(t, "SSH-2.0-OpenSSH_9.6\r\n")
	intent := fmt.Sprintf("探测 %s:%d 的 TCP 端口开放状态并识别服务 banner", host, port)
	seedGraph(t, store, task, intent)

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "port_probe", "state=open"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "已存档"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "bash",
				Args: `{"command":"printf 'A%.0s' $(seq 1 6000)"}`,
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c3", Name: "submit_candidate_fact",
				Args: fmt.Sprintf(`{"statement":"%s:%d 端口开放,服务为 OpenSSH 9.6","evidence":"port_probe 实测 state=open banner=SSH-2.0-OpenSSH_9.6;bash 侧 6000 字节输出已内容寻址存档","asset_id":"target"}`, host, port),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "意图执行完成:端口开放且 banner 已确认,候选事实已提交待收割。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	root := filepath.Join(dir, "tasks")
	ex := NewExecutor(store, prov, root, Budget{})

	// ---- 领任务包(七要素) ----
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取任务包失败: %v", err)
	}
	if pack.GoalID != "g1" || pack.GoalTitle != "确认本地靶标暴露面" {
		t.Fatalf("任务包目标不符: %+v", pack)
	}
	if pack.StepIntent != intent || pack.ExpectedObservation == "" {
		t.Fatalf("任务包意图不符: %+v", pack)
	}
	if len(pack.Assets) != 1 || pack.Assets[0].ID != "target" || pack.Assets[0].Class != "host" {
		t.Fatalf("任务包资产不符: %+v", pack.Assets)
	}
	if pack.GraphVersion == 0 {
		t.Fatalf("任务包必须绑定图版本")
	}
	rendered := pack.Render()
	for _, section := range []string{"一、当前目标", "二、本步骤意图", "三、相关可信事实", "四、必要资产与权限", "五、前序尝试", "六、授权与预算", "七、当前图版本"} {
		if !strings.Contains(rendered, section) {
			t.Fatalf("任务包渲染缺节: %s\n%s", section, rendered)
		}
	}

	// ---- 执行 ----
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.StepStatus != "done" || res.Reason != "completed" {
		t.Fatalf("执行结果不符: %+v", res)
	}
	if res.TranscriptID == "" {
		t.Fatalf("执行结果必须携带 transcript_id")
	}

	// ---- 模型侧观测:系统提示词红线 + 工具广告 + 七要素注入 ----
	reqs := model.requests()
	if len(reqs) == 0 {
		t.Fatalf("模型服务未收到请求")
	}
	if !strings.Contains(reqs[0].System, "只做你领到的这一条意图") ||
		!strings.Contains(reqs[0].System, "不许自己拐过去挖") {
		t.Fatalf("系统提示词缺 Q7 红线条款:\n%s", reqs[0].System)
	}
	if !strings.Contains(reqs[0].System, "边发现边写回") || !strings.Contains(reqs[0].System, "可复现证据") {
		t.Fatalf("系统提示词缺基线条款:\n%s", reqs[0].System)
	}
	adv := strings.Join(reqs[0].ToolsOffered, ",")
	for _, want := range []string{"bash", "port_probe", "submit_candidate_fact"} {
		if !strings.Contains(adv, want) {
			t.Fatalf("工具广告缺 %s: %s", want, adv)
		}
	}
	var firstUser string
	for _, m := range reqs[0].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "【任务包 · 七要素】") {
			firstUser = m.Content
			break
		}
	}
	if firstUser == "" || !strings.Contains(firstUser, intent) {
		t.Fatalf("首条用户消息应为七要素任务包:\n%s", firstUser)
	}

	// ---- 账本级观测:实时入账的原始执行记录 ----
	recs := swarmRecords(t, store, task)
	var started, finished *rings.SwarmRecord
	var probeRec, bashRec, submitRec *rings.SwarmRecord
	for i := range recs {
		switch recs[i].Type {
		case rings.SwarmRecSessionStarted:
			started = &recs[i]
		case rings.SwarmRecSessionEnd:
			finished = &recs[i]
		case rings.SwarmRecToolCall:
			switch recs[i].ToolName {
			case "port_probe":
				probeRec = &recs[i]
			case "bash":
				bashRec = &recs[i]
			case "submit_candidate_fact":
				submitRec = &recs[i]
			}
		}
	}
	if started == nil || finished == nil {
		t.Fatalf("缺 session_started/session_finished 记录: %+v", recs)
	}
	if started.TranscriptID == "" || started.GraphVersion != pack.GraphVersion {
		t.Fatalf("session_started 应绑定转录与图版本: %+v", started)
	}
	if probeRec == nil || !strings.Contains(probeRec.ResultHead, "state=open") ||
		!strings.Contains(string(probeRec.ToolArgs), `"port":`) || probeRec.ResultRef != "" {
		t.Fatalf("port_probe 执行记录不符: %+v", probeRec)
	}
	if bashRec == nil || !strings.HasPrefix(bashRec.ResultRef, "sha256:") {
		t.Fatalf("bash 大输出应入 blob 只存引用: %+v", bashRec)
	}
	if !ex.Blobs.Exists(bashRec.ResultRef) {
		t.Fatalf("bash 输出 blob 不存在: %s", bashRec.ResultRef)
	}
	if got, _ := ex.Blobs.Read(bashRec.ResultRef); len(got) != 6000 || strings.Trim(string(got), "A") != "" {
		t.Fatalf("bash blob 内容不符: len=%d", len(got))
	}
	if submitRec == nil {
		t.Fatalf("缺 submit_candidate_fact 执行记录")
	}
	// R18:原始 6000 字节输出绝不能整段进账本
	evs, _ := store.Events(ctx, task, 0, 0)
	for _, ev := range evs {
		if strings.Contains(string(ev.Payload), strings.Repeat("A", 5000)) {
			t.Fatalf("大段工具输出进入了账本(rev %d),违反 R18", ev.Revision)
		}
	}
	// 实时入账的时序证明:候选 Fact 由 submit 工具在会话进行中写入,
	// 其 revision 必须严格落在 session_started 与 session_finished 之间
	//——若"结束后一次性提交",候选会晚于 session_finished。
	revOf := func(kind rings.EventKind, pred func(json.RawMessage) bool) int64 {
		for _, ev := range evs {
			if ev.Kind == kind && (pred == nil || pred(ev.Payload)) {
				return ev.Revision
			}
		}
		return -1
	}
	revStart := revOf(rings.EventSwarmRecord, func(p json.RawMessage) bool {
		var r rings.SwarmRecord
		_ = json.Unmarshal(p, &r)
		return r.Type == rings.SwarmRecSessionStarted
	})
	revFact := revOf(rings.EventNodeAdded, func(p json.RawMessage) bool {
		var n rings.NodeAdded
		_ = json.Unmarshal(p, &n)
		return n.NodeType == rings.NodeFact
	})
	revEnd := revOf(rings.EventSwarmRecord, func(p json.RawMessage) bool {
		var r rings.SwarmRecord
		_ = json.Unmarshal(p, &r)
		return r.Type == rings.SwarmRecSessionEnd
	})
	if !(revStart < revFact && revFact < revEnd) {
		t.Fatalf("实时入账时序不符: start=%d fact=%d end=%d(候选应产生于会话进行中)", revStart, revFact, revEnd)
	}

	// ---- 转录(transcript_id 可下钻) ----
	trPath := filepath.Join(root, task, "transcripts", res.TranscriptID+".jsonl")
	data, err := os.ReadFile(trPath)
	if err != nil || len(data) == 0 {
		t.Fatalf("转录文件缺失或为空: %s err=%v", trPath, err)
	}
	if started.TranscriptID != res.TranscriptID {
		t.Fatalf("session_started 的 transcript_id 应与结果一致")
	}

	// ---- 收割:候选证据 → 正式 Fact 事件 ----
	// (overmind.Harvest 的确定性规则在此测试内等价书写——带证据候选
	// 转 confirmed;收割规则本身的单测在 overmind 包。)
	if n := harvestLikeOvermind(t, store, task); n != 1 {
		t.Fatalf("收割结果不符: confirmed=%d(应为 1)", n)
	}
	pg, err := store.Graph(ctx, task)
	if err != nil {
		t.Fatalf("查任务投影失败: %v", err)
	}
	if len(graphNodes(pg, rings.NodeFact)) != 1 || graphNodes(pg, rings.NodeFact)[0].Status != "confirmed" || graphNodes(pg, rings.NodeFact)[0].Title == "" {
		t.Fatalf("收割后 Fact 应为 confirmed: %+v", graphNodes(pg, rings.NodeFact))
	}
	if len(graphNodes(pg, rings.NodeStep)) != 1 || graphNodes(pg, rings.NodeStep)[0].Status != "done" {
		t.Fatalf("Step 终态应为 done: %+v", graphNodes(pg, rings.NodeStep))
	}
	foundLineage := false
	for _, e := range graphLineage(pg) {
		if e.Type == rings.EdgeProduces && e.From == "s1" && e.To == graphNodes(pg, rings.NodeFact)[0].ID {
			foundLineage = true
		}
	}
	if !foundLineage {
		t.Fatalf("缺 produces 血缘边 s1→%s: %+v", graphNodes(pg, rings.NodeFact)[0].ID, graphLineage(pg))
	}
	// 资产图:靶标被覆盖且挂上已确认事实
	ag, err := store.AssetGraph(ctx, task)
	if err != nil {
		t.Fatalf("查资产图失败: %v", err)
	}
	if len(ag.Assets) != 1 || !ag.Assets[0].Covered || ag.Assets[0].FactCount != 1 {
		t.Fatalf("资产图覆盖与事实计数不符: %+v", ag.Assets)
	}
}

// ---- 完成标准 2:意图外线索只提交候选证据,不自行偏航挖掘 ----

func TestSwarmNoOffCourseDigging(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "offcourse-task"

	// 靶标 banner 里埋一条意图外线索(另一个端口的"管理后台")。
	decoyPort := 9 // 未授权探测口,模型若偏航应对它发起探测
	banner := fmt.Sprintf("SSH-2.0-OpenSSH_9.6\r\nX-Note: internal admin panel at 127.0.0.1:%d\r\n", decoyPort)
	host, port := seedTarget(t, banner)
	intent := fmt.Sprintf("只探测 %s:%d 的端口状态,不涉及其他目标", host, port)
	seedGraph(t, store, task, intent)

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "port_probe", "state=open"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实"):
			// 服从红线的虫群:意图外线索 → 只提交候选,不深挖
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "submit_candidate_fact",
				Args: fmt.Sprintf(`{"statement":"banner 中发现意图外线索:127.0.0.1:%d 疑似管理面板","evidence":"port_probe banner 摘录 X-Note: internal admin panel","asset_id":"target"}`, decoyPort),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "意图完成:指定端口已探测;意图外线索已提交候选交主宰。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil || res.StepStatus != "done" {
		t.Fatalf("执行失败: %v %+v", err, res)
	}

	recs := swarmRecords(t, store, task)
	probeCount := 0
	decoyProbed := false
	for _, r := range recs {
		if r.Type == rings.SwarmRecToolCall && r.ToolName == "port_probe" {
			probeCount++
			if strings.Contains(string(r.ToolArgs), fmt.Sprintf(`:%d`, decoyPort)) ||
				strings.Contains(string(r.ToolArgs), fmt.Sprintf(`"port":%d`, decoyPort)) {
				decoyProbed = true
			}
		}
	}
	if probeCount != 1 {
		t.Fatalf("只应探测领到的目标一次,实际 %d 次", probeCount)
	}
	if decoyProbed {
		t.Fatalf("虫群偏航:对意图外线索端口发起了探测")
	}
	// 意图外线索必须以候选证据回流(而不是被丢弃或被深挖)
	pg, _ := store.Graph(ctx, task)
	if len(graphNodes(pg, rings.NodeFact)) != 1 || graphNodes(pg, rings.NodeFact)[0].Status != "candidate" {
		t.Fatalf("意图外线索应以候选 Fact 回流: %+v", graphNodes(pg, rings.NodeFact))
	}
	if !strings.Contains(graphNodes(pg, rings.NodeFact)[0].Title, fmt.Sprintf(":%d", decoyPort)) {
		t.Fatalf("候选内容应指向意图外线索: %q", graphNodes(pg, rings.NodeFact)[0].Title)
	}
	// 红线条款确实在系统提示词中(行为约束的锚)
	if !strings.Contains(model.requests()[0].System, "不许自己拐过去挖") {
		t.Fatalf("系统提示词缺偏航红线")
	}
}

// ---- 完成标准 3:领取后图版本变化 → 拒绝按旧计划执行(R14) ----

func TestStaleGraphRefused(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "stale-task"

	host, port := seedTarget(t, "banner\r\n")
	intent := fmt.Sprintf("探测 %s:%d", host, port)
	seedGraph(t, store, task, intent)

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "port_probe", "state=") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	boundRev := pack.GraphVersion

	// 领取后、执行前:修改其 RoE(账本事件 → 图版本前进)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{DeniedTools: []string{"bash"}, Note: "收紧边界"},
	}); err != nil {
		t.Fatalf("改 RoE 失败: %v", err)
	}

	res, err := ex.Run(ctx, pack)
	if !errors.Is(err, ErrStaleGraph) {
		t.Fatalf("应拒绝执行返回 ErrStaleGraph, 得到 %v", err)
	}
	if res == nil || !res.Stale {
		t.Fatalf("结果应标记 Stale: %+v", res)
	}
	if model.requestCount() != 0 {
		t.Fatalf("复检失败后不得发生任何模型调用,实际 %d 次", model.requestCount())
	}
	// step 不得被置 running,仍是待领取态
	pg, _ := store.Graph(ctx, task)
	if graphNodes(pg, rings.NodeStep)[0].Status != "dispatched" {
		t.Fatalf("拒绝执行后 step 应保持 dispatched: %+v", graphNodes(pg, rings.NodeStep))
	}
	// 拒绝留痕:stale_refused 记录携带新旧版本
	recs := swarmRecords(t, store, task)
	found := false
	for _, r := range recs {
		if r.Type == rings.SwarmRecStaleRefused && r.GraphVersion == boundRev {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺 stale_refused 留痕: %+v", recs)
	}

	// 重新核对:重新领取(新图版本)后同一意图可正常执行
	pack2, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("重新领取失败: %v", err)
	}
	if pack2.GraphVersion == boundRev {
		t.Fatalf("重新领取应绑定新图版本")
	}
	if !containsStr(pack2.Authorization.DeniedTools, "bash") {
		t.Fatalf("新任务包应携带收紧后的 RoE: %+v", pack2.Authorization)
	}
	res2, err := ex.Run(ctx, pack2)
	if err != nil || res2.StepStatus != "done" {
		t.Fatalf("重新核对后应可执行: %v %+v", err, res2)
	}
	// 收紧的 RoE 在工具面上生效:bash 不再出现在广告中
	adv := strings.Join(model.requests()[0].ToolsOffered, ",")
	if strings.Contains(adv, "bash") {
		t.Fatalf("RoE 禁止的 bash 不应进入工具广告: %s", adv)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- R16+P4-01 ②:预算到线 = 收住写回 + 原会话续跑(非 failed) ----

func TestBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "budget-task"

	host, port := seedTarget(t, "b\r\n")
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		// 收尾轮(引擎注入预算耗尽提示):写回发现后收口
		if strings.Contains(rr.LastUser, "运行预算即将耗尽") {
			if !seenToolResult(rr, "submit_candidate_fact", "已提交新进事实") {
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "w1", Name: "submit_candidate_fact",
					Args: `{"statement":"目标端口开放","evidence":"预算内 port_probe 实测 state=open","asset_id":"target"}`,
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "预算耗尽,发现已写回。", Finish: "stop"}
		}
		// 续跑轮(P4-01 ②:到线收住后原会话续):确认结论收口
		if strings.Contains(rr.LastUser, "预算 · 续跑") {
			return scriptedReply{Text: "续跑后确认结论,本步骤完成。", Finish: "stop"}
		}
		// 常规轮:永不收敛地重复探测(模拟跑飞的虫群)
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: fmt.Sprintf("p%d", rr.Index), Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"),
		Budget{MaxTurns: 2, RunSeconds: 120, WrapupMaxTurns: 2})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	res, err := ex.Run(ctx, pack)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Reason != "completed" || res.StepStatus != "done" {
		t.Fatalf("到线应原会话续跑完成(非 failed/非切段): %+v", res)
	}
	// 收尾把发现写回了图:候选存在,待收割
	pg, _ := store.Graph(ctx, task)
	if len(graphNodes(pg, rings.NodeFact)) != 1 || graphNodes(pg, rings.NodeFact)[0].Status != "candidate" {
		t.Fatalf("收尾应写回候选: %+v", graphNodes(pg, rings.NodeFact))
	}
	if len(graphNodes(pg, rings.NodeStep)) != 1 || graphNodes(pg, rings.NodeStep)[0].Status != "done" {
		t.Fatalf("step 应为 done(同单位续跑完成): %+v", graphNodes(pg, rings.NodeStep))
	}
	// 转录单文件连续:收尾指令与续跑指令同在一条会话轨迹(换人税为零)。
	raw := readTranscript(t, filepath.Join(dir, "tasks"), task, res.TranscriptID)
	if !strings.Contains(raw, "运行预算即将耗尽") || !strings.Contains(raw, "预算 · 续跑") {
		t.Fatalf("转录应同文件含收尾与续跑指令(原会话连续)")
	}
}

func TestControlledEntries(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "ctrl-task"
	seedGraph(t, store, task, "某意图")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 程序化候选提交
	fid, err := env.SubmitCandidateFact(ctx, "靶标开放 22 端口", "port_probe 实测", "target", nil)
	if err != nil {
		t.Fatalf("程序化候选提交失败: %v", err)
	}
	// 空陈述/空证据拒绝
	if _, err := env.SubmitCandidateFact(ctx, "  ", "e", "", nil); err == nil {
		t.Fatalf("空陈述应拒绝")
	}
	if _, err := env.SubmitCandidateFact(ctx, "s", "", "", nil); err == nil {
		t.Fatalf("空证据应拒绝")
	}
	// 不存在的资产拒绝
	if _, err := env.SubmitCandidateFact(ctx, "s", "e", "ghost", nil); err == nil {
		t.Fatalf("幽灵资产应拒绝")
	}

	// 大证据走 blob 引用(R18)
	bigEvidence := strings.Repeat("证据原文。", 2000)
	fid2, err := env.SubmitCandidateFact(ctx, "大证据候选", bigEvidence, "", nil)
	if err != nil {
		t.Fatalf("大证据候选失败: %v", err)
	}
	n2, _ := store.Node(ctx, task, fid2)
	if ref, _ := n2.Detail["evidence_ref"].(string); ref == "" || !ex.Blobs.Exists(ref) {
		t.Fatalf("大证据应入 blob: %+v", n2.Detail)
	}

	_ = fid
	_ = fid2
}

// R14 变体二:领取后修改前置事实(而非 RoE)同样触发拒绝——
// 图版本比对是统一机制,任何账本变更(前置事实/RoE/资产)都会使旧计划过期。
func TestStaleGraphFactMutationRefused(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "stale-fact-task"

	host, port := seedTarget(t, "x\r\n")
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))
	// 前置事实:靶标端口状态(已确认,挂在 covered 资产上 → 进任务包第三节)
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "f-pre", NodeType: rings.NodeFact, Title: "靶标 22 端口开放", Status: "confirmed",
			Detail: map[string]any{"evidence": "早前实测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f-pre", ToNode: "target"}},
	); err != nil {
		t.Fatalf("种前置事实失败: %v", err)
	}

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "无事可做。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	// 前置事实确实进了任务包(七要素第三节)
	found := false
	for _, f := range pack.Facts {
		if f.ID == "f-pre" {
			found = true
		}
	}
	if !found {
		t.Fatalf("前置事实应进入任务包: %+v", pack.Facts)
	}

	// 领取后:前置事实被新证据推翻(状态变更事件 → 图版本前进)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "overmind",
		Payload: rings.NodeStateChanged{NodeID: "f-pre", Status: "refuted"},
	}); err != nil {
		t.Fatalf("推翻前置事实失败: %v", err)
	}
	if _, err := ex.Run(ctx, pack); !errors.Is(err, ErrStaleGraph) {
		t.Fatalf("前置事实变化后应拒绝执行: %v", err)
	}
	if model.requestCount() != 0 {
		t.Fatalf("不应发生模型调用: %d", model.requestCount())
	}
}

// ---- P1-05 并发适配:并发单位互不误判 R14 过期 ----

// TestConcurrentRunStepNoFalseStale 多只单位经 RunStep 并发领取执行:
// 领取→复检→首笔入账同临界区,友邻单位的记账推进不得把刚绑定的图版本
// 误判成过期(R14 只捕捉真实前提变化,见 TestStaleGraph* 两变体)。
func TestConcurrentRunStepNoFalseStale(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "concurrent-task"

	host, port := seedTarget(t, "b\r\n")
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))
	for i := 2; i <= 3; i++ {
		dispatchStepLikeOvermind(t, store, task, fmt.Sprintf("s%d", i),
			fmt.Sprintf("探测 %s:%d 变体 %d", host, port, i), "g1", "target")
	}

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	var wg sync.WaitGroup
	for _, sid := range []string{"s1", "s2", "s3"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := ex.RunStep(ctx, task, id); err != nil {
				t.Errorf("RunStep %s 失败: %v", id, err)
			}
		}(sid)
	}
	wg.Wait()

	for _, r := range swarmRecords(t, store, task) {
		if r.Type == rings.SwarmRecStaleRefused {
			t.Fatalf("并发单位不得误判过期: %+v", r)
		}
	}
	pg, err := store.Graph(ctx, task)
	if err != nil {
		t.Fatalf("查任务投影失败: %v", err)
	}
	done := 0
	for _, s := range graphNodes(pg, rings.NodeStep) {
		if s.Status == "done" {
			done++
		}
	}
	if done != 3 {
		t.Fatalf("三条意图应全部 done: %+v", graphNodes(pg, rings.NodeStep))
	}
}

// ---- P1-08: TestClaim 受控入口与反证任务包 ----

// claimRecords 取任务内全部 testclaim.record(按类型过滤,空为全部)。
func claimRecords(t *testing.T, store *rings.Store, task, typ string) []rings.TestClaimRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.TestClaimRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventTestClaimRecord {
			continue
		}
		var r rings.TestClaimRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 testclaim.record: %v", err)
		}
		if typ == "" || r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

func TestSubmitClaimEntry(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "tc-entry"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 程序化交卷:六字段齐全、锚定资产、判定标准随创建冻结。
	tcID, _, err := env.SubmitClaim(ctx, "target:8080 /old-backup/ 备份目录可公开列举",
		"high", []string{"ms-a1.1"}, "db-2026-09-01.sql.gz",
		"绕过缓存直接重放仍返回列举;目录内容为真实备份数据非缓存副本", "备份文件可被未授权下载", "target", "信息暴露", "")
	if err != nil {
		t.Fatalf("交卷失败: %v", err)
	}
	n, err := store.Node(ctx, task, tcID)
	if err != nil || n.Type != rings.NodeTestClaim || n.Status != rings.TestClaimCandidate {
		t.Fatalf("交卷声明应入图: %v %+v", err, n)
	}
	if n.Title != "target:8080 /old-backup/ 备份目录可公开列举" {
		t.Fatalf("声明一句话应为节点标题: %q", n.Title)
	}
	if n.Detail["severity"] != "high" || n.Detail["unit_id"] != "u1" || n.Detail["step_id"] != "s1" {
		t.Fatalf("声明 detail 应含自报级别/交卷单位/交卷步骤: %+v", n.Detail)
	}
	if n.Detail["alternatives"] != nil {
		t.Fatalf("P5-01②:交卷单不再冻结竞争解释清单: %+v", n.Detail["alternatives"])
	}
	st, _ := n.Detail["standard"].(map[string]any)
	if st == nil || st["kind"] != "full_falsify" {
		t.Fatalf("判定标准应随创建冻结(R2 契约②,查三依据): %+v", n.Detail)
	}
	// 血缘:derived_from(声明→执行意图 Step)+ about(声明→资产锚点)。
	derived, about := false, false
	for _, e := range edgesOf(t, store, task) {
		if e.Type == rings.EdgeDerivedFrom && e.From == tcID && e.To == "s1" {
			derived = true
		}
		if e.Type == rings.EdgeAbout && e.From == tcID && e.To == "target" {
			about = true
		}
	}
	if !derived || !about {
		t.Fatalf("交卷应挂 derived_from 与 about 边: derived=%v about=%v", derived, about)
	}
	// 交卷事件(P5-01③):六字段随 testclaim.record(submit) 落账。
	recs := claimRecords(t, store, task, rings.TestClaimRecSubmit)
	if len(recs) != 1 {
		t.Fatalf("应恰 1 条交卷单记录: %+v", recs)
	}
	r := recs[0]
	if r.ClaimID != tcID || r.StepID != "s1" || r.UnitID != "u1" {
		t.Fatalf("交卷单锚定不符: %+v", r)
	}
	if r.Summary == "" || r.Severity != "high" || r.ProvingMarker != "db-2026-09-01.sql.gz" ||
		r.CountercheckNote == "" || r.ImpactNote == "" || len(r.EvidenceRefs) != 1 || r.EvidenceRefs[0] != "ms-a1.1" {
		t.Fatalf("交卷单六字段应齐全落账: %+v", r)
	}

	// 入口校验类级(六字段各缺一拒):summary 空/severity 超词表/evidence_refs
	// 空/proving_marker 空/countercheck_note 空/asset_id 空或幽灵,一律拒。
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"summary 空", func() error {
			_, _, err := env.SubmitClaim(ctx, "  ", "high", []string{"ms-x.1"}, "m", "c", "", "target", "", "")
			return err
		}},
		{"severity 超词表", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "致命", []string{"ms-x.1"}, "m", "c", "", "target", "", "")
			return err
		}},
		{"evidence_refs 空", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "high", nil, "m", "c", "", "target", "", "")
			return err
		}},
		{"evidence_refs 全空白去重后为空", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "high", []string{" ", ""}, "m", "c", "", "target", "", "")
			return err
		}},
		{"proving_marker 空", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "high", []string{"ms-x.1"}, "  ", "c", "", "target", "", "")
			return err
		}},
		{"countercheck_note 空", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "high", []string{"ms-x.1"}, "m", " ", "", "target", "", "")
			return err
		}},
		{"asset_id 空", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "high", []string{"ms-x.1"}, "m", "c", "", "", "", "")
			return err
		}},
		{"幽灵资产", func() error {
			_, _, err := env.SubmitClaim(ctx, "另一声明", "high", []string{"ms-x.1"}, "m", "c", "", "ghost", "", "")
			return err
		}},
	} {
		if tc.call() == nil {
			t.Fatalf("%s 应被拒", tc.name)
		}
	}

	// 四档真实漏洞可交卷；none信息性结果必须走事实。
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if _, _, err := env.SubmitClaim(ctx, "声明-"+sev, sev, []string{"ms-s." + sev}, "marker-"+sev,
			"自查通过", "", "target", "", ""); err != nil {
			t.Fatalf("severity=%s 应合法: %v", sev, err)
		}
	}

	// 工具体直调与程序化同构(六字段 schema)。
	res, err := env.submitClaimTool().Call(ctx,
		[]byte(`{"summary":"管理接口未授权可达","severity":"high","evidence_refs":["ms-b2.2"],"proving_marker":"admin-panel","countercheck_note":"直接请求未经缓存","impact_note":"可未授权访问管理功能","asset_id":"target","vulnclass":"访问控制缺失"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("工具体交卷失败: %v %+v", err, res)
	}
	claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, rings.TestClaimCandidate)
	if len(claims) != 6 { // 首报1＋四级4＋工具体1
		t.Fatalf("应恰 6 条候选声明: %d", len(claims))
	}
}

// TestClaimDedupAndListing P2-08f 虫群查重:list_claims 只读清单(可按
// 资产过滤)+ submit_claim 入口确定性查重——同资产同声明(大小写
// 与空白形态无关)拒收并指认已报条目;不同资产同声明、同资产不同声明
// 不误伤。
func TestClaimDedupAndListing(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "tc-dedup"
	seedGraph(t, store, task, "审计敏感路径")
	// 第二个资产(同声明不同资产的合法场景)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "target2", NodeType: rings.NodeAsset,
			Title: "第二靶标 127.0.0.2", Status: "active", AssetClass: rings.AssetHost},
	}); err != nil {
		t.Fatalf("造资产失败: %v", err)
	}

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	tcID, _, err := env.SubmitClaim(ctx, "/old-backup/ 备份目录可公开列举",
		"high", []string{"ms-a1.1"}, "db.sql.gz", "自查通过", "备份可被未授权下载", "target", "信息暴露", "")
	if err != nil {
		t.Fatalf("首报失败: %v", err)
	}

	// 重复交同一漏洞(大小写/空白形态不同):拒收并指认已报条目。
	_, _, err = env.SubmitClaim(ctx, "  /old-backup/  备份目录可公开列举  ",
		"high", []string{"ms-a1.2"}, "db.sql.gz", "自查通过", "备份可被未授权下载", "target", "信息暴露", "")
	if err == nil {
		t.Fatalf("同资产同声明应被查重拒收")
	}
	if !strings.Contains(err.Error(), tcID) || !strings.Contains(err.Error(), "list_claims") {
		t.Fatalf("拒收原因应指认已报声明 %s 并指引查清单: %v", tcID, err)
	}

	// 同声明不同资产:另一靶标上的同一问题是独立声明,不误伤。
	if _, _, err := env.SubmitClaim(ctx, "/old-backup/ 备份目录可公开列举",
		"high", []string{"ms-a1.3"}, "db.sql.gz", "自查通过", "备份可被未授权下载", "target2", "信息暴露", ""); err != nil {
		t.Fatalf("不同资产的同声明不应被误拦: %v", err)
	}
	// 同资产不同声明:改写后的新声明另立,不误伤。
	if _, _, err := env.SubmitClaim(ctx, "/old-backup/config.bak 管理凭据明文可读",
		"high", []string{"ms-a1.4"}, "admin:pass", "自查通过", "凭据直接可用", "target", "敏感信息明文存储", ""); err != nil {
		t.Fatalf("同资产不同声明不应被误拦: %v", err)
	}

	// list_claims 只读清单:全量与按资产过滤。
	res, err := env.listClaimsTool().Call(ctx, []byte(`{}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("查清单失败: %v %+v", err, res)
	}
	full := resultText(res)
	if !strings.Contains(full, tcID) || !strings.Contains(full, "已报判断 3 条") {
		t.Fatalf("全量清单应含 3 条: %s", full)
	}
	res, err = env.listClaimsTool().Call(ctx, []byte(`{"asset_id":"target2"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("按资产查清单失败: %v %+v", err, res)
	}
	filtered := resultText(res)
	if !strings.Contains(filtered, "已报判断 1 条") || !strings.Contains(filtered, "第二靶标 127.0.0.2") {
		t.Fatalf("过滤清单应恰 1 条且带资产标题: %s", filtered)
	}
	res, err = env.listClaimsTool().Call(ctx, []byte(`{"asset_id":"ghost"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("幽灵资产应报错: %v %+v", err, res)
	}
}

// TestSubmitFalsifyRetired P5-01②⑤:反证写回工具(submit_falsify_result)
// 与反证任务包已随三段流水线退役——工具面零暴露、任务包零装配、
// 反证单位零通道(反证自查回到交卷单 countercheck_note)。
func TestSubmitFalsifyRetired(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "falsify-retired"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 工具面零暴露:全集目录与 toolset 均无 submit_falsify_result /
	// submit_test_claim(旧名一并退役)。
	for _, cat := range ToolCatalog() {
		if cat.Name == "submit_falsify_result" || cat.Name == "submit_test_claim" {
			t.Fatalf("旧工具 %s 不得再出现在工具目录", cat.Name)
		}
	}
	for _, tl := range env.toolset() {
		if n := tl.Name(); n == "submit_falsify_result" || n == "submit_test_claim" {
			t.Fatalf("旧工具 %s 不得再进入会话工具面", n)
		}
	}
	// 存量反证步骤(旧账本遗留 detail.testclaim_id 绑定)领取后不装配
	// 反证节:任务包零反证上下文、渲染零反证节(P5-01⑤ 执行侧)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s9", NodeType: rings.NodeStep, Title: "反证(存量遗留)", Status: "dispatched",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-old", "round": 9}},
	}); err != nil {
		t.Fatalf("造存量反证步骤失败: %v", err)
	}
	pack, err := ex.Claim(ctx, task, "s9")
	if err != nil {
		t.Fatalf("领取存量反证步骤失败: %v", err)
	}
	if strings.Contains(pack.Render(), "反证任务") {
		t.Fatalf("任务包不得再渲染反证节(P5-01⑤):\n%s", pack.Render())
	}
}

// edgesOf 测试辅助:任务全部类型化边。
func edgesOf(t *testing.T, store *rings.Store, task string) []rings.EdgeView {
	t.Helper()
	es, err := store.Edges(context.Background(), task)
	if err != nil {
		t.Fatalf("查边失败: %v", err)
	}
	return es
}

// graphNodes 保持旧断言的节点类型筛选，查询本身走正式 Graph。
func graphNodes(g *rings.GraphView, typ rings.NodeType) []rings.NodeView {
	var out []rings.NodeView
	for _, n := range g.Nodes {
		if n.Type == typ {
			out = append(out, n.NodeView)
		}
	}
	return out
}
func graphLineage(g *rings.GraphView) []rings.EdgeView {
	var out []rings.EdgeView
	for _, e := range g.Edges {
		if e.Type == rings.EdgeProduces || e.Type == rings.EdgeDerivedFrom {
			out = append(out, e)
		}
	}
	return out
}
