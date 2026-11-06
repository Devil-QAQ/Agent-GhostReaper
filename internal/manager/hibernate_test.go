package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ---- P2-08 任务冬眠/唤醒;P4-33 冻结收场、让位名额 ----

// hiberTestEnv 最小装配:账本 + 冬眠门 + 断点门(P4-33 收场依赖)+ Manager
// (空转骨架循环体)。
func hiberTestEnv(t *testing.T) (*Manager, *hiber.Gate, *ckpt.Gate, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	gate := hiber.New()
	ckptGate := ckpt.New()
	mgr, err := New(Config{Store: store, RootDir: dir, Hiber: gate, Checkpoint: ckptGate})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	return mgr, gate, ckptGate, store
}

func createHiberTask(t *testing.T, mgr *Manager, id string) *Task {
	t.Helper()
	task, err := mgr.CreateTask(context.Background(), id, "冬眠验证", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	return task
}

// TestHibernateWakeLifecycle 冬眠/唤醒的生命周期记账与状态语义:
// started → hibernated(收场让位)→ 唤醒拉起恢复 started;非运行态不可
// 冬眠;幂等。
func TestHibernateWakeLifecycle(t *testing.T) {
	mgr, gate, _, store := hiberTestEnv(t)
	task := createHiberTask(t, mgr, "hib-1")
	if task.Status() != StatusStarted {
		t.Fatalf("新任务应 started(有空位即拉起): %s", task.Status())
	}

	if err := mgr.Hibernate("hib-1", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	if task.Status() != StatusHibernated {
		t.Fatalf("冬眠后应 hibernated: %s", task.Status())
	}
	if !gate.Active("hib-1") {
		t.Fatalf("冬眠门应关(面板只读/审批冻结的运行时权威)")
	}
	// P4-33 收场锚点:无活 goroutine(收场锚点已关)、运行句柄已清——
	// 进程内状态等价断电重启收养后。
	task.mu.Lock()
	rd := task.runDone
	task.mu.Unlock()
	select {
	case <-rd:
	default:
		t.Fatalf("冬眠收场后运行锚点应已关闭(任务 goroutine 组退出)")
	}
	if task.Context() != context.Background() {
		t.Fatalf("冬眠收场后运行 ctx 应已清空(无活 goroutine)")
	}
	// 幂等。
	if err := mgr.Hibernate("hib-1", nil); err != nil {
		t.Fatalf("重复冬眠应幂等: %v", err)
	}
	// 冬眠中不可暂停(先唤醒)。
	if err := mgr.Pause("hib-1"); err == nil {
		t.Fatalf("冬眠中暂停应被拒")
	}

	// 唤醒:记账恢复 started,门开。
	if err := mgr.Wake("hib-1", nil); err != nil {
		t.Fatalf("唤醒失败: %v", err)
	}
	if task.Status() != StatusStarted {
		t.Fatalf("唤醒后应恢复 started: %s", task.Status())
	}
	if gate.Active("hib-1") {
		t.Fatalf("冬眠门应开")
	}
	// 非冬眠态不可唤醒。
	if err := mgr.Wake("hib-1", nil); err == nil {
		t.Fatalf("非冬眠态唤醒应被拒")
	}

	// 生命周期事件序:created, started, hibernated, started(woken 标记)。
	evs, err := store.Events(context.Background(), "hib-1", 0, 0)
	if err != nil {
		t.Fatalf("查事件: %v", err)
	}
	var actions []string
	var wokenSeen bool
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		actions = append(actions, p.Action)
		if w, ok := p.Detail["woken"].(bool); ok && w {
			wokenSeen = true
		}
	}
	if len(actions) != 4 || actions[2] != "hibernated" || actions[3] != "started" {
		t.Fatalf("生命周期序应 created/started/hibernated/started: %v", actions)
	}
	if !wokenSeen {
		t.Fatalf("唤醒事件应带 woken 标记(配对扫描锚点)")
	}

	// 冬眠时长配对:hibernated→started 间 > 0。
	credit, err := store.HibernateCredit(context.Background(), "hib-1")
	if err != nil || credit <= 0 {
		t.Fatalf("冬眠时长应 >0: %v %v", credit, err)
	}
}

// TestHibernateRetireReleasesSlot P4-33 锁死根治的确定性核:并发占满时
// 冬眠让位名额,排队任务不重启进程被 FIFO 拉起开跑。
func TestHibernateRetireReleasesSlot(t *testing.T) {
	mgr, gate, _, store := hiberTestEnv(t)
	t1 := createHiberTask(t, mgr, "hib-a")
	t2 := createHiberTask(t, mgr, "hib-b")
	t3 := createHiberTask(t, mgr, "hib-c")
	if t1.Status() != StatusStarted || t2.Status() != StatusStarted {
		t.Fatalf("前两个任务应运行中: %s %s", t1.Status(), t2.Status())
	}
	if t3.Status() != StatusQueued {
		t.Fatalf("并发占满时第三个任务应排队中: %s", t3.Status())
	}
	// 冬眠 t1:名额让位 → t3 自动拉起(生命周期事件可证)。
	if err := mgr.Hibernate("hib-a", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	if !gate.Active("hib-a") {
		t.Fatalf("冬眠门应关")
	}
	waitFor(t, func() bool { return t3.Status() == StatusStarted }, "冬眠让位后排队任务应自动拉起")
	// hib-c 生命周期序 created→queued→started(排队后被让位名额拉起)。
	evs, _ := store.Events(context.Background(), "hib-c", 0, 0)
	var actions []string
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) == nil {
			actions = append(actions, p.Action)
		}
	}
	if len(actions) != 3 || actions[0] != StatusCreated || actions[1] != StatusQueued || actions[2] != StatusStarted {
		t.Fatalf("hib-c 生命周期应 created/queued/started: %v", actions)
	}
	// 再冬眠 t2:让位后无排队任务,名额空置(唤醒分支另测)。
	if err := mgr.Hibernate("hib-b", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
}

// TestWakeFullGoesQueued P4-33 唤醒满额分支:无空位 → 任务转排队中,
// 名额空出按 FIFO 自动拉起。
func TestWakeFullGoesQueued(t *testing.T) {
	mgr, _, _, _ := hiberTestEnv(t)
	t1 := createHiberTask(t, mgr, "hib-q1")
	t2 := createHiberTask(t, mgr, "hib-q2")
	t3 := createHiberTask(t, mgr, "hib-q3")
	if t1.Status() != StatusStarted || t2.Status() != StatusStarted || t3.Status() != StatusQueued {
		t.Fatalf("前置应为两跑一排: %s %s %s", t1.Status(), t2.Status(), t3.Status())
	}
	// 冬眠 t1 → 名额让位 → t3 占住(此时两名额被 t2/t3 持有)。
	if err := mgr.Hibernate("hib-q1", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	waitFor(t, func() bool { return t3.Status() == StatusStarted }, "让位名额应被 t3 占住")
	// 唤醒 t1:名额满 → 转排队中。
	if err := mgr.Wake("hib-q1", nil); err != nil {
		t.Fatalf("满额唤醒应转排队不报错: %v", err)
	}
	if t1.Status() != StatusQueued {
		t.Fatalf("满额唤醒后应排队中: %s", t1.Status())
	}
	// 名额空出(t2 冬眠让位)→ 排队任务 FIFO 自动拉起。
	if err := mgr.Hibernate("hib-q2", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	waitFor(t, func() bool { return t1.Status() == StatusStarted }, "名额空出后排队任务应自动拉起")
}

// waitFor 轮询等待条件成立(收场让位是异步让位,毫秒级)。
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}

// TestHibernateFromSettling 收尾中亦可冬眠(收尾冻结),唤醒后恢复
// settling(收尾续走)。
func TestHibernateFromSettling(t *testing.T) {
	mgr, _, _, _ := hiberTestEnv(t)
	task := createHiberTask(t, mgr, "hib-2")
	if err := mgr.MarkSettling("hib-2", nil); err != nil {
		t.Fatalf("进收尾失败: %v", err)
	}
	if err := mgr.Hibernate("hib-2", nil); err != nil {
		t.Fatalf("收尾中冬眠失败: %v", err)
	}
	if task.Status() != StatusHibernated {
		t.Fatalf("应 hibernated: %s", task.Status())
	}
	if err := mgr.Wake("hib-2", nil); err != nil {
		t.Fatalf("唤醒失败: %v", err)
	}
	if task.Status() != StatusSettling {
		t.Fatalf("唤醒应恢复 settling(冬眠前状态): %s", task.Status())
	}
}

// TestStopFromHibernated 冬眠中熔断:终态 stopped,门放行(不留悬挂)。
func TestStopFromHibernated(t *testing.T) {
	mgr, gate, _, _ := hiberTestEnv(t)
	task := createHiberTask(t, mgr, "hib-3")
	if err := mgr.Hibernate("hib-3", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	if err := mgr.StopBy("hib-3", map[string]any{"reason": "冬眠中熔断"}); err != nil {
		t.Fatalf("冬眠中熔断失败: %v", err)
	}
	if task.Status() != StatusStopped {
		t.Fatalf("熔断后应 stopped: %s", task.Status())
	}
	if gate.Active("hib-3") {
		t.Fatalf("熔断后门应放行")
	}
}

// TestRestartKeepsHibernated 重启收养:冬眠保持冬眠(不自动拉起),
// 唤醒走拉起路径恢复 started。
func TestRestartKeepsHibernated(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	store, err := rings.Open(db)
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	// P4-33 起冬眠收场依赖断点令;空转骨架循环体(OnTaskRun=nil)遵守
	// "武装即退出"契约,收场让位后重启。
	gate := hiber.New()
	ckptGate := ckpt.New()
	mgr, err := New(Config{Store: store, RootDir: dir, Hiber: gate, Checkpoint: ckptGate})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	if _, err := mgr.CreateTask(context.Background(), "hib-4", "重启保持", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	if err := mgr.Hibernate("hib-4", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	store.Close()

	// 重启:冬眠任务不重拉(与已暂停同槽),状态保持。
	store2, err := rings.Open(db)
	if err != nil {
		t.Fatalf("重开账本失败: %v", err)
	}
	t.Cleanup(func() { store2.Close() })
	gate2 := hiber.New()
	ckpt2 := ckpt.New()
	mgr2, err := New(Config{Store: store2, RootDir: dir, Hiber: gate2, Checkpoint: ckpt2})
	if err != nil {
		t.Fatalf("重建 Manager 失败: %v", err)
	}
	if t4 := mgr2.Task("hib-4"); t4 == nil || t4.Status() != StatusHibernated {
		t.Fatalf("重启后冬眠应保持: %v", t4)
	}
	// 重启后唤醒:门上无记录,按 started 拉起。
	if err := mgr2.Wake("hib-4", nil); err != nil {
		t.Fatalf("重启后唤醒失败: %v", err)
	}
	if t4 := mgr2.Task("hib-4"); t4.Status() != StatusStarted {
		t.Fatalf("重启后唤醒应 started: %s", t4.Status())
	}
	// 收尾清场。
	_ = mgr2.StopBy("hib-4", nil)
	time.Sleep(100 * time.Millisecond)
}

// TestHibernatedTaskProjection 冬眠态在任务投影中可见(面板数据面)。
func TestHibernatedTaskProjection(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	createHiberTask(t, mgr, "hib-5")
	if err := mgr.Hibernate("hib-5", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	rows, err := store.Tasks(context.Background())
	if err != nil {
		t.Fatalf("查任务: %v", err)
	}
	for _, r := range rows {
		if r.ID == "hib-5" && r.Status != StatusHibernated {
			t.Fatalf("投影状态应 hibernated: %s", r.Status)
		}
	}
}
