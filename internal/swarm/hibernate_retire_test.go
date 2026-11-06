package swarm

// P4-33 冬眠让位·虫群侧:任务冬眠改冻结收场——审批票上的单位经
// Released 令收场落地(步骤回可领取、session_finished 带 Checkpointed,
// 与断点收场同款),票面保持待裁决冻结(零裁决记录);唤醒清出无主票
// (零留痕,等价重启后内存票据清空);熔断结案按拒绝留痕。重领后原会话
// 续跑(唤醒续跑与重启收养的单位侧行为一致)。

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ---- 门三方法白盒:RetireTickets / DropTickets / ExpireTickets ----

func TestGateHibernateTicketLifecycle(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hiber-gate-unit"
	gate := NewLedgerGate(store)
	hiberGate := hiber.New()
	gate.Hiber = hiberGate // 装配同款(Decide 冻结检查的门引用)

	tk, err := gate.Enter(ApprovalRequest{
		TaskID: task, StepID: "s1", UnitID: "u1", Intent: "冬眠放行验证",
		ActionLevel: rings.ActionModifyPersist, ToolName: "bash", ToolArgs: `{"command":"id"}`,
	})
	if err != nil {
		t.Fatalf("Enter 失败: %v", err)
	}
	// 冬眠中:面板裁决冻结(只读)。
	hiberGate.Enter(task, "started")
	if err := gate.Decide(ctx, task, tk.Req.ID, GateDecision{Approved: true}); !errors.Is(err, ErrGateFrozen) {
		t.Fatalf("冬眠中裁决应被冻结,得到 %v", err)
	}

	// 收场放行:等待方收到 Released(非裁决),票面保持待决、零裁决记录。
	var (
		wg   sync.WaitGroup
		got  GateDecision
		gotE error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, gotE = tk.Wait(ctx)
	}()
	if n := gate.RetireTickets(task); n != 1 {
		t.Fatalf("收场放行应送达 1 票,得到 %d", n)
	}
	wg.Wait()
	if gotE != nil || !got.Released || got.Approved {
		t.Fatalf("Wait 应收到 Released 放行令(非裁决): %+v err=%v", got, gotE)
	}
	if len(gate.Pending(task)) != 1 {
		t.Fatalf("放行后票面应保持待决(冻结只读): %d", len(gate.Pending(task)))
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 1 || recs[0].Type != rings.ApprovalRecRequested {
		t.Fatalf("放行零裁决记录(账本 requested 原样留痕): %+v", recs)
	}

	// 唤醒清出:票不可再裁(持有单位已让位),清出零留痕。
	hiberGate.Exit(task)
	if n := gate.DropTickets(task); n != 1 {
		t.Fatalf("唤醒应清出 1 票,得到 %d", n)
	}
	if len(gate.Pending(task)) != 0 {
		t.Fatalf("清出后不得残留待决")
	}
	if recs := approvalEvents(t, store, task); len(recs) != 1 {
		t.Fatalf("清出零新增记录(等价重启后内存票据清空): %+v", recs)
	}

	// 熔断结案:按拒绝留痕(closeout,与挂起单位被 cancel 的口径一致)。
	if _, err := gate.Enter(ApprovalRequest{
		TaskID: task, StepID: "s2", UnitID: "u2", Intent: "熔断结案验证",
		ActionLevel: rings.ActionModifyPersist, ToolName: "bash", ToolArgs: `{"command":"id"}`,
	}); err != nil {
		t.Fatalf("第二笔 Enter 失败: %v", err)
	}
	if n := gate.ExpireTickets(task, "任务冬眠中熔断终止,按拒绝结案"); n != 1 {
		t.Fatalf("熔断应结案 1 票,得到 %d", n)
	}
	recs = approvalEvents(t, store, task)
	if len(recs) != 3 || recs[2].Type != rings.ApprovalRecDenied ||
		recs[2].DenyLine != rings.DenyLineCloseout ||
		!strings.Contains(recs[2].Reason, "熔断终止") {
		t.Fatalf("结案留痕应为 closeout 拒绝(带原因): %+v", recs)
	}
}

// ---- 主契径:票上单位被冬眠收场放行落地,票面冻结;唤醒清出后重领
// 原会话续跑、重新过门、批准后完成 ----

func TestHibernateRetireLandsTicketUnitAndResume(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hiber-retire"

	host, port := seedTarget(t, "x\r\n")
	seedGoalAndAsset(t, store, task, host, port)
	dispatchStepAtLevel(t, store, task, "s1", "写入落地文件验证", "g1", "修改与持久化", "target")

	const marker = "gr-hiber-retire-5f2a"
	calls := 0
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		switch {
		case !seenToolResult(rr, "bash", marker):
			calls++
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: fmt.Sprintf("b%d", calls), Name: "bash",
				Args: fmt.Sprintf(`{"command":"echo %s > retire-marker.txt && cat retire-marker.txt"}`, marker),
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{Text: "写入完成。", Finish: "stop"}
		}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}

	ledgerGate := NewLedgerGate(store)
	hiberGate := hiber.New()
	ledgerGate.Hiber = hiberGate // 装配同款(Decide 冻结检查的门引用)
	ckptGate := ckpt.New()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = ledgerGate
	ex.Hiber = hiberGate
	ex.Ckpt = ckptGate

	done := make(chan *RunResult, 1)
	go func() {
		res, err := ex.RunStep(ctx, task, "s1")
		if err != nil {
			t.Errorf("RunStep: %v", err)
		}
		done <- res
	}()

	// 单位弹门挂起:票已登记、工具未执行。
	waitFor(t, 30*time.Second, "弹门请求登记", func() bool { return len(ledgerGate.Pending(task)) == 1 })
	ticket := ledgerGate.Pending(task)[0]
	if calls := toolCallRecords(t, store, task, "bash"); successfulToolCalls(calls) != 0 {
		t.Fatalf("批准前不得执行工具")
	}

	// 冬眠收场(Manager.Hibernate 的执行层次序):武装收场令 → 关门 →
	// 放行挂起者(票 + 闩)。
	ckptGate.Arm(task)
	hiberGate.Enter(task, "started")
	ex.FlushSuspended(task)

	var res1 *RunResult
	select {
	case res1 = <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("单位未在期限内被放行收场")
	}
	if res1.StepStatus != "dispatched" || res1.Reason != "checkpointed" || !res1.Checkpointed {
		t.Fatalf("冬眠收场应收场 dispatched/checkpointed(会话保留可续): %+v", res1)
	}
	assertCheckpointLedger(t, store, task, res1.TranscriptID)
	// 票面保持待决冻结(面板冬眠中只读可见),零裁决记录。
	if len(ledgerGate.Pending(task)) != 1 || ledgerGate.Pending(task)[0].ID != ticket.ID {
		t.Fatalf("收场后票面应保持原票待决: %+v", ledgerGate.Pending(task))
	}
	recs := approvalEvents(t, store, task)
	if len(recs) != 1 || recs[0].Type != rings.ApprovalRecRequested {
		t.Fatalf("收场零裁决记录: %+v", recs)
	}
	if err := ledgerGate.Decide(ctx, task, ticket.ID, GateDecision{Approved: true}); !errors.Is(err, ErrGateFrozen) {
		t.Fatalf("冬眠中裁决应被冻结,得到 %v", err)
	}

	// 唤醒(Manager.Wake 的执行层配套):开闸 → 清出无主票 → 清收场令。
	hiberGate.Exit(task)
	ex.DropPendingApprovals(task)
	if len(ledgerGate.Pending(task)) != 0 {
		t.Fatalf("唤醒后无主票应已清出")
	}
	if recs := approvalEvents(t, store, task); len(recs) != 1 {
		t.Fatalf("唤醒清出零新增记录: %+v", recs)
	}
	ckptGate.Clear(task)

	// 重领(等价唤醒拉起):原会话续跑(同转录),重新过门立新票,批准后完成。
	var (
		res2  *RunResult
		rerr2 error
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res2, rerr2 = ex.RunStep(ctx, task, "s1")
	}()
	waitFor(t, 30*time.Second, "重领后重新过门立新票", func() bool { return len(ledgerGate.Pending(task)) == 1 })
	newTicket := ledgerGate.Pending(task)[0]
	if newTicket.ID == ticket.ID {
		t.Fatalf("新票应是重新立票,不与冬眠前旧票同 ID")
	}
	if err := ledgerGate.Decide(ctx, task, newTicket.ID, GateDecision{Approved: true, By: "creator", Reason: "唤醒后批准"}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	wg.Wait()
	if rerr2 != nil {
		t.Fatalf("续跑失败: %v", rerr2)
	}
	if res2.StepStatus != "done" {
		t.Fatalf("续跑后步骤应完成: %+v", res2)
	}
	if res2.TranscriptID != res1.TranscriptID {
		t.Fatalf("续跑应沿用原会话转录 %s,得到 %s", res1.TranscriptID, res2.TranscriptID)
	}
	// 账本:requested(旧票,悬而未决=崩溃等价)→ requested(新票)→ approved。
	recs = approvalEvents(t, store, task)
	if len(recs) != 3 || recs[0].Type != rings.ApprovalRecRequested ||
		recs[1].Type != rings.ApprovalRecRequested || recs[2].Type != rings.ApprovalRecApproved {
		t.Fatalf("审批记录应为 旧requested/新requested/approved: %+v", recs)
	}
	// 工具恰执行一次(批准后),marker 真实落地。
	for _, c := range toolCallRecords(t, store, task, "bash") {
		if !c.IsError && !strings.Contains(c.ResultHead, marker) {
			t.Fatalf("执行的调用应含 marker: %+v", c)
		}
	}
}

// ---- 收场放行清单位暂停闩(闩是进程内控制,过不了收场) ----

func TestFlushSuspendedDropsUnitLatch(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "hiber-latch"

	probeHost, probePort := silentTarget(t)
	seedGoalAndAsset(t, store, task, probeHost, probePort)
	dispatchStepAtLevel(t, store, task, "s1", "只读探测", "g1", "只读探测", "target")

	first := true
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if first {
			first = false
			return scriptedReply{ToolCalls: []scriptedToolCall{
				{ID: "p1", Name: "port_probe",
					Args: fmt.Sprintf(`{"host":%q,"port":%d}`, probeHost, probePort)},
			}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "收尾。", Finish: "stop"}
	})
	defer model.Close()
	prov, _ := model.provider()
	ex := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{})

	var (
		res  *RunResult
		rerr error
		wg   sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, rerr = ex.RunStep(ctx, task, "s1")
	}()

	// 单位在飞(步骤 running)→ 下单位暂停闩 → 收场放行清闩。
	waitFor(t, 30*time.Second, "步骤进入 running", func() bool {
		n, err := store.Node(ctx, task, "s1")
		return err == nil && n.Status == "running"
	})
	if err := ex.PauseUnit(ctx, task, "s1", "creator", "冬眠前暂停"); err != nil {
		t.Fatalf("单位暂停失败: %v", err)
	}
	if !ex.UnitPaused(task, "s1") {
		t.Fatalf("单位应在暂停中")
	}
	ex.FlushSuspended(task)
	if ex.UnitPaused(task, "s1") {
		t.Fatalf("收场放行应清闩(闩过不了收场)")
	}
	wg.Wait()
	if rerr != nil {
		t.Fatalf("执行失败: %v", rerr)
	}
	if res.StepStatus != "done" {
		t.Fatalf("清闩后单位应继续干完: %+v", res)
	}
}
