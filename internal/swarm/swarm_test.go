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
	if len(all) != 12 {
		t.Fatalf("工具全集应为 12 个(bash/port_probe/http_probe/read/web_search/web_fetch/list_claims/五个受控入口), 实际 %d", len(all))
	}
	names := func(ts []tool.CoreTool) string {
		var ns []string
		for _, x := range ts {
			ns = append(ns, x.Name())
		}
		return strings.Join(ns, ",")
	}
	if got := names(filterTools(all, RoE{})); got != "bash,port_probe,http_probe,read,web_search,web_fetch,list_claims,submit_candidate_fact,submit_hypothesis,submit_hypothesis_result,submit_test_claim,submit_falsify_result" {
		t.Fatalf("无 RoE 应全集放行: %s", got)
	}
	if got := names(filterTools(all, RoE{DeniedTools: []string{"bash"}})); got != "port_probe,http_probe,read,web_search,web_fetch,list_claims,submit_candidate_fact,submit_hypothesis,submit_hypothesis_result,submit_test_claim,submit_falsify_result" {
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
		case !seenToolResult(rr, "submit_candidate_fact", "已入候选事实"):
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
	for _, want := range []string{"bash", "port_probe", "submit_candidate_fact", "submit_hypothesis"} {
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
	pg, err := store.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查进度图失败: %v", err)
	}
	if len(pg.Facts) != 1 || pg.Facts[0].Status != "confirmed" || pg.Facts[0].Title == "" {
		t.Fatalf("收割后 Fact 应为 confirmed: %+v", pg.Facts)
	}
	if len(pg.Steps) != 1 || pg.Steps[0].Status != "done" {
		t.Fatalf("Step 终态应为 done: %+v", pg.Steps)
	}
	foundLineage := false
	for _, e := range pg.Lineage {
		if e.Type == rings.EdgeProduces && e.From == "s1" && e.To == pg.Facts[0].ID {
			foundLineage = true
		}
	}
	if !foundLineage {
		t.Fatalf("缺 produces 血缘边 s1→%s: %+v", pg.Facts[0].ID, pg.Lineage)
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
		case !seenToolResult(rr, "submit_candidate_fact", "已入候选事实"):
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
	pg, _ := store.ProgressGraph(ctx, task)
	if len(pg.Facts) != 1 || pg.Facts[0].Status != "candidate" {
		t.Fatalf("意图外线索应以候选 Fact 回流: %+v", pg.Facts)
	}
	if !strings.Contains(pg.Facts[0].Title, fmt.Sprintf(":%d", decoyPort)) {
		t.Fatalf("候选内容应指向意图外线索: %q", pg.Facts[0].Title)
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
	pg, _ := store.ProgressGraph(ctx, task)
	if pg.Steps[0].Status != "dispatched" {
		t.Fatalf("拒绝执行后 step 应保持 dispatched: %+v", pg.Steps)
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
			if !seenToolResult(rr, "submit_candidate_fact", "已入候选事实") {
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
	pg, _ := store.ProgressGraph(ctx, task)
	if len(pg.Facts) != 1 || pg.Facts[0].Status != "candidate" {
		t.Fatalf("收尾应写回候选: %+v", pg.Facts)
	}
	if len(pg.Steps) != 1 || pg.Steps[0].Status != "done" {
		t.Fatalf("step 应为 done(同单位续跑完成): %+v", pg.Steps)
	}
	// 转录单文件连续:收尾指令与续跑指令同在一条会话轨迹(换人税为零)。
	raw := readTranscript(t, filepath.Join(dir, "tasks"), task, res.TranscriptID)
	if !strings.Contains(raw, "运行预算即将耗尽") || !strings.Contains(raw, "预算 · 续跑") {
		t.Fatalf("转录应同文件含收尾与续跑指令(原会话连续)")
	}
}

// ---- 受控入口:程序化提交与工具提交同路;假设回流 ----

func TestControlledEntries(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "ctrl-task"
	seedGraph(t, store, task, "某意图")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 程序化候选提交
	fid, err := env.SubmitCandidateFact(ctx, "靶标开放 22 端口", "port_probe 实测", "target")
	if err != nil {
		t.Fatalf("程序化候选提交失败: %v", err)
	}
	// 空陈述/空证据拒绝
	if _, err := env.SubmitCandidateFact(ctx, "  ", "e", ""); err == nil {
		t.Fatalf("空陈述应拒绝")
	}
	if _, err := env.SubmitCandidateFact(ctx, "s", "", ""); err == nil {
		t.Fatalf("空证据应拒绝")
	}
	// 不存在的资产拒绝
	if _, err := env.SubmitCandidateFact(ctx, "s", "e", "ghost"); err == nil {
		t.Fatalf("幽灵资产应拒绝")
	}

	// 大证据走 blob 引用(R18)
	bigEvidence := strings.Repeat("证据原文。", 2000)
	fid2, err := env.SubmitCandidateFact(ctx, "大证据候选", bigEvidence, "")
	if err != nil {
		t.Fatalf("大证据候选失败: %v", err)
	}
	n2, _ := store.Node(ctx, task, fid2)
	if ref, _ := n2.Detail["evidence_ref"].(string); ref == "" || !ex.Blobs.Exists(ref) {
		t.Fatalf("大证据应入 blob: %+v", n2.Detail)
	}

	// 假设工具走同一受控入口语义:必须有资产与依据
	hyp := env.submitHypothesisTool()
	// 通过工具体直接调(与引擎调用同构)
	res, err := hyp.Call(ctx, []byte(`{"statement":"靶标疑似弱口令","asset_id":"target","basis":"banner 风格相似"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("假设提交失败: %v %+v", err, res)
	}
	if res2, _ := hyp.Call(ctx, []byte(`{"statement":"x","asset_id":"ghost","basis":"y"}`), nil); !res2.IsError {
		t.Fatalf("幽灵资产假设应报错")
	}

	// 假设节点与 about 边入图
	hyps, err := store.NodesByType(ctx, task, rings.NodeHypothesis, "candidate")
	if err != nil || len(hyps) != 1 {
		t.Fatalf("候选假设应入图: %v %+v", err, hyps)
	}
	abouts, _ := store.Edges(ctx, task, rings.EdgeAbout)
	foundAbout := false
	for _, e := range abouts {
		if e.From == hyps[0].ID && e.To == "target" {
			foundAbout = true
		}
	}
	if !foundAbout {
		t.Fatalf("假设应挂 about 边指向资产: %+v", abouts)
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
	pg, err := store.ProgressGraph(ctx, task)
	if err != nil {
		t.Fatalf("查进度图失败: %v", err)
	}
	done := 0
	for _, s := range pg.Steps {
		if s.Status == "done" {
			done++
		}
	}
	if done != 3 {
		t.Fatalf("三条意图应全部 done: %+v", pg.Steps)
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

func TestSubmitTestClaimEntry(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "tc-entry"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 程序化提交:存在性与影响面分开、锚定资产、判定标准随创建冻结。
	tcID, err := env.SubmitTestClaim(ctx, "/old-backup/ 备份目录可公开列举",
		"备份文件可被未授权下载,存在敏感数据泄露风险",
		"curl 实测返回目录列举: db-2026-09-01.sql.gz, config.bak", "target", "信息暴露",
		[]string{"缓存污染:内容可能来自中间缓存", "归属错误:可能来自其他资产"})
	if err != nil {
		t.Fatalf("提交声明失败: %v", err)
	}
	n, err := store.Node(ctx, task, tcID)
	if err != nil || n.Type != rings.NodeTestClaim || n.Status != rings.TestClaimCandidate {
		t.Fatalf("候选声明应入图: %v %+v", err, n)
	}
	if n.Title != "/old-backup/ 备份目录可公开列举" {
		t.Fatalf("存在性声明应为节点标题: %q", n.Title)
	}
	if n.Detail["impact"] == nil || n.Detail["unit_id"] != "u1" || n.Detail["step_id"] != "s1" {
		t.Fatalf("声明 detail 应含影响面/发现单位/发现步骤: %+v", n.Detail)
	}
	st, _ := n.Detail["standard"].(map[string]any)
	if st == nil || st["kind"] != "full_falsify" {
		t.Fatalf("判定标准应随创建冻结为 full_falsify: %+v", n.Detail)
	}
	// P2-02:竞争解释清单随创建冻结(模型列出的照收,ID 顺序编号)。
	alts, ok := n.Detail["alternatives"].([]any)
	if !ok || len(alts) != 2 {
		t.Fatalf("冻结解释清单应恰 2 条: %+v", n.Detail["alternatives"])
	}
	first, _ := alts[0].(map[string]any)
	if first["id"] != "alt1" || !strings.Contains(first["label"].(string), "缓存污染") {
		t.Fatalf("解释条目应带稳定 ID 与标签: %+v", first)
	}
	// 血缘:derived_from(声明→发现 Step)+ about(声明→资产锚点)。
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
		t.Fatalf("声明应挂 derived_from 与 about 边: derived=%v about=%v", derived, about)
	}

	// 入口校验:缺存在性/影响/证据/资产、幽灵资产,一律拒。
	for _, args := range [][4]string{
		{"  ", "影响", "证据", "target"},
		{"声明", "  ", "证据", "target"},
		{"声明", "影响", "  ", "target"},
		{"声明", "影响", "证据", ""},
		{"声明", "影响", "证据", "ghost"},
	} {
		if _, err := env.SubmitTestClaim(ctx, args[0], args[1], args[2], args[3], "", nil); err == nil {
			t.Fatalf("非法声明 %v 应被拒", args)
		}
	}

	// 工具体直调与程序化同构。
	res, err := env.submitTestClaimTool().Call(ctx,
		[]byte(`{"statement":"管理接口未授权可达","impact":"可未授权访问管理功能","evidence":"curl 401 质询","asset_id":"target","vulnclass":"访问控制缺失"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("工具体提交失败: %v %+v", err, res)
	}
	claims, _ := store.NodesByType(ctx, task, rings.NodeTestClaim, rings.TestClaimCandidate)
	if len(claims) != 2 {
		t.Fatalf("应恰 2 条候选声明: %d", len(claims))
	}
	// 未列解释的声明兜底四条基线(设计文档 §3.5 示例定稿)。
	dflt, _ := claims[1].Detail["alternatives"].([]any)
	if len(dflt) != 4 {
		t.Fatalf("缺省解释清单应兜底 4 条基线: %+v", claims[1].Detail["alternatives"])
	}
}

// TestClaimDedupAndListing P2-08f 虫群查重:list_claims 只读清单(可按
// 资产过滤)+ submit_test_claim 入口确定性查重——同资产同声明(大小写
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

	tcID, err := env.SubmitTestClaim(ctx, "/old-backup/ 备份目录可公开列举",
		"备份可被未授权下载", "curl 实测列举", "target", "信息暴露", nil)
	if err != nil {
		t.Fatalf("首报失败: %v", err)
	}

	// 重复报同一漏洞(大小写/空白形态不同):拒收并指认已报条目。
	_, err = env.SubmitTestClaim(ctx, "  /old-backup/  备份目录可公开列举  ",
		"备份可被未授权下载", "curl 再次实测", "target", "信息暴露", nil)
	if err == nil {
		t.Fatalf("同资产同声明应被查重拒收")
	}
	if !strings.Contains(err.Error(), tcID) || !strings.Contains(err.Error(), "list_claims") {
		t.Fatalf("拒收原因应指认已报声明 %s 并指引查清单: %v", tcID, err)
	}

	// 同声明不同资产:另一靶标上的同一问题是独立声明,不误伤。
	if _, err := env.SubmitTestClaim(ctx, "/old-backup/ 备份目录可公开列举",
		"备份可被未授权下载", "curl 实测列举", "target2", "信息暴露", nil); err != nil {
		t.Fatalf("不同资产的同声明不应被误拦: %v", err)
	}
	// 同资产不同声明:改写后的新声明另立,不误伤。
	if _, err := env.SubmitTestClaim(ctx, "/old-backup/config.bak 管理凭据明文可读",
		"凭据直接可用", "curl 实测原文", "target", "敏感信息明文存储", nil); err != nil {
		t.Fatalf("同资产不同声明不应被误拦: %v", err)
	}

	// list_claims 只读清单:全量与按资产过滤。
	res, err := env.listClaimsTool().Call(ctx, []byte(`{}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("查清单失败: %v %+v", err, res)
	}
	full := resultText(res)
	if !strings.Contains(full, tcID) || !strings.Contains(full, "已报声明 3 条") {
		t.Fatalf("全量清单应含 3 条: %s", full)
	}
	res, err = env.listClaimsTool().Call(ctx, []byte(`{"asset_id":"target2"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("按资产查清单失败: %v %+v", err, res)
	}
	filtered := resultText(res)
	if !strings.Contains(filtered, "已报声明 1 条") || !strings.Contains(filtered, "第二靶标 127.0.0.2") {
		t.Fatalf("过滤清单应恰 1 条且带资产标题: %s", filtered)
	}
	res, err = env.listClaimsTool().Call(ctx, []byte(`{"asset_id":"ghost"}`), nil)
	if err != nil || !res.IsError {
		t.Fatalf("幽灵资产应报错: %v %+v", err, res)
	}
}

func TestSubmitFalsifyResultGuards(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "falsify-entry"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	discoverer := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	tcID, err := discoverer.SubmitTestClaim(ctx, "备份目录可公开列举", "备份可被未授权下载", "curl 实测列举", "target", "",
		[]string{"缓存污染:内容可能来自中间缓存"})
	if err != nil {
		t.Fatalf("提交声明失败: %v", err)
	}

	// 派发绑定声明的反证步骤(与 overmind.HarvestClaims 同构)并迁移声明到 testing。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "反证(第 1 轮): 备份目录可公开列举", Status: "dispatched",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": tcID, "round": 1,
				"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存污染:内容可能来自中间缓存"}},
				"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: tcID, Status: rings.TestClaimTesting}},
	); err != nil {
		t.Fatalf("派发反证步骤失败: %v", err)
	}

	// 非绑定步骤(发现步骤 s1)写回拒绝。
	s1Env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}
	if err := s1Env.SubmitFalsifyResult(ctx, tcID, false, true, "high", nil, "证据", ""); err == nil {
		t.Fatalf("非反证绑定步骤(s1)写回应被拒")
	}

	// R12:发现单位(u1)即使在绑定步骤上也必须被拒。
	u1Env := &unitEnv{Executor: ex, TaskID: task, StepID: "s2", UnitID: "u1", TranscriptID: "swarm-u1"}
	if err := u1Env.SubmitFalsifyResult(ctx, tcID, false, true, "high", nil, "证据", ""); err == nil {
		t.Fatalf("R12:发现者自证应被拒")
	}

	// 空证据拒绝。
	s2Env := &unitEnv{Executor: ex, TaskID: task, StepID: "s2", UnitID: "u9", TranscriptID: "swarm-u9"}
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, true, "high", nil, "  ", ""); err == nil {
		t.Fatalf("空证据应被拒")
	}

	// P2-02:影响面成立缺定级拒绝;不成立携带定级拒绝;定级超词汇拒绝。
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, true, "", nil, "证据", ""); err == nil {
		t.Fatalf("impact_supported=true 缺定级应被拒")
	}
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, true, "致命", nil, "证据", ""); err == nil {
		t.Fatalf("定级超四级词汇应被拒")
	}
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, false, "high", nil, "证据", ""); err == nil {
		t.Fatalf("impact_supported=false 携带定级应被拒")
	}
	// P2-02:解释 ID 不在冻结清单内拒绝。
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, true, "high",
		[]rings.AltResult{{ID: "alt9", Refuted: true}}, "证据", ""); err == nil {
		t.Fatalf("冻结清单外的解释 ID 应被拒")
	}

	// 正常写回:独立单位 + 绑定步骤 + testing 状态 + 逐条解释结论。
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, true, "high",
		[]rings.AltResult{{ID: "alt1", Refuted: true}}, "curl 重放仍返回列举", "复核一致"); err != nil {
		t.Fatalf("合法写回失败: %v", err)
	}
	recs := claimRecords(t, store, task, rings.TestClaimRecFalsify)
	if len(recs) != 1 {
		t.Fatalf("应恰 1 条反证写回: %+v", recs)
	}
	r := recs[0]
	if r.ClaimID != tcID || r.StepID != "s2" || r.UnitID != "u9" || r.ExistenceRefuted || !r.ImpactSupported {
		t.Fatalf("反证写回负载不符: %+v", r)
	}
	if r.Round != 1 || r.ImpactLevel != "high" || len(r.Alternatives) != 1 || r.Alternatives[0].ID != "alt1" {
		t.Fatalf("P2-02 字段(轮次/定级/解释结论)应随写回落账: %+v", r)
	}
	// P2-02:同轮二次写回拒绝(每轮一次,R1)。
	if err := s2Env.SubmitFalsifyResult(ctx, tcID, false, true, "high", nil, "再写一次", ""); err == nil {
		t.Fatalf("同轮重复写回应被拒")
	}
	if r.EvidenceHead == "" {
		t.Fatalf("反证证据截头必须落账(R18)")
	}

	// 幽灵声明拒绝。
	if err := s2Env.SubmitFalsifyResult(ctx, "tc-ghost", false, true, "high", nil, "证据", ""); err == nil {
		t.Fatalf("幽灵声明应被拒")
	}
}

func TestClaimPackFalsifyContext(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "tc-pack"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	tcID, err := env.SubmitTestClaim(ctx, "备份目录可公开列举", "备份可被未授权下载",
		"curl 实测列举: db-2026-09-01.sql.gz", "target", "信息暴露",
		[]string{"缓存污染:内容可能来自中间缓存", "归属错误:可能来自其他资产"})
	if err != nil {
		t.Fatalf("提交声明失败: %v", err)
	}
	// 派发绑定声明的反证步骤(与 HarvestClaims 同构)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "反证(第 2 轮): 备份目录可公开列举", Status: "dispatched",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": tcID, "round": 2,
				"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存污染:内容可能来自中间缓存"}},
				"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s2", ToNode: "target"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: tcID, Status: rings.TestClaimTesting}},
	); err != nil {
		t.Fatalf("派发反证步骤失败: %v", err)
	}

	pack, err := ex.Claim(ctx, task, "s2")
	if err != nil {
		t.Fatalf("领取反证步骤失败: %v", err)
	}
	if pack.Falsify == nil {
		t.Fatalf("反证任务包必须携带声明上下文")
	}
	f := pack.Falsify
	if f.ClaimID != tcID || f.Statement != "备份目录可公开列举" ||
		f.Impact != "备份可被未授权下载" || f.Vulnclass != "信息暴露" || f.DiscovererUnit != "u1" {
		t.Fatalf("反证上下文不符: %+v", f)
	}
	// P2-02:轮次与派发冻结的存活解释清单随任务包交付。
	if f.Round != 2 || len(f.Alternatives) != 1 || f.Alternatives[0].ID != "alt1" {
		t.Fatalf("反证任务包应携带轮次与本轮存活解释: %+v", f)
	}
	rendered := pack.Render()
	for _, want := range []string{"八、反证任务(验证类意图 · 第 2 轮", "存在性声明", "影响面声明",
		"submit_falsify_result", "R12", "alt1: 缓存污染", "impact_level"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("反证任务渲染缺 %q:\n%s", want, rendered)
		}
	}

	// 普通意图步骤不携带反证节。
	p1, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取普通步骤失败: %v", err)
	}
	if p1.Falsify != nil || strings.Contains(p1.Render(), "八、反证任务") {
		t.Fatalf("普通意图不得携带反证节")
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

// TestSubmitHypothesisEntry P2-03:假设受控入口——produces 血缘边
// (发现 Step→假设)+ about 资产锚点 + 每 Step 限量(防噪音)。
func TestSubmitHypothesisEntry(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hypo-entry"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}

	// 缺依据 / 缺资产 / 幽灵资产拒绝。
	if _, err := env.SubmitHypothesis(ctx, "疑似后台", " ", "target"); err == nil {
		t.Fatalf("缺依据应被拒")
	}
	if _, err := env.SubmitHypothesis(ctx, "疑似后台", "风格", ""); err == nil {
		t.Fatalf("缺资产应被拒")
	}
	if _, err := env.SubmitHypothesis(ctx, "疑似后台", "风格", "ghost"); err == nil {
		t.Fatalf("幽灵资产应被拒")
	}

	id, err := env.SubmitHypothesis(ctx, "独立端口服务疑似管理后台", "配置风格", "target")
	if err != nil {
		t.Fatalf("提交假设失败: %v", err)
	}
	// 回流轨迹:produces(s1→假设)+ about(假设→target)。
	hyp, err := store.Node(ctx, task, id)
	if err != nil {
		t.Fatalf("查假设失败: %v", err)
	}
	if hyp.Type != rings.NodeHypothesis || hyp.Status != rings.HypothesisCandidate {
		t.Fatalf("假设节点不符: %+v", hyp)
	}
	if hyp.Detail["step_id"] != "s1" || hyp.Detail["unit_id"] != "u1" || hyp.Detail["basis"] != "配置风格" {
		t.Fatalf("假设 detail 应记录提出步骤/单位/依据: %+v", hyp.Detail)
	}
	has := func(et rings.EdgeType, from, to string) bool {
		for _, e := range edgesOf(t, store, task) {
			if e.Type == et && e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	if !has(rings.EdgeProduces, "s1", id) {
		t.Fatalf("假设提交必须落 produces 血缘边(s1 → %s)", id)
	}
	if !has(rings.EdgeAbout, id, "target") {
		t.Fatalf("假设提交必须落 about 资产锚点(%s → target)", id)
	}

	// 每 Step 限量:本步已有 1 条,再提 2 条到上限,第 4 条拒绝。
	if _, err := env.SubmitHypothesis(ctx, "隐藏路径", "风格", "target"); err != nil {
		t.Fatalf("第 2 条应成功: %v", err)
	}
	if _, err := env.SubmitHypothesis(ctx, "备份凭据", "风格", "target"); err != nil {
		t.Fatalf("第 3 条应成功: %v", err)
	}
	if _, err := env.SubmitHypothesis(ctx, "第四条", "风格", "target"); err == nil {
		t.Fatalf("超出每步上限应被拒(防噪音)")
	}
	// 换步骤不受限(限量按 Step 计)。
	env2 := &unitEnv{Executor: ex, TaskID: task, StepID: "s2", UnitID: "u2", TranscriptID: "swarm-u2"}
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
		Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep, Title: "另一步", Status: "done"}}); err != nil {
		t.Fatalf("铺步骤失败: %v", err)
	}
	if _, err := env2.SubmitHypothesis(ctx, "另一步的假设", "风格", "target"); err != nil {
		t.Fatalf("另一步首条应成功: %v", err)
	}
}

// TestSubmitHypothesisResultGuards P2-03:验证结论写回守卫——状态机、
// 步骤绑定、R12 提出者不自证、结论词汇、每验证步骤一次。
func TestSubmitHypothesisResultGuards(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hypo-result"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	proposer := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	hypID, err := proposer.SubmitHypothesis(ctx, "独立端口服务疑似管理后台", "配置风格", "target")
	if err != nil {
		t.Fatalf("提交假设失败: %v", err)
	}

	// 未派发验证(candidate 状态)写回拒绝。
	verifier := &unitEnv{Executor: ex, TaskID: task, StepID: "s2", UnitID: "u9", TranscriptID: "swarm-u9"}
	if err := verifier.SubmitHypothesisResult(ctx, hypID, rings.HypothesisSupported, "证据", ""); err == nil {
		t.Fatalf("非验证中状态写回应被拒")
	}

	// 派发绑定假设的验证步骤(与 overmind.dispatchVerifyStep 同构)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "验证假设: 独立端口服务疑似管理后台", Status: "dispatched",
			Detail: map[string]any{"kind": "verify", "hypothesis_id": hypID, "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeVerifies, FromNode: "s2", ToNode: hypID}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: hypID, Status: rings.HypothesisTesting}},
	); err != nil {
		t.Fatalf("派发验证步骤失败: %v", err)
	}

	// 非绑定步骤(发现步骤 s1)写回拒绝。
	s1Env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}
	if err := s1Env.SubmitHypothesisResult(ctx, hypID, rings.HypothesisSupported, "证据", ""); err == nil {
		t.Fatalf("非验证绑定步骤(s1)写回应被拒")
	}
	// R12:提出单位(u1)即使在绑定步骤上也必须被拒。
	u1Env := &unitEnv{Executor: ex, TaskID: task, StepID: "s2", UnitID: "u1", TranscriptID: "swarm-u1"}
	if err := u1Env.SubmitHypothesisResult(ctx, hypID, rings.HypothesisSupported, "证据", ""); err == nil {
		t.Fatalf("R12:提出者自证应被拒")
	}
	// 结论超词汇 / 空证据拒绝。
	if err := verifier.SubmitHypothesisResult(ctx, hypID, "maybe", "证据", ""); err == nil {
		t.Fatalf("结论超词汇应被拒")
	}
	if err := verifier.SubmitHypothesisResult(ctx, hypID, rings.HypothesisSupported, " ", ""); err == nil {
		t.Fatalf("空证据应被拒")
	}
	if err := verifier.SubmitHypothesisResult(ctx, "h-ghost", rings.HypothesisSupported, "证据", ""); err == nil {
		t.Fatalf("幽灵假设应被拒")
	}

	// 合法写回:独立单位 + 绑定步骤 + testing 状态。
	if err := verifier.SubmitHypothesisResult(ctx, hypID, rings.HypothesisSupported, "401 + console", "备注不构成结论"); err != nil {
		t.Fatalf("合法写回失败: %v", err)
	}
	evs, _ := store.Events(ctx, task, 0, 0)
	var recs []rings.HypothesisRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventHypothesisRecord {
			continue
		}
		var r rings.HypothesisRecord
		if json.Unmarshal(ev.Payload, &r) == nil {
			recs = append(recs, r)
		}
	}
	if len(recs) != 1 || recs[0].Type != rings.HypothesisRecResult {
		t.Fatalf("应恰 1 条验证结论: %+v", recs)
	}
	r := recs[0]
	if r.HypothesisID != hypID || r.StepID != "s2" || r.UnitID != "u9" ||
		r.Verdict != rings.HypothesisSupported || r.EvidenceHead == "" {
		t.Fatalf("验证结论负载不符: %+v", r)
	}
	// 每验证步骤一次。
	if err := verifier.SubmitHypothesisResult(ctx, hypID, rings.HypothesisRefuted, "再写", ""); err == nil {
		t.Fatalf("同一验证步骤重复写回应被拒")
	}
}

// TestHypothesisPackVerifyContext P2-03:验证类步骤的任务包携带假设上下文
// (陈述/依据/提出单位),渲染假设验证节;普通意图不携带。
func TestHypothesisPackVerifyContext(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hypo-pack"
	seedGraph(t, store, task, "审计敏感路径")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	hypID, err := env.SubmitHypothesis(ctx, "独立端口服务疑似管理后台", "配置风格相似", "target")
	if err != nil {
		t.Fatalf("提交假设失败: %v", err)
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "验证假设: 独立端口服务疑似管理后台", Status: "dispatched",
			Detail: map[string]any{"kind": "verify", "hypothesis_id": hypID, "action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s2", ToNode: "target"}},
	); err != nil {
		t.Fatalf("派发验证步骤失败: %v", err)
	}

	pack, err := ex.Claim(ctx, task, "s2")
	if err != nil {
		t.Fatalf("领取验证步骤失败: %v", err)
	}
	if pack.Verify == nil {
		t.Fatalf("验证任务包必须携带假设上下文")
	}
	v := pack.Verify
	if v.HypothesisID != hypID || v.Statement != "独立端口服务疑似管理后台" ||
		v.Basis != "配置风格相似" || v.ProposerUnit != "u1" {
		t.Fatalf("验证上下文不符: %+v", v)
	}
	rendered := pack.Render()
	for _, want := range []string{"九、假设验证任务", "假设陈述", "提出者依据", "submit_hypothesis_result", "R12", "R11"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("验证任务渲染缺 %q:\n%s", want, rendered)
		}
	}

	// 普通意图步骤不携带验证节。
	p1, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取普通步骤失败: %v", err)
	}
	if p1.Verify != nil || strings.Contains(p1.Render(), "九、假设验证任务") {
		t.Fatalf("普通意图不得携带假设验证节")
	}
}
