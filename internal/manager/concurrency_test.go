package manager

// P4-34 任务并发上限入系统设置:Manager 各准入判定点活读全局键
// (键覆盖优先、缺省 2、值非法 fail-safe 退默认),调大后队列扫描按
// FIFO 拉起排队任务,调小不硬杀在跑、自然终态后回落;重启收养按当前
// 生效值分配名额。确定性单测(无模型)。

import (
	"context"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// waitStatus 轮询等待任务到达目标状态(retire 的拉起在 goroutine 退出
// 后异步发生)。
func waitStatus(t *testing.T, tk *Task, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for tk.Status() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tk.Status() != want {
		t.Fatalf("任务 %s 状态应为 %s,实际 %s", tk.ID, want, tk.Status())
	}
}

// TestTaskConcurrencyKVLiveRead 键值表覆盖全链:缺省 2 → 写键 3 +
// ScanQueue 拉起排队 → 调小 1 不硬杀、新建排队 → 名额逐个释放后回落。
func TestTaskConcurrencyKVLiveRead(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	// 不设显式 MaxConcurrentTasks:各判定点活读全局键。
	m, err := New(Config{Store: store, RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})

	// 缺省 2(无任何配置,行为与 P4-34 前一致):a/b 起跑、c 排队。
	a, _ := m.CreateTask(ctx, "tc-a", "A", rings.RoeUpdated{}, nil)
	b, _ := m.CreateTask(ctx, "tc-b", "B", rings.RoeUpdated{}, nil)
	c, _ := m.CreateTask(ctx, "tc-c", "C", rings.RoeUpdated{}, nil)
	if a.Status() != StatusStarted || b.Status() != StatusStarted || c.Status() != StatusQueued {
		t.Fatalf("缺省上限 2 应 a/b 起跑 c 排队: %s %s %s", a.Status(), b.Status(), c.Status())
	}

	// 写键 3 + 队列扫描:c 立即自动拉起(started 落账可证)。
	if err := store.SetSetting(ctx, SettingKeyMaxConcurrentTasks, "3", "test"); err != nil {
		t.Fatalf("写并发键失败: %v", err)
	}
	m.ScanQueue()
	if c.Status() != StatusStarted {
		t.Fatalf("调大并扫描后 c 应自动拉起,实际 %s", c.Status())
	}
	if got := lifecycleActions(t, store, "tc-c"); strings.Join(got, ",") != "created,queued,started" {
		t.Fatalf("c 生命周期不符: %v", got)
	}

	// 调小 1:在跑 3 个不硬杀;d 新建只能排队。
	if err := store.SetSetting(ctx, SettingKeyMaxConcurrentTasks, "1", "test"); err != nil {
		t.Fatalf("写并发键失败: %v", err)
	}
	d, _ := m.CreateTask(ctx, "tc-d", "D", rings.RoeUpdated{}, nil)
	if d.Status() != StatusQueued {
		t.Fatalf("上限 1 满员时 d 应排队,实际 %s", d.Status())
	}
	for _, id := range []string{"tc-a", "tc-b", "tc-c"} {
		if tk := m.Task(id); tk.Status() != StatusStarted {
			t.Fatalf("调小后 %s 不应被硬杀,实际 %s", id, tk.Status())
		}
	}

	// 自然终态逐个回落:停 a、b(名额仍 ≥1,d 不动);停 c 后名额归 0,
	// d 按 FIFO 自动拉起。
	_ = m.Stop("tc-a")
	_ = m.Stop("tc-b")
	time.Sleep(100 * time.Millisecond) // 两次 retire 落定
	if d.Status() != StatusQueued {
		t.Fatalf("名额未空时 d 不应拉起,实际 %s", d.Status())
	}
	_ = m.Stop("tc-c")
	waitStatus(t, d, StatusStarted)
}

// TestTaskConcurrencyFailSafe 键值非法(非整数/0/负数)与清空:一律退
// 默认 2,配置故障不挡运行。
func TestTaskConcurrencyFailSafe(t *testing.T) {
	store := openStore(t)
	m, err := New(Config{Store: store, RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})
	if got := m.maxTasks(); got != DefaultMaxConcurrentTasks {
		t.Fatalf("无配置时应退默认 %d,实际 %d", DefaultMaxConcurrentTasks, got)
	}
	for _, bad := range []string{"abc", "0", "-3", " "} {
		if err := store.SetSetting(context.Background(), SettingKeyMaxConcurrentTasks, bad, "test"); err != nil {
			t.Fatalf("写并发键失败: %v", err)
		}
		if got := m.maxTasks(); got != DefaultMaxConcurrentTasks {
			t.Fatalf("非法值 %q 应退默认,实际 %d", bad, got)
		}
	}
	// 清空(删键)同样回默认。
	if err := store.DeleteSetting(context.Background(), SettingKeyMaxConcurrentTasks); err != nil {
		t.Fatalf("删并发键失败: %v", err)
	}
	if got := m.maxTasks(); got != DefaultMaxConcurrentTasks {
		t.Fatalf("清空后应回默认,实际 %d", got)
	}
	// 装配显式值优先于全局键(测试/演示确定性场景不受全局值干扰)。
	m2, err := New(Config{Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 1})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(m2.Close)
	if err := store.SetSetting(context.Background(), SettingKeyMaxConcurrentTasks, "9", "test"); err != nil {
		t.Fatalf("写并发键失败: %v", err)
	}
	if got := m2.maxTasks(); got != 1 {
		t.Fatalf("显式值应优先,实际 %d", got)
	}
}

// TestTaskConcurrencyRestartAdoption 重启收养按当前生效键值分配名额:
// 键 3 时三个"上一进程运行中"任务全部重拉;键 2 时两个保名额、一个按
// FIFO 溢出转排队(在跑不硬杀语义的收养侧对应:溢出不丢,排队等让位)。
func TestTaskConcurrencyRestartAdoption(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	dir := t.TempDir()

	adopt := func(key string, ids ...string) (started, queued int) {
		t.Helper()
		if err := store.SetSetting(ctx, SettingKeyMaxConcurrentTasks, key, "test"); err != nil {
			t.Fatalf("写并发键失败: %v", err)
		}
		for _, id := range ids {
			seedCrash(t, store, id, []string{StatusCreated, StatusStarted})
		}
		m, err := New(Config{Store: store, RootDir: dir, OnTaskRun: idleLoop})
		if err != nil {
			t.Fatalf("重启收养构建 Manager 失败: %v", err)
		}
		for _, id := range ids {
			switch m.Task(id).Status() {
			case StatusStarted:
				started++
			case StatusQueued:
				queued++
			default:
				t.Fatalf("收养后 %s 状态异常: %s", id, m.Task(id).Status())
			}
		}
		stopAllUntilTerminal(m)
		m.Close()
		return started, queued
	}

	// 键 3:三个运行中任务收养后全部保住名额。
	if s, q := adopt("3", "tc-r1", "tc-r2", "tc-r3"); s != 3 || q != 0 {
		t.Fatalf("键 3 收养应 3 起跑 0 排队,实际 %d/%d", s, q)
	}
	// 键 2:两个保名额,溢出一个转排队。
	if s, q := adopt("2", "tc-r4", "tc-r5", "tc-r6"); s != 2 || q != 1 {
		t.Fatalf("键 2 收养应 2 起跑 1 排队,实际 %d/%d", s, q)
	}
}

// idleLoop 测试用空转循环体(等 ctx 结束),任务保持运行中不自然退出。
func idleLoop(ctx context.Context, _ string, _ <-chan struct{}) {
	<-ctx.Done()
}
