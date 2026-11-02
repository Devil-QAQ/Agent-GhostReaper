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

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ---- P2-08 单位暂停/任务冬眠/回执状态机 ----

// parkHarness 挂起类测试的共用脚手架:跑一个逐轮 port_probe 的任务;
// 前两次调用完成后模型阻塞在 hold 上(测试在此窗口下达暂停/冬眠,
// close(hold) 放行后续轮次——挂起必然发生在还有边界的时刻)。
type parkHarness struct {
	store  *rings.Store
	exec   *Executor
	gate   *hiber.Gate
	model  *scriptedModel
	root   string
	task   string
	host   string
	port   int
	hold   chan struct{}
	done   chan error
	res    *RunResult
	resErr error
	once   sync.Once
}

// newParkHarness 建任务+图+执行器(挂冬眠门),RunStep 在后台 goroutine 跑。
func newParkHarness(t *testing.T, task string, totalCalls int) *parkHarness {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	h := &parkHarness{
		store: store, root: filepath.Join(dir, "tasks"), task: task,
		gate: hiber.New(), hold: make(chan struct{}), done: make(chan error, 1),
	}
	h.host, h.port = seedTarget(t, "banner\r\n")
	ctx := context.Background()
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "挂起验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("逐轮探测 %s:%d", h.host, h.port))

	var mu sync.Mutex
	n := 0
	h.model = newScriptedModel(func(rr *recordedRequest) scriptedReply {
		mu.Lock()
		at := n
		mu.Unlock()
		if at < totalCalls {
			mu.Lock()
			n++
			mu.Unlock()
			if at >= 2 {
				<-h.hold // 前两次完成后阻塞:测试在此窗口下达暂停/冬眠
			}
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: fmt.Sprintf("c%d", at+1), Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, h.host, h.port),
			}}, Finish: "tool_calls"}
		}
		return scriptedReply{Text: "探测完成。", Finish: "stop"}
	})
	t.Cleanup(h.model.Close)
	prov, err := h.model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	h.exec = NewExecutor(store, prov, h.root, Budget{MaxTurns: 60, RunSeconds: 120, WrapupMaxTurns: 2})
	h.exec.Hiber = h.gate
	go func() {
		res, rerr := h.exec.RunStep(ctx, task, "s1")
		h.once.Do(func() { h.res, h.resErr = res, rerr })
		h.done <- rerr
	}()
	h.waitToolCalls(t, 2, 5*time.Second)
	return h
}

// waitParkSignature 等到挂起签名:账本出现"已暂缓(未执行)"的调用记录
// (guard 在边界收段的留痕),即单位已进入挂起等待。
func (h *parkHarness) waitParkSignature(t *testing.T, deadline time.Duration) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < deadline {
		for _, rec := range stepToolCalls(t, h.store, h.task, "s1") {
			if rec.IsError && strings.Contains(rec.ResultHead, "已暂缓") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待挂起签名超时(暂缓调用记录未出现)")
}

// waitUnitLanded 等 RunStep 返回。
func (h *parkHarness) waitUnitLanded(t *testing.T, deadline time.Duration) (*RunResult, error) {
	t.Helper()
	select {
	case <-h.done:
		return h.res, h.resErr
	case <-time.After(deadline):
		t.Fatalf("等待单位落地超时")
		return nil, nil
	}
}

// toolCallCount 已执行(未暂缓)的工具调用数。
func (h *parkHarness) toolCallCount(t *testing.T) int {
	t.Helper()
	n := 0
	for _, rec := range stepToolCalls(t, h.store, h.task, "s1") {
		if !rec.IsError {
			n++
		}
	}
	return n
}

// sessionNote 最新一笔 session_finished 的 Note。
func (h *parkHarness) sessionNote(t *testing.T) string {
	t.Helper()
	evs, err := h.store.Events(context.Background(), h.task, 0, 0)
	if err != nil {
		t.Fatalf("查事件: %v", err)
	}
	note := ""
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var rec rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.SwarmRecSessionEnd {
			note = rec.Note
		}
	}
	return note
}

// noteField 取 session_finished note 的 字段=值(到下一空格)。
func noteField(note, key string) string {
	i := strings.Index(note, key+"=")
	if i < 0 {
		return ""
	}
	v := note[i+len(key)+1:]
	if j := strings.IndexByte(v, ' '); j >= 0 {
		v = v[:j]
	}
	return v
}

// mimicHibernate 模拟 Manager 的冬眠次序:先记账(hibernated 生命周期
// 事件,R14 锚点),后关闸。
func (h *parkHarness) mimicHibernate(t *testing.T, pre string) {
	t.Helper()
	if _, err := h.store.Append(context.Background(), h.task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "hibernated", Detail: map[string]any{"pre_state": pre}},
	}); err != nil {
		t.Fatalf("冬眠记账失败: %v", err)
	}
	h.gate.Enter(h.task, pre)
}

// mimicWake 模拟 Manager 的唤醒次序:先记账(恢复 started),后开闸。
func (h *parkHarness) mimicWake(t *testing.T) {
	t.Helper()
	if _, err := h.store.Append(context.Background(), h.task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "started", Detail: map[string]any{"woken": true}},
	}); err != nil {
		t.Fatalf("唤醒记账失败: %v", err)
	}
	h.gate.Exit(h.task)
}

// waitToolCalls 等到未暂缓调用数达到 n。
func (h *parkHarness) waitToolCalls(t *testing.T, n int, deadline time.Duration) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < deadline {
		if h.toolCallCount(t) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 %d 次调用超时(当前 %d)", n, h.toolCallCount(t))
}

// TestUnitPauseResumeInPlace 单位级暂停/继续(P2-08 完成标准:暂停后
// 该单位冻结——会话不退出、计时停;继续后原地接着干)。
func TestUnitPauseResumeInPlace(t *testing.T) {
	h := newParkHarness(t, "pause-task", 8)

	if err := h.exec.PauseUnit(context.Background(), h.task, "s1", "creator", "验证暂停"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	close(h.hold) // 放行:下一调用边界即挂起(当前调用暂缓不执行)
	h.waitParkSignature(t, 5*time.Second)
	// 冻结期:无新调用推进,步骤仍 running(会话不退出)。
	before := h.toolCallCount(t)
	time.Sleep(400 * time.Millisecond)
	if after := h.toolCallCount(t); after != before {
		t.Fatalf("暂停期间不应有新调用: 前 %d 后 %d", before, after)
	}
	if n, _ := h.store.NodesByType(context.Background(), h.task, rings.NodeStep, "running"); len(n) != 1 {
		t.Fatalf("暂停期间步骤应仍 running(单位不退出)")
	}
	if !h.exec.UnitPaused(h.task, "s1") {
		t.Fatalf("UnitPaused 应为 true")
	}

	time.Sleep(300 * time.Millisecond) // 计 pause_wait(≥0.3s)
	if err := h.exec.ResumeUnit(context.Background(), h.task, "s1", "creator"); err != nil {
		t.Fatalf("继续失败: %v", err)
	}
	res, err := h.waitUnitLanded(t, 10*time.Second)
	if err != nil {
		t.Fatalf("继续后执行失败: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("继续后应完成: %+v", res)
	}
	// 时间账:pause_wait ≥ 0.3s 且挂在 session_finished note;原会话续
	//(续跑指令进同一 transcript)。
	note := h.sessionNote(t)
	if pw := noteField(note, "pause_wait"); pw == "" {
		t.Fatalf("note 应含 pause_wait: %s", note)
	}
	if status := noteField(note, "status"); status != "done" {
		t.Fatalf("note status 应为 done: %s", note)
	}
	// 控制记账:pause + resume 两条 unit.control。
	ctl := h.unitControls(t)
	if len(ctl) != 2 || ctl[0].Action != rings.UnitCtlPause || ctl[1].Action != rings.UnitCtlResume {
		t.Fatalf("应恰有 pause+resume 两条控制记账: %+v", ctl)
	}
	// 原地续跑:续跑指令进入同一会话转录。
	raw := readTranscriptRaw(t, h.root, h.task, "swarm-u1")
	if !strings.Contains(raw, "单位继续 · 续跑") {
		t.Fatalf("续跑指令应进入原会话转录")
	}
}

// TestHibernateFreezeWakeInPlace 任务冬眠/唤醒:同一单位同一会话原地继续
// (账本与 transcript 可证);唤醒后计时对账。
func TestHibernateFreezeWakeInPlace(t *testing.T) {
	h := newParkHarness(t, "hiber-task", 8)

	h.mimicHibernate(t, "started")
	close(h.hold) // 放行:单位在下一调用边界冻结
	h.waitParkSignature(t, 5*time.Second)
	before := h.toolCallCount(t)
	time.Sleep(500 * time.Millisecond)
	if after := h.toolCallCount(t); after != before {
		t.Fatalf("冬眠期间不应有新调用: 前 %d 后 %d", before, after)
	}
	// 冬眠时长入账(HibernateCredit:hibernated→started 配对)。
	time.Sleep(100 * time.Millisecond)
	h.mimicWake(t)
	res, err := h.waitUnitLanded(t, 10*time.Second)
	if err != nil {
		t.Fatalf("唤醒后执行失败: %v", err)
	}
	if res.StepStatus != "done" {
		t.Fatalf("唤醒后应原地完成: %+v", res)
	}
	credit, err := h.store.HibernateCredit(context.Background(), h.task)
	if err != nil || credit < 500*time.Millisecond {
		t.Fatalf("冬眠时长应 ≥500ms: %v %v", credit, err)
	}
	note := h.sessionNote(t)
	if pw := noteField(note, "pause_wait"); pw == "" {
		t.Fatalf("note 应含 pause_wait(冬眠挂起合计): %s", note)
	}
	raw := readTranscriptRaw(t, h.root, h.task, "swarm-u1")
	if !strings.Contains(raw, "任务唤醒 · 续跑") {
		t.Fatalf("唤醒续跑指令应进入原会话转录")
	}
}

// TestHibernateRoEChangedR14Stale 冬眠期间 RoE 变过 → 唤醒后 R14 拒绝
// 旧计划:步骤回可领取(dispatched),交主宰重判。
func TestHibernateRoEChangedR14Stale(t *testing.T) {
	h := newParkHarness(t, "hiber-roe-task", 8)

	h.mimicHibernate(t, "started")
	close(h.hold)
	h.waitParkSignature(t, 5*time.Second)
	// 冬眠期间操作者改 RoE(前提层变化)。
	if _, err := h.store.Append(context.Background(), h.task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{DeniedTools: []string{"port_probe"}, Note: "冬眠期间收紧"},
	}); err != nil {
		t.Fatalf("RoE 记账失败: %v", err)
	}
	h.mimicWake(t)
	res, err := h.waitUnitLanded(t, 10*time.Second)
	if err == nil {
		t.Fatalf("前提变化应返回 ErrStaleGraph,得到 nil(状态 %s)", res.StepStatus)
	}
	if res == nil || res.StepStatus != "dispatched" || !res.Stale {
		t.Fatalf("R14 拒绝应回 dispatched 且 Stale: %+v", res)
	}
	// 步骤状态与留痕。
	if n, _ := h.store.NodesByType(context.Background(), h.task, rings.NodeStep, "dispatched"); len(n) != 1 {
		t.Fatalf("步骤应回 dispatched 待重领")
	}
	found := false
	for _, rec := range stepSwarmRecords(t, h.store, h.task) {
		if rec.Type == rings.SwarmRecStaleRefused && strings.Contains(rec.Note, "RoE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应留 stale_refused 记录(RoE 已更新)")
	}
	// 无暂停闩残留。
	if h.exec.UnitPaused(h.task, "s1") {
		t.Fatalf("R14 收场后不应残留暂停闩")
	}
}

// TestSteerReceiptFlow 回执状态机:单位消化纠偏指令后在回复中输出回执
// 标记 → adopted/rejected 落账(同一指令至多一条,重复标记忽略)。
func TestSteerReceiptFlow(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "receipt-task"
	host, port := seedTarget(t, "banner\r\n")
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "回执验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("回执探测 %s:%d", host, port))

	var id1, id2 atomicString
	ready := make(chan struct{}) // 舵杆排队完成前阻塞模型续跑(确定性)
	var mu sync.Mutex
	n := 0
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		if strings.Contains(allMessageText(rr), steerMarker) {
			// 读到纠偏指令:拒绝第一条(附理由)、采纳第二条;第一条的
			// 标记重复输出一次(去重断言)。
			return scriptedReply{Text: fmt.Sprintf(
				"【STEER:%s:拒绝:当前方向仍有效】\n【STEER:%s:拒绝:当前方向仍有效】\n【STEER:%s:采纳】\n收到,已按第二条调整。",
				id1.Load(), id1.Load(), id2.Load()), Finish: "stop"}
		}
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		<-ready // 等舵杆排队后再发起下一次调用(其边界送达指令)
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: "c2", Name: "port_probe",
			Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
		}}, Finish: "tool_calls"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	exec := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 30, RunSeconds: 60, WrapupMaxTurns: 1})
	done := make(chan error, 1)
	go func() { _, err := exec.RunStep(ctx, task, "s1"); done <- err }()

	// 首次调用结果入账后排队两条舵杆指令。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(stepToolCalls(t, store, task, "s1")) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(stepToolCalls(t, store, task, "s1")) == 0 {
		t.Fatalf("等待首次调用超时")
	}
	s1, err := exec.Helm.SteerWork(ctx, task, "s1", "改查另一个端口", "creator")
	if err != nil {
		t.Fatalf("排队第一条失败: %v", err)
	}
	s2, err := exec.Helm.SteerWork(ctx, task, "s1", "汇总当前发现后收尾", "creator")
	if err != nil {
		t.Fatalf("排队第二条失败: %v", err)
	}
	id1.Store(s1)
	id2.Store(s2)
	close(ready)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("执行失败: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("执行超时")
	}

	// 回执断言:两条指令各自 queued→delivered→adopted/rejected(最新态),
	// 重复标记不产生第二条 rejected。
	recs := steerRecordsFromLedger(t, store, task)
	latest := map[string]string{}
	counts := map[string]int{}
	for _, r := range recs {
		latest[r.SteerID] = r.State
		counts[r.SteerID]++
	}
	if latest[s1] != rings.SteerStateRejected {
		t.Fatalf("第一条应被拒(最新态),实际 %v(%+v)", latest[s1], recs)
	}
	if latest[s2] != rings.SteerStateAdopted {
		t.Fatalf("第二条应已采纳(最新态),实际 %v", latest[s2])
	}
	for id, c := range counts {
		if c != 3 { // queued + delivered + 终态回执,恰三条
			t.Fatalf("指令 %s 应恰 3 条记录(queued/delivered/终态),实际 %d", id, c)
		}
	}
	// 拒绝理由随回执留痕。
	foundReason := false
	for _, r := range recs {
		if r.SteerID == s1 && r.State == rings.SteerStateRejected && strings.Contains(r.Note, "当前方向仍有效") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatalf("拒绝回执应携带理由: %+v", recs)
	}
}

// TestSteerStaleWhenUnitLands 生效前单位落地:已排队未送达的指令落
// stale 回执(已过时)。
func TestSteerStaleWhenUnitLands(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "stale-steer-task"
	host, port := seedTarget(t, "banner\r\n")
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "过时回执验证"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("过时探测 %s:%d", host, port))

	ready := make(chan struct{})
	var mu sync.Mutex
	n := 0
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "c1", Name: "port_probe",
				Args: fmt.Sprintf(`{"host":%q,"port":%d}`, host, port),
			}}, Finish: "tool_calls"}
		}
		<-ready // 等指令排队;此后直接收尾(无下一边界,指令失去生效点)
		return scriptedReply{Text: "完成。", Finish: "stop"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	exec := NewExecutor(store, prov, filepath.Join(dir, "tasks"), Budget{MaxTurns: 20, RunSeconds: 60, WrapupMaxTurns: 1})
	done := make(chan error, 1)
	go func() { _, err := exec.RunStep(ctx, task, "s1"); done <- err }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(stepToolCalls(t, store, task, "s1")) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sid, err := exec.Helm.SteerWork(ctx, task, "s1", "换方向", "creator")
	if err != nil {
		t.Fatalf("排队失败: %v", err)
	}
	close(ready)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("执行失败: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("执行超时")
	}
	latest := ""
	for _, r := range steerRecordsFromLedger(t, store, task) {
		if r.SteerID == sid {
			latest = r.State
		}
	}
	if latest != rings.SteerStateStale {
		t.Fatalf("落地后未送达指令应已过时,最新态 %q", latest)
	}
}

// ---- 测试小件 ----

// unitControls 账本里的单位控制记录(时序)。
func (h *parkHarness) unitControls(t *testing.T) []rings.UnitControlRecord {
	t.Helper()
	evs, err := h.store.Events(context.Background(), h.task, 0, 0)
	if err != nil {
		t.Fatalf("查事件: %v", err)
	}
	var out []rings.UnitControlRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventUnitControl {
			continue
		}
		var r rings.UnitControlRecord
		if json.Unmarshal(ev.Payload, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// stepSwarmRecords 该任务全部虫群记录。
func stepSwarmRecords(t *testing.T, store *rings.Store, task string) []rings.SwarmRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件: %v", err)
	}
	var out []rings.SwarmRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventSwarmRecord {
			continue
		}
		var r rings.SwarmRecord
		if json.Unmarshal(ev.Payload, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// readTranscriptRaw 读单位转录原文(续跑指令可查性断言)。
func readTranscriptRaw(t *testing.T, rootDir, task, unitID string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rootDir, task, "transcripts", unitID+".jsonl"))
	if err != nil {
		t.Fatalf("读 transcript 失败: %v", err)
	}
	return string(b)
}

// atomicString 并发安全的字符串(模型脚本与测试主线程传递舵杆 ID)。
type atomicString struct {
	mu sync.Mutex
	v  string
}

func (a *atomicString) Load() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.v
}

func (a *atomicString) Store(s string) {
	a.mu.Lock()
	a.v = s
	a.mu.Unlock()
}

// TestHibernateNoGateWakeContinues P4-02 活体发现的缺陷回归:无整体超时
// (零值收尾线)的任务,挂起唤醒路径的冬眠抵扣不得把零值线加成"已过期
// 的非零线"——否则唤醒后下一段被收敛到 1 秒、慢工具被误打断,单位落
// exhausted(timeout)。修复后:唤醒续走,慢工具照常完成。
// (既有 TestHibernateFreezeWakeInPlace 未拦住的原因:其工具为毫秒级
// port_probe,1 秒窗口内自然完成。)
func TestHibernateNoGateWakeContinues(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	root := filepath.Join(dir, "tasks")
	gate := hiber.New()
	const task = "hiber-nogate-task"
	host, port := seedTarget(t, "banner\r\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "无总闸冬眠回归"},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, fmt.Sprintf("慢工具探测 %s:%d", host, port))

	// 第 2 次模型请求前阻塞,给测试下达冬眠的窗口;每轮都发 3 秒慢命令
	//(超过误杀路径的 1 秒收敛线)。
	parkCh := make(chan struct{})
	var mu sync.Mutex
	n := 0
	model := newScriptedModel(func(rr *recordedRequest) scriptedReply {
		mu.Lock()
		at := n
		n++
		mu.Unlock()
		if at == 1 {
			select {
			case <-parkCh:
			case <-ctx.Done():
				return scriptedReply{Text: "收口。", Finish: "stop"}
			}
		}
		return scriptedReply{ToolCalls: []scriptedToolCall{{
			ID: fmt.Sprintf("s%d", at+1), Name: "bash",
			Args: `{"command":"sleep 3 && echo slow"}`,
		}}, Finish: "tool_calls"}
	})
	defer model.Close()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("起脚本模型失败: %v", err)
	}
	ex := NewExecutor(store, prov, root, Budget{}) // 预算不限
	ex.Hiber = gate
	go func() {
		_, _ = ex.RunStep(ctx, task, "s1")
	}()

	// 第 1 次慢命令落地后:入眠 → 放行第 2 次请求 → 边界挂起。
	waitParkCalls := func(n int) int {
		for {
			got := 0
			for _, rec := range stepToolCalls(t, store, task, "s1") {
				if !rec.IsError {
					got++
				}
			}
			if got >= n {
				return got
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitParkCalls(1)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "hibernated"},
	}); err != nil {
		t.Fatalf("冬眠记账失败: %v", err)
	}
	gate.Enter(task, "started")
	close(parkCh)
	h := &parkHarness{store: store, task: task}
	h.waitParkSignature(t, 8*time.Second)
	time.Sleep(300 * time.Millisecond) // 冬眠时长入账(credit > 0)
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "started"},
	}); err != nil {
		t.Fatalf("唤醒记账失败: %v", err)
	}
	gate.Exit(task)

	// 唤醒后观察窗(8 秒):步骤应持续执行——误杀路径在唤醒 ~1 秒打断
	// 慢工具、收尾后落 exhausted(timeout),窗内必被抓住;正常路径单位
	// 一直慢命令循环(轮次不限),不会落地。
	landedExhausted := func() string {
		note := ""
		for _, rec := range stepSwarmRecords(t, store, task) {
			if rec.Type == rings.SwarmRecSessionEnd {
				note = rec.Note
			}
		}
		return note
	}
	observed := time.Now().Add(8 * time.Second)
	for time.Now().Before(observed) {
		if note := landedExhausted(); note != "" {
			t.Fatalf("唤醒后单位落地(误判总闸到点): %s", note)
		}
		if st, err := store.Node(ctx, task, "s1"); err != nil || st.Status != "running" {
			t.Fatalf("唤醒后步骤应仍在执行: status=%s err=%v", st.Status, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// 且唤醒后确有新进展(第 2 次慢命令完成,非原地静默)。
	if got := waitParkCalls(2); got < 2 {
		t.Fatalf("唤醒后应有新的已完成调用: %d", got)
	}
}
