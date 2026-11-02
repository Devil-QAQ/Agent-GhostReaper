package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

// ---- P2-08e 完成标准(任务级意图拦截) ----
// ① "不对某资产做数据读取"命中拒绝;
// ② "上传测试允许但 ≤N 次"第 N+1 次自动拒绝(计数入账可查);
// ③ 规则增删随 RoE 下一轮生效(在飞单位维持领取时规则);
// ④ 动作归类与资产锚点的确定性单元行为。

// TestClassifyIntentAction 动作归类的确定性词表(工具直判 + bash 命令
// 分类;归不出的返回空——无规则可命中)。
func TestClassifyIntentAction(t *testing.T) {
	cases := []struct {
		tool string
		args string
		want rings.IntentAction
	}{
		{"port_probe", `{"host":"t","port":80}`, rings.IntentPortScan},
		{"http_probe", `{"url":"http://t/"}`, rings.IntentDataRead},
		{"bash", `{"command":"curl -s http://t/page"}`, rings.IntentDataRead},
		{"bash", `{"command":"wget -q http://t/f"}`, rings.IntentDataRead},
		{"bash", `{"command":"sqlmap -u http://t/?p=1 --batch"}`, rings.IntentInjectTest},
		{"bash", `{"command":"curl -s -d 'a=1' http://t/form"}`, rings.IntentInjectTest},
		{"bash", `{"command":"echo up > f && curl -s -T f http://t/upload"}`, rings.IntentUpload},
		{"bash", `{"command":"curl -s -F file=@s.txt http://t/up"}`, rings.IntentUpload},
		{"bash", `{"command":"hydra -l admin -P pw.txt t http-post-form"}`, rings.IntentBruteForce},
		{"bash", `{"command":"gobuster dir -u http://t/ -w w.txt"}`, rings.IntentCrawl},
		{"bash", `{"command":"wget --mirror http://t/"}`, rings.IntentCrawl},
		{"bash", `{"command":"nmap -sV t"}`, rings.IntentPortScan},
		{"bash", `{"command":"ls -la && cat note.txt"}`, ""},
		{"submit_test_claim", `{"statement":"x"}`, ""},
	}
	for i, c := range cases {
		if got := classifyIntentAction(c.tool, json.RawMessage(c.args)); got != c.want {
			t.Fatalf("第 %d 条 %s %s 归类为 %q,期望 %q", i, c.tool, c.args, got, c.want)
		}
	}
}

// TestAssetAnchorAndCallTargets 资产锚点与调用目标解析:URL / host:port
// 形态可锚定,纯名称型只靠步骤 covers;host 相等且双方带端口时须端口
// 也相等。
func TestAssetAnchorAndCallTargets(t *testing.T) {
	if a, ok := assetAnchor("http://127.0.0.1:8080/app"); !ok || a.host != "127.0.0.1" || a.port != "8080" {
		t.Fatalf("URL 锚点解析不符: %+v ok=%v", a, ok)
	}
	if a, ok := assetAnchor("10.1.2.3:22"); !ok || a.host != "10.1.2.3" || a.port != "22" {
		t.Fatalf("host:port 锚点解析不符: %+v ok=%v", a, ok)
	}
	if a, ok := assetAnchor("web-front-01"); !ok || a.host != "web-front-01" || a.port != "" {
		t.Fatalf("裸主机名锚点解析不符: %+v ok=%v", a, ok)
	}
	if _, ok := assetAnchor("这是一个句子 标题"); ok {
		t.Fatalf("非定位形态不应锚定")
	}
	// 调用目标:http_probe 的 url、port_probe 的 host/port、bash 命令里的 URL。
	ts := callTargets("http_probe", json.RawMessage(`{"url":"https://t.example:8443/x"}`))
	if len(ts) != 1 || ts[0].host != "t.example" || ts[0].port != "8443" {
		t.Fatalf("http_probe 目标解析不符: %+v", ts)
	}
	ts = callTargets("port_probe", json.RawMessage(`{"host":"h2","port":443}`))
	if len(ts) != 1 || ts[0].host != "h2" || ts[0].port != "443" {
		t.Fatalf("port_probe 目标解析不符: %+v", ts)
	}
	ts = callTargets("bash", json.RawMessage(`{"command":"curl -s http://a.b:9000/x && curl -s http://c.d/y"}`))
	if len(ts) != 2 || ts[0].host != "a.b" || ts[0].port != "9000" || ts[1].host != "c.d" || ts[1].port != "" {
		t.Fatalf("bash URL 目标解析不符: %+v", ts)
	}
	// 匹配:端口双在须相等;任一缺端口按主机。
	if !anchorHits(hostPort{host: "a.b", port: "9000"}, hostPort{host: "a.b", port: "9000"}) {
		t.Fatalf("同主机同端口应命中")
	}
	if anchorHits(hostPort{host: "a.b", port: "9000"}, hostPort{host: "a.b", port: "9001"}) {
		t.Fatalf("同主机不同端口不应命中")
	}
	if !anchorHits(hostPort{host: "a.b"}, hostPort{host: "a.b", port: "80"}) {
		t.Fatalf("单边缺端口按主机匹配")
	}
	if anchorHits(hostPort{host: "a.b"}, hostPort{host: "x.y"}) {
		t.Fatalf("主机不同不应命中")
	}
}

// intentGateEnv 任务级判定的白盒环境(带资产与规则的任务包)。
func intentGateEnv(t *testing.T, dir string, seeded bool, rules []rings.IntentRule, assets []AssetBrief) (*unitEnv, *Executor) {
	t.Helper()
	store := openStore(t, dir)
	if seeded {
		seedHardRulesOrFail(t, store)
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	return &unitEnv{Executor: ex, TaskID: "intent-task", StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}, ex
}

// intentPack 带规则与资产的任务包(分级声明为主动测试,默认门槛下不弹门
// ——任务级判定在弹门之前独立成立)。
func intentPack(rules []rings.IntentRule, assets ...AssetBrief) *TaskPack {
	return &TaskPack{
		TaskID: "intent-task", StepID: "s1", StepIntent: "意图", Assets: assets,
		ActionLevel: rings.ActionActiveTest, Authorization: RoE{IntentRules: rules},
	}
}

// guardRecords 解码账本内全部拦截判定记录。
func guardRecords(t *testing.T, store *rings.Store, task string) []rings.GuardRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.GuardRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventGuardRecord {
			continue
		}
		var r rings.GuardRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 guard.record: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// TestIntentRuleForbidDataRead 完成标准①:"不对某资产做数据读取"命中
// 拒绝——bash 通道(经审批门)与只读工具(调用边界补口)都拦;范围外
// 资产不误伤;未归类调用不命中。
func TestIntentRuleForbidDataRead(t *testing.T) {
	dir := t.TempDir()
	env, ex := intentGateEnv(t, dir, true, nil, nil)
	ctx := context.Background()
	rules := []rings.IntentRule{{
		ID: "ir-1", Action: string(rings.IntentDataRead),
		AssetIDs: []string{"a1"}, Disposition: rings.IntentForbid,
	}}
	pack := intentPack(rules, AssetBrief{ID: "a1", Title: "http://target-a:8080"})

	// bash 通道:curl GET 命中(步骤 covers 资产 a1)——只拒本次调用,
	// 不熔断单位(审核迭代定则:熔断保留给硬规则与危害拒绝线)。
	d := env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://target-a:8080/x"}`), permission.Context{})
	if d.Behavior != permission.Deny || !containsAll(d.Message, "数据读取") {
		t.Fatalf("数据读取禁止应命中拒绝: %+v", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("任务级拒绝不得熔断单位(只拒本次调用)")
	}
	recs := approvalEvents(t, ex.Store, "intent-task")
	if len(recs) != 1 || recs[0].DenyLine != rings.DenyLineIntentRule {
		t.Fatalf("拦截留痕不符: %+v", recs)
	}

	// 只读工具:http_probe 命中(调用边界补口——不经 CanUseTool)。
	env2, _ := intentGateEnv(t, dir, true, nil, nil)
	g := newToolGuard(env2, pack, nil, 100, time.Time{}, 0)
	wt := &wrappedTool{inner: env2.httpProbeTool(), guard: g}
	if reason := callToolText(t, wt, `{"url":"http://target-a:8080/x"}`); reason == "" || !containsAll(reason, "数据读取") {
		t.Fatalf("只读通道的数据读取禁止应命中: %q", reason)
	}
	if env2.deniedReason != "" {
		t.Fatalf("只读通道拦截同样不熔断单位")
	}

	// 范围外资产不误伤:b2 不在规则范围。
	env3, _ := intentGateEnv(t, dir, true, nil, nil)
	packB := intentPack(rules, AssetBrief{ID: "a1", Title: "http://target-a:8080"}, AssetBrief{ID: "b2", Title: "http://other-b:90"})
	// 步骤 covers b2(不在规则内)且目标指向 b2:范围不命中。
	packB2 := &TaskPack{TaskID: "intent-task", StepID: "s1", StepIntent: "意图",
		Assets:      []AssetBrief{{ID: "b2", Title: "http://other-b:90"}},
		ActionLevel: rings.ActionActiveTest, Authorization: RoE{IntentRules: rules}}
	d = env3.approvalGate(packB2)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://other-b:90/x"}`), permission.Context{})
	if d.Behavior != permission.Allow {
		t.Fatalf("范围外资产不应误伤: %+v", d)
	}
	// 未归类调用(本地命令)不命中。
	d = env3.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"ls -la"}`), permission.Context{})
	if d.Behavior != permission.Allow {
		t.Fatalf("未归类调用不应命中: %+v", d)
	}
	_ = packB
}

// containsAll 全包含断言辅助。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !jsonFreeContains(s, sub) {
			return false
		}
	}
	return true
}

func jsonFreeContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// currentIntentOfPack 从任务包文本解析「本步骤意图」节的首个非空行
// (测试辅助,与 server 包 avCurrentIntent 同语义:标题行之后才是意图)。
func currentIntentOfPack(pack string) string {
	i := strings.Index(pack, "本步骤意图")
	if i < 0 {
		return pack
	}
	for _, line := range strings.Split(pack[i:], "\n")[1:] {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// callToolText 调用包装工具并返回错误结果文本(空 = 非错误)。
func callToolText(t *testing.T, wt *wrappedTool, args string) string {
	t.Helper()
	res, err := wt.Call(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("调用异常: %v", err)
	}
	if !res.IsError {
		return ""
	}
	return flattenResult(res.Content)
}

// TestIntentRuleQuota 完成标准②:"上传允许但 ≤N 次"——前 N 次放行且
// 计数入账,第 N+1 次自动拒绝(超额转禁止)。
func TestIntentRuleQuota(t *testing.T) {
	dir := t.TempDir()
	env, ex := intentGateEnv(t, dir, true, nil, nil)
	ctx := context.Background()
	pack := intentPack([]rings.IntentRule{{
		ID: "ir-2", Action: string(rings.IntentUpload),
		AssetIDs: []string{"a1"}, Disposition: rings.IntentAllow, Quota: 2,
	}}, AssetBrief{ID: "a1", Title: "http://target-a:8080"})
	upload := json.RawMessage(`{"command":"echo up > f && curl -s -T f http://target-a:8080/upload"}`)

	// 第 1、2 次:放行并计数。
	for i := 1; i <= 2; i++ {
		e := &unitEnv{Executor: ex, TaskID: "intent-task", StepID: "s1", UnitID: fmt.Sprintf("u%d", i), TranscriptID: "swarm-u"}
		d := e.approvalGate(pack)(ctx, "bash", upload, permission.Context{})
		if d.Behavior != permission.Allow {
			t.Fatalf("第 %d 次上传应放行: %+v", i, d)
		}
	}
	// 计数入账可查:两条 quota_count,序号 1、2。
	recs := guardRecords(t, ex.Store, "intent-task")
	if len(recs) != 2 || recs[0].Nth != 1 || recs[1].Nth != 2 || recs[0].RuleID != "ir-2" {
		t.Fatalf("计数入账不符: %+v", recs)
	}
	if recs[0].Action != string(rings.IntentUpload) || recs[1].Action != string(rings.IntentUpload) {
		t.Fatalf("计数应携带动作类型: %+v", recs)
	}
	// 第 3 次:超额自动转禁止。
	e := &unitEnv{Executor: ex, TaskID: "intent-task", StepID: "s1", UnitID: "u3", TranscriptID: "swarm-u"}
	d := e.approvalGate(pack)(ctx, "bash", upload, permission.Context{})
	if d.Behavior != permission.Deny || !containsAll(d.Message, "2 次上限") {
		t.Fatalf("第 3 次应超额拒绝: %+v", d)
	}
	if e.deniedReason != "" {
		t.Fatalf("超额拒绝不熔断单位(只拒本次调用)")
	}
	denies := approvalEvents(t, ex.Store, "intent-task")
	if len(denies) != 1 || denies[0].DenyLine != rings.DenyLineIntentRule {
		t.Fatalf("超额拒绝留痕不符: %+v", denies)
	}
	// 计数不因拒绝前进(仍是 2 条)。
	if n := len(guardRecords(t, ex.Store, "intent-task")); n != 2 {
		t.Fatalf("拒绝不应计数,仍应 2 条,得到 %d", n)
	}
	_ = env
}

// TestIntentRulesNextRoERound 完成标准③:规则增删随 RoE 下一轮生效——
// roe.updated 入账后新领取(Claim)读到新规则;判定用任务包冻结的规则,
// 在飞单位(旧包)不受影响。
func TestIntentRulesNextRoERound(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	ctx := context.Background()
	const task = "intent-round"
	// 铺垫:目标 + 资产 + 步骤(covers 资产)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a1", NodeType: rings.NodeAsset, Title: "http://target-a:8080", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "读取资产", Status: "dispatched",
			Detail: map[string]any{"goal_id": "g1", "action_level": "主动测试"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s1", ToNode: "a1"}},
	); err != nil {
		t.Fatalf("种图失败: %v", err)
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)

	// 第一轮:无规则,放行。
	pack1, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if len(pack1.Authorization.IntentRules) != 0 {
		t.Fatalf("第一轮应无规则")
	}
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u"}
	d := env.approvalGate(pack1)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://target-a:8080/x"}`), permission.Context{})
	if d.Behavior != permission.Allow {
		t.Fatalf("第一轮应放行: %+v", d)
	}
	// 审核迭代定则:判定现读当前规则——规则入账后在飞单位(旧包)的
	// 下一次调用即按新规则判;范围只圈 a1,圈外单位零感知。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{IntentRules: []rings.IntentRule{{
				ID: "ir-3", Action: string(rings.IntentDataRead), AssetIDs: []string{"a1"},
				Disposition: rings.IntentForbid,
			}}}},
		// 圈外资产 b2(步骤 covers b2、调用指向 other-b:90)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "b2", NodeType: rings.NodeAsset, Title: "http://other-b:90",
			Status: "active", AssetClass: rings.AssetService}},
	); err != nil {
		t.Fatalf("RoE 入账失败: %v", err)
	}
	d = env.approvalGate(pack1)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://target-a:8080/x"}`), permission.Context{})
	if d.Behavior != permission.Deny {
		t.Fatalf("规则入账后在飞单位下一次调用即按新规则判(现读): %+v", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("拒绝不熔断单位")
	}
	// 圈外单位:b2 范围的调用不受 a1 规则影响(范围过滤,零感知)。
	packB := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "意图",
		Assets:      []AssetBrief{{ID: "b2", Title: "http://other-b:90"}},
		ActionLevel: rings.ActionActiveTest, Authorization: RoE{}}
	envB := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "uB", TranscriptID: "swarm-uB"}
	d = envB.approvalGate(packB)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://other-b:90/x"}`), permission.Context{})
	if d.Behavior != permission.Allow {
		t.Fatalf("范围外单位零感知,应放行: %+v", d)
	}
	// 新领取同样读到规则(任务包仍携带,审计/上下文用)。
	pack2, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("第二轮领取失败: %v", err)
	}
	if len(pack2.Authorization.IntentRules) != 1 {
		t.Fatalf("第二轮应读到 1 条规则,得到 %d", len(pack2.Authorization.IntentRules))
	}
}

// TestIntentRulesScriptedE2E 全链 E2E(脚本模型):数据读取禁止规则下,
// 单位经 http_probe(只读通道,调用边界补口)被拦截,单位以
// approval_denied 终态收场、留痕可查;上传配额规则下前 2 步完成、
// 第 3 步 approval_denied——完成标准的引擎级实证。
func TestIntentRulesScriptedE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	ctx := context.Background()
	const task = "intent-e2e"
	host, port := seedTarget(t, "e2e\r\n")
	base := fmt.Sprintf("http://%s:%d", host, port)
	// 规则:数据读取禁止(范围 a1)+ 上传允许 ≤2 次(范围 a1)。
	rules := []rings.IntentRule{
		{ID: "ir-read", Action: string(rings.IntentDataRead), AssetIDs: []string{"a1"}, Disposition: rings.IntentForbid},
		{ID: "ir-up", Action: string(rings.IntentUpload), AssetIDs: []string{"a1"}, Disposition: rings.IntentAllow, Quota: 2},
	}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{IntentRules: rules}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "active"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a1", NodeType: rings.NodeAsset, Title: base, Status: "active", AssetClass: rings.AssetService}},
	); err != nil {
		t.Fatalf("种图失败: %v", err)
	}
	step := func(id, title string) {
		t.Helper()
		if _, err := store.Append(ctx, task,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
				NodeID: id, NodeType: rings.NodeStep, Title: title, Status: "dispatched",
				Detail: map[string]any{"goal_id": "g1", "action_level": "主动测试"}}},
			rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
				EdgeType: rings.EdgeCovers, FromNode: id, ToNode: "a1"}},
		); err != nil {
			t.Fatalf("种步骤 %s 失败: %v", id, err)
		}
	}
	step("s1", "读取演练")
	step("s2", "上传演练一")
	step("s3", "上传演练二")
	step("s4", "上传演练三")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		// 只看「本步骤意图」节:前序尝试节列有其他步骤标题,整包 Contains
		// 会误匹配(gate_test 同坑备注)。拒绝不熔断(审核迭代定则):
		// 模型读到拦截拒绝文案后收口换线,单位继续跑完其余工作。
		intent := currentIntentOfPack(rr.LastUser)
		switch {
		case strings.Contains(intent, "读取演练"):
			if seenToolResult(rr, "http_probe", "status=") {
				return scriptedReply{Text: "完成。", Finish: "stop"}
			}
			if seenToolResult(rr, "http_probe", "任务级拦截") {
				return scriptedReply{Text: "读取被规则禁止,跳过该项,收口。", Finish: "stop"}
			}
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "h1", Name: "http_probe", Args: fmt.Sprintf(`{"url":"%s"}`, base),
			}}, Finish: "tool_calls"}
		default:
			if seenToolResult(rr, "bash", "UP-OK") {
				return scriptedReply{Text: "完成。", Finish: "stop"}
			}
			if seenToolResult(rr, "bash", "任务级拦截") {
				return scriptedReply{Text: "上传额度已满,不再尝试,收口。", Finish: "stop"}
			}
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash",
				Args: fmt.Sprintf(`{"command":"echo UP-OK > up.txt && cat up.txt && curl -s -T up.txt %s/upload"}`, base),
			}}, Finish: "tool_calls"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 4, RunSeconds: 30, WrapupMaxTurns: 1})
	ex.Gate = NewLedgerGate(store)

	// s1 读取:只读通道拦截——拒绝该次调用,单位收口跑完(不熔断)。
	res, err := ex.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("RunStep 异常: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("拒绝不熔断:读取演练单位应收口完成,得到 %+v", res)
	}
	// s2/s3 上传:各计一次,完成。
	for _, s := range []string{"s2", "s3"} {
		res, err := ex.RunStep(ctx, task, s)
		if err != nil || res.StepStatus != "done" {
			t.Fatalf("%s 应完成: %+v err=%v", s, res, err)
		}
	}
	// s4 第三次上传:超额拒绝该次调用,单位收口完成(不熔断)。
	res, err = ex.RunStep(ctx, task, "s4")
	if err != nil {
		t.Fatalf("RunStep 异常: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("超额拒绝不熔断:第三次上传单位应收口完成,得到 %+v", res)
	}
	// 账面:两条计数(序号 1、2)+ 两条 intent_rule 拒绝(s1 读取 + s4 超额)。
	recs := guardRecords(t, store, task)
	if len(recs) != 2 || recs[0].Nth != 1 || recs[1].Nth != 2 || recs[0].RuleID != "ir-up" {
		t.Fatalf("配额计数不符: %+v", recs)
	}
	var intentDenies int
	for _, a := range approvalEvents(t, store, task) {
		if a.Type == rings.ApprovalRecDenied && a.DenyLine == rings.DenyLineIntentRule {
			intentDenies++
		}
	}
	if intentDenies != 2 {
		t.Fatalf("应恰 2 条任务级拒绝留痕(读取禁止 + 上传超额),得到 %d", intentDenies)
	}
	// 拒绝的调用没有执行(is_error 记录,无副作用)。
	for _, c := range toolCallRecords(t, store, task, "http_probe") {
		if !c.IsError {
			t.Fatalf("被拒的 http_probe 不得真实执行: %+v", c)
		}
	}
}

// TestIntentRuleFreeTextPatterns 审核迭代三轮:动作描述自由文本 + 规则
// 自带正则——判定按正则(描述不必在词表内);不匹配的命令不命中;无
// 正则的词表描述走兼容分类路径(旧规则语义不变)。
func TestIntentRuleFreeTextPatterns(t *testing.T) {
	dir := t.TempDir()
	env, ex := intentGateEnv(t, dir, true, nil, nil)
	ctx := context.Background()
	rules := []rings.IntentRule{{
		ID: "ir-free", Action: "改配置文件",
		AssetIDs: []string{"a1"}, Disposition: rings.IntentForbid,
		Patterns: []string{`(?i)\b(sed\s+-i|chmod|chown)\b[^|;&]*/etc/`},
	}}
	pack := intentPack(rules, AssetBrief{ID: "a1", Title: "http://target-a:8080"})

	// 命中:sed -i 改 /etc 配置。
	d := env.approvalGate(pack)(ctx, "bash",
		json.RawMessage(`{"command":"sed -i 's/x/y/' /etc/nginx/nginx.conf"}`), permission.Context{})
	if d.Behavior != permission.Deny || !containsAll(d.Message, "改配置文件") {
		t.Fatalf("自由文本规则应按正则命中,原因含描述: %+v", d)
	}
	if env.deniedReason != "" {
		t.Fatalf("拒绝不熔断单位")
	}
	// 不命中:同工具但动作无关(读文件)。
	env2, _ := intentGateEnv(t, dir, true, nil, nil)
	d = env2.approvalGate(pack)(ctx, "bash",
		json.RawMessage(`{"command":"cat /etc/hosts"}`), permission.Context{})
	if d.Behavior != permission.Allow {
		t.Fatalf("不匹配正则的调用不应命中: %+v", d)
	}
	// 兼容路径:无正则 + 描述恰为词表词,按分类判(旧规则)。
	packLegacy := intentPack([]rings.IntentRule{{
		ID: "ir-old", Action: string(rings.IntentUpload),
		AssetIDs: []string{"a1"}, Disposition: rings.IntentForbid,
	}}, AssetBrief{ID: "a1", Title: "http://target-a:8080"})
	env3, _ := intentGateEnv(t, dir, true, nil, nil)
	d = env3.approvalGate(packLegacy)(ctx, "bash",
		json.RawMessage(`{"command":"curl -s -T f http://target-a:8080/up"}`), permission.Context{})
	if d.Behavior != permission.Deny {
		t.Fatalf("词表兼容路径应命中上传: %+v", d)
	}
	_ = ex
}
