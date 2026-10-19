package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P2-05 纠偏纵深:循环内重锚定 + 舵杆 steer_work / kill_work ----

// anchorCountFromTranscript 数 transcript JSONL 里的重锚定注入条数
// (完成标准:重锚定注入在 transcript 可查)。
func anchorCountFromTranscript(t *testing.T, rootDir, task, unitID string) (int, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(rootDir, task, "transcripts", unitID+".jsonl"))
	if err != nil {
		t.Fatalf("读 transcript 失败: %v", err)
	}
	return strings.Count(string(raw), anchorMarker), string(raw)
}

// stepToolCalls ledger 里该步骤的 tool_call 记录(执行审计;按步骤过滤,
// 与 gate_test 的 toolCallRecords(按工具过滤)互补)。
func stepToolCalls(t *testing.T, store *rings.Store, task, stepID string) []rings.SwarmRecord {
	t.Helper()
	var out []rings.SwarmRecord
	for _, r := range swarmRecords(t, store, task) {
		if r.StepID == stepID && r.Type == rings.SwarmRecToolCall {
			out = append(out, r)
		}
	}
	return out
}

// steerRecordsFromLedger 解出全部舵杆记录。
func steerRecordsFromLedger(t *testing.T, store *rings.Store, task string) []rings.SteerRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []rings.SteerRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventSteerRecord {
			continue
		}
		var r rings.SteerRecord
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("解码 steer.record: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// allMessageText 模型请求里全部消息文本(注入可见性断言用)。
func allMessageText(rr *recordedRequest) string {
	var b strings.Builder
	for _, m := range rr.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// runAnchorTask 跑一个 N 步重锚定的探测任务:created 首事件携带
// anchorEverySteps(0 = 缺省,验证默认 10),脚本模型逐轮发 1 次探测,
// calls 轮后收尾。
func runAnchorTask(t *testing.T, anchorEverySteps, calls int) (*RunResult, int, *rings.Store, string, string, *scriptedModel) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "anchor-task"
	host, port := seedTarget(t, "banner\r\n")

	detail := map[string]any{}
	if anchorEverySteps > 0 {
		detail["anchor_every_steps"] = anchorEverySteps
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "重锚定验证", Detail: detail},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("逐轮探测 %s:%d", host, port))

	var mu sync.Mutex
	n := 0
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		mu.Lock()
		defer mu.Unlock()
		if n < calls {
			n++
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: fmt.Sprintf("c%d", n), Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "探测完成。", Finish: "stop"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	exec := NewExecutor(store, prov, root, Budget{MaxTurns: 40, RunSeconds: 120, WrapupMaxTurns: 2})
	res, err := exec.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	return res, len(stepToolCalls(t, store, task, "s1")), store, root, task, model
}

// TestReanchorCadence 重锚定注入在 transcript 可查且频率与设置一致:
// N=2、7 次调用 → 恰 3 次注入;锚内容含目标/意图/预期观察/剩余预算;
// 模型后续请求可见锚(注入确实进入上下文)。
func TestReanchorCadence(t *testing.T) {
	res, boundaries, _, root, task, model := runAnchorTask(t, 2, 7)
	if res.StepStatus != "done" {
		t.Fatalf("步骤应完成: %+v", res)
	}
	if boundaries != 7 {
		t.Fatalf("调用边界应为 7,实际 %d", boundaries)
	}
	anchors, raw := anchorCountFromTranscript(t, root, task, "swarm-u1")
	if want := boundaries / 2; anchors != want {
		t.Fatalf("重锚定注入次数应为 %d(N=2、%d 边界),实际 %d", want, boundaries, anchors)
	}
	for _, want := range []string{"目标: 确认本地靶标暴露面", "本步骤意图", "预期观察", "剩余预算"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("任务锚应包含 %q", want)
		}
	}
	sawAnchor := false
	for _, rr := range model.requests() {
		if strings.Contains(allMessageText(&rr), anchorMarker) {
			sawAnchor = true
			break
		}
	}
	if !sawAnchor {
		t.Fatalf("模型后续请求应看到注入的任务锚")
	}
}

// TestReanchorDefaultCadence 缺省间隔(不设 anchor_every_steps)按默认
// 10 步:12 次调用 → 恰 1 次注入。
func TestReanchorDefaultCadence(t *testing.T) {
	_, boundaries, _, root, task, _ := runAnchorTask(t, 0, 12)
	if boundaries != 12 {
		t.Fatalf("调用边界应为 12,实际 %d", boundaries)
	}
	anchors, _ := anchorCountFromTranscript(t, root, task, "swarm-u1")
	if anchors != 1 {
		t.Fatalf("默认 N=10、12 边界应恰注入 1 次,实际 %d", anchors)
	}
}

// TestSteerDeliveredBeforeNextToolCall 舵杆 steer_work 在下一步工具调用
// 前生效(E2E):指令在模型的第 2 次请求处理期间排队(严格早于其后的
// 任何调用边界)→ 第 2 次调用被暂缓不执行、指令注入上下文(transcript
// 可查 + 送达记账)→ 模型读到指令后改做指令动作 → 完成。
func TestSteerDeliveredBeforeNextToolCall(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "steer-task"
	host, port := seedTarget(t, "banner\r\n")
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "舵杆验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("探测 %s:%d", host, port))

	var exec *Executor
	var steerErr error
	var steered bool
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		// 第 2 次请求(看到首次结果后):在回复前排队舵杆指令——
		// 确保指令先于下一次工具调用边界存在。
		if rr.Index == 1 {
			_, steerErr = exec.Helm.SteerWork(ctx, task, "s1",
				fmt.Sprintf("别再重复探测 %d,改查 9090 端口", port), "creator")
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c2", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		if strings.Contains(allMessageText(rr), steerMarker) {
			if !steered {
				steered = true
				return scriptedReply{ToolCalls: []scriptedToolCall{{
					ID: "c3", Name: "port_probe",
					Args: fmt.Sprintf(`{"host":%q,"port":9090}`, host),
				}}, Finish: "tool_calls"}
			}
			return scriptedReply{Text: "已按纠偏指令调整方向,完成。", Finish: "stop"}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "c1", Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	exec = NewExecutor(store, prov, root, Budget{MaxTurns: 20, RunSeconds: 60, WrapupMaxTurns: 2})
	res, err := exec.RunStep(ctx, task, "s1")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if steerErr != nil {
		t.Fatalf("排队舵杆指令失败: %v", steerErr)
	}
	if res.StepStatus != "done" || !steered {
		t.Fatalf("步骤应在消化指令后完成: status=%s steered=%v", res.StepStatus, steered)
	}

	// 账本对账:queued + delivered 两级,送达带单位与消息原文。
	recs := steerRecordsFromLedger(t, store, task)
	if len(recs) != 2 || recs[0].State != rings.SteerStateQueued || recs[1].State != rings.SteerStateDelivered {
		t.Fatalf("舵杆记录应为 queued→delivered: %+v", recs)
	}
	if recs[1].UnitID != "u1" || recs[1].SteerID != recs[0].SteerID ||
		!strings.Contains(recs[1].Message, "9090") || recs[0].By != "creator" {
		t.Fatalf("送达记录应回指排队记录并带单位/消息: %+v", recs[1])
	}

	// 生效点对账:第 2 次同参数调用被暂缓(未执行),指令动作已执行。
	calls := stepToolCalls(t, store, task, "s1")
	execOld, deferred, execNew := 0, 0, 0
	for _, c := range calls {
		switch {
		case c.IsError && strings.Contains(c.ResultHead, "暂缓"):
			deferred++
		case strings.Contains(string(c.ToolArgs), "9090"):
			execNew++
		default:
			execOld++
		}
	}
	if execOld != 1 || deferred != 1 || execNew != 1 {
		t.Fatalf("调用对账: 原参数执行 %d(应1)、被暂缓 %d(应1)、指令动作 %d(应1)",
			execOld, deferred, execNew)
	}

	// transcript 可查:注入的纠偏指令原文在场。
	_, raw := anchorCountFromTranscript(t, root, task, "swarm-u1")
	if !strings.Contains(raw, steerMarker) || !strings.Contains(raw, "改查 9090 端口") {
		t.Fatalf("transcript 应含注入的纠偏指令")
	}
}

// TestKillWorkAbortsUnit 舵杆 kill_work 止损终止:在跑单位被真中断
// (aborted,不进收尾),kill 送达记账;步骤前的未送达 steer 停在
// 已排队(过时回执属 P2-08)。
func TestKillWorkAbortsUnit(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "kill-task"
	host, port := seedTarget(t, "banner\r\n")
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "止损验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("持续探测 %s:%d", host, port))

	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: fmt.Sprintf("c%d", rr.Index), Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"} // 永不收尾,直到被止损
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	exec := NewExecutor(store, prov, root, Budget{MaxTurns: 50, RunSeconds: 30, WrapupMaxTurns: 2})

	type outcome struct {
		res *RunResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := exec.RunStep(context.Background(), task, "s1")
		done <- outcome{res, err}
	}()

	// 等首个工具调用入账(单位确在跑),再下达 kill。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(stepToolCalls(t, store, task, "s1")) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	killID, err := exec.Helm.KillWork(ctx, task, "s1", "方向整个错误,止损", "creator")
	if err != nil {
		t.Fatalf("kill_work 失败: %v", err)
	}
	out := <-done
	if out.err != nil {
		t.Fatalf("执行返回错误: %v", out.err)
	}
	if out.res.StepStatus != "aborted" || !strings.HasPrefix(out.res.Reason, "aborted") {
		t.Fatalf("单位应被真中断: %+v", out.res)
	}

	// 步骤终态 aborted(中性可重派);kill 送达记账带单位。
	step, err := store.Node(ctx, task, "s1")
	if err != nil || step.Status != "aborted" {
		t.Fatalf("步骤应落 aborted: status=%s err=%v", step.Status, err)
	}
	var killRec *rings.SteerRecord
	for _, r := range steerRecordsFromLedger(t, store, task) {
		if r.Command == rings.SteerCommandKill {
			killRec = &r
		}
	}
	if killRec == nil || killRec.State != rings.SteerStateDelivered || killRec.UnitID != "u1" || killRec.SteerID != killID {
		t.Fatalf("kill 应送达记账: %+v(id=%s)", killRec, killID)
	}
}

// TestHelmKillClearsPending kill_work 清空该步骤未送达的 steer 指令
// (工作已止损,后续无生效点;queued 记录留在账本,过时回执属 P2-08)。
// 纯 Helm 行为测试:止损钩子用假件,步骤状态由投影事件直接置 running。
func TestHelmKillClearsPending(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "helm-clear-task"
	seedGraph(t, store, task, "探测本地靶标")
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
		Payload: rings.NodeStateChanged{NodeID: "s1", Status: "running"},
	}); err != nil {
		t.Fatalf("置步骤 running 失败: %v", err)
	}

	helm := NewHelm(store)
	var killedStep string
	helm.attachTask(task, func(taskID, stepID string) (string, bool) {
		killedStep = stepID
		return "u1", true
	})
	if _, err := helm.SteerWork(ctx, task, "s1", "第一条", "creator"); err != nil {
		t.Fatalf("排队失败: %v", err)
	}
	if len(helm.take(task, "s1")) != 1 {
		t.Fatalf("排队后应可取到指令")
	}
	if _, err := helm.SteerWork(ctx, task, "s1", "第二条", "creator"); err != nil {
		t.Fatalf("再排队失败: %v", err)
	}
	if _, err := helm.KillWork(ctx, task, "s1", "止损", "creator"); err != nil {
		t.Fatalf("kill 失败: %v", err)
	}
	if killedStep != "s1" {
		t.Fatalf("止损钩子应命中 s1,实际 %q", killedStep)
	}
	if left := helm.take(task, "s1"); len(left) != 0 {
		t.Fatalf("kill 后未送达指令应被清空,剩 %d 条", len(left))
	}
}

// TestHelmTargetValidation 舵杆指向校验:非 step 节点 / 不在跑的步骤 /
// 空消息均拒绝,账本零残留。
func TestHelmTargetValidation(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "helm-task"
	seedGraph(t, store, task, "探测本地靶标") // s1=dispatched,目标 g1,资产 target

	helm := NewHelm(store)
	if _, err := helm.SteerWork(ctx, task, "s1", "msg", "creator"); err == nil {
		t.Fatalf("非 running 步骤应拒绝 steer")
	}
	if _, err := helm.SteerWork(ctx, task, "g1", "msg", "creator"); err == nil {
		t.Fatalf("非 step 节点应拒绝")
	}
	if _, err := helm.SteerWork(ctx, task, "s1", "  ", "creator"); err == nil {
		t.Fatalf("空消息应拒绝")
	}
	if _, err := helm.KillWork(ctx, task, "s1", "", "creator"); err == nil {
		t.Fatalf("非 running 步骤应拒绝 kill")
	}
	if recs := steerRecordsFromLedger(t, store, task); len(recs) != 0 {
		t.Fatalf("坏请求应零残留,实际 %d 条", len(recs))
	}
}
