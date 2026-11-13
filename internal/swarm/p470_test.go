package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P4-70(问题清单 #87 ①②③,RoE 复核修正批次第一阶):红队信息源
// 动作复查与信息源攻击卡会话档 ----
// ② 红队命令路不再对信息源清单命中直接放行:命中的 host 逐命令做动作
// 性质判定(与渗透 ③ 间支同一代码路径 gateListedSource,判定即弃不落
// tg- 归类留痕);清单外范围外地址照旧直接放行留痕照镜(零判定调用)。
// ③ 信息源攻击卡＝会话档(档键=单位×信息源 host):裁决一次后同会话
// 不再重复问——允许档后续攻击放行免卡(零判定调用,不计连拒升级计数
// ——已授权动作不是拒绝)、禁止档后续同类攻击自动拒不弹卡(注入换路
// 反馈,计入 gd:attack 连拒升级保险,3 次升级杀);换单位重新出卡;
// 单位销毁档随 unitEnv 失效。

// p470RedTeamPack 红队形态:渗透环境闭包置 nil(红队/未标任务领取时
// ScopeClosure 即 nil,pack.go 只为渗透任务建闭包)。
func p470RedTeamPack(pack *TaskPack) *TaskPack {
	clone := *pack
	clone.ScopeClosure = nil
	return &clone
}

// p470ScanCmd 扫描形态命令(bash;目标提取面覆盖 nmap 与 curl)。
func p470ScanCmd(t *testing.T, cmd string) json.RawMessage {
	t.Helper()
	return json.RawMessage(mustJSON(t, cmd))
}

// TestP470RedTeamListedAcquisitionAllows 完成标准②(获取放行):红队任务
// 清单 host 的获取使用放行——零卡、零 tg- 归类留痕(判定即弃)、恰一次
// 动作判定调用。
func TestP470RedTeamListedAcquisitionAllows(t *testing.T) {
	store, _, env, pack := p450Env(t)
	rpack := p470RedTeamPack(pack)
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	called := 0
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		called++
		v, basis := TriageCandidate, "归类"
		if in.ListedInfoSource {
			v, basis = TriageMethodical, "获取使用"
		}
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: v, Basis: basis}}}, nil
	}
	if reason := env.denyScope(rpack, "bash", p467Target(t, "gitee.com")); reason != "" {
		t.Fatalf("红队清单 host 获取使用应放行,得: %q", reason)
	}
	if env.park != nil || env.scopeHold != nil {
		t.Fatalf("获取使用不得出卡或冻结: park=%v hold=%v", env.park, env.scopeHold)
	}
	if called != 1 {
		t.Fatalf("清单命中应恰一次动作判定,得 %d 次", called)
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("获取使用零范围卡,得 %d 张", len(cards))
	}
	if n := p467TriageRecords(t, store, env.TaskID); n != 0 {
		t.Fatalf("动作判定即弃:零 tg- 归类留痕,得 %d 笔", n)
	}
}

// TestP470RedTeamListedAttackCards 完成标准②(攻击出卡):红队任务清单
// host 上的攻击动作出攻击行为卡(挂票带目标、零范围卡、零 tg- 留痕)。
func TestP470RedTeamListedAttackCards(t *testing.T) {
	store, _, env, pack := p450Env(t)
	rpack := p470RedTeamPack(pack)
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	called := 0
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		called++
		v, basis := TriageCandidate, "归类"
		if in.ListedInfoSource {
			v, basis = TriageAttack, "对信息源主动攻击"
		}
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: v, Basis: basis}}}, nil
	}
	d := env.denyScope(rpack, "bash", p470ScanCmd(t, "nmap -p 443 gitee.com"))
	if d == "" || !strings.Contains(d, "信息源") || !strings.Contains(d, "已提交面板待决") {
		t.Fatalf("清单命中的攻击动作应出攻击行为卡: %q", d)
	}
	if env.park == nil || env.park.Req.AttackTarget != "gitee.com:443" {
		t.Fatalf("应挂在攻击行为卡票上: %+v", env.park)
	}
	if env.scopeHold != nil {
		t.Fatalf("攻击行为卡不得置位范围冻结")
	}
	if cards := p451ScopeWaitCards(t, store, env.TaskID); len(cards) != 0 {
		t.Fatalf("攻击行为卡不是范围卡,得 %d 张", len(cards))
	}
	if n := p467TriageRecords(t, store, env.TaskID); n != 0 {
		t.Fatalf("动作判定即弃:零 tg- 归类留痕,得 %d 笔", n)
	}
	if called != 1 {
		t.Fatalf("应恰一次动作判定,得 %d 次", called)
	}
}

// TestP470RedTeamUnlistedZeroTriage 完成标准②(清单外照旧):清单外范围
// 外地址直接放行——零判定调用(同任务里存在已入清单 host,证明按 host
// 分流);清单 host 的非攻击访问不受影响。
func TestP470RedTeamUnlistedZeroTriage(t *testing.T) {
	store, _, env, pack := p450Env(t)
	rpack := p470RedTeamPack(pack)
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	called := 0
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		called++
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: TriageCandidate, Basis: "不该走到"}}}, nil
	}
	for _, host := range []string{"unknown-out.net", "9.9.9.9", "evil.example.org"} {
		if reason := env.denyScope(rpack, "bash", p467Target(t, host)); reason != "" {
			t.Fatalf("清单外 host %s 应直接放行(红队哲学),得: %q", host, reason)
		}
	}
	if called != 0 {
		t.Fatalf("清单外零判定调用,得 %d 次", called)
	}
	if env.park != nil || env.scopeHold != nil {
		t.Fatalf("清单外零卡零冻结")
	}
}

// TestP470AllowedRulingSkipsCardAndTriage 完成标准③(允许档):同单位第
// 二次起对该信息源的攻击零卡零判定(档命中直接放行),且不计连拒升级
// 计数(已授权动作不是拒绝)。
func TestP470AllowedRulingSkipsCardAndTriage(t *testing.T) {
	store, _, env, pack := p450Env(t)
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	env.setAttackRuling("gitee.com", true)
	called := 0
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		called++
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: TriageAttack, Basis: "不该判定"}}}, nil
	}
	for i := 0; i < 3; i++ {
		if reason := env.denyScope(pack, "bash", p470ScanCmd(t, "nmap -p 80 gitee.com")); reason != "" {
			t.Fatalf("允许档攻击应免卡放行(第 %d 次),得: %q", i+1, reason)
		}
	}
	if called != 0 {
		t.Fatalf("档命中零判定调用,得 %d 次", called)
	}
	if env.park != nil {
		t.Fatalf("允许档不得出卡")
	}
	if n := env.denyHits["gd:attack"]; n != 0 {
		t.Fatalf("允许档攻击不计连拒升级计数,得 %d", n)
	}
}

// TestP470DeniedRulingAutoDenies 完成标准③(禁止档):后续同类攻击自动
// 拒——不弹卡、反馈在案(approval denied 留痕)、单位存活、计入
// gd:attack 计数。
func TestP470DeniedRulingAutoDenies(t *testing.T) {
	store, _, env, pack := p450Env(t)
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	env.setAttackRuling("gitee.com", false)
	called := 0
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		called++
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: TriageAttack, Basis: "不该判定"}}}, nil
	}
	d := env.denyScope(pack, "bash", p470ScanCmd(t, "nmap -p 80 gitee.com"))
	if d == "" || !strings.Contains(d, "已被面板禁止") || !strings.Contains(d, "不要再发起") {
		t.Fatalf("禁止档后续攻击应自动拒不弹卡: %q", d)
	}
	if env.park != nil {
		t.Fatalf("自动拒不得弹卡")
	}
	if env.deniedReason != "" {
		t.Fatalf("自动拒不杀单位(未到升级线),得 deniedReason=%q", env.deniedReason)
	}
	if called != 0 {
		t.Fatalf("档命中零判定调用,得 %d 次", called)
	}
	if n := env.denyHits["gd:attack"]; n != 1 {
		t.Fatalf("自动拒应计入 gd:attack 计数,得 %d", n)
	}
	// 反馈在案:denied 留痕(roe_scope)。
	denied := 0
	for _, r := range approvalEvents(t, store, env.TaskID) {
		if r.Type == rings.ApprovalRecDenied && strings.Contains(r.Reason, "已被面板禁止") {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("自动拒应恰一笔留痕,得 %d", denied)
	}
}

// TestP470AutoDenyEscalatesAfterThree 完成标准③(升级断言):禁止档自动
// 拒计入连拒升级——第 3 次升级杀单位(approval_denied 终态)。
func TestP470AutoDenyEscalatesAfterThree(t *testing.T) {
	store, _, env, pack := p450Env(t)
	_ = store
	if err := GrantInfoSource(context.Background(), store, env.TaskID, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	env.setAttackRuling("gitee.com", false)
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: TriageAttack, Basis: "不该判定"}}}, nil
	}
	for i := 1; i <= 3; i++ {
		env.deniedReason = ""
		d := env.denyScope(pack, "bash", p470ScanCmd(t, "nmap -p 80 gitee.com"))
		if d == "" {
			t.Fatalf("第 %d 次应自动拒", i)
		}
		if i < 3 {
			if env.deniedReason != "" {
				t.Fatalf("第 %d 次不应升级,得 %q", i, env.deniedReason)
			}
			continue
		}
		if !strings.Contains(env.deniedReason, "同类拒绝累计 3 次") ||
			!strings.Contains(env.deniedReason, "升级终止单位") {
			t.Fatalf("第 3 次应升级杀单位,得 %q", env.deniedReason)
		}
	}
}

// TestP470RulingPerHostPerUnit 完成标准③(档键粒度):同一单位对不同信
// 息源各自定档(档键=host);换单位(新 unitEnv)攻击同一信息源重新出卡
// (单位销毁档失效)。
func TestP470RulingPerHostPerUnit(t *testing.T) {
	store, ex, env, pack := p450Env(t)
	ctx := context.Background()
	for _, h := range []string{"gitee.com", "pypi.org"} {
		if err := GrantInfoSource(ctx, store, env.TaskID, h, "测试依据", "creator"); err != nil {
			t.Fatal(err)
		}
	}
	// gitee.com 定允许档;pypi.org 未定档——对 pypi.org 的攻击仍出卡。
	env.setAttackRuling("gitee.com", true)
	env.triage = func(_ context.Context, in TriageInput) (TriageReply, error) {
		v, basis := TriageCandidate, "归类"
		if in.ListedInfoSource {
			v, basis = TriageAttack, "对信息源主动攻击"
		}
		return TriageReply{Targets: []TriageTarget{{Host: in.Host, Verdict: v, Basis: basis}}}, nil
	}
	if reason := env.denyScope(pack, "bash", p470ScanCmd(t, "nmap -p 80 gitee.com")); reason != "" {
		t.Fatalf("gitee.com 允许档应放行,得: %q", reason)
	}
	d := env.denyScope(pack, "bash", p470ScanCmd(t, "nmap -p 443 pypi.org"))
	if d == "" || env.park == nil || env.park.Req.AttackTarget != "pypi.org:443" {
		t.Fatalf("未定档的 pypi.org 攻击应出卡: %q park=%v", d, env.park)
	}
	// 换单位(新 unitEnv,同任务同清单):对 gitee.com 的攻击重新出卡。
	env.park = nil
	env2 := &unitEnv{Executor: ex, TaskID: env.TaskID, StepID: env.StepID, UnitID: "u2", TranscriptID: "swarm-u2"}
	env2.triage = env.triage
	d2 := env2.denyScope(pack, "bash", p470ScanCmd(t, "nmap -p 443 gitee.com"))
	if d2 == "" || env2.park == nil || env2.park.Req.AttackTarget != "gitee.com:443" {
		t.Fatalf("换单位应对同一信息源重新出卡: %q park=%v", d2, env2.park)
	}
}

// ---- drive 级(真实链路:脚本模型真分诊真挂票真裁决) ----

// p470LocalTarget 本机短命服务经 --resolve 落地(P4-66 后环回不可直接当
// 靶标;目标侧 host 照判,连接实落到本机)。
func p470LocalTarget(t *testing.T, host string, m1, m2 string) (cmd1, cmd2 string) {
	t.Helper()
	var hits atomic.Int32
	out := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/scan2":
			w.Write([]byte(m2))
		default:
			w.Write([]byte(m1))
		}
	}))
	t.Cleanup(out.Close)
	u, _ := url.Parse(out.URL)
	port := u.Port()
	return fmt.Sprintf("curl -s --max-time 5 --resolve %s:%s:127.0.0.1 http://%s:%s/scan1", host, port, host, port),
		fmt.Sprintf("curl -s --max-time 5 --resolve %s:%s:127.0.0.1 http://%s:%s/scan2", host, port, host, port)
}

// p470IsTriageReq 该请求是否分诊调用(分诊系统段角色标识)。
func p470IsTriageReq(rr *recordedRequest) bool {
	return strings.Contains(rr.System, "边界研判员")
}

// TestP470RedTeamDriveApproveArmsSessionRuling 完成标准②③(红队全链):
// 清单 host 攻击出卡(真实分诊)→ 批准落会话档 → 该次攻击执行 → 同单
// 位第二次攻击零卡零判定直接执行(会话级配额)。
func TestP470RedTeamDriveApproveArmsSessionRuling(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	cmd1, cmd2 := p470LocalTarget(t, "gitee.com", "p470-rt-1", "p470-rt-2")
	const task = "p470-red-drive"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypeRedteam}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "对信息源发起扫描", Status: "dispatched",
			Detail: map[string]any{"action_level": "主动测试"}}},
	); err != nil {
		t.Fatal(err)
	}
	if err := GrantInfoSource(ctx, store, task, "gitee.com", "测试依据", "creator"); err != nil {
		t.Fatal(err)
	}
	var triageCalls atomic.Int32
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if p470IsTriageReq(rr) {
			// 动作判定:判攻击(真实解析链路;档命中后不再到达)。
			if strings.Contains(rr.LastUser, "已在任务信息源清单") {
				triageCalls.Add(1)
				return scriptedReply{Text: `{"verdict":"attack_action","basis":"对信息源的扫描动作"}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"verdict":"related_candidate","basis":"无关"}`, Finish: "stop"}
		}
		switch {
		case !seenToolResult(rr, "bash", "p470-rt-1"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b1", Name: "bash", Args: mustJSON(t, cmd1)}}, Finish: "tool_calls"}
		case !seenToolResult(rr, "bash", "p470-rt-2"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b2", Name: "bash", Args: mustJSON(t, cmd2)}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "两次攻击均已执行,收尾。", Finish: "stop"}
	})
	t.Cleanup(func() { model.Close() })
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)

	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	rid := p467WaitAttackTicket(t, ex, task)
	if err := ex.Gate.Decide(context.Background(), task, rid, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	res := <-done
	if res == nil || res.StepStatus != "done" {
		t.Fatalf("批准后单位应完成: %+v", res)
	}
	// 1 笔待决提示 + 2 笔真实执行(该次动作 + 第二次攻击免卡放行)。
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 3 || !calls[0].IsError || calls[1].IsError || calls[2].IsError {
		t.Fatalf("应 1 笔待决 + 2 笔真实执行: %+v", calls)
	}
	if !strings.Contains(calls[1].ResultHead, "p470-rt-1") || !strings.Contains(calls[2].ResultHead, "p470-rt-2") {
		t.Fatalf("两次攻击均应真实执行: %+v", calls)
	}
	// 恰 1 张攻击卡、恰 1 笔批准;第二次攻击零新卡。
	recs := approvalEvents(t, store, task)
	var wait, approved int
	for _, r := range recs {
		if r.Type == rings.ApprovalRecAttackWait {
			wait++
		}
		if r.Type == rings.ApprovalRecApproved && strings.HasPrefix(r.RequestID, "ap-") {
			approved++
		}
	}
	if wait != 1 || approved != 1 {
		t.Fatalf("应恰 1 待决 + 1 批准,得 wait=%d approved=%d: %+v", wait, approved, recs)
	}
	// 第二次攻击零判定调用(档命中直接放行——会话档的核心断言)。
	if n := triageCalls.Load(); n != 1 {
		t.Fatalf("第二次攻击应零判定调用(档命中),动作判定共应 1 次,得 %d", n)
	}
	if cards := p451ScopeWaitCards(t, store, task); len(cards) != 0 {
		t.Fatalf("红队攻击行为卡不产生范围卡,得 %d 张", len(cards))
	}
}

// TestP470DriveDenyThenAutoDeny 完成标准③(禁止档全链):面板拒绝后同单
// 位再攻——自动拒不弹卡不执行、反馈在案、单位存活换路完成;连拒计数
// 累计(面板拒 1 + 自动拒 1 = 2,未到 3)。
func TestP470DriveDenyThenAutoDeny(t *testing.T) {
	store, model, ex, task, scanCmd := p467AttackDriveEnv(t)
	cmd1 := scanCmd
	_, cmd2 := p470LocalTarget(t, "1.2.3.4", "p470-pen-1", "p470-pen-2")
	model.mu.Lock()
	// 轮次按请求序编排(渗透侧 tg- 攻击缓存预置,零分诊调用,序号即
	// 驱动轮):0=首发攻击(出卡挂票),1=面板拒绝反馈后换参数硬撞(应
	// 被自动拒),2=换路收尾。
	model.script = func(rr *recordedRequest) scriptedReply {
		switch rr.Index {
		case 0:
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b1", Name: "bash", Args: cmd1}}, Finish: "tool_calls"}
		case 1:
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b2", Name: "bash", Args: mustJSON(t, cmd2)}}, Finish: "tool_calls"}
		case 2:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b3", Name: "bash", Args: mustJSON(t, "echo rerouted-after-auto-deny"),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "已停止对该信息源的攻击,换方向收尾。", Finish: "stop"}
	}
	model.mu.Unlock()
	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	rid := p467WaitAttackTicket(t, ex, task)
	if err := ex.Gate.Decide(context.Background(), task, rid, GateDecision{Approved: false, By: "creator", Reason: "不许扫它"}); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	res := <-done
	if res == nil || res.StepStatus != "done" {
		t.Fatalf("自动拒不杀单位(未到升级线),单位应存活完成: %+v", res)
	}
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 3 || !calls[0].IsError || !calls[1].IsError || calls[2].IsError {
		t.Fatalf("应 1 笔待决 + 1 笔自动拒提示 + 1 笔换路执行: %+v", calls)
	}
	if !strings.Contains(calls[1].ResultHead, "已被面板禁止") {
		t.Fatalf("自动拒反馈应在案: %+v", calls[1])
	}
	if !strings.Contains(calls[2].ResultHead, "rerouted-after-auto-deny") {
		t.Fatalf("换路命令应执行: %+v", calls[2])
	}
	// 恰 1 张攻击卡;恰 1 笔面板拒绝(deny_line=panel) + 1 笔自动拒
	//(deny_line=roe_scope,反馈在案)。
	recs := approvalEvents(t, store, task)
	var wait, panelDeny, autoDeny int
	for _, r := range recs {
		switch {
		case r.Type == rings.ApprovalRecAttackWait:
			wait++
		case r.Type == rings.ApprovalRecDenied && r.DenyLine == "panel":
			panelDeny++
		case r.Type == rings.ApprovalRecDenied && r.DenyLine == rings.DenyLineRoeScope &&
			strings.Contains(r.Reason, "已被面板禁止"):
			autoDeny++
		}
	}
	if wait != 1 || panelDeny != 1 || autoDeny != 1 {
		t.Fatalf("应 1 卡 + 1 面板拒 + 1 自动拒,得 wait=%d panel=%d auto=%d: %+v", wait, panelDeny, autoDeny, recs)
	}
}

// TestP470DriveAutoDenyEscalates 完成标准③(升级杀一例):面板拒(1)+
// 自动拒(2)+ 自动拒(3)——第 3 次升级杀单位(approval_denied 终态,
// 终因含"同类拒绝累计 3 次")。
func TestP470DriveAutoDenyEscalates(t *testing.T) {
	store, model, ex, task, scanCmd := p467AttackDriveEnv(t)
	cmd1 := scanCmd
	_, cmd2 := p470LocalTarget(t, "1.2.3.4", "p470-pen-1", "p470-pen-2")
	model.mu.Lock()
	// 轮次按请求序编排:0=首发攻击(出卡,面板拒绝),1 与 2=被拒后连环
	// 硬撞(自动拒→再攻→第 3 次升级杀)。
	model.script = func(rr *recordedRequest) scriptedReply {
		if rr.Index == 0 {
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b1", Name: "bash", Args: cmd1}}, Finish: "tool_calls"}
		}
		if rr.Index <= 2 {
			return scriptedReply{ToolCalls: []scriptedToolCall{{ID: "b2", Name: "bash", Args: mustJSON(t, cmd2)}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "仍在尝试。", Finish: "stop"}
	}
	model.mu.Unlock()
	done := make(chan *RunResult, 1)
	go func() {
		r, _ := ex.RunStep(context.Background(), task, "s1")
		done <- r
	}()
	rid := p467WaitAttackTicket(t, ex, task)
	if err := ex.Gate.Decide(context.Background(), task, rid, GateDecision{Approved: false, By: "creator", Reason: "不许扫它"}); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	select {
	case res := <-done:
		if res == nil || res.StepStatus != StepApprovalDenied {
			t.Fatalf("第 3 次应升级杀单位(approval_denied),得: %+v", res)
		}
		if !strings.Contains(res.Reason, "同类拒绝累计 3 次") {
			t.Fatalf("终因应含升级说明: %q", res.Reason)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("单位未收场")
	}
	_ = store
}
