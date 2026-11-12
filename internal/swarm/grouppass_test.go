package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

// ---- P4-17 完成标准(问题清单 #28:代理旁路弹门审批疲劳)----
// ① 单位首条旁路命令弹门、批准后同组不同规则、不同参数的后续命令
//    直接放行执行、逐次留痕可对账(group_pass 审批记录带命令原文与
//    命中规则);
// ② 同任务新单位再发旁路命令重新弹门(放行随单位销毁即失效);
// ③ 用户自建 gate 规则回归:批准后同参数放行一次、不同参数照旧重新
//    弹门(现状语义不漂移);放行不外溢到其他组;
// ④ 拒绝路径照旧熔断;
// ⑤ 其他拒绝类规则(组内改拒绝处置、危害类、意图拦截)回归无损。

// bashResults 脚本模型看到的 bash 工具结果条数(挂起暂拒也计入——
// 驳回消息随工具结果进会话)。
func bashResults(rr *recordedRequest) int {
	id2name := map[string]string{}
	for _, msg := range rr.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				id2name[tc.ID] = tc.Function.Name
			}
		}
	}
	n := 0
	for _, msg := range rr.Messages {
		if msg.Role == "tool" && id2name[msg.ToolCallID] == "bash" {
			n++
		}
	}
	return n
}

// TestProxyBypassGroupPassE2E ①主行为全链:首条旁路命令弹门 → 面板批准
// → 重发同调用走单次配额 → 同组不同规则、同规则不同参数的后续命令
// 直接放行执行、逐次留痕;全程不再出现第二条弹门。
func TestProxyBypassGroupPassE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	enableProxyBypassGroup(t, store) // P4-21 起默认禁用,先启用(等价面板一键启用)
	ctx := context.Background()
	const task = "proxy-pass-e2e"

	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "直连对照复测观测污染", "g1", "只读探测", "target")

	base := fmt.Sprintf("http://%s:%d", host, port)
	curl1 := fmt.Sprintf(`{"command":"curl --noproxy '*' %s/first"}`, base)
	curl1Again := curl1
	envI := fmt.Sprintf(`{"command":"env -i /usr/bin/curl -s %s/second"}`, base)
	curl2 := fmt.Sprintf(`{"command":"curl --noproxy '*' %s/third?b=2"}`, base)

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch bashResults(rr) {
		case 0:
			return scriptedReply{ToolCalls: []scriptedToolCall{
				{ID: "b1", Name: "bash", Args: curl1}}, Finish: "tool_calls"}
		case 1: // 批准续跑:重发同调用(单次放行配额)。
			return scriptedReply{ToolCalls: []scriptedToolCall{
				{ID: "b2", Name: "bash", Args: curl1Again}}, Finish: "tool_calls"}
		case 2: // 同组不同规则(env -i 清环境):应直接放行执行。
			return scriptedReply{ToolCalls: []scriptedToolCall{
				{ID: "b3", Name: "bash", Args: envI}}, Finish: "tool_calls"}
		case 3: // 同规则不同参数(另一条 --noproxy):应直接放行执行。
			return scriptedReply{ToolCalls: []scriptedToolCall{
				{ID: "b4", Name: "bash", Args: curl2}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "对照复测完成,直连与代理观测一致。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{
		MaxTurns: 10, RunSeconds: 30, WrapupMaxTurns: 2,
	})
	gate := NewLedgerGate(store)
	ex.Gate = gate

	var (
		res  *RunResult
		rerr error
		done sync.WaitGroup
	)
	done.Add(1)
	go func() {
		defer done.Done()
		res, rerr = ex.RunStep(ctx, task, "s1")
	}()

	// 首条旁路命令:恰一条弹门请求(带组名)。
	waitFor(t, 30*time.Second, "弹门请求登记", func() bool { return len(gate.Pending(task)) == 1 })
	pending := gate.Pending(task)[0]
	if pending.GateGroup != HardGroupProxyBypass {
		t.Fatalf("待决请求应带弹门来源组: %+v", pending)
	}
	if err := gate.Decide(ctx, task, pending.ID, GateDecision{Approved: true, By: "creator", Reason: "允许直连对照"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	done.Wait()
	if rerr != nil {
		t.Fatalf("执行失败: %v", rerr)
	}
	if res.StepStatus != "done" {
		t.Fatalf("批准后续跑应完成: %+v", res)
	}

	// 全程不再出现第二条弹门。
	if left := gate.Pending(task); len(left) != 0 {
		t.Fatalf("后续同组命令不应再弹门: %+v", left)
	}

	// 审批流恰四笔:requested → approved → group_pass ×2(逐次留痕可对账)。
	recs := approvalEvents(t, store, task)
	if len(recs) != 4 {
		t.Fatalf("审批记录应为 requested+approved+group_pass×2: %+v", recs)
	}
	if recs[0].Type != rings.ApprovalRecRequested || !strings.Contains(recs[0].Note, HardGroupProxyBypass) {
		t.Fatalf("首笔应为带组名的弹门请求: %+v", recs[0])
	}
	if recs[1].Type != rings.ApprovalRecApproved {
		t.Fatalf("第二笔应为批准: %+v", recs[1])
	}
	for i, want := range map[int]struct{ args, rule string }{
		2: {"env -i /usr/bin/curl", "清空环境绕过代理"},
		3: {"--noproxy", "curl 绕过代理"},
	} {
		r := recs[i]
		if r.Type != rings.ApprovalRecGroupPass {
			t.Fatalf("第 %d 笔应为同组放行留痕: %+v", i+1, r)
		}
		if !strings.Contains(string(r.ToolArgs), want.args) {
			t.Fatalf("放行留痕应带命令原文: %+v", r)
		}
		if !strings.Contains(r.Note, want.rule) || !strings.Contains(r.Note, HardGroupProxyBypass) {
			t.Fatalf("放行留痕应带命中规则与组: %+v", r)
		}
		if !strings.Contains(r.Reason, recs[0].RequestID) || r.DecidedBy != "creator" {
			t.Fatalf("放行留痕应回指批准请求: %+v", r)
		}
	}

	// 执行面:恰 4 条 bash 记录(1 条挂起暂拒 + 3 条真实执行,同组放行的
	// 两条都执行了)。
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 4 || !calls[0].IsError || successfulToolCalls(calls) != 3 {
		t.Fatalf("应恰 4 条 bash 记录(1 暂拒 + 3 执行): %+v", calls)
	}

	// 续跑指令告知单位级放行(转录可查)。
	raw, terr := os.ReadFile(filepath.Join(dir, "tasks", task, "transcripts", "swarm-u1.jsonl"))
	if terr != nil {
		t.Fatalf("读转录失败: %v", terr)
	}
	if !strings.Contains(string(raw), "已一并获准") {
		t.Fatalf("批准续跑指令应告知代理旁路组单位级放行")
	}
}

// TestProxyBypassGroupDenyStillFuses ④拒绝路径(P4-68 ② 断言反转,原
// "面板拒绝照旧熔断"):首条旁路弹门被面板拒绝 → 只杀该动作不杀单位
// ——被拒调用未执行,单位读到换路反馈收尾,同会话正常完成。
func TestProxyBypassGroupDenyStillFuses(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	enableProxyBypassGroup(t, store) // P4-21 起默认禁用,先启用
	ctx := context.Background()
	const task = "proxy-pass-deny"

	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "直连对照复测观测污染", "g1", "只读探测", "target")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if bashResults(rr) == 0 {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash",
				Args: fmt.Sprintf(`{"command":"curl --noproxy '*' http://%s:%d/"}`, host, port),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "动作被拒,停止直连对照。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 6, RunSeconds: 20, WrapupMaxTurns: 2})
	gate := NewLedgerGate(store)
	ex.Gate = gate

	var (
		res  *RunResult
		done sync.WaitGroup
	)
	done.Add(1)
	go func() {
		defer done.Done()
		res, _ = ex.RunStep(ctx, task, "s1")
	}()

	waitFor(t, 30*time.Second, "弹门请求登记", func() bool { return len(gate.Pending(task)) == 1 })
	pending := gate.Pending(task)[0]
	if err := gate.Decide(ctx, task, pending.ID, GateDecision{Approved: false, By: "creator", Reason: "不允许绕开流量留痕"}); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	done.Wait()

	if res.StepStatus != "done" {
		t.Fatalf("面板拒绝只杀该动作,单位应换路收尾(P4-68 ②): %+v", res)
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 2 || recs[1].Type != rings.ApprovalRecDenied || recs[1].DenyLine != rings.DenyLinePanel {
		t.Fatalf("审批记录应为 requested+denied(panel): %+v", recs)
	}
	if calls := toolCallRecords(t, store, task, "bash"); successfulToolCalls(calls) != 0 {
		t.Fatalf("被拒调用不得执行: %+v", calls)
	}
}

// grouppassEnv env 级测试铺垫:播种 + 启用代理旁路组(P4-21 默认禁用)
// + 执行器 + 弹门。
func grouppassEnv(t *testing.T, task string) (*rings.Store, *Executor, *LedgerGate) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	seedHardRulesOrFail(t, store)
	enableProxyBypassGroup(t, store)
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	return store, ex, gate
}

// approveBypass env 级快路:让 unit 发一条旁路命令过门并获批准、武装
// 执行侧授权(与 drive 收到 Decide 批准后调 armGateApproval 同构)。
func approveBypass(t *testing.T, gate *LedgerGate, env *unitEnv, pack *TaskPack, args json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	d := env.approvalGate(pack)(ctx, "bash", args, permission.Context{})
	if d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("旁路命令应弹门: %+v", d)
	}
	req := gate.Pending(pack.TaskID)[0]
	if req.GateGroup != HardGroupProxyBypass || req.UnitID != env.UnitID {
		t.Fatalf("弹门请求应带组名与单位: %+v", req)
	}
	if err := gate.Decide(ctx, pack.TaskID, req.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	env.armGateApproval(req)
	env.park = nil
}

// TestProxyBypassGroupPassNewUnitRegates ②同任务新单位重新弹门:放行
// 随单位(u1)销毁即失效,u2 再发旁路命令重新过门。
func TestProxyBypassGroupPassNewUnitRegates(t *testing.T) {
	const task = "proxy-pass-units"
	_, ex, gate := grouppassEnv(t, task)
	ctx := context.Background()

	mkEnv := func(unit string) (*unitEnv, *TaskPack) {
		env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: unit, TranscriptID: "swarm-" + unit}
		pack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "直连对照复测", ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}
		return env, pack
	}

	// u1:首条弹门批准 → 放行武装;同组不同命令直接执行。
	u1, pack1 := mkEnv("u1")
	approveBypass(t, gate, u1, pack1, json.RawMessage(`{"command":"curl --noproxy '*' http://127.0.0.1:1/a"}`))
	if u1.bypassPass == nil || u1.allowance == nil {
		t.Fatalf("批准后应武装单次配额与单位级放行")
	}
	d := u1.approvalGate(pack1)(ctx, "bash", json.RawMessage(`{"command":"env -i /usr/bin/curl -s http://127.0.0.1:1/b"}`), permission.Context{})
	if d.Behavior != permission.Allow || u1.park != nil {
		t.Fatalf("u1 同组后续命令应直接放行: %+v", d)
	}

	// u2:同任务新单位,再发旁路命令重新弹门(不继承 u1 的放行)。
	u2, pack2 := mkEnv("u2")
	d = u2.approvalGate(pack2)(ctx, "bash", json.RawMessage(`{"command":"curl --noproxy '*' http://127.0.0.1:1/c"}`), permission.Context{})
	if d.Behavior != permission.Deny || u2.park == nil || u2.bypassPass != nil {
		t.Fatalf("新单位应重新弹门且无放行继承: %+v pass=%v", d, u2.bypassPass)
	}
	pending := gate.Pending(task)
	if len(pending) != 1 || pending[0].UnitID != "u2" {
		t.Fatalf("新单位的弹门请求应在待决表: %+v", pending)
	}
}

// TestUserGateRuleKeepsSingleShotQuota ③用户自建 gate 规则回归:批准后
// 同参数放行一次、不同参数照旧重新弹门;代理旁路组的放行不外溢到
// 其他组。
func TestUserGateRuleKeepsSingleShotQuota(t *testing.T) {
	const task = "proxy-pass-custom"
	store, ex, gate := grouppassEnv(t, task)
	ctx := context.Background()

	// 用户自建 gate 规则(非代理旁路组)。
	if _, err := SaveHardRule(ctx, store, HardRule{
		Group: HardGroupHTTPKill, Name: "自建 危险管理端点复核", Pattern: `\bp417-gate-cmd\b`,
		ToolScope: "bash", Action: HardActionGate, Enabled: true,
	}, "creator"); err != nil {
		t.Fatalf("保存自建 gate 规则失败: %v", err)
	}

	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "复核", ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}

	// u1 已获代理旁路组放行(先走一条真实旁路弹门批准)。
	approveBypass(t, gate, env, pack, json.RawMessage(`{"command":"curl --noproxy '*' http://127.0.0.1:1/a"}`))
	if env.bypassPass == nil {
		t.Fatalf("旁路组批准应武装单位级放行")
	}

	// 自建规则命中:照旧弹门(放行不外溢到其他组)。
	args := json.RawMessage(`{"command":"echo p417-gate-cmd one"}`)
	d := env.approvalGate(pack)(ctx, "bash", args, permission.Context{})
	if d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("自建 gate 规则应照旧弹门: %+v", d)
	}
	req := gate.Pending(task)[0]
	if req.GateGroup != HardGroupHTTPKill {
		t.Fatalf("自建规则的弹门应带其组名: %+v", req)
	}
	if err := gate.Decide(ctx, task, req.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	env.armGateApproval(req)
	env.park = nil

	// 同参数:单次配额放行一次。
	d = env.approvalGate(pack)(ctx, "bash", args, permission.Context{})
	if d.Behavior != permission.Allow || env.allowance != nil {
		t.Fatalf("批准后同参数应恰放行一次: %+v", d)
	}
	// 放行不产生同组放行留痕(不同组)。
	for _, r := range approvalEvents(t, store, task) {
		if r.Type == rings.ApprovalRecGroupPass {
			t.Fatalf("其他组的配额放行不应产生 group_pass 留痕: %+v", r)
		}
	}

	// 不同参数:照旧重新弹门(现状语义不漂移)。
	d = env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"echo p417-gate-cmd two"}`), permission.Context{})
	if d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("不同参数应重新弹门: %+v", d)
	}
}

// TestGroupPassKeepsDenyLines ⑤其他拒绝类规则回归:组内改成拒绝处置的
// 条目照旧拒绝(放行不越过用户意志);危害声明拒绝线与任务级意图拦截
// 对放行态单位照常生效。
func TestGroupPassKeepsDenyLines(t *testing.T) {
	const task = "proxy-pass-denylines"
	store, ex, gate := grouppassEnv(t, task)
	ctx := context.Background()

	// 组内自建"拒绝"处置条目(用户显式收紧)。
	if _, err := SaveHardRule(ctx, store, HardRule{
		Group: HardGroupProxyBypass, Name: "自建 内网直连禁面", Pattern: `\bp417-deny-cmd\b`,
		ToolScope: "bash", Enabled: true,
	}, "creator"); err != nil {
		t.Fatalf("保存组内拒绝规则失败: %v", err)
	}

	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "直连对照", ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}
	approveBypass(t, gate, env, pack, json.RawMessage(`{"command":"curl --noproxy '*' http://127.0.0.1:1/a"}`))

	// 组内拒绝处置:放行态不越过,直接拒绝(P4-63 B:拒动作不熔断单位,
	// 拒绝文案为换路指引)。
	d := env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"echo p417-deny-cmd"}`), permission.Context{})
	if d.Behavior != permission.Deny || env.deniedReason != "" || !strings.Contains(d.Message, "命令已被拦截") {
		t.Fatalf("拒绝处置的组内规则应照旧拒绝(不熔断单位): %+v", d)
	}
	recs := approvalEvents(t, store, task)
	if last := recs[len(recs)-1]; last.Type != rings.ApprovalRecDenied || last.DenyLine != rings.DenyLineHardRule {
		t.Fatalf("拒绝留痕不符: %+v", last)
	}

	// 危害声明拒绝线:非旁路命令照旧直接拒绝。
	env2 := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}
	approveBypass(t, gate, env2, pack, json.RawMessage(`{"command":"curl --noproxy '*' http://127.0.0.1:1/b"}`))
	harmPack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "破坏验证", ActionLevel: rings.ActionModifyPersist,
		HarmClass: rings.HarmDataDestruction, Authorization: RoE{}}
	d = env2.approvalGate(harmPack)(ctx, "bash", json.RawMessage(`{"command":"echo hi"}`), permission.Context{})
	if d.Behavior != permission.Deny || env2.deniedReason == "" {
		t.Fatalf("危害声明拒绝线应照旧生效: %+v", d)
	}
	recs = approvalEvents(t, store, task)
	if last := recs[len(recs)-1]; last.Type != rings.ApprovalRecDenied || last.DenyLine != rings.DenyLineHarm {
		t.Fatalf("危害拒绝留痕不符: %+v", last)
	}

	// 任务级意图拦截:非旁路命令照旧拒绝(只拒本次调用,不熔断)。
	env3 := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u3", TranscriptID: "swarm-u3"}
	approveBypass(t, gate, env3, pack, json.RawMessage(`{"command":"curl --noproxy '*' http://127.0.0.1:1/c"}`))
	intentPack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "读取验证", ActionLevel: rings.ActionReadOnly,
		Authorization: RoE{IntentRules: []rings.IntentRule{{
			ID: "ir-p417", Action: string(rings.IntentDataRead),
			AssetIDs: []string{"a1"}, Disposition: rings.IntentForbid,
		}}},
		Assets: []AssetBrief{{ID: "a1", Title: "http://target-a:8080"}}}
	d = env3.approvalGate(intentPack)(ctx, "bash", json.RawMessage(`{"command":"curl -s http://target-a:8080/x"}`), permission.Context{})
	if d.Behavior != permission.Deny || env3.deniedReason != "" {
		t.Fatalf("意图拦截应照旧只拒本次调用: %+v denied=%q", d, env3.deniedReason)
	}
	recs = approvalEvents(t, store, task)
	if last := recs[len(recs)-1]; last.Type != rings.ApprovalRecDenied || last.DenyLine != rings.DenyLineIntentRule {
		t.Fatalf("意图拦截留痕不符: %+v", last)
	}
}

// TestProxyBypassGroupPassReadOnlyPath 只读工具补口路径(调用边界)同款:
// 未放行弹门、批准武装后同组不同参数直接放行并留痕。
func TestProxyBypassGroupPassReadOnlyPath(t *testing.T) {
	const task = "proxy-pass-ro"
	store, ex, gate := grouppassEnv(t, task)
	ctx := context.Background()

	// 组内自建规则限定只读工具(补口路径的载体)。
	if _, err := SaveHardRule(ctx, store, HardRule{
		Group: HardGroupProxyBypass, Name: "自建 只读旁路面", Pattern: `p417-ro`,
		ToolScope: "http_probe", Action: HardActionGate, Enabled: true,
	}, "creator"); err != nil {
		t.Fatalf("保存只读旁路规则失败: %v", err)
	}

	// 真实 HTTP 靶(内层探测要真实执行)。
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("p417-ro-ok"))
	}))
	defer target.Close()

	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "只读探测", ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}
	g := newToolGuard(env, pack, nil, 100, time.Time{}, 0)
	wt := &wrappedTool{inner: env.httpProbeTool(), guard: g}

	// 未放行:命中弹门(暂挂,不执行内层)。
	if reason := callToolText(t, wt, fmt.Sprintf(`{"url":%q}`, target.URL+"/p417-ro-a")); reason == "" || !strings.Contains(reason, approvalPendingNote) {
		t.Fatalf("只读路径命中应弹门: %q", reason)
	}
	if env.park == nil {
		t.Fatalf("只读路径弹门应挂起")
	}
	req := gate.Pending(task)[0]
	if req.GateGroup != HardGroupProxyBypass || req.ToolName != "http_probe" {
		t.Fatalf("只读路径弹门请求应带组名与工具: %+v", req)
	}

	// 批准武装(与 drive 同构)后:同参数走单次配额,不同参数直接放行留痕。
	if err := gate.Decide(ctx, task, req.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	env.armGateApproval(req)
	env.park = nil
	if reason := callToolText(t, wt, fmt.Sprintf(`{"url":%q}`, target.URL+"/p417-ro-a")); reason != "" {
		t.Fatalf("同参数应走单次配额放行: %q", reason)
	}
	if reason := callToolText(t, wt, fmt.Sprintf(`{"url":%q}`, target.URL+"/p417-ro-b")); reason != "" {
		t.Fatalf("同组不同参数应直接放行: %q", reason)
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 3 || recs[2].Type != rings.ApprovalRecGroupPass ||
		!strings.Contains(string(recs[2].ToolArgs), "p417-ro-b") || !strings.Contains(recs[2].Note, HardGroupProxyBypass) {
		t.Fatalf("只读路径放行应逐次留痕: %+v", recs)
	}
}
