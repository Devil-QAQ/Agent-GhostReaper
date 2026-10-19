package server

// P3-14b ④ 人工标记达成:无谓词目标机器不可判完成,操作者看到证据后
// 手动收场——依据必填入账留痕(actor=creator);全部目标达成时任务按
// 既有完成路径正确收场(completed)。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/overmind"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// TestGoalAchieveGuards 守卫:任务/目标不存在 404;非 goal 节点 400;
// 依据必填 400;非进行中状态 409;成功后目标 achieved 且重复标记 409。
func TestGoalAchieveGuards(t *testing.T) {
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

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks",
		`{"id":"ga-1","title":"人工标记达成守卫","goals":["确认暴露面"],
		  "assets":[{"class":"host","title":"10.9.9.9"}]}`,
		http.StatusOK, &created)

	var goals []map[string]any
	get(t, h, "/api/tasks/ga-1/goals", http.StatusOK, &goals)
	if len(goals) != 1 {
		t.Fatalf("应恰 1 个种子目标: %+v", goals)
	}
	goalID := goals[0]["id"].(string)
	var tree struct {
		Assets []struct {
			ID string `json:"id"`
		} `json:"assets"`
	}
	get(t, h, "/api/tasks/ga-1/assets", http.StatusOK, &tree)
	if len(tree.Assets) != 1 {
		t.Fatalf("应恰 1 个种子资产")
	}

	// 404:任务不存在 / 目标不存在。
	post(t, h, http.MethodPost, "/api/tasks/none/goals/"+goalID+"/achieve", `{"reason":"x"}`,
		http.StatusNotFound, &created)
	post(t, h, http.MethodPost, "/api/tasks/ga-1/goals/g-nope/achieve", `{"reason":"x"}`,
		http.StatusNotFound, &created)
	// 400:依据为空;非 goal 节点。
	post(t, h, http.MethodPost, "/api/tasks/ga-1/goals/"+goalID+"/achieve", `{"reason":"  "}`,
		http.StatusBadRequest, &created)
	post(t, h, http.MethodPost, "/api/tasks/ga-1/goals/"+tree.Assets[0].ID+"/achieve", `{"reason":"x"}`,
		http.StatusBadRequest, &created)

	// 成功:200 + 状态迁移入账(依据随迁移动作一笔)。
	var out map[string]string
	post(t, h, http.MethodPost, "/api/tasks/ga-1/goals/"+goalID+"/achieve",
		`{"reason":"靶场实测:SQL 注入提取 users 表全部 5 行"}`, http.StatusOK, &out)
	if out["status"] != "achieved" {
		t.Fatalf("响应应回 achieved: %+v", out)
	}
	evs, _ := store.Events(context.Background(), "ga-1", 0, 0)
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged || ev.Actor != "creator" {
			continue
		}
		var p rings.NodeStateChanged
		if json.Unmarshal(ev.Payload, &p) == nil && p.NodeID == goalID {
			if p.Status != overmind.GoalStatusAchieved || !strings.Contains(p.Note, "users 表全部 5 行") {
				t.Fatalf("迁移事件应携带依据: %+v", p)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("应入账 actor=creator 的达成迁移(依据留痕)")
	}
	// 重复标记:409(已达成)。
	post(t, h, http.MethodPost, "/api/tasks/ga-1/goals/"+goalID+"/achieve", `{"reason":"再标"}`,
		http.StatusConflict, &created)
}

// TestGoalAchieveCompletesTask 全链路:无谓词目标在跑任务上人工标记达成
// → 唤醒主宰 → judgeGoals 全达成 → 循环退出 → 任务 completed(正确
// 收场,不再等总闸)。
func TestGoalAchieveCompletesTask(t *testing.T) {
	dir := t.TempDir()
	store := openSStore(t, dir)
	ctx := context.Background()

	// 脚本模型:拆解(无工具广告)恒回空意图——目标无谓词,机器不可判,
	// 循环每轮拆解后静默等待。
	model := newSModel(func(rr *srecorded) sreply {
		if len(rr.ToolsOffered) == 0 {
			return sreply{Text: `{"new_intents":[]}`}
		}
		return sreply{Text: "无意图。"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider: %v", err)
	}
	ex := swarm.NewExecutor(store, prov, filepath.Join(dir, "tasks"), swarm.Budget{})
	gate := swarm.NewLedgerGate(store)
	ex.Gate = gate
	mgr, err := manager.New(manager.Config{
		Store: store, RootDir: filepath.Join(dir, "tasks"),
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			(&overmind.Loop{TaskID: taskID, Store: store, Swarm: ex, Provider: prov}).Run(tctx, wake)
		},
	})
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

	var created map[string]any
	post(t, h, http.MethodPost, "/api/tasks",
		`{"id":"ga-2","title":"人工标记达成收场","goals":["拿到数据库凭据"]}`,
		http.StatusOK, &created)

	// 等第一轮真实拆解发生(无谓词目标 pending → 拆解空意图 → 静默)。
	waitUntil(t, 60*time.Second, "首轮拆解发生", func() bool {
		for _, rr := range model.snapshot() {
			if len(rr.ToolsOffered) == 0 && strings.Contains(rr.LastUser, "拿到数据库凭据") {
				return true
			}
		}
		return false
	})
	if tk := mgr.Task("ga-2"); tk == nil || tk.Status() != manager.StatusStarted {
		t.Fatalf("标记前任务应仍 started(无谓词目标机器不可判)")
	}

	// 面板人工标记达成(依据入账)。
	var goals []map[string]any
	get(t, h, "/api/tasks/ga-2/goals", http.StatusOK, &goals)
	if len(goals) != 1 {
		t.Fatalf("应恰 1 个目标: %+v", goals)
	}
	post(t, h, http.MethodPost, fmt.Sprintf("/api/tasks/ga-2/goals/%s/achieve", goals[0]["id"]),
		`{"reason":"凭据已在报告页核对:5 组账号口令全部还原"}`, http.StatusOK, &created)

	// 任务正确收场:completed(完成理由如实含人工标记路径)。
	waitUntil(t, 60*time.Second, "任务进入完成终态", func() bool {
		return mgr.Task("ga-2").Status() == manager.StatusCompleted
	})
	var reason string
	evs, _ := store.Events(ctx, "ga-2", 0, 0)
	for _, ev := range evs {
		if ev.Kind != rings.EventTaskLifecycle {
			continue
		}
		var p rings.TaskLifecycle
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == manager.StatusCompleted {
			reason, _ = p.Detail["reason"].(string)
		}
	}
	if !strings.Contains(reason, "人工标记") {
		t.Fatalf("完成理由应如实含人工标记路径: %q", reason)
	}
	goals2, _ := store.NodesByType(ctx, "ga-2", rings.NodeGoal, "")
	if len(goals2) != 1 || goals2[0].Status != overmind.GoalStatusAchieved {
		t.Fatalf("目标应已达成: %+v", goals2)
	}
}

// TestTaskExtractPromptDecidable 提取提示词含可判定完成条件引导
// (P3-14b ④):目标写成可判定条件 + pred 判定口径,反向规则(不编造)保留。
func TestTaskExtractPromptDecidable(t *testing.T) {
	s := taskExtractSystem()
	for _, want := range []string{"可判定的完成条件", `"pred"`, "判定口径", "不编造"} {
		if !strings.Contains(s, want) {
			t.Fatalf("提取提示词应含 %q:\n%s", want, s)
		}
	}
}
