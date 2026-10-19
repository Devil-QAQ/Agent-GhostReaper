package manager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

func openStore(t *testing.T) *rings.Store {
	t.Helper()
	s, err := rings.Open(filepath.Join(t.TempDir(), "rings.db"))
	if err != nil {
		t.Fatalf("打开年轮存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// lifecycleActions 按顺序解出任务的生命周期动作序列(账本可审计)。
func lifecycleActions(t *testing.T, store *rings.Store, taskID string) []string {
	t.Helper()
	evs, err := store.Events(context.Background(), taskID, 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		out = append(out, p.Action)
	}
	return out
}

func TestManagerLifecycleAndFIFO(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	a, err := m.CreateTask(context.Background(), "task-a", "任务 A", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建 A 失败: %v", err)
	}
	if a.Status() != StatusStarted {
		t.Fatalf("空位时 A 应直接拉起,实际 %s", a.Status())
	}
	select {
	case <-a.Context().Done():
		t.Fatalf("A 的 ctx 不应已结束")
	default:
	}

	b, err := m.CreateTask(context.Background(), "task-b", "任务 B", rings.RoeUpdated{DeniedTools: []string{"bash"}}, nil)
	if err != nil {
		t.Fatalf("创建 B 失败: %v", err)
	}
	if b.Status() != StatusQueued {
		t.Fatalf("并发满员时 B 应排队,实际 %s", b.Status())
	}

	// 熔断 A:先控制事件、后 cancel;B 应被 FIFO 自动拉起
	if err := m.Stop("task-a"); err != nil {
		t.Fatalf("停止 A 失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.Status() != StatusStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Status() != StatusStarted {
		t.Fatalf("A 停止后 B 应自动拉起,实际 %s", b.Status())
	}
	// A 的 RoE 初始约束与生命周期动作全部入账
	got := lifecycleActions(t, store, "task-a")
	want := []string{StatusCreated, StatusStarted, StatusStopped}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("A 生命周期不符: %v", got)
	}
	gotB := lifecycleActions(t, store, "task-b")
	if strings.Join(gotB, ",") != "created,queued,started" {
		t.Fatalf("B 生命周期不符: %v", gotB)
	}
	roe, err := store.CurrentRoE(context.Background(), "task-b")
	if err != nil || len(roe.DeniedTools) != 1 || roe.DeniedTools[0] != "bash" {
		t.Fatalf("B 的初始 RoE 未入账: %+v err=%v", roe, err)
	}
	if err := m.Stop("task-b"); err != nil {
		t.Fatalf("停止 B 失败: %v", err)
	}
	// 幂等
	if err := m.Stop("task-b"); err != nil {
		t.Fatalf("重复停止应幂等: %v", err)
	}
}

// TestStopByLedgerDetail 熔断审计契约(P1-09):StopBy 的 detail 随
// stopped 事件落账——账本能回答"谁在何时熔断了哪个任务"。
func TestStopByLedgerDetail(t *testing.T) {
	store := openStore(t)
	m, _ := New(Config{Store: store, RootDir: t.TempDir()})
	t.Cleanup(m.Close)
	if _, err := m.CreateTask(context.Background(), "stop-by", "可审计熔断", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := m.StopBy("stop-by", map[string]any{"operator": "creator", "reason": "范围变更"}); err != nil {
		t.Fatalf("StopBy 失败: %v", err)
	}
	evs, err := store.Events(context.Background(), "stop-by", 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		if p.Action != StatusStopped {
			continue
		}
		found = true
		if p.Detail["operator"] != "creator" || p.Detail["reason"] != "范围变更" {
			t.Fatalf("stopped 事件应携带操作者与理由: %+v", p.Detail)
		}
		if ev.OccurredAt.IsZero() {
			t.Fatalf("stopped 事件应携带发生时刻")
		}
	}
	if !found {
		t.Fatalf("账本中应有 stopped 事件")
	}
}

func TestTaskCtxIsolation(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 2})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	a, _ := m.CreateTask(context.Background(), "iso-a", "A", rings.RoeUpdated{}, nil)
	b, _ := m.CreateTask(context.Background(), "iso-b", "B", rings.RoeUpdated{}, nil)
	if a.Status() != StatusStarted || b.Status() != StatusStarted {
		t.Fatalf("两个任务都应拉起: %s %s", a.Status(), b.Status())
	}

	// 熔断 A:A 的 ctx 结束,B 无感知且可继续写账本
	if err := m.Stop("iso-a"); err != nil {
		t.Fatalf("停止 A 失败: %v", err)
	}
	select {
	case <-a.Context().Done():
	default:
		t.Fatalf("A 的 ctx 应已结束")
	}
	select {
	case <-b.Context().Done():
		t.Fatalf("B 的 ctx 不应受 A 熔断影响")
	default:
	}
	if _, err := store.Append(b.Context(), "iso-b", rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: StatusPaused},
	}); err != nil {
		t.Fatalf("B 应仍可写账本: %v", err)
	}
}

func TestPanicIsolation(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	a, _ := m.CreateTask(context.Background(), "boom", "会炸的任务", rings.RoeUpdated{}, nil)
	a.mu.Lock()
	a.testHook = func(ctx context.Context, t *Task) { panic("任务内恐慌") }
	a.mu.Unlock()
	// testHook 在 goroutine 启动时执行——对已在跑的任务注入可能错过首次
	// 启动(读钩子与注入赛跑),因此走 暂停→恢复 重新拉起一次,钩子必然被消费。
	if err := m.Pause("boom"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	if err := m.Resume("boom"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	// Resume 后 goroutine 重启并触发 panic 钩子:任务应让位 paused 且带细节,
	// Manager 与账本不受影响。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := store.Events(context.Background(), "boom", 0, 0)
		for _, ev := range evs {
			if ev.Kind == rings.EventTaskLifecycle && strings.Contains(string(ev.Payload), "任务内恐慌") {
				// 崩溃留痕确认;同时验证名额已释放:新任务可拉起
				c, err := m.CreateTask(context.Background(), "after-boom", "C", rings.RoeUpdated{}, nil)
				if err != nil {
					t.Fatalf("崩溃后 Manager 应可用: %v", err)
				}
				if c.Status() != StatusStarted {
					t.Fatalf("崩溃释放的名额应可被新任务使用,实际 %s", c.Status())
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("panic 未被隔离留痕")
}

// ---- P1-05:任务循环体挂接点(OnTaskRun) ----

// TestOnTaskRunHook 挂接契约:回调拿到任务 ID 与唤醒通道;唤醒信号
// 经 Task.Wake() 送达(容量 1,挂起期不丢);熔断经任务 ctx 传达给
// 回调;回调返回后任务 goroutine 结束、名额让位(Close 可返回)。
func TestOnTaskRunHook(t *testing.T) {
	store := openStore(t)
	var mu sync.Mutex
	var taskIDs []string
	wakeCount := 0
	ctxSeenDone := false
	release := make(chan struct{})
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1,
		OnTaskRun: func(ctx context.Context, taskID string, wake <-chan struct{}) {
			mu.Lock()
			taskIDs = append(taskIDs, taskID)
			mu.Unlock()
			<-wake // 事件唤醒通道送达
			mu.Lock()
			wakeCount++
			mu.Unlock()
			<-release // 挂住循环体,直到测试放行
			select {
			case <-ctx.Done():
				mu.Lock()
				ctxSeenDone = true
				mu.Unlock()
			default:
			}
		}})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(m.Close)

	a, err := m.CreateTask(context.Background(), "hook-a", "挂接任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if a.Status() != StatusStarted {
		t.Fatalf("任务应拉起: %s", a.Status())
	}
	a.Wake()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := wakeCount == 1 && len(taskIDs) == 1
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	gotIDs, gotWake := append([]string(nil), taskIDs...), wakeCount
	mu.Unlock()
	if len(gotIDs) != 1 || gotIDs[0] != "hook-a" || gotWake != 1 {
		t.Fatalf("回调未收到任务身份/唤醒: ids=%v wake=%d", gotIDs, gotWake)
	}

	// 熔断:先控制事件后 cancel(次序由既有测试保证),回调经 ctx 感知。
	if err := m.Stop("hook-a"); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	close(release)
	m.Close() // 等任务 goroutine 退出
	if !ctxSeenDone {
		t.Fatalf("回调应经任务 ctx 感知熔断")
	}
	if a.Status() != StatusStopped {
		t.Fatalf("任务应 stopped: %s", a.Status())
	}
}

// ---- P1-10:任务完成终态 ----

// TestTaskBodyReturnCompletes 完成终态契约:任务主体(OnTaskRun 循环体)
// 自然返回(未熔断、未暂停)= 宣告完成——completed 事件落账
// (actor=manager,理由注明经仲裁者判定)、状态翻转为 completed、
// 并发名额让位(排队任务可拉起);终态不可再熔断(StopBy 幂等)。
func TestTaskBodyReturnCompletes(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1,
		OnTaskRun: func(ctx context.Context, taskID string, wake <-chan struct{}) {
			// 模拟主宰循环:等一次唤醒后自然返回(全部目标达成退出);
			// 熔断则随 ctx 收尾退出(与 Loop.Run 的 select 语义同构)。
			select {
			case <-wake:
			case <-ctx.Done():
			}
		}})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	a, err := m.CreateTask(context.Background(), "done-a", "会完成的任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建 A 失败: %v", err)
	}
	b, err := m.CreateTask(context.Background(), "done-b", "排队任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建 B 失败: %v", err)
	}
	if b.Status() != StatusQueued {
		t.Fatalf("满员时 B 应排队: %s", b.Status())
	}

	a.Wake() // 循环体返回 → 完成
	deadline := time.Now().Add(5 * time.Second)
	for a.Status() != StatusCompleted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Status() != StatusCompleted {
		t.Fatalf("循环体自然返回后任务应 completed: %s", a.Status())
	}
	// 名额让位:B 被自动拉起。
	for b.Status() != StatusStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Status() != StatusStarted {
		t.Fatalf("完成让出的名额应被 B 复用: %s", b.Status())
	}

	// 账本:completed 事件(actor=manager、理由可审计),恰 1 条。
	evs, err := store.Events(context.Background(), "done-a", 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	completed := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		if p.Action != StatusCompleted {
			continue
		}
		completed++
		if ev.Actor != "manager" || p.Detail["reason"] != "全部目标达成(仲裁者判定或面板人工标记)" {
			t.Fatalf("completed 事件审计字段不符: actor=%s detail=%+v", ev.Actor, p.Detail)
		}
		if ev.OccurredAt.IsZero() {
			t.Fatalf("completed 事件应携带发生时刻")
		}
	}
	if completed != 1 {
		t.Fatalf("应恰 1 条 completed 事件,实际 %d", completed)
	}

	// 终态不可再熔断:StopBy 幂等,不追加 stopped。
	if err := m.StopBy("done-a", nil); err != nil {
		t.Fatalf("completed 任务再熔断应幂等: %v", err)
	}
	got := lifecycleActions(t, store, "done-a")
	want := []string{StatusCreated, StatusStarted, StatusCompleted}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("A 生命周期不符(终态后不得再有动作): %v", got)
	}
}

// TestTaskBodyStoppedReturnNoComplete 熔断竞态:循环体因熔断 cancel 返回
// 时不得误记 completed(stopped 是唯一终态)。
func TestTaskBodyStoppedReturnNoComplete(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir(),
		OnTaskRun: func(ctx context.Context, _ string, wake <-chan struct{}) {
			<-ctx.Done() // 挂到被熔断
		}})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(m.Close)

	if _, err := m.CreateTask(context.Background(), "stop-ret", "被熔断", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := m.Stop("stop-ret"); err != nil {
		t.Fatalf("熔断失败: %v", err)
	}
	m.Close()
	got := lifecycleActions(t, store, "stop-ret")
	want := []string{StatusCreated, StatusStarted, StatusStopped}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("熔断返回不得记 completed: %v", got)
	}
}

func TestPauseResumeQueue(t *testing.T) {
	store := openStore(t)
	m, _ := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1})
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	a, _ := m.CreateTask(context.Background(), "pr-a", "A", rings.RoeUpdated{}, nil)
	b, _ := m.CreateTask(context.Background(), "pr-b", "B", rings.RoeUpdated{}, nil)
	if b.Status() != StatusQueued {
		t.Fatalf("B 应排队: %s", b.Status())
	}
	if err := m.Pause("pr-a"); err != nil {
		t.Fatalf("暂停 A 失败: %v", err)
	}
	if a.Status() != StatusPaused {
		t.Fatalf("A 应 paused: %s", a.Status())
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.Status() != StatusStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Status() != StatusStarted {
		t.Fatalf("暂停让位后 B 应拉起: %s", b.Status())
	}
	// 恢复 A:满员 → 排队
	if err := m.Resume("pr-a"); err != nil {
		t.Fatalf("恢复 A 失败: %v", err)
	}
	if a.Status() != StatusQueued {
		t.Fatalf("恢复但满员时 A 应排队: %s", a.Status())
	}
	_ = m.Stop("pr-b")
	deadline = time.Now().Add(5 * time.Second)
	for a.Status() != StatusStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Status() != StatusStarted {
		t.Fatalf("B 停止后 A 应最终拉起: %s", a.Status())
	}
}

// ---- P2-02c:任务专属循环体(主装配路径的每任务全家桶入口) ----

// TestTaskLoopRegistration 专属循环体契约:登记的任务拉起时运行专属
// 循环体而非全局 OnTaskRun;未登记任务照走全局;登记进程内黏着(暂停
// 后恢复再拉起仍用专属循环体);专属循环体自然返回同样落 completed
// 终态(与全局装配同一完成语义);已存在任务再登记即拒。
func TestTaskLoopRegistration(t *testing.T) {
	store := openStore(t)
	var mu sync.Mutex
	ran := map[string]int{} // taskID -> 专属循环体运行次数
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 3,
		OnTaskRun: func(ctx context.Context, taskID string, wake <-chan struct{}) {
			// 全局装配的哨兵:专属任务不得走到这里。
			mu.Lock()
			ran[taskID+"-global"]++
			mu.Unlock()
			<-ctx.Done()
		}})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	if err := m.RegisterLoop("own-a", func(ctx context.Context, _ string, _ <-chan struct{}) {
		mu.Lock()
		ran["own-a"]++
		mu.Unlock()
		<-ctx.Done() // 挂在任务 ctx 上,熔断/暂停即退出
	}); err != nil {
		t.Fatalf("登记专属循环体失败: %v", err)
	}
	if _, err := m.CreateTask(context.Background(), "own-a", "专属循环体任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := m.CreateTask(context.Background(), "plain-b", "全局装配任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := ran["own-a"] == 1 && ran["plain-b-global"] == 1 && ran["own-a-global"] == 0
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	gotOwn, gotPlainLeak := ran["own-a"], ran["own-a-global"]
	mu.Unlock()
	if gotOwn != 1 || gotPlainLeak != 0 {
		t.Fatalf("专属任务应只运行专属循环体: own=%d own走全局=%d", gotOwn, gotPlainLeak)
	}

	// 黏着:暂停 → 恢复再拉起,仍用专属循环体(不走全局装配)。
	if err := m.Pause("own-a"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	if err := m.Resume("own-a"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	for time.Now().Before(deadline) {
		mu.Lock()
		twice := ran["own-a"] == 2
		mu.Unlock()
		if twice {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	twice := ran["own-a"]
	mu.Unlock()
	if twice != 2 {
		t.Fatalf("恢复再拉起应仍用专属循环体: %d", twice)
	}

	// 已存在任务再登记即拒(须在建任务前登记)。
	if err := m.RegisterLoop("plain-b", func(context.Context, string, <-chan struct{}) {}); err == nil {
		t.Fatalf("已存在任务登记专属循环体应拒绝")
	}
	_ = m.Stop("own-a")
	_ = m.Stop("plain-b")

	// 专属循环体自然返回 → completed 终态(与全局装配同一完成语义)。
	if err := m.RegisterLoop("own-done", func(context.Context, string, <-chan struct{}) {
		// 模拟全部目标达成后主宰循环自然退出。
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	c, err := m.CreateTask(context.Background(), "own-done", "会完成的专属任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	for c.Status() != StatusCompleted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Status() != StatusCompleted {
		t.Fatalf("专属循环体自然返回应落 completed: %s", c.Status())
	}
	got := lifecycleActions(t, store, "own-done")
	want := []string{StatusCreated, StatusStarted, StatusCompleted}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("专属任务生命周期不符: %v", got)
	}
}

// TestRestartAdoptsRunningTasks 重启收养契约(P2-02c,第七章 §6):上一
// 进程突然死亡后,账本中"运行中/排队中"的任务被新 Manager 收养为已
// 暂停并记账(orphan 细节携带原状态);其余状态原样装载且零新增事件;
// 收养任务在主 Manager 内存注册表内可熔断、可恢复——不存在"账本
// 运行中、内存无运行体"的悬空态。
func TestRestartAdoptsRunningTasks(t *testing.T) {
	store := openStore(t)
	// 模拟上一进程崩亡:各状态任务的账本残迹(直写账本即崩亡形态)。
	mk := func(id string, actions ...string) {
		t.Helper()
		var evs []rings.Event
		for _, a := range actions {
			evs = append(evs, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
				Payload: rings.TaskLifecycle{Action: a, Title: "上一进程的任务"}})
		}
		if _, err := store.Append(context.Background(), id, evs...); err != nil {
			t.Fatalf("铺账本 %s 失败: %v", id, err)
		}
	}
	mk("was-run", StatusCreated, StatusStarted)
	mk("was-queued", StatusCreated, StatusQueued)
	mk("was-paused", StatusCreated, StatusStarted, StatusPaused)
	mk("was-stopped", StatusCreated, StatusStarted, StatusStopped)
	mk("was-completed", StatusCreated, StatusStarted, StatusCompleted)

	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1})
	if err != nil {
		t.Fatalf("重启构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	// 全部任务装载进内存注册表;运行中/排队中收养为已暂停,其余原状。
	for id, want := range map[string]string{
		"was-run": StatusPaused, "was-queued": StatusPaused, "was-paused": StatusPaused,
		"was-stopped": StatusStopped, "was-completed": StatusCompleted,
	} {
		tk := m.Task(id)
		if tk == nil {
			t.Fatalf("任务 %s 应装载进主 Manager 内存", id)
		}
		if tk.Status() != want {
			t.Fatalf("任务 %s 重启后应为 %s,实际 %s", id, want, tk.Status())
		}
	}

	// 收养记账:恰各多一条 paused 事件,orphan 细节携带原状态;未收养
	// 任务零新增事件。
	for id, want := range map[string][]string{
		"was-run":       {StatusCreated, StatusStarted, StatusPaused},
		"was-queued":    {StatusCreated, StatusQueued, StatusPaused},
		"was-paused":    {StatusCreated, StatusStarted, StatusPaused},
		"was-stopped":   {StatusCreated, StatusStarted, StatusStopped},
		"was-completed": {StatusCreated, StatusStarted, StatusCompleted},
	} {
		got := lifecycleActions(t, store, id)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("任务 %s 生命周期不符: %v", id, got)
		}
	}
	evs, err := store.Events(context.Background(), "was-run", 0, 0)
	if err != nil {
		t.Fatalf("查事件流失败: %v", err)
	}
	var adopt *rings.TaskLifecycle
	for i := range evs {
		if evs[i].Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(evs[i].Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		if p.Action == StatusPaused {
			adopt = &p
		}
	}
	if adopt == nil || adopt.Detail["orphan"] != true || adopt.Detail["from"] != StatusStarted {
		t.Fatalf("收养事件应带 orphan 细节与原状态: %+v", adopt)
	}

	// 收养任务可熔断(主 Manager 内存注册表可操作)且幂等。
	if err := m.Stop("was-run"); err != nil {
		t.Fatalf("熔断收养任务失败: %v", err)
	}
	if err := m.Stop("was-run"); err != nil {
		t.Fatalf("重复熔断应幂等: %v", err)
	}
	// 收养任务可恢复续跑(按当前进程装配拉起)。
	if err := m.Resume("was-queued"); err != nil {
		t.Fatalf("恢复收养任务失败: %v", err)
	}
	if tk := m.Task("was-queued"); tk.Status() != StatusStarted {
		t.Fatalf("恢复后应收养任务拉起: %s", tk.Status())
	}
}

// ---- P2-04:任务级总闸的收尾操控面契约 ----

// blockingLoop 挂住任务直到 ctx 结束(测试最小循环体)。
func blockingLoop(started *sync.WaitGroup) TaskRunFunc {
	return func(ctx context.Context, _ string, _ <-chan struct{}) {
		if started != nil {
			started.Done()
		}
		<-ctx.Done()
	}
}

// TestSettlingLifecycleContract settling 状态机契约:MarkSettling 只从
// started 进入;FinishSettling 全达成落 completed、未达成落 stopped;
// 收尾中可熔断(操作者硬停优先);非法迁移如实报错。
func TestSettlingLifecycleContract(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 2})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	// 两名额占满,第三个任务排队——排队任务(非 started)不得进收尾。
	for i, id := range []string{"fill-1", "fill-2"} {
		var wg sync.WaitGroup
		wg.Add(1)
		_ = m.RegisterLoop(id, blockingLoop(&wg))
		if _, err := m.CreateTask(context.Background(), id, "占位"+strconv.Itoa(i), rings.RoeUpdated{}, nil); err != nil {
			t.Fatalf("建占位任务失败: %v", err)
		}
		wg.Wait()
	}
	if _, err := m.CreateTask(context.Background(), "set-q", "排队任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建排队任务失败: %v", err)
	}
	if err := m.MarkSettling("set-q", nil); err == nil {
		t.Fatalf("排队任务不得进入收尾")
	}
	if err := m.FinishSettling("set-q", true, nil); err == nil {
		t.Fatalf("非 settling 任务不得定终态")
	}

	// 释放一名额,排队任务转 started → settling → completed(全达成)。
	if err := m.Stop("fill-2"); err != nil {
		t.Fatalf("释放名额失败: %v", err)
	}
	waitForStatus(t, m, "set-q", StatusStarted)
	if err := m.MarkSettling("set-q", map[string]any{"reason": "整体超时"}); err != nil {
		t.Fatalf("进入收尾失败: %v", err)
	}
	if got := m.Task("set-q").Status(); got != StatusSettling {
		t.Fatalf("状态应收尾中: %s", got)
	}
	if err := m.FinishSettling("set-q", true, nil); err != nil {
		t.Fatalf("定终态失败: %v", err)
	}
	if got := m.Task("set-q").Status(); got != StatusCompleted {
		t.Fatalf("全达成应收完成终态: %s", got)
	}
	if err := m.Stop("set-q"); err != nil {
		t.Fatalf("完成终态再熔断应幂等: %v", err)
	}
	if got := lifecycleActions(t, store, "set-q"); len(got) < 4 ||
		got[0] != "created" || got[1] != "queued" || got[2] != "started" || got[3] != "settling" {
		t.Fatalf("生命周期序列应含 settling: %v", got)
	}

	// started → settling → stopped(未达成)。
	var wgB sync.WaitGroup
	wgB.Add(1)
	_ = m.RegisterLoop("set-b", blockingLoop(&wgB))
	if _, err := m.CreateTask(context.Background(), "set-b", "收尾契约 B", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务 B 失败: %v", err)
	}
	wgB.Wait()
	if err := m.MarkSettling("set-b", nil); err != nil {
		t.Fatalf("进入收尾 B 失败: %v", err)
	}
	if err := m.FinishSettling("set-b", false, nil); err != nil {
		t.Fatalf("定终态 B 失败: %v", err)
	}
	if got := m.Task("set-b").Status(); got != StatusStopped {
		t.Fatalf("未达成应收停止终态: %s", got)
	}

	// 收尾中被熔断:stopped 由 StopBy 记账,FinishSettling 如实报错。
	var wgC sync.WaitGroup
	wgC.Add(1)
	_ = m.RegisterLoop("set-c", blockingLoop(&wgC))
	if _, err := m.CreateTask(context.Background(), "set-c", "收尾契约 C", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务 C 失败: %v", err)
	}
	wgC.Wait()
	if err := m.MarkSettling("set-c", nil); err != nil {
		t.Fatalf("进入收尾 C 失败: %v", err)
	}
	if err := m.StopBy("set-c", map[string]any{"operator": "creator", "reason": "收尾中硬停"}); err != nil {
		t.Fatalf("收尾中熔断失败: %v", err)
	}
	if got := m.Task("set-c").Status(); got != StatusStopped {
		t.Fatalf("熔断应即时生效: %s", got)
	}
	if err := m.FinishSettling("set-c", true, nil); err == nil {
		t.Fatalf("已熔断任务不得再定终态")
	}
}

// TestRestartAdoptsSettlingTask 重启收养(收尾中):账本 settling 的任务
// 收养为已暂停(orphan from=settling),无悬空态。
func TestRestartAdoptsSettlingTask(t *testing.T) {
	store := openStore(t)
	dir := t.TempDir()
	m1, err := New(Config{Store: store, RootDir: dir})
	if err != nil {
		t.Fatalf("构建 Manager 1 失败: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	_ = m1.RegisterLoop("was-settling", blockingLoop(&wg))
	if _, err := m1.CreateTask(context.Background(), "was-settling", "崩溃于收尾", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	wg.Wait()
	if err := m1.MarkSettling("was-settling", nil); err != nil {
		t.Fatalf("进入收尾失败: %v", err)
	}
	// 模拟进程消亡:不 Close(Close 等任务收场,而崩溃本就是不等收场)、
	// 不熔断(熔断会记 stopped)——账本停在 settling,运行件原地蒸发;
	// m1 残余 goroutine 随测试进程退出消亡。

	m2, err := New(Config{Store: store, RootDir: dir})
	if err != nil {
		t.Fatalf("构建 Manager 2 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m2.Tasks() {
			_ = m2.Stop(tk.ID)
		}
		m2.Close()
	})
	if got := m2.Task("was-settling").Status(); got != StatusPaused {
		t.Fatalf("收尾中崩溃应收养为已暂停: %s", got)
	}
	evs, _ := store.Events(context.Background(), "was-settling", 0, 0)
	var adopt *rings.TaskLifecycle
	for i := range evs {
		if evs[i].Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(evs[i].Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		if p.Action == StatusPaused {
			adopt = &p
		}
	}
	if adopt == nil || adopt.Detail["from"] != StatusSettling {
		t.Fatalf("收养事件应带 from=settling: %+v", adopt)
	}
}

func waitForStatus(t *testing.T, m *Manager, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Task(id).Status() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待任务 %s 状态 %s 超时,当前 %s", id, want, m.Task(id).Status())
}

// TestOnTerminalHook 终态回调(P2-08f):熔断落 stopped 终态后触发一次,
// 携带终态词;非终态记账不触发。
func TestOnTerminalHook(t *testing.T) {
	store := openStore(t)
	var mu sync.Mutex
	var fired []string
	m, err := New(Config{Store: store, RootDir: t.TempDir(),
		OnTerminal: func(taskID, status string) {
			mu.Lock()
			fired = append(fired, taskID+"|"+status)
			mu.Unlock()
		}})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	a, err := m.CreateTask(context.Background(), "task-term", "终态回调", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := m.StopBy(a.ID, map[string]any{"reason": "测试熔断", "by": "creator"}); err != nil {
		t.Fatalf("熔断失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(fired)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != "task-term|"+StatusStopped {
		t.Fatalf("应恰触发一次终态回调(task-term|stopped),得到 %v", fired)
	}
	// 幂等熔断不重复触发。
	if err := m.StopBy(a.ID, map[string]any{"reason": "再按一次", "by": "creator"}); err != nil {
		t.Fatalf("幂等熔断失败: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if len(fired) != 1 {
		t.Fatalf("幂等熔断不应重复触发,得到 %v", fired)
	}
}
