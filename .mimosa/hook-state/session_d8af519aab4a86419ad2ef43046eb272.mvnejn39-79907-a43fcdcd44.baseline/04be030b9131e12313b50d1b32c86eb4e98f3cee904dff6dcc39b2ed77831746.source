package server

// P3-05c ①⑥ 恢复闭环(执行侧拉起)+ 单任务恢复端点:暂停任务的
// 受阻步骤重开后必须有人领取执行——恢复路径对已暂停任务补 Resume
// 拉起循环(唤醒落在无人消费的通道里时,步骤躺在图上没人干);工作台
// 单任务恢复只处理目标任务。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mirror"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// recoverTestRig 恢复闭环测试装配(main.go 同构的最小全家桶):脚本
// 模型端点 + 链 + 执行器 + Manager 循环体。workerCalls 计数单位会话的
// 模型请求(拆解请求 system 含"主宰",单位请求 system 含"虫群"——按
// 系统提示词区分计数)。
type recoverTestRig struct {
	mgr  *manager.Manager
	srv  *Server
	h    http.Handler
	hits atomic.Int64
}

func newRecoverTestRig(t *testing.T, dir string) *recoverTestRig {
	t.Helper()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	rig := &recoverTestRig{}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.hits.Add(1)
		// 真实流式线(与 server_gate_test 同款):拆解回合法的空研判
		//(零意图,主宰静默);单位收到同一正文即作为回合输出完成。
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		send := func(p any) {
			b, _ := json.Marshal(p)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"content": `{"assessment":{},"new_intents":[]}`}}}})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 40}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(endpoint.Close)
	mgr, err := manager.New(manager.Config{Store: store, RootDir: filepath.Join(dir, "tasks")})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		for _, tk := range mgr.Tasks() {
			_ = mgr.Stop(tk.ID)
		}
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	rig.mgr, rig.srv, rig.h = mgr, srv, srv.Handler()

	chain := failover.NewChain(store, failover.EnvConfig{})
	srv.Chain = chain
	var (
		asmMu sync.Mutex
		exec  *swarm.Executor
	)
	loops := overmind.NewRegistry()
	assemble := func() {
		asmMu.Lock()
		defer asmMu.Unlock()
		if exec != nil || !chain.Ready() {
			return
		}
		exec = swarm.NewExecutor(store, llm.Provider(chain), filepath.Join(dir, "tasks"), swarm.Budget{})
		exec.Gate = gate
		exec.Model = chain.ActiveModel()
		exec.Mirror = mirror.New(store, exec.Blobs, t.TempDir())
		mgr.SetOnTaskRun(func(ctx context.Context, taskID string, wake <-chan struct{}) {
			l := &overmind.Loop{
				TaskID: taskID, Store: store, Swarm: exec, Provider: llm.Provider(chain),
				RootDir: filepath.Join(dir, "tasks"), Model: chain.ActiveModel(), SettleCtl: mgr,
			}
			unregister := loops.Register(taskID, l)
			defer unregister()
			l.Run(ctx, wake)
		})
		srv.AttachRuntime(exec)
	}
	srv.RuntimeAssemble = assemble
	srv.Loops = loops
	// 激活模型配置使链就绪并装配(走面板路径)。
	post(t, rig.h, http.MethodPost, "/api/models", `{
		"name":"rc","format":"openai","base_url":"`+endpoint.URL+`",
		"api_key":"sk-rc-key","model":"m-rc"}`, http.StatusOK, &struct{}{})
	post(t, rig.h, http.MethodPost, "/api/models/activate", `{"primary":"rc"}`, http.StatusOK, &struct{}{})
	return rig
}

// waitStepStatus 等待步骤到达目标状态(恢复拉起后异步执行)。
func waitStepStatus(t *testing.T, store *rings.Store, taskID, stepID, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n, err := store.Node(context.Background(), taskID, stepID)
		if err == nil && n.Status == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	n, err := store.Node(context.Background(), taskID, stepID)
	t.Fatalf("步骤 %s 未到 %s: %v %+v", stepID, want, err, n)
}

// TestRecoverPausedTaskRelaunchesAndRuns 恢复闭环(完成标准核心):任务
// 已暂停(如无模型启动被收养)+ 步骤受阻 → 单任务恢复 → 任务被拉起
// (执行侧拉起)→ 受阻步骤被领取执行到终态。
func TestRecoverPausedTaskRelaunchesAndRuns(t *testing.T) {
	dir := t.TempDir()
	rig := newRecoverTestRig(t, dir)
	store := rig.srv.Store

	if _, err := rig.mgr.CreateTask(context.Background(), "rc-paused", "暂停中的受阻任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedBlockedStep(t, store, "rc-paused", "s1", "探测目标端口状态")
	// 任务转暂停(模拟无模型启动被收养后的状态)。
	if err := rig.mgr.Pause("rc-paused"); err != nil {
		t.Fatalf("暂停任务失败: %v", err)
	}
	if st := rig.mgr.Task("rc-paused").Status(); st != manager.StatusPaused {
		t.Fatalf("前置:任务应已暂停,得到 %s", st)
	}

	// 单任务恢复:步骤重开 + 任务拉起 + 步骤被执行。
	var body struct {
		Tasks int `json:"tasks"`
		Steps int `json:"steps"`
	}
	post(t, rig.h, http.MethodPost, "/api/tasks/rc-paused/recover", `{}`, http.StatusOK, &body)
	if body.Tasks != 1 || body.Steps != 1 {
		t.Fatalf("单任务恢复应重开 1 任务 1 步骤: %+v", body)
	}
	if st := rig.mgr.Task("rc-paused").Status(); st != manager.StatusStarted {
		t.Fatalf("恢复应拉起暂停任务(执行侧拉起),得到 %s", st)
	}
	waitStepStatus(t, store, "rc-paused", "s1", "done")
}

// TestRecoverSingleTaskScoped 单任务恢复只处理目标任务:他任务的受阻
// 步骤不动(仍是 blocked)。
func TestRecoverSingleTaskScoped(t *testing.T) {
	dir := t.TempDir()
	rig := newRecoverTestRig(t, dir)
	store := rig.srv.Store

	for _, id := range []string{"rc-a", "rc-b"} {
		if _, err := rig.mgr.CreateTask(context.Background(), id, "scoped-"+id, rings.RoeUpdated{}, nil); err != nil {
			t.Fatalf("建任务 %s 失败: %v", id, err)
		}
		seedBlockedStep(t, store, id, "s1", "探测目标端口状态")
		if err := rig.mgr.Pause(id); err != nil {
			t.Fatalf("暂停任务 %s 失败: %v", id, err)
		}
	}

	var body struct {
		Tasks int `json:"tasks"`
		Steps int `json:"steps"`
	}
	post(t, rig.h, http.MethodPost, "/api/tasks/rc-a/recover", `{}`, http.StatusOK, &body)
	if body.Tasks != 1 || body.Steps != 1 {
		t.Fatalf("应只重开目标任务 1 步骤: %+v", body)
	}
	waitStepStatus(t, store, "rc-a", "s1", "done")
	// rc-b 未被处理:任务保持暂停、步骤保持受阻。
	if st := rig.mgr.Task("rc-b").Status(); st != manager.StatusPaused {
		t.Fatalf("他任务不应被拉起: %s", st)
	}
	n, err := store.Node(context.Background(), "rc-b", "s1")
	if err != nil || n.Status != "blocked" {
		t.Fatalf("他任务步骤应保持受阻: %v %+v", err, n)
	}
}

// TestRecoverAllPausedTasks 恢复全部同样带执行侧拉起:两个暂停任务
// 各有受阻步骤,一键全部重开并拉起执行。
func TestRecoverAllPausedTasks(t *testing.T) {
	dir := t.TempDir()
	rig := newRecoverTestRig(t, dir)
	store := rig.srv.Store

	for _, id := range []string{"rc-all-1", "rc-all-2"} {
		if _, err := rig.mgr.CreateTask(context.Background(), id, "all-"+id, rings.RoeUpdated{}, nil); err != nil {
			t.Fatalf("建任务 %s 失败: %v", id, err)
		}
		seedBlockedStep(t, store, id, "s1", "探测目标端口状态")
		if err := rig.mgr.Pause(id); err != nil {
			t.Fatalf("暂停任务 %s 失败: %v", id, err)
		}
	}
	var body struct {
		Tasks int `json:"tasks"`
		Steps int `json:"steps"`
	}
	post(t, rig.h, http.MethodPost, "/api/tasks/recover-all", `{}`, http.StatusOK, &body)
	if body.Tasks != 2 || body.Steps != 2 {
		t.Fatalf("恢复全部应重开 2 任务 2 步骤: %+v", body)
	}
	waitStepStatus(t, store, "rc-all-1", "s1", "done")
	waitStepStatus(t, store, "rc-all-2", "s1", "done")
}
