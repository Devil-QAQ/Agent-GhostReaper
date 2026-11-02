package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ---- P2-08 任务冬眠/唤醒 ----

// hiberTestEnv 最小装配:账本 + 冬眠门 + Manager(空循环体)。
func hiberTestEnv(t *testing.T) (*Manager, *hiber.Gate, *rings.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := rings.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("开账本失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	gate := hiber.New()
	mgr, err := New(Config{Store: store, RootDir: dir, Hiber: gate})
	if err != nil {
		t.Fatalf("建 Manager 失败: %v", err)
	}
	return mgr, gate, store
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
// started → hibernated → 唤醒恢复 started;非运行态不可冬眠;幂等。
func TestHibernateWakeLifecycle(t *testing.T) {
	mgr, gate, store := hiberTestEnv(t)
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
		t.Fatalf("冬眠门应关(执行层冻结)")
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

// TestHibernateFromSettling 收尾中亦可冬眠(收尾预算同步冻结),唤醒
// 恢复 settling。
func TestHibernateFromSettling(t *testing.T) {
	mgr, _, _ := hiberTestEnv(t)
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
	mgr, gate, _ := hiberTestEnv(t)
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
		t.Fatalf("熔断后门应放行(挂起者醒来见 ctx 结束收场)")
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
	// 循环体阻塞在 ctx 上(不自然返回——返回即完成终态)。
	blocking := func(ctx context.Context, taskID string, wake <-chan struct{}) { <-ctx.Done() }
	gate := hiber.New()
	mgr, err := New(Config{Store: store, RootDir: dir, Hiber: gate, OnTaskRun: blocking})
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
	mgr2, err := New(Config{Store: store2, RootDir: dir, Hiber: gate2, OnTaskRun: blocking})
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
	mgr, _, store := hiberTestEnv(t)
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
