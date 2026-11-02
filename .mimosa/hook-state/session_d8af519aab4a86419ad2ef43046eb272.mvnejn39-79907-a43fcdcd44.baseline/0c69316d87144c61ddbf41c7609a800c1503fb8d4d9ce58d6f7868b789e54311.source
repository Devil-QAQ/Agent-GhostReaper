package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-18 收口信号(问题清单 #30)面板 API ----
// 任务视图的待决提醒投影(仅运行中)/「继续」重计 /「结束」走优雅
// 收尾(真实 Manager:settling → settle → stopped,settled_by=
// closure_prompt;非熔断)/ 无待决与无循环的如实拒绝 / Agent 栏阈值
// 可配(改小生效,越界拒)。

// seedClosurePrompt 种一条待决收口提醒(机器路径的同一负载形态)。
func seedClosurePrompt(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	_, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventClosureRecord, Actor: "overmind",
		Payload: rings.ClosureRecord{
			Type: rings.ClosureRecPrompt, Rounds: 6, Minutes: 41,
			RoundsThreshold: 5, MinutesThreshold: 30,
			AnchorAt:   time.Now().UTC().Add(-41 * time.Minute).Format(time.RFC3339),
			AnchorKind: rings.ClosureAnchorFinding,
		},
	})
	if err != nil {
		t.Fatalf("种收口提醒失败: %v", err)
	}
}

// taskOf 从任务列表取指定任务的视图。
func taskOf(t *testing.T, h *http.ServeMux, task string) map[string]any {
	t.Helper()
	var list []map[string]any
	get(t, h, "/api/tasks", http.StatusOK, &list)
	for _, v := range list {
		if v["id"] == task {
			return v
		}
	}
	t.Fatalf("任务 %s 不在列表", task)
	return nil
}

func TestClosureAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	ctx := context.Background()
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) { <-tctx.Done() },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop("p418-api")
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	loops := overmind.NewRegistry()
	srv.Loops = loops
	h := srv.Handler()

	// 建任务(经面板 API,目标/资产必填口径)——自动拉起(started)。
	var created map[string]string
	post(t, h, http.MethodPost, "/api/tasks",
		`{"goals":["测试目标"],"assets":[{"class":"host","title":"10.0.0.1"}],"id":"p418-api","title":"收口 E2E"}`,
		http.StatusOK, &created)

	// 无待决:列表无 closure 字段;两键动作如实 409。
	if v := taskOf(t, h, "p418-api"); v["closure"] != nil {
		t.Fatalf("无待决不应有 closure 字段: %+v", v["closure"])
	}
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"continue"}`, http.StatusConflict, nil)
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"end"}`, http.StatusConflict, nil)

	// 待决在:列表携带提醒投影(数字取自快照)。
	seedClosurePrompt(t, store, "p418-api")
	v := taskOf(t, h, "p418-api")
	c, _ := v["closure"].(map[string]any)
	if c == nil {
		t.Fatalf("待决提醒应在任务视图: %+v", v)
	}
	if c["rounds"] != float64(6) || c["rounds_threshold"] != float64(5) ||
		c["minutes_threshold"] != float64(30) || c["anchor_kind"] != "finding" || c["prompted_at"] == "" {
		t.Fatalf("提醒投影快照不符: %+v", c)
	}

	// 「继续」:任务不停(状态仍 started),计数重计(无待决),再点 409。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"continue","reason":"再看一会"}`,
		http.StatusOK, &out)
	if out["status"] != "continued" {
		t.Fatalf("继续应返回 continued: %+v", out)
	}
	if v := taskOf(t, h, "p418-api"); v["closure"] != nil || v["status"] != "started" {
		t.Fatalf("继续后应无待决且任务仍运行: closure=%+v status=%v", v["closure"], v["status"])
	}
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"continue"}`, http.StatusConflict, nil)

	// 「结束」前置:无在跑循环时如实拒绝(不熔断顶替优雅收尾)。
	seedClosurePrompt(t, store, "p418-api")
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"end"}`, http.StatusConflict, nil)

	// 注册在跑循环(与生产同构:SettleCtl=Manager)→「结束」走优雅收尾。
	// 循环真跑(Run):结束键的 settleKick 由 Run 消费 → settle 流程 →
	// 终局判定 → 定终态,与生产路径一致。
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	wake := make(chan struct{})
	l := &overmind.Loop{TaskID: "p418-api", Store: store, SettleCtl: mgr}
	unregister := loops.Register("p418-api", l)
	defer unregister()
	go l.Run(runCtx, wake)
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"end"}`,
		http.StatusOK, &out)
	if out["status"] != "settling" {
		t.Fatalf("结束应返回 settling: %+v", out)
	}
	if st := mgr.Task("p418-api").Status(); st != manager.StatusSettling {
		t.Fatalf("任务应已进入收尾: %s", st)
	}
	if v := taskOf(t, h, "p418-api"); v["closure"] != nil {
		t.Fatalf("已裁决的提醒不应再呈待决: %+v", v["closure"])
	}

	// settle 流程(循环 Run 消费 settleKick)→ 终态 stopped + settled_by=
	// closure_prompt → 列表 settled=true 且透出 settled_by(显示层译为
	//「收口结束」,P4-25 审核迭代),closure 不再出现。
	waitUntil(t, 10*time.Second, "收口终态 stopped", func() bool {
		return mgr.Task("p418-api").Status() == manager.StatusStopped
	})
	evs, _ := store.Events(ctx, "p418-api", 0, 0)
	last := map[string]any{}
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "stopped" {
			last = p.Detail
		}
	}
	if last["settled_by"] != rings.SettleByClosurePrompt {
		t.Fatalf("终态应携带 settled_by=closure_prompt: %+v", last)
	}
	v = taskOf(t, h, "p418-api")
	if v["settled"] != true || v["settled_by"] != rings.SettleByClosurePrompt || v["closure"] != nil {
		t.Fatalf("收口终态应 settled 且带结束方式锚点、无待决: settled=%v settled_by=%v closure=%+v",
			v["settled"], v["settled_by"], v["closure"])
	}

	// 终态后两键均 409(无待决)。
	post(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"end"}`, http.StatusConflict, nil)

	// 非法 action 拒绝。
	seedClosurePrompt(t, store, "p418-api")
	rec := doReq(t, h, http.MethodPost, "/api/tasks/p418-api/closure", `{"action":"later"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 action 应 400: %d %s", rec.Code, rec.Body.String())
	}
}

// TestClosureConfigAPI 任务级配置(审核迭代 1):建任务初始组落 created
// detail、任务视图投影生效值与任务级显式值;运行中变更端点快照式覆盖;
// 坏输入拒收。
func TestClosureConfigAPI(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) { <-tctx.Done() },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop("p418-cfg")
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	// 建任务带收口组(3 轮/2 分钟)→ created detail 落库,投影生效值
	// 优先于全局默认。
	post(t, h, http.MethodPost, "/api/tasks",
		`{"goals":["g"],"assets":[{"class":"host","title":"10.0.0.9"}],"id":"p418-cfg","title":"配置","closure":{"enabled":true,"rounds":3,"minutes":2}}`,
		http.StatusOK, nil)
	v := taskOf(t, h, "p418-cfg")
	c, _ := v["closure_config"].(map[string]any)
	if c == nil || c["enabled"] != true || c["rounds"] != float64(3) || c["minutes"] != float64(2) ||
		c["task_set"] != true || c["task_rounds"] != float64(3) || c["task_minutes"] != float64(2) {
		t.Fatalf("任务级初始配置投影不符: %+v", c)
	}

	// 运行中关闭:整体快照(数字回 0 = 用全局默认)。
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p418-cfg/closure/config",
		`{"enabled":false,"rounds":0,"minutes":0}`, http.StatusOK, &out)
	if out["enabled"] != false || out["task_rounds"] != float64(0) ||
		out["rounds"] != float64(overmind.DefaultClosureRounds) {
		t.Fatalf("关闭后投影不符: %+v", out)
	}
	// 再打开、数字留 0:生效回全局默认层,任务级仍有配置(开关开)。
	post(t, h, http.MethodPost, "/api/tasks/p418-cfg/closure/config",
		`{"enabled":true}`, http.StatusOK, &out)
	if out["enabled"] != true || out["task_set"] != true ||
		out["rounds"] != float64(overmind.DefaultClosureRounds) || out["minutes"] != float64(overmind.DefaultClosureMinutes) {
		t.Fatalf("打开后应回全局默认层: %+v", out)
	}
	// 单独改轮数。
	post(t, h, http.MethodPost, "/api/tasks/p418-cfg/closure/config",
		`{"enabled":true,"rounds":6}`, http.StatusOK, &out)
	if out["rounds"] != float64(6) || out["task_rounds"] != float64(6) {
		t.Fatalf("单改轮数不符: %+v", out)
	}

	// 坏输入:缺开关 400;越界 400;建任务坏组 400。
	post(t, h, http.MethodPost, "/api/tasks/p418-cfg/closure/config", `{"rounds":2}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks/p418-cfg/closure/config", `{"enabled":true,"rounds":101}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks/p418-cfg/closure/config", `{"enabled":true,"minutes":-1}`, http.StatusBadRequest, nil)
	post(t, h, http.MethodPost, "/api/tasks",
		`{"goals":["g"],"assets":[{"class":"host","title":"10.0.0.9"}],"id":"p418-bad","closure":{"rounds":999}}`,
		http.StatusBadRequest, nil)
}

// TestClosureAgentParams Agent 栏阈值可配(全局默认):默认 20 轮/50 分钟
// (审核迭代 1 拍板);改小生效(下一次研判轮评估即用新值);越界拒绝。
func TestClosureAgentParams(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	gate := swarm.NewLedgerGate(store)
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) { <-tctx.Done() },
	})
	if err != nil {
		t.Fatalf("构建 Manager 失败: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	var body struct {
		Params struct {
			Closure struct {
				Rounds  agentIntParam `json:"rounds"`
				Minutes agentIntParam `json:"minutes"`
			} `json:"closure"`
		} `json:"params"`
	}
	get(t, h, "/api/agents", http.StatusOK, &body)
	if body.Params.Closure.Rounds.Value != overmind.DefaultClosureRounds ||
		body.Params.Closure.Minutes.Value != overmind.DefaultClosureMinutes {
		t.Fatalf("默认阈值应为 %d/%d: %+v", overmind.DefaultClosureRounds, overmind.DefaultClosureMinutes, body.Params.Closure)
	}

	post(t, h, http.MethodPost, "/api/agents/params", `{"closure_rounds":2,"closure_minutes":5}`, http.StatusOK, &body)
	if body.Params.Closure.Rounds.Value != 2 || body.Params.Closure.Minutes.Value != 5 ||
		!body.Params.Closure.Rounds.Overridden || !body.Params.Closure.Minutes.Overridden {
		t.Fatalf("改小应生效并带覆盖审计: %+v", body.Params.Closure)
	}

	// 越界拒绝(轮数 0 / 间隔超 24 小时)。
	rec := doReq(t, h, http.MethodPost, "/api/agents/params", `{"closure_rounds":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("轮数 0 应 400: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(t, h, http.MethodPost, "/api/agents/params", `{"closure_minutes":1441}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("间隔 1441 应 400: %d %s", rec.Code, rec.Body.String())
	}
}
