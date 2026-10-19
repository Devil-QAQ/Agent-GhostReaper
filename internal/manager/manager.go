// Package manager 实现单进程多任务的 Manager 与任务生命周期:
// 全局共享资源、任务独立 context(熔断 Fuse 的机制基础)。
// 设计依据:《设计决策关键思想》第七章;实施于 P1-04,任务循环体
// 挂接(OnTaskRun)为 P1-05 主宰循环入驻点。
package manager

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"ghostreaper/internal/blackbox"
	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/runlog"
)

// 任务生命周期状态(与账本 task.lifecycle 动作一一对应)。
// completed 是完成终态(P1-10):全部目标经仲裁者机器判定达成、任务
// 主体(主宰循环)自然退出时落账;与 stopped 同为终态。
// settling(P2-04,第四章 §6 任务级总闸)是运行中的收尾子态:任务整体
// 超时到点,主宰停止派发、在跑单位优雅收尾(宽限 90 秒硬停兜底)、
// 仲裁者终局判定后经 FinishSettling 定终态(completed|stopped)。
// checkpointed(P3-07,第七章「故障与恢复」断点续接)是手动断点存档态:
// 操作者主动冻结(在跑单位到边界收拢、会话保留可续),进程可关;重启
// 收养与运行中同列自动重拉,单位重领步骤后原会话续跑。与 hibernated
// 的分工:冬眠=内存冻结(进程在、唤醒原地解冻),断点=进程级收场
// (进程可亡、重启续跑)。
const (
	StatusCreated      = "created"
	StatusQueued       = "queued"
	StatusStarted      = "started"
	StatusSettling     = "settling"
	StatusPaused       = "paused"
	StatusHibernated   = "hibernated"
	StatusCheckpointed = "checkpointed"
	StatusStopped      = "stopped"
	StatusCompleted    = "completed"
)

// TaskRunFunc 任务循环体,与 Config.OnTaskRun 同签名:任务 goroutine 的
// 运行件入口,返回即任务让位(自然返回=完成终态,由 Manager 记账)。
type TaskRunFunc func(ctx context.Context, taskID string, wake <-chan struct{})

// Config Manager 全局配置。
type Config struct {
	Store              *rings.Store // 年轮账本(进程级单例,第七章 §1)
	RootDir            string       // 任务工作目录根(如 "tasks");零值 "tasks"
	MaxConcurrentTasks int          // 任务级并发上限;超出的任务 FIFO 排队;零值 2
	// Hiber 任务冬眠门(P2-08,第七章「任务冬眠」):Hibernate/Wake 的
	// 执行层同步原语,与虫群执行器/主宰循环/牧者/审批门共持同一实例。
	// nil = 冬眠不可用(Hibernate 如实报错;单测用)。
	Hiber *hiber.Gate
	// Checkpoint 断点冻结信号(P3-07,第七章「故障与恢复」断点续接):
	// Checkpoint(存档断点)的执行层同步原语——武装后虫群单位与主宰
	// 循环(OnTaskRun 挂接的循环体须遵守"武装即退出"契约)在各自边界
	// 收场,进程可关;与虫群执行器/主宰循环共持同一实例。拉起(launch)
	// 时统一解除。nil = 断点不可用(Checkpoint 如实报错;单测用)。
	Checkpoint *ckpt.Gate
	// OnTaskRun 任务 goroutine 的全局循环体(P1-05 主宰循环挂接点)。设置后,
	// 任务拉起即把循环交给它:ctx 为任务专属 ctx(熔断/暂停 cancel 即
	// 收尾),wake 为事件唤醒通道(外部经 Task.Wake() 触发,容量 1 合并
	// 短时多次唤醒)。回调返回即任务 goroutine 结束、并发名额让位,整体
	// 处于 panic 隔离边界内。未设置时维持 P1-04 的空转等待骨架。
	// 登记了专属循环体的任务(RegisterLoop)优先走专属循环体,不走本回调。
	OnTaskRun TaskRunFunc
	// OnTerminal 任务终态回调(P2-08f,装配层注入牧者收尾总结):任务
	// 落 completed|stopped 终态记账后异步触发一次。nil = 无收尾动作。
	OnTerminal func(taskID, status string)
}

// Manager 单进程多任务的管理者:持全局共享资源,管理任务生命周期与
// 任务级并发(第七章)。每任务一套独立 ctx 与工作目录;任务间隔离靠
// task_id 贯穿 + 独立 ctx,互不感知。
type Manager struct {
	cfg Config

	mu    sync.Mutex
	tasks map[string]*Task
	// loops 任务专属循环体(第七章 §1"每任务全家桶":任务可自带私有
	// 运行件,LLM 配置链每任务可覆盖全局的最小形态)。拉起时优先于
	// 全局 OnTaskRun——两条入口同属主装配路径,非旁路(见 RegisterLoop)。
	loops   map[string]TaskRunFunc
	queue   []*Task // FIFO 等待队列(按入队顺序)
	running int
	wg      sync.WaitGroup
}

// Task 一个任务的生命周期句柄(第七章 §1"每任务全家桶"的骨架)。
// ctx 与 cancel 是熔断(Fuse)的机制基础:暂停/停止/超时 cancel 该
// 任务 ctx,其主宰与虫群顺次收尾退出,其他任务无感知。
type Task struct {
	m *Manager

	ID    string
	Title string

	mu     sync.Mutex
	status string
	ctx    context.Context
	cancel context.CancelFunc

	// wake 事件唤醒通道(容量 1,合并短时多次唤醒;P1-05 起由
	// OnTaskRun 挂入的主宰循环消费)。
	wake chan struct{}

	// runDone 本次拉起的运行件生命周期锚点(P3-07):launch 建通道、
	// runTask 退出时关闭——Checkpoint(存档断点)等待冻结收拢即等它。
	// t.mu 保护;再次拉起换新通道。
	runDone chan struct{}

	// testHook 仅供测试注入任务 goroutine 启动回调(panic 隔离验证)。
	testHook func(ctx context.Context, t *Task)
}

// New 构建 Manager 并执行重启收养固定序列(第七章「故障与恢复语义」
// 后半,实施于 P2-02c、P2-06f 升级为全量自动恢复):
//
//	① 从账本重建全部任务句柄(内存注册表与账本同源);
//	② 幽灵清理——崩溃残留 running 的 Step 重置回可领取(dispatched),
//	  此时进程内无任何活单位,全部 running 步骤都是幽灵,不让界面永远
//	  转圈;
//	③ 休眠类保持——已暂停任务重启后仍已暂停(重启不自动拉起;P2-08
//	  冬眠态落地后落同一保持槽),终态任务原样装载;
//	④ 任务超时协调器重建——收尾线锚在 created 事件(绝对墙钟),循环
//	  重拉时 loadDeadline 重建,已过期的任务立即走收尾时序;
//	⑤ 运行中的任务重拉主宰/虫群循环(OnTaskRun 全局装配;专属循环体
//	  随进程消失,按全局装配续跑);
//	⑥ 排队任务重新 FIFO 准入——按原状态落定次序(原运行任务保名额、
//	  原排队任务按入队先后),无并发名额死锁。
//
// 未配循环体(OnTaskRun 为空,如未配 LLM)时维持 P2-02c 兜底:活跃
// 任务(运行中/收尾中/排队中/已创建)收养为已暂停并记账——无运行件
// 可拉起,不制造"账本运行中、内存空转"的假象;可经恢复再拉起。
func New(cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("manager: 缺少年轮账本句柄")
	}
	if cfg.RootDir == "" {
		cfg.RootDir = "tasks"
	}
	if cfg.MaxConcurrentTasks <= 0 {
		cfg.MaxConcurrentTasks = 2
	}
	m := &Manager{cfg: cfg, tasks: map[string]*Task{}, loops: map[string]TaskRunFunc{}}
	rows, err := cfg.Store.Tasks(context.Background())
	if err != nil {
		return nil, fmt.Errorf("manager: 装载既有任务: %w", err)
	}
	// ① 重建任务句柄(UpdatedRev 是任务最后一条生命周期事件的版本号,
	// 作 FIFO 重准入的原状态落定次序锚点)。
	anchors := map[string]int64{}
	for _, r := range rows {
		m.tasks[r.ID] = &Task{m: m, ID: r.ID, Title: r.Title, status: r.Status, wake: make(chan struct{}, 1)}
		anchors[r.ID] = r.UpdatedRev
	}
	// ②③ 幽灵清理 + 分类收养(逐任务落定;清理先于任何 goroutine 存在,
	// 全部 running 步骤都是无主幽灵,含已暂停/终态任务的残留)。
	type candidate struct {
		t    *Task
		from string
	}
	var runnable, waiting []candidate // 运行类(重拉)/排队类(重准入)
	for _, r := range rows {
		t := m.tasks[r.ID]
		ghosts, err := m.resetGhostSteps(r.ID)
		if err != nil {
			return nil, fmt.Errorf("manager: 幽灵清理 %s: %w", r.ID, err)
		}
		action := "keep"
		switch r.Status {
		case StatusStarted, StatusSettling, StatusCheckpointed:
			// checkpointed(P3-07 断点续接)与运行中同列自动重拉:断点
			// 存档的恢复入口就是重启本身——单位重领可领取步骤后原会话续
			//(checkpointed 标记),任务原地续跑。launch 已解除断点信号,
			// 新循环体出生即运行。
			if cfg.OnTaskRun != nil {
				runnable = append(runnable, candidate{t, r.Status})
				action = "relaunch"
			} else {
				if err := m.adoptPaused(t, r.Status, "进程重启收养:无循环体可拉起(未配置模型),收养为已暂停(可恢复)"); err != nil {
					return nil, err
				}
				action = "adopt_paused"
			}
		case StatusQueued, StatusCreated:
			if cfg.OnTaskRun != nil {
				waiting = append(waiting, candidate{t, r.Status})
				action = "requeue"
			} else {
				if err := m.adoptPaused(t, r.Status, "进程重启收养:无循环体可拉起(未配置模型),收养为已暂停(可恢复)"); err != nil {
					return nil, err
				}
				action = "adopt_paused"
			}
		case StatusHibernated:
			// 冬眠保持冬眠(P2-08,第七章「重启收养固定序列」):重启不
			// 自动拉起,唤醒(Wake)是唯一恢复入口;幽灵步骤照常清理,
			// 唤醒后重领原会话续跑(pendingSession 机制)。
		}
		if len(ghosts) > 0 || action != "keep" {
			detail := map[string]any{"action": action, "from": r.Status}
			if len(ghosts) > 0 {
				detail["ghost_steps"] = ghosts
				detail["ghost_note"] = "崩溃残留 running 的步骤已重置回可领取"
			}
			runlog.Infof("进程重启收养: 任务 %s(%s → %s,幽灵步骤 %d 个)", r.ID, r.Status, action, len(ghosts))
			if err := blackbox.Record(context.Background(), m.cfg.Store, r.ID, "manager",
				rings.BlackboxRecord{Type: rings.BlackboxRestartAdoption, Detail: detail}); err != nil {
				return nil, fmt.Errorf("manager: 收养留痕 %s: %w", r.ID, err)
			}
		}
	}
	if cfg.OnTaskRun == nil {
		return m, nil
	}
	// ⑤⑥ 名额分配:原运行任务优先保名额(它们崩溃前占着),余下按原
	// 状态落定次序 FIFO 重准入;溢出任务进等待队列,名额由 retire 释放
	// 时自动拉起(与运行期排队同一动态,无死锁)。
	sort.SliceStable(runnable, func(i, j int) bool { return anchors[runnable[i].t.ID] < anchors[runnable[j].t.ID] })
	sort.SliceStable(waiting, func(i, j int) bool { return anchors[waiting[i].t.ID] < anchors[waiting[j].t.ID] })
	ordered := append(append([]candidate(nil), runnable...), waiting...)
	m.mu.Lock()
	var toStart []candidate
	for _, c := range ordered {
		if m.running < m.cfg.MaxConcurrentTasks {
			m.running++
			toStart = append(toStart, c)
		} else {
			c.t.mu.Lock()
			c.t.status = StatusQueued
			c.t.mu.Unlock()
			m.queue = append(m.queue, c.t)
		}
	}
	queued := append([]*Task(nil), m.queue...)
	m.mu.Unlock()
	for _, c := range toStart {
		if err := m.record(c.t.ID, StatusStarted, map[string]any{
			"adopted": true, "from": c.from,
			"reason": "进程重启收养:重新拉起任务循环(超时协调器随循环重建,已过期立即收尾)",
		}); err != nil {
			// 局部记账失败不挡其余任务:名额回滚,该任务退回已暂停。
			m.mu.Lock()
			m.running--
			m.mu.Unlock()
			c.t.mu.Lock()
			c.t.status = StatusPaused
			c.t.mu.Unlock()
			_ = m.record(c.t.ID, StatusPaused, map[string]any{
				"orphan": true, "from": c.from,
				"reason": "进程重启收养:拉起记账失败,收养为已暂停(可恢复)",
			})
			continue
		}
		m.launch(c.t)
		// 收养唤醒:拉起后立即一轮 cycle——领取幽灵清理后的步骤续跑。
		c.t.Wake()
	}
	for _, t := range queued {
		if err := m.record(t.ID, StatusQueued, map[string]any{
			"adopted": true,
			"reason":  "进程重启收养:重新排队等待并发名额",
		}); err != nil {
			m.mu.Lock()
			m.dequeueLocked(t)
			m.mu.Unlock()
			t.mu.Lock()
			t.status = StatusPaused
			t.mu.Unlock()
			_ = m.record(t.ID, StatusPaused, map[string]any{
				"orphan": true,
				"reason": "进程重启收养:排队记账失败,收养为已暂停(可恢复)",
			})
			continue
		}
		// 排队任务的收养唤醒落进容量 1 的通道缓冲,retire 拉起后消费,
		// 不丢(P1-04 起的既有语义)。
		t.Wake()
	}
	return m, nil
}

// resetGhostSteps 幽灵清理:把任务下全部 running 步骤重置回 dispatched
// (可领取)。只在启动序列调用——进程刚起,不存在活单位,running 必为
// 崩溃残留(没有活单位拥有它);正常路径的状态迁移由虫群单位入账。
// 返回被清理的步骤 ID 清单(空 = 无幽灵,零留痕)。
func (m *Manager) resetGhostSteps(taskID string) ([]string, error) {
	steps, err := m.cfg.Store.NodesByType(context.Background(), taskID, rings.NodeStep, "running")
	if err != nil {
		return nil, fmt.Errorf("manager: 查幽灵步骤: %w", err)
	}
	if len(steps) == 0 {
		return nil, nil
	}
	events := make([]rings.Event, 0, len(steps))
	ids := make([]string, 0, len(steps))
	for _, s := range steps {
		ids = append(ids, s.ID)
		events = append(events, rings.Event{
			Kind: rings.EventNodeStateChanged, Actor: "manager",
			Payload: rings.NodeStateChanged{NodeID: s.ID, Status: "dispatched"},
		})
	}
	if _, err := m.cfg.Store.Append(context.Background(), taskID, events...); err != nil {
		return nil, fmt.Errorf("manager: 重置幽灵步骤: %w", err)
	}
	return ids, nil
}

// adoptPaused P2-02c 兜底收养:无循环体时把活跃任务收养为已暂停并记账
// (orphan 细节携带原状态;已暂停任务可经恢复再拉起)。
func (m *Manager) adoptPaused(t *Task, from, reason string) error {
	if err := m.record(t.ID, StatusPaused, map[string]any{"orphan": true, "from": from, "reason": reason}); err != nil {
		return err
	}
	t.mu.Lock()
	t.status = StatusPaused
	t.mu.Unlock()
	return nil
}

// Task 按 ID 取任务。
func (m *Manager) Task(id string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[id]
}

// RootDir 任务工作目录根(装配层据此定位 blob/转录等落盘位置)。
func (m *Manager) RootDir() string { return m.cfg.RootDir }

// Tasks 列出全部任务(快照)。
func (m *Manager) Tasks() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	return out
}

// RegisterLoop 为任务登记专属循环体(第七章 §1"每任务全家桶"的装配
// 入口,实施于 P2-02c):任务可自带私有运行件(如面板临时演示入口的
// 脚本模型全家桶),拉起时优先运行专属循环体,未登记的任务走全局
// OnTaskRun。专属循环体与全局装配是同一条主装配路径——同一任务
// goroutine 组、同一熔断/暂停/完成语义、生命周期同由本 Manager 权威
// 记账,不是旁路装配。须在 CreateTask 之前登记(任务已存在即拒);
// 进程内黏着(暂停后恢复再拉起仍用专属循环体);进程重启后登记随
// 进程消失,该任务按重启收养语义落已暂停,恢复后走全局装配。
func (m *Manager) RegisterLoop(taskID string, run TaskRunFunc) error {
	if run == nil {
		return fmt.Errorf("manager: 任务 %s 的专属循环体不能为空", taskID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tasks[taskID]; exists {
		return fmt.Errorf("manager: 任务 %s 已存在,专属循环体须在建任务前登记", taskID)
	}
	m.loops[taskID] = run
	return nil
}

// loopFor 取任务专属循环体(未登记返回 nil;须持锁读)。
func (m *Manager) loopFor(taskID string) TaskRunFunc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loops[taskID]
}

// SetOnTaskRun 运行时挂接全局任务循环体(P3-05b ⑤ 模型激活免重启):
// 面板激活主用模型、装配层拉起执行器全家桶后注入,此后的任务拉起即走
// 循环体。已在跑的任务不追溯(挂接前拉起的按原语义空转/收养为已暂停,
// 经暂停恢复入口重开);重复挂接取最后一次。nil = 摘除循环体(单测)。
func (m *Manager) SetOnTaskRun(run TaskRunFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.OnTaskRun = run
}

// onTaskRun 读全局循环体(须持锁;runTask 拉起时取一次快照)。
func (m *Manager) onTaskRun() TaskRunFunc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.OnTaskRun
}

// Wake 唤醒任务的主宰循环(事件驱动:种子完成、边界变更、面板操作等
// 事件方调用;P1-05 起由主宰循环消费)。
func (t *Task) Wake() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Status 任务当前状态(线程安全)。
func (t *Task) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// Context 任务专属 ctx:虫群/主宰执行都应挂在其下,熔断 cancel 即全任务
// 收尾。未启动(queued/paused/stopped)时返回 Background。
func (t *Task) Context() context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

// CreateTask 创建任务:记账(created + 初始 RoE)→ 有空位即拉起(started),
// 否则排队(queued)。单批事务保证任务与初始边界同时落账。
// detail 进 created 事件负载:任务级总闸字段 timeout_seconds(P2-04
// 整体超时,秒,0/缺省=不限)在此透出——收尾线锚点取 created 事件
// 发生时刻(rings.TaskGate,事件流不可变)。
func (m *Manager) CreateTask(ctx context.Context, taskID, title string, roe rings.RoeUpdated, detail map[string]any) (*Task, error) {
	events := []rings.Event{
		{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: StatusCreated, Title: title, Detail: detail}},
	}
	// P2-08f:提取确认的任务级拦截规则(IntentRules)也是 RoE 的一部分,
	// 单独携带时同样落 roe.updated(建任务即生效)。
	if len(roe.AllowedTools) > 0 || len(roe.DeniedTools) > 0 || len(roe.IntentRules) > 0 || roe.Note != "" {
		events = append(events, rings.Event{Kind: rings.EventRoeUpdated, Actor: "manager", Payload: roe})
	}
	if _, err := m.cfg.Store.Append(ctx, taskID, events...); err != nil {
		return nil, fmt.Errorf("manager: 创建任务记账: %w", err)
	}

	m.mu.Lock()
	if _, exists := m.tasks[taskID]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("manager: 任务 %s 已存在", taskID)
	}
	t := &Task{m: m, ID: taskID, Title: title, status: StatusCreated, wake: make(chan struct{}, 1)}
	m.tasks[taskID] = t
	// 空位判定在锁内,与 retire 的释放路径互斥。
	if m.running < m.cfg.MaxConcurrentTasks {
		m.running++ // start 无失败路径会回滚名额(见 start)
		m.mu.Unlock()
		if err := m.record(taskID, StatusStarted, nil); err != nil {
			m.mu.Lock()
			m.running--
			m.mu.Unlock()
			return t, fmt.Errorf("manager: 记账 started: %w", err)
		}
		m.launch(t)
		return t, nil
	}
	t.status = StatusQueued
	m.queue = append(m.queue, t)
	m.mu.Unlock()
	if err := m.record(taskID, StatusQueued, nil); err != nil {
		m.mu.Lock()
		m.dequeueLocked(t)
		t.status = StatusCreated
		m.mu.Unlock()
		return t, err
	}
	return t, nil
}

// launch 为任务建独立 ctx 并起 goroutine 组(panic 隔离边界,第七章
// 实现注意)。名额已由调用方占好。拉起即解除断点信号(P3-07):新
// 循环体出生不带收场令——重启收养/暂停恢复/排队腾位同享本入口,
// "checkpointed 任务重拉即续跑"由此统一成立。
func (m *Manager) launch(t *Task) {
	if m.cfg.Checkpoint != nil {
		m.cfg.Checkpoint.Clear(t.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rd := make(chan struct{})
	t.mu.Lock()
	t.ctx, t.cancel = ctx, cancel
	t.runDone = rd
	t.status = StatusStarted
	t.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runTask(ctx, t, rd)
	}()
}

// runTask 任务 goroutine 组的入口:正常等 ctx 结束或唤醒信号
// (P1-05 主宰循环在此挂接);panic 被 recover 挡在任务边界内。
// 名额不变式:每次 launch 恰好一次 running++,runTask 每次退出
// (正常/panic/熔断 cancel)恰好一次 retire——无须 Once。
func (m *Manager) runTask(ctx context.Context, t *Task, runDone chan struct{}) {
	// 运行件生命周期锚点(P3-07):goroutine 退出即关闭——Checkpoint 的
	// 冻结等待、宽限硬停的退出判定共用。先关锚点再让位(retire 的名额
	// 腾挪是后续动作,不在"冻结完成"语义内)。
	defer m.retire()
	defer close(runDone)
	defer func() {
		if r := recover(); r != nil {
			// 崩溃留痕:任务让位为 paused(带 panic 细节),不带走进程。
			// 运行日志同步落 ERROR(P3-13b ③:panic 隔离兜住也留痕,含堆栈)。
			runlog.Errorf("任务 %s 循环崩溃(panic 隔离兜住): %v\n%s", t.ID, r, debug.Stack())
			t.mu.Lock()
			if t.status == StatusStarted {
				t.status = StatusPaused
			}
			t.mu.Unlock()
			_ = m.record(t.ID, StatusPaused, map[string]any{"panic": fmt.Sprint(r)})
		}
	}()
	t.mu.Lock()
	hook := t.testHook
	t.mu.Unlock()
	if hook != nil {
		hook(ctx, t)
	}
	// 任务专属循环体优先(每任务全家桶);未登记走全局 OnTaskRun。
	// 两者同一完成/让位语义。
	run := m.loopFor(t.ID)
	if run == nil {
		run = m.onTaskRun()
	}
	if run != nil {
		// 任务循环体挂接点(P1-05 起):循环体交给回调,返回即任务让位。
		run(ctx, t.ID, t.wake)
		// 循环体自然返回(ctx 未结束、状态仍 started)= 任务主体宣告
		// 完成(P1-10:全部目标经仲裁者机器判定达成后主宰循环退出)。
		// 完成的因果依据在同账本的 arbiter.verdict 裁决链里,此处只落
		// 终态事件;与 StopBy/Pause 的竞态由次序兜住——两者均先置状态
		// 再 cancel,届时 ctx.Err() 非 nil 或状态已非 started,不误记。
		// 收尾中(settling)返回 = 收尾流程异常退出(正常路径应经
		// FinishSettling 定终态后再返回)——如实落 stopped 终态,绝不
		// 让任务悬在收尾中无运行件(与重启收养同一条反悬空纪律)。
		t.mu.Lock()
		st := t.status
		if st == StatusStarted && ctx.Err() == nil {
			t.status = StatusCompleted
		}
		t.mu.Unlock()
		switch st {
		case StatusStarted:
			if ctx.Err() != nil {
				return // 熔断/暂停已由操控方记账,此处不重复
			}
			_ = m.record(t.ID, StatusCompleted,
				map[string]any{"reason": "全部目标达成(仲裁者判定或面板人工标记)"})
		case StatusSettling:
			if ctx.Err() != nil {
				return // 收尾中被熔断:stopped 已由 StopBy 记账
			}
			t.mu.Lock()
			t.status = StatusStopped
			t.mu.Unlock()
			_ = m.record(t.ID, StatusStopped,
				map[string]any{"reason": "收尾流程异常退出,如实落停止终态"})
		case StatusCheckpointed:
			// 断点收场(P3-07):checkpointed 已由 Checkpoint 记账,不发
			// 完成终态;收掉任务 ctx 停掉随行 goroutine(收尾线看护/心跳
			// 巡检),句柄清空(恢复走 Resume 同款拉起路径;重启收养走
			// relaunch)。名额由 retire 释放。
			t.mu.Lock()
			c := t.cancel
			t.ctx, t.cancel = nil, nil
			t.mu.Unlock()
			if c != nil {
				c()
			}
		}
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.wake:
			// 未挂循环体时唤醒信号被丢弃(P1-04 空转骨架);断点令例外:
			// 无运行件可收,让位即冻结完成(账本已记 checkpointed)。
		}
		if m.cfg.Checkpoint != nil && m.cfg.Checkpoint.Armed(t.ID) {
			return
		}
	}
}

// retire 释放一个并发名额并按 FIFO 拉起队首任务。
func (m *Manager) retire() {
	m.mu.Lock()
	m.running--
	var next *Task
	if len(m.queue) > 0 && m.running < m.cfg.MaxConcurrentTasks {
		next = m.queue[0]
		m.queue = m.queue[1:]
		m.running++
	}
	m.mu.Unlock()
	if next != nil {
		if err := m.record(next.ID, StatusStarted, nil); err != nil {
			m.mu.Lock()
			m.running--
			m.queue = append([]*Task{next}, m.queue...)
			m.mu.Unlock()
			next.mu.Lock()
			next.status = StatusQueued
			next.mu.Unlock()
			return
		}
		m.launch(next)
	}
}

// Stop 熔断(停止):先追加控制事件、再 cancel 任务 ctx(P1-09 面板
// 熔断按钮的机制次序在此定型);排队中任务直接出队。任务不可重用。
func (m *Manager) Stop(taskID string) error {
	return m.StopBy(taskID, nil)
}

// StopBy 熔断并携带审计细节(如 operator/reason):账本里的 stopped
// 事件因此能回答"谁在何时熔断了哪个任务"(P1-09 面板熔断入口)。
// 收尾中(settling)任务照常可熔断——操作者硬停优先于预算收尾的
// 自然节奏;随后主宰循环随 ctx 退出,不再定终态(终态就是本 stopped)。
// 冬眠中(hibernated,P2-08)熔断:先记账,再 cancel 任务 ctx 并开冬眠
// 门——挂起的单位与循环从冻结中醒来即见 ctx 结束,按中止收场,不留
// 悬挂 goroutine。
func (m *Manager) StopBy(taskID string, detail map[string]any) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	m.mu.Lock()
	st := t.Status()
	switch st {
	case StatusStopped, StatusCompleted:
		m.mu.Unlock()
		return nil // 幂等;completed 与 stopped 同为终态,不可再熔断
	case StatusStarted, StatusSettling:
		// 名额由 goroutine 退出时的 retire 释放。
		m.mu.Unlock()
		if err := m.record(taskID, StatusStopped, detail); err != nil {
			return err
		}
		t.mu.Lock()
		t.status = StatusStopped
		c := t.cancel
		t.mu.Unlock()
		if c != nil {
			c()
		}
		return nil
	case StatusHibernated:
		m.mu.Unlock()
		if err := m.record(taskID, StatusStopped, detail); err != nil {
			return err
		}
		t.mu.Lock()
		t.status = StatusStopped
		c := t.cancel
		t.mu.Unlock()
		if c != nil {
			c()
		}
		if m.cfg.Hiber != nil {
			m.cfg.Hiber.Exit(taskID) // 放行挂起者:醒来即见 ctx 结束,按中止收场
		}
		return nil
	default: // created/queued/paused:从未在跑或已让位
		m.dequeueLocked(t)
		m.mu.Unlock()
		t.mu.Lock()
		t.status = StatusStopped
		t.mu.Unlock()
		return m.record(taskID, StatusStopped, detail)
	}
}

// ---- 任务级总闸的收尾操控面(P2-04,第四章 §6;由主宰循环经
// overmind.SettleCtl 回调驱动,装配层注入 Manager 实现) ----

// MarkSettling 进入收尾:started → settling 记账 + 状态翻转。任务整体
// 超时到点,主宰停止派发、在跑单位优雅收尾——收尾中是运行子态(不是
// 终态),面板任务列表可见"收尾中"。非 started(已熔断/已让位)时
// 如实报错,主宰循环据此放弃收尾流程(终态已由他方定)。
func (m *Manager) MarkSettling(taskID string, detail map[string]any) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	if st := t.Status(); st != StatusStarted {
		return fmt.Errorf("manager: 任务 %s 状态 %s 不可进入收尾", taskID, st)
	}
	if err := m.record(taskID, StatusSettling, detail); err != nil {
		return err
	}
	t.mu.Lock()
	t.status = StatusSettling
	t.mu.Unlock()
	return nil
}

// FinishSettling 收尾定终态(第四章 §6 时序终点):终局判定过仲裁者后
// 落定——目标全达成 → completed;未全达成 → stopped(整体超时有序
// 收尾,不是失败,R16:不得因超时静默标记失败,达成与否由机器判定)。
// 随后 cancel 任务 ctx 让任务 goroutine 让位;名额由 retire 释放。
// 非 settling 状态调用如实报错(竞态兜底:操作者已先熔断)。
func (m *Manager) FinishSettling(taskID string, achieved bool, detail map[string]any) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	if st := t.Status(); st != StatusSettling {
		return fmt.Errorf("manager: 任务 %s 状态 %s 不是收尾中,不定终态", taskID, st)
	}
	terminal := StatusStopped
	if achieved {
		terminal = StatusCompleted
	}
	if err := m.record(taskID, terminal, detail); err != nil {
		return err
	}
	t.mu.Lock()
	t.status = terminal
	c := t.cancel
	t.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

// Pause 暂停:记账 → cancel ctx 让位并发名额(名额由 goroutine 退出
// 时的 retire 释放);Resume 可再拉起。冬眠中不可暂停(先唤醒——挂起
// 的单位与循环停在冬眠门上,暂停语义叠加只会留悬挂 goroutine)。
func (m *Manager) Pause(taskID string) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	if st := t.Status(); st != StatusStarted {
		return fmt.Errorf("manager: 任务 %s 状态 %s 不可暂停", taskID, st)
	}
	if err := m.record(taskID, StatusPaused, nil); err != nil {
		return err
	}
	t.mu.Lock()
	t.status = StatusPaused
	c := t.cancel
	t.ctx, t.cancel = nil, nil
	t.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

// Resume 恢复暂停的任务:有空位即拉起,否则重新排队。checkpointed
// (P3-07 断点存档)同款可恢复——不重启进程也想续跑时,这是断点任务
// 的原地恢复入口(拉起即解除断点信号,单位重领后原会话续)。
func (m *Manager) Resume(taskID string) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	m.mu.Lock()
	if st := t.Status(); st != StatusPaused && st != StatusCheckpointed {
		m.mu.Unlock()
		return fmt.Errorf("manager: 任务 %s 状态 %s 不可恢复", taskID, st)
	}
	if m.running < m.cfg.MaxConcurrentTasks {
		m.running++
		m.mu.Unlock()
		if err := m.record(taskID, StatusStarted, nil); err != nil {
			m.mu.Lock()
			m.running--
			m.mu.Unlock()
			return err
		}
		m.launch(t)
		// 执行侧拉起补一脚唤醒(P3-05c ①):恢复即跑一轮 cycle,领取
		// dispatched 步骤(受阻重开的步骤由此被领取执行)。
		t.Wake()
		return nil
	}
	t.mu.Lock()
	t.status = StatusQueued
	t.mu.Unlock()
	m.queue = append(m.queue, t)
	m.mu.Unlock()
	if err := m.record(taskID, StatusQueued, nil); err != nil {
		m.mu.Lock()
		m.dequeueLocked(t)
		t.mu.Lock()
		t.status = StatusPaused
		t.mu.Unlock()
		m.mu.Unlock()
		return err
	}
	return nil
}

// ---- 任务冬眠/唤醒(P2-08,第七章「任务冬眠(Hibernation)语义」) ----

// Hibernate 冬眠:记账在先(hibernated 生命周期事件,审计权威)、关闸
// 在后(执行层同步原语)。生效后主宰循环在轮次边界挂起、虫群单位在
// 当前工具调用完成后挂起(不退出、会话原地冻结)、牧者会话与审批裁决
// 冻结、全部计时器停走(冬眠时长由账本配对扫描抵扣任务总闸墙钟)。
// 任务 ctx 不取消——与 Pause(让位退出)的本质区别;并发名额保持
// 占用(单位与循环仍驻留内存,原地冻结)。settling 亦可冬眠(收尾
// 预算同步冻结),唤醒后恢复收尾流程。
func (m *Manager) Hibernate(taskID string, detail map[string]any) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	if m.cfg.Hiber == nil {
		return fmt.Errorf("manager: 冬眠门未装配,冬眠不可用")
	}
	switch st := t.Status(); st {
	case StatusHibernated:
		return nil // 幂等
	case StatusStarted, StatusSettling:
		if detail == nil {
			detail = map[string]any{}
		}
		if _, ok := detail["reason"]; !ok {
			detail["reason"] = "任务冬眠:主宰与虫群挂起、牧者会话冻结、计时器停走(唤醒可恢复)"
		}
		if err := m.record(taskID, StatusHibernated, detail); err != nil {
			return err
		}
		m.cfg.Hiber.Enter(taskID, st)
		t.mu.Lock()
		t.status = StatusHibernated
		t.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("manager: 任务 %s 状态 %s 不可冬眠", taskID, st)
	}
}

// Wake 唤醒(唯一恢复入口):记账在先(恢复冬眠前状态 started|settling,
// detail 带 woken 标记——账本配对扫描 hibernated→唤醒即冬眠时长)、
// 开闸在后。进程内挂起的任务原地解冻(单位与循环从挂起点继续,R14
// 复检在单位侧挂起路径执行);重启后唤醒的走 Resume 同款拉起路径
// (空窗期无活循环,幽灵步骤已被启动序列重置,重领后原会话续)。
func (m *Manager) Wake(taskID string, detail map[string]any) error {
	t := m.Task(taskID)
	if t == nil {
		return fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	if st := t.Status(); st != StatusHibernated {
		return fmt.Errorf("manager: 任务 %s 状态 %s 不可唤醒", taskID, st)
	}
	pre := m.cfg.Hiber.Pre(taskID)
	if pre != StatusStarted && pre != StatusSettling {
		pre = StatusStarted // 门上无记录(重启后的唤醒):按运行中拉起
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["woken"] = true
	detail["reason"] = "任务唤醒:主宰与虫群原地继续、牧者会话解冻、计时器恢复(挂起期间冬眠时长已抵扣)"
	t.mu.Lock()
	live := t.ctx != nil
	t.mu.Unlock()
	if live {
		if err := m.record(taskID, pre, detail); err != nil {
			return err
		}
		m.cfg.Hiber.Exit(taskID)
		t.mu.Lock()
		t.status = pre
		t.mu.Unlock()
		// 解冻的主宰循环可能停在 select:补一脚唤醒(容量 1 合并)。
		t.Wake()
		return nil
	}
	// 重启后唤醒:同 Resume 的拉起路径(名额有空位即拉起,否则排队)。
	m.mu.Lock()
	if m.running < m.cfg.MaxConcurrentTasks {
		m.running++
		m.mu.Unlock()
		if err := m.record(taskID, StatusStarted, detail); err != nil {
			m.mu.Lock()
			m.running--
			m.mu.Unlock()
			return err
		}
		m.launch(t)
		// 执行侧拉起补一脚唤醒(P3-05c ①):拉起即跑一轮 cycle——领取
		// dispatched 步骤(含受阻重开回可领取的),不依赖通道里恰好有
		// 缓冲唤醒(容量 1,可能已被此前的唤醒消费掉)。
		t.Wake()
		return nil
	}
	t.mu.Lock()
	t.status = StatusQueued
	t.mu.Unlock()
	m.queue = append(m.queue, t)
	m.mu.Unlock()
	if err := m.record(taskID, StatusQueued, detail); err != nil {
		m.mu.Lock()
		m.dequeueLocked(t)
		t.mu.Lock()
		t.status = StatusHibernated
		t.mu.Unlock()
		m.mu.Unlock()
		return err
	}
	return nil
}

// dequeueLocked 从等待队列移除任务(须持 m.mu)。
func (m *Manager) dequeueLocked(t *Task) {
	for i, q := range m.queue {
		if q == t {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
}

// ---- 手动断点存档(P3-07,第七章「故障与恢复」断点续接)----

// checkpointFreezeCap 存档断点等待冻结收拢的上限:在跑单位到工具调用
// 边界收场(bash 长命令限时可达 600 秒),上限给足。超时不是失败——
// 账本已记 checkpointed,此刻关进程按崩溃恢复语义续跑(悬空会话由
// pendingSession 收养),冻结未收拢的事实如实返回。
const checkpointFreezeCap = 15 * time.Minute

// Checkpoint 存档断点:记账在先(checkpointed 生命周期事件,审计权威)、
// 武装信号在后(虫群单位与主宰循环在各自边界收场),再补一脚唤醒让
// 静默中的循环立即见到信号。等待冻结完成(在跑单位落地、循环退出让
// 位、名额释放)后返回——此刻账本状态一致,进程可关;重启收养把
// checkpointed 与运行中同列自动重拉,任务原地续跑(账本/transcript
// 可证)。与冬眠的分工:冬眠=内存冻结(进程在,原地解冻);断点=
// 进程级收场(单位落地,进程可亡,重启续跑)。恢复不重启进程也行:
// Resume 对 checkpointed 同款可恢复。
func (m *Manager) Checkpoint(taskID string, detail map[string]any) (frozen bool, err error) {
	if m.cfg.Checkpoint == nil {
		return false, fmt.Errorf("manager: 断点信号门未装配,存档断点不可用")
	}
	t := m.Task(taskID)
	if t == nil {
		return false, fmt.Errorf("manager: 任务 %s 不存在", taskID)
	}
	switch st := t.Status(); st {
	case StatusStarted, StatusSettling:
	default:
		return false, fmt.Errorf("manager: 任务 %s 状态 %s 不可存档断点(运行中/收尾中才可;冬眠中先唤醒)", taskID, st)
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["reason"] = "手动断点存档:在跑单位到边界收拢,进程可关;重启后任务自动原地续跑(原会话续)"
	if err := m.record(taskID, StatusCheckpointed, detail); err != nil {
		return false, err
	}
	t.mu.Lock()
	t.status = StatusCheckpointed
	runDone := t.runDone
	t.mu.Unlock()
	m.cfg.Checkpoint.Arm(taskID)
	t.Wake()
	if runDone == nil {
		m.cfg.Checkpoint.Clear(taskID) // 无运行件可等:信号即已消费
		return true, nil
	}
	select {
	case <-runDone:
		m.cfg.Checkpoint.Clear(taskID) // 冻结收拢完成:信号消费完毕
		return true, nil
	case <-time.After(checkpointFreezeCap):
		return false, nil // 如实:尚有单位未到边界;此刻关进程仍安全(按崩溃恢复语义续跑)
	}
}

// record 记账任务生命周期动作。熔断次序(先事件后 cancel)依赖此
// 同步记账;账本排队语义保证入队后必然提交。P3-13b ③:任务生命周期
// 在此单点落运行日志 INFO(创建/受阻恢复外的状态迁移/冬眠/熔断/完成)。
func (m *Manager) record(taskID, action string, detail map[string]any) error {
	_, err := m.cfg.Store.Append(context.Background(), taskID, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: action, Detail: detail},
	})
	if err != nil {
		runlog.Errorf("账本写入失败: 任务生命周期 %s/%s: %v", taskID, action, err)
		return fmt.Errorf("manager: 记账 %s/%s: %w", taskID, action, err)
	}
	msg := fmt.Sprintf("任务生命周期: %s %s", action, taskID)
	if t := m.Task(taskID); t != nil && t.Title != "" {
		msg += " " + t.Title
	}
	if reason, ok := detail["reason"].(string); ok && reason != "" {
		msg += " (" + reason + ")"
	}
	runlog.Infof("%s", msg)
	// P2-08f:终态记账后异步通知(牧者出一句进展总结)。异步防回调阻塞
	// 终态流程(牧者应答是一次完整 LLM 轮);终态是任务生命周期终点,
	// 每任务至多触发一次(终态记账本身幂等前置)。
	if action == StatusCompleted || action == StatusStopped {
		if fn := m.cfg.OnTerminal; fn != nil {
			go fn(taskID, action)
		}
	}
	return nil
}

// Close 等待全部任务 goroutine 退出(不触发熔断;熔断走 Stop)。
func (m *Manager) Close() {
	m.wg.Wait()
}
