package overmind

// P4-42 验收(问题清单 #69/#72):② 主宰研判墙钟超时中断(黑匣子留痕、
// 循环可再触发)与 ⑤ 循环拉起唤醒链——空轮求值(触发门不得短路 0 意图
// 硬底线的求值)与四条拉起路径(队列 admitQueued/重启接管 adoption/
// 冬眠唤醒 Wake/暂停恢复 Resume)拉起后一轮研判必达、心跳到点必醒。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- 公用件 ----

// p442ZeroIntentModel 脚本模型:拆解一律回 0 意图(违硬底线的真实形态
// ——#72 的模型侧前提);虫群请求无事可做直接收口。
func p442ZeroIntentModel() *gatedScriptedModel {
	return newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
		}
		return scriptedReply{Text: "任务包无绑定资产,无事可做。", Finish: "stop"}
	})
}

// p442HangModel 挂死假端点(#69 形态):拆解请求回响应头后停发,直到
// 放行通道关闭才补一个合法回复(供"挂死解除后再触发"的验证)。虫群
// 请求立即正常回。请求逐条留痕(全部经 mutex)。
type p442HangModel struct {
	srv   *httptest.Server
	free  chan struct{}
	mu    sync.Mutex
	reqs  int
	freed bool
}

func newP442HangModel() *p442HangModel {
	m := &p442HangModel{free: make(chan struct{})}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hasTools := len(body.Tools) > 0
		m.mu.Lock()
		m.reqs++
		m.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fl.Flush() // 头到、数据停发:挂死形态起点
		m.mu.Lock()
		freed := m.freed
		m.mu.Unlock()
		if !hasTools && !freed {
			<-m.free // 拆解请求挂死,直到测试放行
		}
		m.writeReply(w, hasTools)
	}))
	return m
}

func (m *p442HangModel) Close()               { m.srv.Close() }
func (m *p442HangModel) release()             { close(m.free); m.mu.Lock(); m.freed = true; m.mu.Unlock() }
func (m *p442HangModel) requests() int        { m.mu.Lock(); defer m.mu.Unlock(); return m.reqs }
func (m *p442HangModel) provider(t *testing.T) llm.Provider {
	p, err := llm.NewProvider(llm.Config{Format: llm.FormatOpenAI, BaseURL: m.srv.URL, APIKey: "k", Model: "hang"})
	if err != nil {
		t.Fatalf("构建挂死 provider: %v", err)
	}
	return p
}

func (m *p442HangModel) writeReply(w http.ResponseWriter, tools bool) {
	fl := w.(http.Flusher)
	send := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fl.Flush()
	}
	if tools {
		send(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		send("[DONE]")
		return
	}
	send(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"{\"new_intents\":[]}"}}]}`)
	send(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	send("[DONE]")
}

// p442SeedGoalAsset 种一个无判据目标(永远 pending——硬底线的求值前提)
// 与一个本地 TCP 靶标资产。
func p442SeedGoalAsset(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	if err := SeedGoal(context.Background(), store, task, "g1", "拿下目标", nil); err != nil {
		t.Fatalf("种目标: %v", err)
	}
	host, port := seedTCPTarget(t, "banner-p442\r\n")
	seedAssets(t, store, task, []string{"a1"}, map[string]string{"a1": net.JoinHostPort(host, strconv.Itoa(port))})
}

// waitStatus 等任务到达指定状态。
func waitStatus(t *testing.T, tk *manager.Task, status string) {
	t.Helper()
	waitFor(t, 10*time.Second, "任务状态变更为 "+status, func() bool { return tk.Status() == status })
}

// hasBlackbox 事件流里是否已有指定类型的黑匣子记录。
func hasBlackbox(t *testing.T, store *rings.Store, task, typ string) bool {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件: %v", err)
	}
	for _, ev := range evs {
		if ev.Kind != rings.EventBlackboxRecord {
			continue
		}
		var rec rings.BlackboxRecord
		if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == typ {
			return true
		}
	}
	return false
}

// ---- ⑤ 空轮求值:#72 的精确复现(补踢到达、无新触发事件) ----

// TestP442IdleFloorEmptyRoundEval 触发门修复的单点验证:拆解回 0 意图
// (硬底线被违)且图上零步骤、目标未达成时,后续每次补踢(拉起/心跳
// 轮)必须再给主宰一轮拆解——修复前触发门短路这些轮次,补踢到达而
// 零研判零外联(SIGQUIT 实证形态)。三轮补踢 = 三次拆解请求。
func TestP442IdleFloorEmptyRoundEval(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	const task = "p442-idle-floor"

	model := p442ZeroIntentModel()
	t.Cleanup(model.Close)
	model.release() // 不闸虫群(本测试无单位起飞)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov}
	p442SeedGoalAsset(t, store, task)

	wake := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(runCtx, wake)
	t.Cleanup(cancel)

	kick := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
		// 每轮补踢后等拆解请求数增长(无触发事件的空轮也要研判)。
	}
	want := 0
	for i := 0; i < 3; i++ {
		kick()
		want++
		waitFor(t, 5*time.Second, fmt.Sprintf("第 %d 次补踢后空轮研判必达", want),
			func() bool { return len(model.decomposeRequests()) >= want })
	}
	// 全程零步骤、目标未达成(硬底线求值前提在);拆解确实看见了态势。
	if got := countNodes(t, store, task, rings.NodeStep, ""); got != 0 {
		t.Fatalf("0 意图回复下不得有步骤,实际 %d", got)
	}
	if reqs := model.decomposeRequests(); !strings.Contains(reqs[len(reqs)-1].LastUser, "拿下目标") {
		t.Fatalf("拆解请求应含态势快照")
	}
}

// ---- ⑤ 四条拉起路径:拉起后一轮研判必达 ----

// TestP442LaunchQueueAdmit 队列拉起(admitQueued,修复前缺补踢):
// 名额腾出拉起排队任务后,一轮研判必达(不等首拍心跳)。
func TestP442LaunchQueueAdmit(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const t1, t2 = "p442-q1", "p442-q2"

	// 占位任务(t1)无目标无资产:拉起后空轮求值不触发,安静占名额。
	m1 := p442ZeroIntentModel()
	t.Cleanup(m1.Close)
	m1.release()
	// 被测任务(t2)有目标无步骤:拉起后必须出一轮拆解。
	m2 := p442ZeroIntentModel()
	t.Cleanup(m2.Close)
	m2.release()
	p1, _ := m1.provider()
	p2, err := m2.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex1 := swarm.NewExecutor(store, p1, root, swarm.Budget{})
	ex2 := swarm.NewExecutor(store, p2, root, swarm.Budget{})
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			switch taskID {
			case t1:
				(&Loop{TaskID: taskID, Store: store, Swarm: ex1, Provider: p1}).Run(tctx, wake)
			case t2:
				(&Loop{TaskID: taskID, Store: store, Swarm: ex2, Provider: p2}).Run(tctx, wake)
			}
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	if _, err := mgr.CreateTask(ctx, t1, "占位", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建 t1: %v", err)
	}
	p442SeedGoalAsset(t, store, t2)
	if _, err := mgr.CreateTask(ctx, t2, "排队任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建 t2: %v", err)
	}
	if tk := mgr.Task(t2); tk.Status() != manager.StatusQueued {
		t.Fatalf("t2 应排队中,实际 %s", tk.Status())
	}
	// 排队期间零研判(名额未腾出)。
	time.Sleep(300 * time.Millisecond)
	if got := len(m2.decomposeRequests()); got != 0 {
		t.Fatalf("排队任务不得提前研判,实际 %d 次", got)
	}
	// 腾名额:retire → admitQueued 拉起 t2 + 补踢 → 一轮研判必达。
	if err := mgr.Stop(t1); err != nil {
		t.Fatalf("停 t1: %v", err)
	}
	waitFor(t, 5*time.Second, "队列拉起后一轮研判必达",
		func() bool { return len(m2.decomposeRequests()) >= 1 })
}

// TestP442LaunchAdoption 重启接管(adoption):账本 started 任务被重启
// 收养重拉后,一轮研判必达。
func TestP442LaunchAdoption(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	const task = "p442-adopt"

	// 先落账:created → started(模拟上次进程留下的在跑任务)。
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: manager.StatusCreated, Title: "接管任务"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: manager.StatusStarted}},
	); err != nil {
		t.Fatalf("落 started 账: %v", err)
	}
	p442SeedGoalAsset(t, store, task)

	model := p442ZeroIntentModel()
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	waitFor(t, 5*time.Second, "接管拉起后一轮研判必达",
		func() bool { return len(model.decomposeRequests()) >= 1 })
}

// TestP442LaunchHibernateWake 冬眠唤醒(Wake):冬眠收场让位后经 Wake
// 拉起,一轮研判必达。
func TestP442LaunchHibernateWake(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "p442-hiber"

	model := p442ZeroIntentModel()
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	hiberGate := hiber.New()
	ckptGate := ckpt.New()
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	ex.Hiber, ex.Ckpt = hiberGate, ckptGate

	var mgr *manager.Manager
	mgr, err = manager.New(manager.Config{
		Store: store, RootDir: root, Hiber: hiberGate, Checkpoint: ckptGate,
		MaxConcurrentTasks: 1,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov,
				Hiber: hiberGate, Ckpt: ckptGate}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	p442SeedGoalAsset(t, store, task)
	if _, err := mgr.CreateTask(ctx, task, "冬眠任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	tk := mgr.Task(task)
	tk.Wake() // 基线一轮(CreateTask 拉起不自带唤醒,事件驱动语义)
	waitFor(t, 5*time.Second, "拉起基线研判", func() bool { return len(model.decomposeRequests()) >= 1 })
	baseline := len(model.decomposeRequests())

	if err := mgr.Hibernate(task, nil); err != nil {
		t.Fatalf("冬眠: %v", err)
	}
	waitStatus(t, tk, manager.StatusHibernated)
	if got := len(model.decomposeRequests()); got != baseline {
		t.Fatalf("冬眠期间不得新增研判,基线 %d 实际 %d", baseline, got)
	}
	if err := mgr.Wake(task, nil); err != nil {
		t.Fatalf("唤醒: %v", err)
	}
	waitStatus(t, tk, manager.StatusStarted)
	waitFor(t, 5*time.Second, "唤醒拉起后一轮研判必达",
		func() bool { return len(model.decomposeRequests()) > baseline })
}

// TestP442LaunchPauseResume 暂停恢复(Resume):暂停让位后经 Resume 拉起,
// 一轮研判必达。
func TestP442LaunchPauseResume(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "p442-resume"

	model := p442ZeroIntentModel()
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	p442SeedGoalAsset(t, store, task)
	if _, err := mgr.CreateTask(ctx, task, "暂停任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	tk := mgr.Task(task)
	tk.Wake() // 基线一轮(CreateTask 拉起不自带唤醒,事件驱动语义)
	waitFor(t, 5*time.Second, "拉起基线研判", func() bool { return len(model.decomposeRequests()) >= 1 })
	baseline := len(model.decomposeRequests())

	if err := mgr.Pause(task); err != nil {
		t.Fatalf("暂停: %v", err)
	}
	waitStatus(t, tk, manager.StatusPaused)
	if err := mgr.Resume(task); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	waitFor(t, 5*time.Second, "恢复拉起后一轮研判必达",
		func() bool { return len(model.decomposeRequests()) > baseline })
}

// TestP442HeartbeatAlwaysWakes 心跳到点必醒(findings=0 仍唤醒且空轮
// 求值必研判):间隔缩到 250ms,不发送任何外部唤醒——多拍心跳各驱动
// 一轮拆解(无新触发事件的空轮,修复前被触发门短路)。
func TestP442HeartbeatAlwaysWakes(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "p442-hb"

	model := p442ZeroIntentModel()
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov,
				HeartbeatInterval: 250 * time.Millisecond}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	p442SeedGoalAsset(t, store, task)
	if _, err := mgr.CreateTask(ctx, task, "心跳任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	// 不发任何唤醒:250ms 一拍 × 5 拍窗口,拆解请求至少 3 次(每拍
	// findings=0——图上零在跑步骤——仍踢达且空轮必研判)。
	waitFor(t, 5*time.Second, "心跳空轮连续研判", func() bool { return len(model.decomposeRequests()) >= 3 })
}

// ---- ② 主宰研判墙钟 ----

// TestP442AssessWallTimeout 研判墙钟:挂死端点下单轮研判在墙钟上限内
// 中断,黑匣子记"研判超时中断",任务仍在跑、循环不永久卡死——放行
// 端点后再触发,新一轮研判照常到达。
func TestP442AssessWallTimeout(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "p442-wall"

	hang := newP442HangModel()
	t.Cleanup(hang.Close)
	prov := hang.provider(t)
	root := filepath.Join(dir, "tasks")
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{})
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: root, MaxConcurrentTasks: 1,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov,
				AssessWall: 400 * time.Millisecond}).Run(tctx, wake)
		},
	})
	if err != nil {
		t.Fatalf("构建 Manager: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	p442SeedGoalAsset(t, store, task)
	tk, err := mgr.CreateTask(ctx, task, "墙钟任务", rings.RoeUpdated{}, nil)
	if err != nil {
		t.Fatalf("建任务: %v", err)
	}
	tk.Wake() // 触发首轮拆解(CreateTask 拉起不自带唤醒,事件驱动语义)

	// 首轮拆解挂死 → 墙钟 400ms 到点中断。
	waitFor(t, 3*time.Second, "挂死拆解请求到达", func() bool { return hang.requests() >= 1 })
	waitFor(t, 3*time.Second, "黑匣子记研判超时中断", func() bool {
		return hasBlackbox(t, store, task, rings.BlackboxAssessTimeout)
	})
	// 超时不是终态:任务仍运行中(循环活着,不永久卡死)。
	if st := tk.Status(); st != manager.StatusStarted {
		t.Fatalf("墙钟中断后任务应仍运行中,实际 %s", st)
	}
	// 放行端点 + 事件重触发 → 新一轮研判到达并成功收尾。
	hang.release()
	tk.Wake()
	waitFor(t, 5*time.Second, "重触发后新一轮研判成功", func() bool {
		return len(storePlanningRecords(t, store, task)) >= 1 && hang.requests() >= 2
	})
}

// storePlanningRecords 任务事件流里的规划类研判记录数(overmind.record
// planning:拆解轮真实到达引擎并回包的账面证据)。
func storePlanningRecords(t *testing.T, store *rings.Store, task string) []rings.OvermindRecord {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件: %v", err)
	}
	var out []rings.OvermindRecord
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindRecord {
			continue
		}
		var r rings.OvermindRecord
		if json.Unmarshal(ev.Payload, &r) == nil && r.Type == rings.OvermindRecPlanning {
			out = append(out, r)
		}
	}
	return out
}
