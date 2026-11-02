package swarm

// ---- P4-27 ⑧(问题清单 #54 ①:同步骤逐条弹门连环轰炸)----
// 同一单位同一步骤内,审批门槛批准一次后,该步骤后续命令(不同工具、
// 不同参数,含查看类)直接放行执行、逐次留痕;门第一次照弹、意图照审;
// 新单位重新弹门;用户自建 gate 规则不享步骤级放行(P4-17 语义不漂移);
// 只读步骤(门槛之下的分级)照旧零弹门。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/norma/permission"

	"ghostreaper/internal/rings"
)

// stepPassEnv env 级铺垫:库 + 执行器 + 弹门(无硬规则参与,走审批
// 门槛线)。
func stepPassEnv(t *testing.T, task string) (*rings.Store, *Executor, *LedgerGate) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	return store, ex, gate
}

// mkL4Pack 修改与持久化分级的任务包(默认审批门槛 L4:该分级弹门)。
func mkL4Pack(task string) *TaskPack {
	return &TaskPack{TaskID: task, StepID: "s1", StepIntent: "注册账号建立立足点",
		ActionLevel: rings.ActionModifyPersist, Authorization: RoE{}}
}

// TestStepPassAfterApproval 批准一次后本步骤后续命令直接放行:首条弹
// 门(意图照审)→ 批准武装 → 重发同调用走单次配额 → 不同命令(含查
// 看类)直接执行、逐次 step_pass 留痕;待决表不再增长。
func TestStepPassAfterApproval(t *testing.T) {
	const task = "steppass-basic"
	store, ex, gate := stepPassEnv(t, task)
	ctx := context.Background()

	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := mkL4Pack(task)

	// 首条命令:审批门槛弹门(门第一次照弹)。
	d := env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"curl -X POST -d 'u=x' http://t.local:9902/register"}`), permission.Context{})
	if d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("首条命令应弹门: %+v", d)
	}
	req := gate.Pending(task)[0]
	if req.GateGroup != "" {
		t.Fatalf("审批门槛弹门不带组名: %+v", req)
	}
	if err := gate.Decide(ctx, task, req.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	env.armGateApproval(req)
	env.park = nil
	if env.stepPass == nil {
		t.Fatalf("审批门槛批准应武装同步骤放行")
	}

	// 重发同调用:单次配额放行一次。
	d = env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"curl -X POST -d 'u=x' http://t.local:9902/register"}`), permission.Context{})
	if d.Behavior != permission.Allow || env.allowance != nil {
		t.Fatalf("重发同调用应走单次配额: %+v", d)
	}

	// 后续不同命令(含纯查看类)直接放行执行、逐次留痕。
	for i, cmd := range []string{
		`{"command":"pwd"}`,
		`{"command":"ls -la /tmp"}`,
		`{"command":"curl -s http://t.local:9902/login"}`,
	} {
		before := len(approvalEvents(t, store, task))
		d := env.approvalGate(pack)(ctx, "bash", json.RawMessage(cmd), permission.Context{})
		if d.Behavior != permission.Allow {
			t.Fatalf("第 %d 条后续命令应直接放行: %+v", i+1, d)
		}
		recs := approvalEvents(t, store, task)
		if len(recs) != before+1 || recs[len(recs)-1].Type != rings.ApprovalRecStepPass {
			t.Fatalf("后续命令应逐次 step_pass 留痕: %+v", recs)
		}
		last := recs[len(recs)-1]
		if !strings.Contains(string(last.ToolArgs), cmd[12:len(cmd)-2]) ||
			!strings.Contains(last.Reason, req.ID) || last.DecidedBy != "creator" {
			t.Fatalf("step_pass 留痕应带命令原文与批准回指: %+v", last)
		}
	}

	// 待决表不再增长(全程只首条弹门)。
	if left := gate.Pending(task); len(left) != 0 {
		t.Fatalf("后续命令不应再弹门: %+v", left)
	}
}

// TestStepPassNewUnitRegates 新单位重新弹门:步骤级放行随单位销毁失效,
// 同任务新单位(u2)的同步骤命令重新过门。
func TestStepPassNewUnitRegates(t *testing.T) {
	const task = "steppass-units"
	_, ex, gate := stepPassEnv(t, task)
	ctx := context.Background()
	pack := mkL4Pack(task)

	u1 := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	d := u1.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"curl -X POST http://t.local:9902/register"}`), permission.Context{})
	if d.Behavior != permission.Deny || u1.park == nil {
		t.Fatalf("u1 首条命令应弹门: %+v", d)
	}
	req := gate.Pending(task)[0]
	_ = gate.Decide(ctx, task, req.ID, GateDecision{Approved: true, By: "creator"})
	u1.armGateApproval(req)
	u1.park = nil

	// u2:同步骤新单位,重新弹门(不继承 u1 的步骤级放行)。
	u2 := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u2", TranscriptID: "swarm-u2"}
	d = u2.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"pwd"}`), permission.Context{})
	if d.Behavior != permission.Deny || u2.park == nil || u2.stepPass != nil {
		t.Fatalf("新单位应重新弹门且无步骤放行继承: %+v pass=%v", d, u2.stepPass)
	}
	if pending := gate.Pending(task); len(pending) != 1 || pending[0].UnitID != "u2" {
		t.Fatalf("新单位的弹门请求应在待决表: %+v", pending)
	}
}

// TestStepPassUserGateRuleExcluded 用户自建 gate 规则回归(P4-17 语义
// 不漂移):其批准只武装单次配额,不武装步骤级放行——不同参数照旧
// 重新弹门。
func TestStepPassUserGateRuleExcluded(t *testing.T) {
	const task = "steppass-custom"
	store, ex, gate := stepPassEnv(t, task)
	ctx := context.Background()

	if _, err := SaveHardRule(ctx, store, HardRule{
		Group: HardGroupHTTPKill, Name: "自建 危险端点复核", Pattern: `\bp427-gate-cmd\b`,
		ToolScope: "bash", Action: HardActionGate, Enabled: true,
	}, "creator"); err != nil {
		t.Fatalf("保存自建 gate 规则失败: %v", err)
	}

	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := mkL4Pack(task)

	args := json.RawMessage(`{"command":"echo p427-gate-cmd one"}`)
	d := env.approvalGate(pack)(ctx, "bash", args, permission.Context{})
	if d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("自建规则应弹门: %+v", d)
	}
	req := gate.Pending(task)[0]
	_ = gate.Decide(ctx, task, req.ID, GateDecision{Approved: true, By: "creator"})
	env.armGateApproval(req)
	env.park = nil
	if env.stepPass != nil {
		t.Fatalf("自建 gate 规则批准不武装步骤级放行(P4-17 语义)")
	}

	// 同参数:单次配额;不同参数:重新弹门。
	if d = env.approvalGate(pack)(ctx, "bash", args, permission.Context{}); d.Behavior != permission.Allow {
		t.Fatalf("同参数应走配额: %+v", d)
	}
	if d = env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"echo p427-gate-cmd two"}`), permission.Context{}); d.Behavior != permission.Deny || env.park == nil {
		t.Fatalf("不同参数应重新弹门: %+v", d)
	}
}

// TestStepPassReadOnlyNoGate 只读步骤回归:门槛之下的分级(默认门槛
// L4,L1 命令)零弹门直接放行。
func TestStepPassReadOnlyNoGate(t *testing.T) {
	const task = "steppass-ro"
	_, ex, gate := stepPassEnv(t, task)
	ctx := context.Background()

	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	pack := &TaskPack{TaskID: task, StepID: "s1", StepIntent: "查看目录结构",
		ActionLevel: rings.ActionReadOnly, Authorization: RoE{}}
	d := env.approvalGate(pack)(ctx, "bash", json.RawMessage(`{"command":"pwd && ls"}`), permission.Context{})
	if d.Behavior != permission.Allow {
		t.Fatalf("只读步骤应零弹门放行: %+v", d)
	}
	if pending := gate.Pending(task); len(pending) != 0 {
		t.Fatalf("只读步骤不应有弹门请求: %+v", pending)
	}
}

// TestStepPassE2E u94 场景全链(修改与持久化步骤,步骤内 10 条命令):
// 首条弹门批准后,其余 9 条(含 pwd/ls 纯查看)直接执行零弹门、逐次
// 留痕;审批流恰 requested+approved+step_pass×9。
func TestStepPassE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "steppass-e2e"

	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "注册账号建立立足点", "g1", "修改与持久化", "target")
	base := fmt.Sprintf("http://%s:%d", host, port)

	commands := []string{
		fmt.Sprintf(`{"command":"curl -X POST -d 'u=a&p=b' %s/register"}`, base), // 首条:弹门
		fmt.Sprintf(`{"command":"curl -X POST -d 'u=a&p=b' %s/register"}`, base), // 批准后重发:配额
		`{"command":"pwd"}`, // 以下 8 条:直接放行
		`{"command":"ls -la"}`,
		`{"command":"cat /etc/hostname"}`,
		fmt.Sprintf(`{"command":"curl -s %s/"}`, base),
		fmt.Sprintf(`{"command":"curl -s %s/login"}`, base),
		`{"command":"id"}`,
		fmt.Sprintf(`{"command":"curl -s -o /dev/null -w %%{http_code} %s/admin"}`, base),
		`{"command":"whoami"}`,
	}

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		n := bashResults(rr)
		if n < len(commands) {
			return scriptedReply{ToolCalls: []scriptedToolCall{
				{ID: fmt.Sprintf("b%d", n+1), Name: "bash", Args: commands[n]},
			}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "注册账号完成,立足点已建立。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 20, RunSeconds: 60, WrapupMaxTurns: 2})
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

	// 首条命令弹门(恰一条,无组名)。
	waitFor(t, 30*time.Second, "首条弹门登记", func() bool { return len(gate.Pending(task)) == 1 })
	pending := gate.Pending(task)[0]
	if pending.GateGroup != "" {
		t.Fatalf("审批门槛弹门不带组名: %+v", pending)
	}
	if err := gate.Decide(ctx, task, pending.ID, GateDecision{Approved: true, By: "creator", Reason: "允许注册"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	done.Wait()
	if rerr != nil {
		t.Fatalf("执行失败: %v", rerr)
	}
	if res.StepStatus != "done" {
		t.Fatalf("批准后续跑应完成: %+v", res)
	}

	// 全程零追加弹门。
	if left := gate.Pending(task); len(left) != 0 {
		t.Fatalf("后续命令不应再弹门: %+v", left)
	}
	// 审批流:requested + approved + step_pass×8(批准重发走配额不另留
	// 留痕;10 条命令 = 1 弹门 + 1 配额重发 + 8 步骤放行)。
	recs := approvalEvents(t, store, task)
	pass := 0
	for _, r := range recs {
		if r.Type == rings.ApprovalRecStepPass {
			pass++
		}
	}
	if len(recs) != 2+8 || pass != 8 {
		t.Fatalf("审批流应恰 requested+approved+step_pass×8: 共 %d 笔,step_pass %d 笔", len(recs), pass)
	}
	// 执行面:10 条 bash 记录(1 条挂起暂拒 + 9 条真实执行)。
	calls := toolCallRecords(t, store, task, "bash")
	if len(calls) != 10 || !calls[0].IsError || successfulToolCalls(calls) != 9 {
		t.Fatalf("应恰 10 条 bash 记录(1 暂拒 + 9 执行): 共 %d 条", len(calls))
	}
}
