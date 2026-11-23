package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"path/filepath"
	"strings"
	"sync"

	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P5-05 挂等让位调度(五分钟宽限):五类挂等源 × 两分支类级 ----
//
//	每源:宽限内解除=原位继续(session_started 恰 1 次、名额未让)/
//	超时让位(步骤停等态、goroutine 退出、票/闩保留、计费停走)+解除
//	后队首上位(原会话 Resume、领取次序可证);竞态互斥两序;拒绝分族;
//	计时对账;冬眠/终局边界。GraceYield 测试旋钮只压时序不改语义。

// p505SessionStarts 步骤的 session_started 记录数(同转录续跑计数用)。
func p505SessionStarts(t *testing.T, store *rings.Store, task, step string) (int, string) {
	t.Helper()
	n := 0
	tid := ""
	for _, rec := range swarmRecords(t, store, task) {
		if rec.StepID == step && rec.Type == rings.SwarmRecSessionStarted {
			n++
			tid = rec.TranscriptID
		}
	}
	return n, tid
}

// p505LatestYield 步骤最近一笔 session_finished 的 Yield 标记(无则 nil)。
func p505LatestYield(t *testing.T, store *rings.Store, task, step string) *rings.YieldNote {
	t.Helper()
	var yn *rings.YieldNote
	for _, rec := range swarmRecords(t, store, task) {
		if rec.StepID == step && rec.Type == rings.SwarmRecSessionEnd {
			yn = rec.Yield
		}
	}
	return yn
}

// p505HasBlackboxYield 黑匣子是否留有指定来源的让位记录。
func p505HasBlackboxYield(t *testing.T, store *rings.Store, task, source string) bool {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var r rings.BlackboxRecord
		if json.Unmarshal(ev.Payload, &r) != nil || r.Type != rings.BlackboxUnitYield {
			continue
		}
		if s, _ := r.Detail["source"].(string); s == source || source == "" {
			return true
		}
	}
	return false
}

// ---- 弹门卡两分支 + 竞态互斥 ----

// TestP505GateGraceApproveInPlace 完成标准①弹门卡分支 A:宽限内批准 =
// 原位继续(session_started 恰 1 次、放行配额消费、零开销)。
func TestP505GateGraceApproveInPlace(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-gate-inplace"
	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "写入落地文件验证", "g1", "修改与持久化", "target")

	const marker = "p505-gate-ok"
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "bash", marker) {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash",
				Args: fmt.Sprintf(`{"command":"echo %s > m.txt && cat m.txt"}`, marker),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	model.requireBashApproval()
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 8})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ex.GraceYield = 3 * time.Second

	var res *RunResult
	var rerr error
	done := make(chan struct{})
	go func() { defer close(done); res, rerr = ex.RunStep(ctx, task, "s1") }()
	waitFor(t, 5*time.Second, "弹门请求登记", func() bool { return len(gate.Pending(task)) == 1 })
	pending := gate.Pending(task)[0]

	if err := gate.Decide(ctx, task, pending.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("宽限内批准应原位继续完成")
	}
	if rerr != nil || res.StepStatus != "done" {
		t.Fatalf("原位继续应完成: %v %+v", rerr, res)
	}
	if n, _ := p505SessionStarts(t, store, task, "s1"); n != 1 {
		t.Fatalf("原位继续应零次换人,得 %d 次", n)
	}
	if calls := toolCallRecords(t, store, task, "bash"); successfulToolCalls(calls) != 1 {
		t.Fatalf("批准后应恰执行一次: %+v", calls)
	}
	if steps, _ := store.NodesByType(ctx, task, rings.NodeStep, rings.StepGateWait); len(steps) != 0 {
		t.Fatalf("宽限内批准不得让位")
	}
}

// TestP505GateGraceTimeoutYieldApproveWake 完成标准①弹门卡分支 B +
// ②让位先到序:超时让位(步骤 gate_wait、票留待决表、goroutine 退出、
// 计费停走、黑匣子入账)→ 批准 → 已批准待上位(dispatched+队首档)→
// 原会话 Resume 上位、放行配额消费、领取次序可证。
func TestP505GateGraceTimeoutYieldApproveWake(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-gate-yield"
	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "写入落地文件验证", "g1", "修改与持久化", "target")

	const marker = "p505-gate-yield-ok"
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "bash", marker) {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash",
				Args: fmt.Sprintf(`{"command":"echo %s > m.txt && cat m.txt"}`, marker),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	model.requireBashApproval()
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 10})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ex.GraceYield = 300 * time.Millisecond

	var res *RunResult
	var rerr error
	done := make(chan struct{})
	go func() { defer close(done); res, rerr = ex.RunStep(ctx, task, "s1") }()

	// 超时让位:RunStep 返回=goroutine 退出、名额释放;步骤 gate_wait、
	// 票留待决表面板可裁。
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("宽限耗尽应让位返回")
	}
	if rerr != nil || res == nil {
		t.Fatalf("让位收场不是错误: %v %+v", rerr, res)
	}
	if res.StepStatus != rings.StepGateWait || !res.YieldWait || res.Yield == nil ||
		res.Yield.Source != "approval" || res.Yield.Request == "" || res.Yield.ToolName != "bash" {
		t.Fatalf("应落 gate_wait 让位(Yield 上下文齐全): %+v %+v", res, res.Yield)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != rings.StepGateWait {
		t.Fatalf("步骤应落 gate_wait: %+v", step)
	}
	if len(gate.Pending(task)) != 1 {
		t.Fatalf("让位后票应留待决表(面板可裁): %d", len(gate.Pending(task)))
	}
	if !p505HasBlackboxYield(t, store, task, "approval") {
		t.Fatalf("让位应入黑匣子(unit_yield/approval)")
	}
	yn := p505LatestYield(t, store, task, "s1")
	if yn == nil || yn.Request != res.Yield.Request {
		t.Fatalf("session_finished 应携带 Yield 标记: %+v", yn)
	}
	// 停等不可领取。
	if _, err := ex.RunStep(ctx, task, "s1"); err == nil {
		t.Fatalf("停等态步骤不得领取")
	}

	// 让位先到 → 批准走已批准待上位:Decide 记账 + 唤醒(dispatched+
	// 队首档 + head_seq)。
	ticket := gate.Pending(task)[0]
	if err := gate.Decide(ctx, task, ticket.ID, GateDecision{Approved: true, By: "creator", Reason: "允许"}); err != nil {
		t.Fatalf("让位后批准失败: %v", err)
	}
	step, _ = store.Node(ctx, task, "s1")
	if step.Status != "dispatched" {
		t.Fatalf("让位后批准应唤醒回可领取: %+v", step)
	}
	if head, _ := step.Detail["scope_head"].(bool); !head {
		t.Fatalf("唤醒应带队首档标记: %+v", step.Detail)
	}
	if _, ok := step.Detail["head_seq"]; !ok {
		t.Fatalf("唤醒应带进档序: %+v", step.Detail)
	}
	if len(gate.Pending(task)) != 0 {
		t.Fatalf("裁决后票应出待决表")
	}

	// 队首上位:重领原会话 Resume(同转录二次起飞)+ 放行配额消费被拦
	// 命令 + 批准续跑指令入转录。
	res2, rerr2 := ex.RunStep(ctx, task, "s1")
	if rerr2 != nil || res2 == nil || res2.StepStatus != "done" {
		t.Fatalf("上位续跑应完成: %v %+v", rerr2, res2)
	}
	if n, tid := p505SessionStarts(t, store, task, "s1"); n != 2 || tid == "" {
		t.Fatalf("原会话应二次起飞续跑,得 %d 次", n)
	}
	if calls := toolCallRecords(t, store, task, "bash"); successfulToolCalls(calls) != 1 {
		t.Fatalf("被拦命令应恰执行一次: %+v", calls)
	}
	raw := readTranscriptRaw(t, filepath.Join(dir, "tasks"), task, "swarm-u1")
	if !strings.Contains(raw, "审批结果 · 续跑") {
		t.Fatalf("续跑指令应入原会话转录(让位上位可审计)")
	}
	// 计时:宽限等待计入 gate_wait、计费停走(billable 近零)。
	note := finishNoteOf(t, store, task, "s1")
	if gw := noteField(note, "gate_wait"); gw == "" {
		t.Fatalf("gate_wait 应有值: %s", note)
	}
}

// TestP505GateYieldDenyFeedbackContinue 完成标准③:弹门拒(让位后到)
// ——动作类拒绝只杀动作不杀单位:回队首+换路反馈续跑,单位换路完成。
func TestP505GateYieldDenyFeedbackContinue(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-gate-deny"
	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "写入落地文件验证", "g1", "修改与持久化", "target")

	// 模型:先发被拦命令;见到拒绝反馈(换路指引)后改走只读探测收尾。
	const marker = "p505-deny-route"
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if seenToolResult(rr, "port_probe", "open") {
			return scriptedReply{Text: "已换只读方向完成意图。", Finish: "stop"}
		}
		if seenFeedback(rr, "动作被拒") {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "p1", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "b1", Name: "bash",
			Args: `{"command":"echo denied-route > m.txt"}`,
		}}, Finish: "tool_calls"}
	})
	model.requireBashApproval()
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 10})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ex.GraceYield = 250 * time.Millisecond

	var res *RunResult
	done := make(chan struct{})
	go func() { defer close(done); var e error; res, e = ex.RunStep(ctx, task, "s1"); _ = e }()
	waitFor(t, 5*time.Second, "弹门请求登记", func() bool { return len(gate.Pending(task)) == 1 })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("宽限耗尽应让位返回")
	}
	if res.StepStatus != rings.StepGateWait {
		t.Fatalf("应落 gate_wait: %+v", res)
	}
	// 让位后拒绝:回队首(与批准同档——重领后反馈分流)。
	ticket := gate.Pending(task)[0]
	if err := gate.Decide(ctx, task, ticket.ID, GateDecision{Approved: false, By: "creator", Reason: "不要写文件"}); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != "dispatched" || step.Detail["scope_head"] != true {
		t.Fatalf("动作类拒绝应回队首续跑: %+v", step)
	}
	// 重领:拒绝反馈注入原会话,模型换路只读完成。
	res2, rerr2 := ex.RunStep(ctx, task, "s1")
	if rerr2 != nil || res2 == nil || res2.StepStatus != "done" {
		t.Fatalf("换路续跑应完成: %v %+v", rerr2, res2)
	}
	raw := readTranscriptRaw(t, filepath.Join(dir, "tasks"), task, "swarm-u1")
	if !strings.Contains(raw, "动作被拒") || !strings.Contains(raw, "不要写文件") {
		t.Fatalf("拒绝反馈(含理由)应注入原会话")
	}
	if calls := toolCallRecords(t, store, task, "bash"); successfulToolCalls(calls) != 0 {
		t.Fatalf("被拒动作不得执行: %+v", calls)
	}
}

// seenFeedback 会话历史(user 消息)中是否已含某反馈标记。
func seenFeedback(rr *recordedRequest, marker string) bool {
	for _, msg := range rr.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, marker) {
			return true
		}
	}
	return false
}

// TestP505GateBoundaryRaceApprovalFirst 完成标准②批准先到序:第 5 分钟
// 边界批准先于让位判定 → 让位取消、原位继续(票消费、零换人)。
func TestP505GateBoundaryRaceApprovalFirst(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-race-approve-first"
	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "写入落地文件验证", "g1", "修改与持久化", "target")

	const marker = "p505-race-ok"
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if !seenToolResult(rr, "bash", marker) {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "b1", Name: "bash",
				Args: fmt.Sprintf(`{"command":"echo %s > m.txt && cat m.txt"}`, marker),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	model.requireBashApproval()
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 8})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ex.GraceYield = 600 * time.Millisecond

	var res *RunResult
	var rerr error
	done := make(chan struct{})
	go func() { defer close(done); res, rerr = ex.RunStep(ctx, task, "s1") }()
	waitFor(t, 5*time.Second, "弹门请求登记", func() bool { return len(gate.Pending(task)) == 1 })
	// 边界内拍批准(恰在宽限线上、让位定时器将触未触)。
	ticket := gate.Pending(task)[0]
	time.Sleep(400 * time.Millisecond)
	if err := gate.Decide(ctx, task, ticket.ID, GateDecision{Approved: true, By: "creator"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("批准先到应原位继续完成")
	}
	if rerr != nil || res.StepStatus != "done" {
		t.Fatalf("批准先到不让位: %v %+v", rerr, res)
	}
	if n, _ := p505SessionStarts(t, store, task, "s1"); n != 1 {
		t.Fatalf("让位取消应零换人,得 %d 次", n)
	}
	if steps, _ := store.NodesByType(ctx, task, rings.NodeStep, rings.StepGateWait); len(steps) != 0 {
		t.Fatalf("批准先到不得落停等态")
	}
}

// TestP505GateTicketRaceStress 完成标准②竞态互斥类级:第 5 分钟边界
// 批准/让位真并发(随机时序多轮)——同锁串行保证不变式:裁决不丢
// (要么消费续跑、要么让位后 Decide 唤醒),永无"停等态+已裁票+无唤醒"
// 的丢裁决形态。
func TestP505GateTicketRaceStress(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-race-stress"
	gate := NewLedgerGate(store)
	const grace = 120 * time.Millisecond
	const rounds = 120
	for i := 0; i < rounds; i++ {
		ticket, err := gate.Enter(ApprovalRequest{
			TaskID: task, StepID: "s1", UnitID: "u1",
			Intent: "竞态", ActionLevel: rings.ActionModifyPersist,
			ToolName: "bash", ToolArgs: `{"command":"id"}`,
		})
		if err != nil {
			t.Fatalf("登记失败: %v", err)
		}
		// 并发裁决:恰在宽限边界±窗内随机到达。
		offset := time.Duration(randInt64(int64(grace/2)+40)) - 20*time.Millisecond
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(abs64(int64(offset))))
			_ = gate.Decide(ctx, task, ticket.Req.ID, GateDecision{Approved: true, By: "creator"})
		}()
		d, werr, yielded := ticket.WaitGrace(ctx, grace)
		if werr != nil {
			t.Fatalf("等待不应被取消: %v", werr)
		}
		if !yielded {
			// 让位取消:裁决必在手上(批准先到)。
			if !d.Approved {
				t.Fatalf("第 %d 轮:非让位路径应消费到批准裁决", i)
			}
		} else {
			// 让位先到:票已标记,Decide 必走唤醒路径(步骤回队首);
			// 等并发 Decide 完成后核验唤醒落账。
			wg.Wait()
			step, err := store.Node(ctx, task, "s1")
			if err != nil || step.Status != "dispatched" || step.Detail["scope_head"] != true {
				t.Fatalf("第 %d 轮:让位后裁决应唤醒步骤回队首: %v %+v", i, err, step)
			}
			// 复位步骤态供下一轮(sweep 等价:直接迁移回 gate_wait 模拟
			// 下一只单位的让位)。复位走真实事件流。
			if _, err := store.Append(ctx, task, rings.Event{
				Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
				Payload: rings.NodeStateChanged{NodeID: "s1", Status: rings.StepGateWait, Note: ""},
			}); err != nil {
				t.Fatal(err)
			}
		}
		wg.Wait()
	}
}

func randInt64(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return time.Now().UnixNano() % n
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---- 模型故障两分支 ----

// TestP505ModelGraceRetryInPlace 分支 A:宽限内模型恢复(退避重试中
// 转好)→ 原位续试完成,零让位零换人,block_wait 记失败段。
func TestP505ModelGraceRetryInPlace(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-model-inplace"
	seedGraph(t, store, task, "探测本地靶标端口状态")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "意图执行完成。", Finish: "stop"}
	})
	defer model.Close()
	gw := newFlakyGateway(t, model, 2, http.StatusServiceUnavailable)
	prov, err := gw.provider("scripted-flaky")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.ModelRetryLadder = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	ex.BlockedDeepAfter = time.Hour // 深阈值不触发;默认宽限 5 分钟内恢复

	res, err := ex.RunStep(ctx, task, "s1")
	if err != nil || res.StepStatus != "done" {
		t.Fatalf("宽限内恢复应原位续试完成: %v %+v", err, res)
	}
	if n, _ := p505SessionStarts(t, store, task, "s1"); n != 1 {
		t.Fatalf("原位续试应零换人,得 %d 次", n)
	}
	if steps, _ := store.NodesByType(ctx, task, rings.NodeStep, rings.StepModelWait); len(steps) != 0 {
		t.Fatalf("宽限内恢复不得让位")
	}
	note := finishNoteOf(t, store, task, "s1")
	if bw := noteField(note, "block_wait"); bw == "" {
		t.Fatalf("失败段应计入 block_wait: %s", note)
	}
}

// TestP505ModelGraceTimeoutYieldRecover 分支 B+完成标准④:退避等待跨
// 宽限线 → 冻结收场让位(model_wait 停等、名额释放、计费停走)→ 模型
// 恢复(链就绪,由主宰循环轮对账唤醒——此处以同款事件模拟)→ 队首
// 上位原会话 Resume 续跑完成。
func TestP505ModelGraceTimeoutYieldRecover(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-model-yield"
	seedGraph(t, store, task, "探测本地靶标端口状态")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{Text: "恢复后一次完成。", Finish: "stop"}
	})
	defer model.Close()
	// 注入网关:前 2 枪 503(第 1 枪失败→宽限内短退避→第 2 枪失败→
	// 下一轮退避跨宽限线→让位);恢复后(第 3 枪起)透传。
	gw := newFlakyGateway(t, model, 2, http.StatusServiceUnavailable)
	prov, err := gw.provider("scripted-flaky")
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 8})
	ex.ModelRetryLadder = []time.Duration{100 * time.Millisecond, time.Hour} // 第 2 次退避跨线
	ex.BlockedDeepAfter = time.Hour
	ex.GraceYield = 500 * time.Millisecond

	res, rerr := ex.RunStep(ctx, task, "s1")
	if rerr != nil || res == nil {
		t.Fatalf("让位收场不是错误: %v %+v", rerr, res)
	}
	if res.StepStatus != rings.StepModelWait || !res.YieldWait || res.Yield == nil || res.Yield.Source != "model" {
		t.Fatalf("退避跨宽限应让位 model_wait: %+v", res)
	}
	step, _ := store.Node(ctx, task, "s1")
	if step.Status != rings.StepModelWait {
		t.Fatalf("步骤应落 model_wait: %+v", step)
	}
	if !p505HasBlackboxYield(t, store, task, "model") {
		t.Fatalf("让位应入黑匣子(unit_yield/model)")
	}
	// 停等不可领取(冷却期不重复领取占坑)。
	if _, err := ex.RunStep(ctx, task, "s1"); err == nil {
		t.Fatalf("model_wait 不得领取")
	}

	// 恢复:循环轮对账唤醒(overmind.resolveModelWaits 同款事件——
	// dispatched+scope_head)。
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "overmind",
		Payload: rings.NodeStateChanged{NodeID: "s1", Status: "dispatched", Note: "scope_head"},
	}); err != nil {
		t.Fatal(err)
	}
	step, _ = store.Node(ctx, task, "s1")
	if step.Status != "dispatched" || step.Detail["scope_head"] != true {
		t.Fatalf("恢复应唤醒回队首档: %+v", step)
	}

	// 队首上位:原会话 Resume(恢复续跑指令入转录),一次完成。
	res2, rerr2 := ex.RunStep(ctx, task, "s1")
	if rerr2 != nil || res2 == nil || res2.StepStatus != "done" {
		t.Fatalf("恢复上位应完成: %v %+v", rerr2, res2)
	}
	if n, tid := p505SessionStarts(t, store, task, "s1"); n != 2 || tid == "" {
		t.Fatalf("原会话应二次起飞续跑,得 %d 次", n)
	}
	raw := readTranscriptRaw(t, filepath.Join(dir, "tasks"), task, "swarm-u1")
	if !strings.Contains(raw, "恢复 · 续跑") {
		t.Fatalf("恢复续跑指令应入原会话转录")
	}
	// 计时:让位段 block_wait 记退避等待、billable 近零。
	note := finishNoteOf(t, store, task, "s1")
	if bw := noteField(note, "block_wait"); bw == "" {
		t.Fatalf("退避等待应计入 block_wait: %s", note)
	}
}

// ---- 人工暂停两分支 ----

// TestP505PauseGraceTimeoutYieldResume 分支 B+完成标准④:暂停挂等超
// 宽限 → 冻结收场让位(pause_wait 停等、闩转移清零、名额释放、计费停
// 走)→ 操作者点继续(ResumeUnit 停等态唤醒)→ 队首上位原会话续跑。
// (分支 A 宽限内继续原位接着干:既有 TestUnitPauseResumeInPlace 回归
// 样本,P5-05 语义不变。)
func TestP505PauseGraceTimeoutYieldResume(t *testing.T) {
	h := newParkHarness(t, "p505-pause-yield", 8)
	h.exec.GraceYield = 400 * time.Millisecond
	ctx := context.Background()

	if err := h.exec.PauseUnit(ctx, h.task, "s1", "creator", "验证暂停让位"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	close(h.hold)
	h.waitParkSignature(t, 5*time.Second)

	// 超时让位:RunStep 返回=goroutine 退出、名额释放;步骤 pause_wait、
	// 闩已转移清零。
	res, err := h.waitUnitLanded(t, 10*time.Second)
	if err != nil {
		t.Fatalf("让位收场失败: %v", err)
	}
	if res.StepStatus != rings.StepPauseWait || !res.YieldWait || res.Yield == nil || res.Yield.Source != "pause" {
		t.Fatalf("暂停超宽限应让位 pause_wait: %+v", res)
	}
	st, _ := h.store.Node(ctx, h.task, "s1")
	if st.Status != rings.StepPauseWait {
		t.Fatalf("步骤应落 pause_wait: %+v", st)
	}
	if h.exec.UnitPaused(h.task, "s1") {
		t.Fatalf("让位后暂停闩应转移清零(继续按钮走停等态通道)")
	}
	if !p505HasBlackboxYield(t, h.store, h.task, "pause") {
		t.Fatalf("让位应入黑匣子(unit_yield/pause)")
	}
	// 停等不可领取。
	if _, err := h.exec.RunStep(ctx, h.task, "s1"); err == nil {
		t.Fatalf("pause_wait 不得领取")
	}

	// 操作者点继续:停等态唤醒回队首档。
	if err := h.exec.ResumeUnit(ctx, h.task, "s1", "creator"); err != nil {
		t.Fatalf("停等态继续失败: %v", err)
	}
	st, _ = h.store.Node(ctx, h.task, "s1")
	if st.Status != "dispatched" || st.Detail["scope_head"] != true {
		t.Fatalf("继续应唤醒回队首档: %+v", st)
	}

	// 队首上位:重领原会话 Resume,续跑指令入转录,完成。
	res2, rerr2 := h.exec.RunStep(ctx, h.task, "s1")
	if rerr2 != nil || res2 == nil || res2.StepStatus != "done" {
		t.Fatalf("继续上位应完成: %v %+v", rerr2, res2)
	}
	if n, tid := p505SessionStarts(t, h.store, h.task, "s1"); n != 2 || tid == "" {
		t.Fatalf("原会话应二次起飞续跑,得 %d 次", n)
	}
	raw := readTranscriptRaw(t, h.root, h.task, "swarm-u1")
	if !strings.Contains(raw, "单位继续 · 续跑") {
		t.Fatalf("继续续跑指令应入原会话转录")
	}
	// 计时:暂停挂等计入 pause_wait。
	note := h.sessionNote(t)
	if pw := noteField(note, "pause_wait"); pw == "" {
		t.Fatalf("pause_wait 应有值: %s", note)
	}
}

// ---- 冬眠/终局边界(完成标准⑥) ----

// TestP505HibernateDuringGateGrace 冬眠在弹门宽限期中:收场放行令
// (RetireTickets)→ 单位 checkpointed 收场(票面保持待裁决冻结)——
// P4-33 纪律回归。
func TestP505HibernateDuringGateGrace(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-hiber-grace"
	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "写入落地文件验证", "g1", "修改与持久化", "target")

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "b1", Name: "bash", Args: `{"command":"echo x > m.txt"}`,
		}}, Finish: "tool_calls"}
	})
	model.requireBashApproval()
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 8})
	gate := NewLedgerGate(store)
	ex.Gate = gate
	ex.GraceYield = 5 * time.Second

	var res *RunResult
	var rerr error
	done := make(chan struct{})
	go func() { defer close(done); res, rerr = ex.RunStep(ctx, task, "s1") }()
	waitFor(t, 5*time.Second, "弹门请求登记", func() bool { return len(gate.Pending(task)) == 1 })

	// 冬眠收场:放行令送达 → 单位 checkpointed 收场落地。
	gate.RetireTickets(task)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("冬眠收场令应令单位落地")
	}
	if rerr != nil || res == nil || !res.Checkpointed || res.StepStatus != "dispatched" {
		t.Fatalf("冬眠宽限期收场应 checkpointed: %v %+v", rerr, res)
	}
	if steps, _ := store.NodesByType(ctx, task, rings.NodeStep, rings.StepGateWait); len(steps) != 0 {
		t.Fatalf("冬眠收场不落 gate_wait(票冻结留待唤醒清出)")
	}
}

// TestP505HibernateReleasesGateWaits 冬眠唤醒放行:已让位的 gate_wait
// 步骤随清票放行回可领取(重领后重新过门立新票)。
func TestP505HibernateReleasesGateWaits(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "p505-hiber-release"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "意图", Status: rings.StepGateWait}},
	); err != nil {
		t.Fatal(err)
	}
	gate := NewLedgerGate(store)
	ticket, err := gate.Enter(ApprovalRequest{
		TaskID: task, StepID: "s1", UnitID: "u1", Intent: "意图",
		ActionLevel: rings.ActionModifyPersist, ToolName: "bash", ToolArgs: `{"command":"id"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = ticket

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = gate
	// 唤醒次序:清票 → 放行停等步骤(与 manager.Wake 同款)。
	ex.DropPendingApprovals(task)
	if got := ex.ReleaseGateWaits(task); got != 1 {
		t.Fatalf("应放行 1 个 gate_wait 步骤,得 %d", got)
	}
	st, _ := store.Node(ctx, task, "s1")
	if st.Status != "dispatched" || st.Detail["scope_head"] != true {
		t.Fatalf("冬眠唤醒应放行 gate_wait 回队首: %+v", st)
	}
	if len(gate.Pending(task)) != 0 {
		t.Fatalf("票应已清出")
	}
	// 幂等:二次放行零落笔。
	if got := ex.ReleaseGateWaits(task); got != 0 {
		t.Fatalf("放行应幂等,得 %d", got)
	}
}
