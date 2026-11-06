package overmind

// P4-33 冬眠让位·主宰侧端到端:收尾中(settling)冬眠 → 冻结收场(无活
// goroutine、终局判定不偷跑)→ 唤醒拉起 → 循环读账本识别收尾中续走
// 收尾 → 终局判定 → completed。全链确定性(脚本模型 + mock 靶)。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ghostreaper/internal/ckpt"
	"ghostreaper/internal/hiber"
	"ghostreaper/internal/manager"
	"ghostreaper/internal/mocktarget"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

func TestHibernateSettlingResumeE2E(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t)
	ctx := context.Background()
	const task = "hiber-settle-e2e"

	target, err := mocktarget.Start()
	if err != nil {
		t.Fatalf("起 mock 靶站失败: %v", err)
	}
	t.Cleanup(target.Close)

	// 脚本:拆解派 1 条意图;单位先提交候选事实(收尾终局判定的燃料),
	// 随后挂长探测(在飞跨过冬眠,收场时被 ctx 按中止收场——崩溃等价)。
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if strings.Contains(rr.LastUser, "步骤:\n  (无)") {
				return scriptedReply{Text: `{"new_intents":[{"intent":"提交事实并持续探测","expected_observation":"事实在案","action_level":"只读探测","covers":["web1"]}]}`, Finish: "stop"}
			}
			return scriptedReply{Text: `{"new_intents":[]}`, Finish: "stop"}
		}
		switch {
		case !seenToolResult(rr, "submit_candidate_fact", "已提交待确认事实"):
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "w1", Name: "submit_candidate_fact",
				Args: `{"statement":"靶站首页可访问","evidence":"探测响应 200","asset_id":"web1"}`,
			}}, Finish: "tool_calls"}
		default:
			return scriptedReply{ToolCalls: []scriptedToolCall{{
				ID: "s1", Name: "bash", Args: `{"command":"sleep 60"}`,
			}}, Finish: "tool_calls"}
		}
	})
	t.Cleanup(model.Close)
	model.release()
	prov, err := model.provider()
	if err != nil {
		t.Fatalf("构建 provider 失败: %v", err)
	}
	root := filepath.Join(dir, "tasks")

	hiberGate := hiber.New()
	ckptGate := ckpt.New()
	ex := swarm.NewExecutor(store, prov, root, swarm.Budget{
		MaxTurns: 20, RunSeconds: 300, WrapupMaxTurns: 3,
	})
	ex.Hiber = hiberGate
	ex.Ckpt = ckptGate

	var mgr *manager.Manager
	loop := &Loop{TaskID: task, Store: store, Swarm: ex, Provider: prov,
		RootDir: root, SettleCtl: mgr, Hiber: hiberGate, Ckpt: ckptGate,
		SettleGraceSeconds: 30}
	mgr, err = manager.New(manager.Config{
		Store: store, RootDir: root, Hiber: hiberGate, Checkpoint: ckptGate,
		OnTaskRun: func(tctx context.Context, taskID string, wake <-chan struct{}) {
			loop.SettleCtl = mgr // 闭包引用变量,任务拉起前必已赋值
			loop.Run(tctx, wake)
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

	if _, err := mgr.CreateTask(ctx, task, "settling 冬眠唤醒续收尾",
		rings.RoeUpdated{}, map[string]any{"timeout_seconds": 5}); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	mustSeedGoal(t, store, task, "g1", "探测靶站", map[string]any{
		"op": "all_of",
		"conditions": []any{
			map[string]any{"kind": "fact_confirmed", "asset_id": "web1", "min": 1},
		},
	})
	mustSeedAsset(t, store, task, "web1", target.MainURL())
	// 种图后踢一脚(建任务即拉起的循环首轮未见目标,补一轮拆解)。
	mgr.Task(task).Wake()

	// 等候选事实入账 + 任务进收尾中(在飞单位挂着长探测)。
	waitFor(t, 60*time.Second, "候选事实提交", func() bool {
		return countNodes(t, store, task, rings.NodeFact, "") >= 1
	})
	waitFor(t, 60*time.Second, "任务进入收尾中", func() bool {
		return mgr.Task(task).Status() == "settling"
	})

	// 冬眠(收尾中):冻结收场——settle 流程在等齐循环收住、不偷跑终局
	// 判定;在飞单位按中止收场(崩溃等价);名额让位。
	if err := mgr.Hibernate(task, nil); err != nil {
		t.Fatalf("收尾中冬眠失败: %v", err)
	}
	if st := mgr.Task(task).Status(); st != manager.StatusHibernated {
		t.Fatalf("冬眠后应 hibernated: %s", st)
	}
	if !hiberGate.Active(task) {
		t.Fatalf("冬眠门应关")
	}
	// 冻结实证:收尾中挂着已落地的在途(无活 goroutine),终局判定
	// 不在冬眠中偷跑——状态保持 hibernated。
	time.Sleep(300 * time.Millisecond)
	if st := mgr.Task(task).Status(); st != manager.StatusHibernated {
		t.Fatalf("冬眠期间状态应保持 hibernated(终局判定不偷跑): %s", st)
	}

	// 唤醒:拉起续跑——循环读账本识别收尾中,直接续走收尾流程;候选
	// 事实经收割确认 → 目标达成 → completed。
	if err := mgr.Wake(task, nil); err != nil {
		t.Fatalf("唤醒失败: %v", err)
	}
	waitFor(t, 60*time.Second, "唤醒后续走收尾至完成终态", func() bool {
		return mgr.Task(task).Status() == "completed"
	})

	acts := lifecycleActionsFor(t, store, task)
	if !containsInOrder(acts, []string{"created", "started", "settling",
		"hibernated", "settling", "completed"}) {
		t.Fatalf("生命周期序列应含 settling→hibernated→settling→completed: %v", acts)
	}
	if facts := countNodes(t, store, task, rings.NodeFact, "confirmed"); facts != 1 {
		t.Fatalf("收割应确认事实,实际 %d", facts)
	}
}
