package server

// P3-05b ⑤ 模型激活免重启:无模型启动 → 面板激活主用 → 不重启进程
// 即装配镜/主宰循环/虫群执行器,新建任务由主宰循环接管。装配机制与
// 行为双验(链就绪触发钩子、幂等、任务拉起即走循环体)。

import (
	"context"
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

// TestModelActivateAssemblesRuntimeWithoutRestart 完成标准(⑤):无模型
// 启动进程 → 面板激活主用 → 不重启即能建任务执行。装配层钩子按
// main.go 同构搭(build exec+mirror、挂 Manager 循环体、AttachRuntime),
// 断言:激活前无执行器;激活即装配且钩子恰一次(幂等);此后建任务,
// 主宰循环真实跑起来(脚本端点收到拆解请求)。
func TestModelActivateAssemblesRuntimeWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
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
	h := srv.Handler()

	// 脚本模型端点:任何请求回合法的空研判拆解(零意图 → 主宰静默轮
	// 空转,不派发);计数器证明循环真实发出过 LLM 请求。
	var llmCalls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"{\"assessment\":{},\"new_intents\":[]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":5}}`))
	}))
	t.Cleanup(endpoint.Close)

	chain := failover.NewChain(store, failover.EnvConfig{})
	srv.Chain = chain

	// 装配层钩子(main.go 同构):链就绪时拉起执行器全家桶。
	var (
		asmMu     sync.Mutex
		exec      *swarm.Executor
		assembles int
	)
	loops := overmind.NewRegistry()
	assemble := func() {
		asmMu.Lock()
		defer asmMu.Unlock()
		if exec != nil || !chain.Ready() {
			return
		}
		assembles++
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

	// ① 无模型启动:链未就绪,无执行器。
	if chain.Ready() {
		t.Fatalf("空库空环境,链不应就绪")
	}
	if srv.swarmExec() != nil {
		t.Fatalf("未装配不应有执行器")
	}

	// ② 面板保存模型配置:配置即备链腿,链就绪 → 钩子触发 → 执行器/
	// 镜/循环体装配(与启动时就绪即刻装配同一条入口)。
	post(t, h, http.MethodPost, "/api/models", `{
		"name":"rt","format":"openai","base_url":"`+endpoint.URL+`",
		"api_key":"sk-rt-key","model":"m-rt"}`, http.StatusOK, &struct{}{})
	if !chain.Ready() {
		t.Fatalf("保存配置后链应就绪(备链腿)")
	}
	asmMu.Lock()
	got := exec
	n := assembles
	asmMu.Unlock()
	if got == nil || n != 1 {
		t.Fatalf("配置写就绪应触发恰一次装配: exec=%v n=%d", got != nil, n)
	}
	if got.Mirror == nil || got.Gate == nil {
		t.Fatalf("执行器应带镜与审批门: %+v", got)
	}
	if srv.swarmExec() != got || srv.helmOf() != got.Helm {
		t.Fatalf("面板应换入执行器与同一舵杆实例")
	}

	// ③ 面板激活主用:链重载(主用腿切换),钩子再触发但装配幂等。
	post(t, h, http.MethodPost, "/api/models/activate", `{"primary":"rt"}`, http.StatusOK, &struct{}{})
	if !chain.Ready() {
		t.Fatalf("激活后链应就绪")
	}
	asmMu.Lock()
	again := exec
	n = assembles
	asmMu.Unlock()
	if n != 1 || again != got {
		t.Fatalf("重复就绪不应重复装配: n=%d same=%v", n, again == got)
	}

	// ⑤ 不重启建任务:任务拉起即走循环体,唤醒后主宰向脚本端点发出
	// 拆解请求(真实面板路径经建任务 API 自带种子事件唤醒,这里直建
	// 任务后补一脚唤醒,等价)。
	if _, err := mgr.CreateTask(context.Background(), "rt-task", "免重启任务", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	mgr.Task("rt-task").Wake()
	deadline := time.Now().Add(20 * time.Second)
	for llmCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if llmCalls.Load() == 0 {
		t.Fatalf("激活后不重启,主宰循环应已运行(脚本端点零请求)")
	}
	if tk := mgr.Task("rt-task"); tk == nil || tk.Status() != manager.StatusStarted {
		t.Fatalf("任务应处于运行中: %v", tk)
	}
}
