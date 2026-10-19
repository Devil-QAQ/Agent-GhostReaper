package manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// P2-06f 重启收养固定序列(第七章「故障与恢复语义」后半)的契约测试:
// 有循环体(OnTaskRun)时全量自动恢复——重建句柄 → 幽灵清理 → 休眠类
// 保持 → 运行中重拉循环 → 排队任务按原次序 FIFO 重准入;无循环体时
// 维持 P2-02c 兜底(收养为已暂停,TestRestartAdoptsRunningTasks 覆盖)。

// adoptionLoop 记录拉起与唤醒的测试循环体:等一次收养唤醒(信号),
// 随后挂住直到 ctx 结束。
func adoptionLoop(started chan<- string) TaskRunFunc {
	return func(ctx context.Context, taskID string, wake <-chan struct{}) {
		select {
		case <-wake:
			select {
			case started <- taskID:
			default:
			}
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}
}

// seedCrash 模拟上一进程崩亡的账本残迹:生命周期事件 + 可选幽灵步骤
// (running 残留,崩溃时单位在飞)。
func seedCrash(t *testing.T, store *rings.Store, id string, actions []string, ghostSteps ...string) {
	t.Helper()
	ctx := context.Background()
	var evs []rings.Event
	for _, a := range actions {
		evs = append(evs, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: a, Title: "上一进程的任务 " + id}})
	}
	for _, s := range ghostSteps {
		evs = append(evs, rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: s, NodeType: rings.NodeStep, Title: "在飞意图", Status: "running"}})
	}
	if _, err := store.Append(ctx, id, evs...); err != nil {
		t.Fatalf("铺崩亡残迹 %s 失败: %v", id, err)
	}
}

func adoptionBlackboxOf(t *testing.T, store *rings.Store, id string) []rings.BlackboxRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatalf("查 %s 事件流失败: %v", id, err)
	}
	var out []rings.BlackboxRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var rec rings.BlackboxRecord
		if err := json.Unmarshal(ev.Payload, &rec); err != nil {
			t.Fatalf("解码黑匣子: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// stopAllUntilTerminal 清理兜底:反复 Stop 直至全部任务落终态。
// 规避 Stop/retire 的既有竞态窗口(retire 已从队列取出任务、尚未
// launch 时,Stop 按 queued 分支将其置停,retire 随后照常 launch,
// 该任务再无人 cancel 其 ctx,Close 永等)——第二轮 Stop 会把被重拉
// 的任务按 started 正常熔断。断言不受影响(仅清理路径加固)。
func stopAllUntilTerminal(m *Manager) {
	for pass := 0; pass < 5; pass++ {
		all := true
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
			if st := tk.Status(); st != StatusStopped && st != StatusCompleted {
				all = false
			}
		}
		if all {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRestartFullAdoption 全量收养契约:运行中/收尾中任务重拉(收到
// 收养唤醒)、排队任务按原入队次序重准入、已暂停保持(含其幽灵步骤
// 照样清理)、终态原样零新增;幽灵步骤全部重置回可领取;黑匣子留
// restart_adoption 审计。
func TestRestartFullAdoption(t *testing.T) {
	store := openStore(t)
	// 崩亡形态:两个运行中(其一在飞幽灵步骤)、两个排队(先 q1 后
	// q2)、一个暂停(也残留幽灵步骤)、一个终态。
	seedCrash(t, store, "run-a", []string{StatusCreated, StatusStarted}, "s-a1")
	seedCrash(t, store, "settle-b", []string{StatusCreated, StatusStarted, StatusSettling}, "s-b1")
	seedCrash(t, store, "q1", []string{StatusCreated, StatusQueued})
	seedCrash(t, store, "q2", []string{StatusCreated, StatusQueued})
	seedCrash(t, store, "paused-c", []string{StatusCreated, StatusStarted, StatusPaused}, "s-c1")
	seedCrash(t, store, "done-d", []string{StatusCreated, StatusStarted, StatusCompleted})

	woken := make(chan string, 8)
	m, err := New(Config{
		Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 2,
		OnTaskRun: adoptionLoop(woken),
	})
	if err != nil {
		t.Fatalf("重启构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		stopAllUntilTerminal(m)
		m.Close()
	})

	// 状态落定:运行类拉起、排队类排队、暂停保持、终态原样。
	for id, want := range map[string]string{
		"run-a": StatusStarted, "settle-b": StatusStarted,
		"q1": StatusQueued, "q2": StatusQueued,
		"paused-c": StatusPaused, "done-d": StatusCompleted,
	} {
		tk := m.Task(id)
		if tk == nil {
			t.Fatalf("任务 %s 应装载进内存注册表", id)
		}
		if tk.Status() != want {
			t.Fatalf("任务 %s 重启后应为 %s,实际 %s", id, want, tk.Status())
		}
	}

	// 收养唤醒送达:重拉的任务立即一轮 cycle(拉起即唤醒)。
	gotWoken := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for len(gotWoken) < 2 {
		select {
		case id := <-woken:
			gotWoken[id] = true
		case <-deadline:
			t.Fatalf("重拉任务应收到收养唤醒,已收到 %v", gotWoken)
		}
	}
	if gotWoken["q1"] || gotWoken["q2"] {
		t.Fatalf("排队任务不应被唤醒执行(尚无名额): %v", gotWoken)
	}

	// 幽灵清理:全部 running 步骤(含已暂停任务的残留)重置回可领取。
	for task, step := range map[string]string{
		"run-a": "s-a1", "settle-b": "s-b1", "paused-c": "s-c1",
	} {
		n, err := store.Node(context.Background(), task, step)
		if err != nil {
			t.Fatalf("查步骤 %s/%s: %v", task, step, err)
		}
		if n.Status != "dispatched" {
			t.Fatalf("幽灵步骤 %s/%s 应重置为 dispatched,实际 %s", task, step, n.Status)
		}
	}

	// 黑匣子审计:收养动作留 restart_adoption(带幽灵清单)。
	for task := range map[string]string{"run-a": "", "settle-b": "", "q1": "", "q2": "", "paused-c": ""} {
		found := false
		for _, rec := range adoptionBlackboxOf(t, store, task) {
			if rec.Type == rings.BlackboxRestartAdoption {
				found = true
			}
		}
		if !found {
			t.Fatalf("任务 %s 应留 restart_adoption 黑匣子审计", task)
		}
	}
	// 终态任务零新增事件(无收养动作、无幽灵)。
	if evs, err := store.Events(context.Background(), "done-d", 0, 0); err != nil || len(evs) != 3 {
		t.Fatalf("终态任务应零新增事件,得到 %d 条(err=%v)", len(evs), err)
	}
	// 收养生命周期记账:重拉任务带 adopted 细节与原状态。
	adopt := lifecycleDetailOf(t, store, "settle-b", StatusStarted)
	if adopt["adopted"] != true || adopt["from"] != StatusSettling {
		t.Fatalf("重拉事件应带 adopted/from 细节: %+v", adopt)
	}

	// FIFO 重准入次序:释放一个名额(run-a 熔断),队首 q1 先拉起。
	if err := m.Stop("run-a"); err != nil {
		t.Fatalf("熔断 run-a 失败: %v", err)
	}
	select {
	case id := <-woken:
		if id != "q1" {
			t.Fatalf("名额释放应先拉起原队首 q1,实际 %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("释放名额后 q1 未被拉起(并发名额死锁)")
	}
	if tk := m.Task("q2"); tk.Status() != StatusQueued {
		t.Fatalf("q2 应仍在排队,实际 %s", tk.Status())
	}
}

// lifecycleDetailOf 取任务指定动作的最新事件 detail。
func lifecycleDetailOf(t *testing.T, store *rings.Store, id, action string) map[string]any {
	t.Helper()
	evs, err := store.Events(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatalf("查 %s 事件流失败: %v", id, err)
	}
	var out map[string]any
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解码生命周期: %v", err)
		}
		if p.Action == action {
			out = p.Detail
		}
	}
	if out == nil {
		t.Fatalf("任务 %s 缺少 %s 事件", id, action)
	}
	return out
}

// TestRestartAdoptionConcurrencyOverflow 名额不足的溢出收养:并发上限
// 收窄后,原运行任务溢出进等待队列,按原状态落定次序重准入(先起者
// 先入队),不丢任务不占超名额。
func TestRestartAdoptionConcurrencyOverflow(t *testing.T) {
	store := openStore(t)
	seedCrash(t, store, "old-1", []string{StatusCreated, StatusStarted})
	seedCrash(t, store, "old-2", []string{StatusCreated, StatusStarted})
	seedCrash(t, store, "old-3", []string{StatusCreated, StatusStarted})

	woken := make(chan string, 8)
	m, err := New(Config{
		Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1,
		OnTaskRun: adoptionLoop(woken),
	})
	if err != nil {
		t.Fatalf("重启构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		stopAllUntilTerminal(m)
		m.Close()
	})
	if tk := m.Task("old-1"); tk.Status() != StatusStarted {
		t.Fatalf("先起任务应占名额拉起,实际 %s", tk.Status())
	}
	for _, id := range []string{"old-2", "old-3"} {
		if tk := m.Task(id); tk.Status() != StatusQueued {
			t.Fatalf("溢出任务 %s 应重排队,实际 %s", id, tk.Status())
		}
	}
	// 队内次序:old-2 先于 old-3(按原 started 落定次序)。
	m.mu.Lock()
	var order []string
	for _, q := range m.queue {
		order = append(order, q.ID)
	}
	m.mu.Unlock()
	if strings.Join(order, ",") != "old-2,old-3" {
		t.Fatalf("等待队列次序应为 old-2,old-3,实际 %v", order)
	}
	// 名额守恒:逐一释放,两个溢出任务先后拉起,无死锁。先排干 old-1
	// 的初始收养唤醒缓冲,再观察 retire 拉起队首。
	select {
	case <-woken:
	default:
	}
	if err := m.Stop("old-1"); err != nil {
		t.Fatalf("熔断 old-1: %v", err)
	}
	select {
	case id := <-woken:
		if id != "old-2" {
			t.Fatalf("应先拉起 old-2,实际 %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("old-2 未被拉起")
	}
}
