package manager

// P3-07 断点续接·管理器侧:存档断点(Checkpoint)的完整闭环——记账 →
// 武装 → 循环退出让位(冻结收拢后返回 frozen)→ 名额释放;重启收养把
// checkpointed 与运行中同列自动重拉(恢复续跑的入口就是重启本身);
// 不重启的恢复走 Resume;与冬眠语义不冲突(冬眠=内存冻结原地解冻,
// 断点=进程级收场重启续跑)。

import (
	"context"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
)

// ckptLoopFunc 契约循环体(与 overmind.Loop 的断点纪律同款):事件驱动
// 等待,断点武装即退出。进入等待时向 started 报到(缓冲通道,多任务
// 共用不阻塞)。
func ckptLoopFunc(gate *ckpt.Gate, started chan string) TaskRunFunc {
	return func(ctx context.Context, taskID string, wake <-chan struct{}) {
		select {
		case started <- taskID:
		default:
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			if gate.Armed(taskID) {
				return
			}
		}
	}
}

// waitStarted 等指定任务的循环体进入等待(报到)。
func waitStarted(t *testing.T, started chan string, taskID string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	seen := map[string]bool{}
	for {
		select {
		case id := <-started:
			seen[id] = true
			if seen[taskID] {
				return
			}
		case <-deadline:
			t.Fatalf("任务 %s 的循环体未进入等待(已见 %v)", taskID, seen)
		}
	}
}

// TestManagerCheckpointFlow 断点闭环主契径:存档(冻结收拢、名额让位)
// → 模拟重启(新 Manager 收养重拉,断点信号解除)→ 循环体再次运行;
// 生命周期事件序列账本可证。
func TestManagerCheckpointFlow(t *testing.T) {
	store := openStore(t)
	root := t.TempDir()
	gate := ckpt.New()
	started := make(chan string, 16)

	m, err := New(Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		Checkpoint: gate, OnTaskRun: ckptLoopFunc(gate, started),
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	ta, err := m.CreateTask(context.Background(), "task-ck", "断点任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if ta.Status() != StatusStarted {
		t.Fatalf("任务应直接拉起,实际 %s", ta.Status())
	}
	waitStarted(t, started, "task-ck")

	// 存档断点:同步等待冻结收拢(循环退出、名额让位)。
	frozen, err := m.Checkpoint("task-ck", nil)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if !frozen {
		t.Fatalf("应完成冻结收拢(frozen=true)")
	}
	if st := ta.Status(); st != StatusCheckpointed {
		t.Fatalf("任务应处已存档态,实际 %s", st)
	}
	if gate.Armed("task-ck") {
		t.Fatalf("冻结完成后信号仍武装(不影响后续拉起)")
	}
	// 名额已让位:并发 1 下再建任务可直接拉起。
	tb, err := m.CreateTask(context.Background(), "task-ck2", "接位任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("创建接位任务失败: %v", err)
	}
	if tb.Status() != StatusStarted {
		t.Fatalf("断点让位后名额应可用,接位任务实际 %s", tb.Status())
	}
	waitStarted(t, started, "task-ck2")

	// 生命周期序列:created → started → checkpointed(账本可证)。
	acts := lifecycleActions(t, store, "task-ck")
	want := []string{"created", "started", "checkpointed"}
	if len(acts) < len(want) || strings.Join(acts[:3], ",") != strings.Join(want, ",") {
		t.Fatalf("断点任务生命周期序列不符: %v", acts)
	}

	// 停止接位任务腾位,模拟重启(进程亡,Manager 重建)。
	_ = m.Stop("task-ck2")
	m.Close()

	gate2 := ckpt.New()
	started2 := make(chan string, 16)
	m2, err := New(Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		Checkpoint: gate2, OnTaskRun: ckptLoopFunc(gate2, started2),
	})
	if err != nil {
		t.Fatalf("重启构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m2.Tasks() {
			_ = m2.Stop(tk.ID)
		}
		m2.Close()
	})
	waitStarted(t, started2, "task-ck")
	if st := m2.Task("task-ck").Status(); st != StatusStarted {
		t.Fatalf("重启收养应把断点任务重拉为运行中,实际 %s", st)
	}
	if gate2.Armed("task-ck") {
		t.Fatalf("重拉时断点信号应解除(出生即收场不会发生)")
	}
	// 账本:checkpointed 之后有 started(收养重拉,恢复续跑可证)。
	acts = lifecycleActions(t, store, "task-ck")
	if len(acts) < 4 || acts[3] != "started" {
		t.Fatalf("重启后应有收养重拉的 started,序列: %v", acts)
	}
}

// TestManagerCheckpointGuards 断点入口守卫:非运行态拒(已存档/冬眠中);
// 无信号门装配拒;不存在的任务拒;不重启的恢复走 Resume;冬眠唤醒与
// 断点互不相扰。
func TestManagerCheckpointGuards(t *testing.T) {
	store := openStore(t)
	gate := ckpt.New()
	hiberGate := hiber.New()
	started := make(chan string, 16)
	m, err := New(Config{
		Store: store, RootDir: t.TempDir(), MaxConcurrentTasks: 2,
		Checkpoint: gate, Hiber: hiberGate, OnTaskRun: ckptLoopFunc(gate, started),
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range m.Tasks() {
			_ = m.Stop(tk.ID)
		}
		m.Close()
	})
	if _, err := m.CreateTask(context.Background(), "task-g", "守卫任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	waitStarted(t, started, "task-g")

	// 已存档再存档:拒;不存在的任务:拒。
	if _, err := m.Checkpoint("task-g", nil); err != nil {
		t.Fatalf("首次存档应成功: %v", err)
	}
	if _, err := m.Checkpoint("task-g", nil); err == nil || !strings.Contains(err.Error(), "不可存档断点") {
		t.Fatalf("重复存档应拒,得到 %v", err)
	}
	if _, err := m.Checkpoint("task-none", nil); err == nil {
		t.Fatalf("未知任务应拒")
	}

	// 无信号门:拒(独立裸 Manager)。
	m2, err := New(Config{Store: openStore(t), RootDir: t.TempDir(), MaxConcurrentTasks: 1})
	if err != nil {
		t.Fatalf("构建裸 Manager 失败: %v", err)
	}
	t.Cleanup(m2.Close)
	if _, err := m2.Checkpoint("whatever", nil); err == nil || !strings.Contains(err.Error(), "断点信号门未装配") {
		t.Fatalf("无门应拒,得到 %v", err)
	}

	// 不重启的恢复:checkpointed 经 Resume 原地拉起。
	if err := m.Resume("task-g"); err != nil {
		t.Fatalf("checkpointed 应可 Resume: %v", err)
	}
	if st := m.Task("task-g").Status(); st != StatusStarted {
		t.Fatalf("恢复后应运行中,实际 %s", st)
	}
	waitStarted(t, started, "task-g")

	// 冬眠不冲突:运行中冬眠 → 存档拒(先唤醒)→ 唤醒原地继续。
	if err := m.Hibernate("task-g", nil); err != nil {
		t.Fatalf("冬眠应成功: %v", err)
	}
	if _, err := m.Checkpoint("task-g", nil); err == nil || !strings.Contains(err.Error(), "不可存档断点") {
		t.Fatalf("冬眠中存档应拒(先唤醒),得到 %v", err)
	}
	if err := m.Wake("task-g", nil); err != nil {
		t.Fatalf("唤醒应成功: %v", err)
	}
	if st := m.Task("task-g").Status(); st != StatusStarted {
		t.Fatalf("唤醒后应运行中,实际 %s", st)
	}
}
