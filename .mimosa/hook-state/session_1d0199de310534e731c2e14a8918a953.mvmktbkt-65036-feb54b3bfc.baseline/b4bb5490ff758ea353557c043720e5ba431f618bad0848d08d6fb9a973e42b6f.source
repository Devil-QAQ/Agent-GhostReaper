package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"ghostreaper/internal/manager"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-23 待处理徽标(问题清单 #36)服务端 ----
// 收口弹窗直接关闭(dismiss):待决保留、任务视图 closure.parked=true
//(任务行红点落点),继续/结束照常解除;无待决 409。
// 任务视图 pending_approvals:审批门现读,进出待决随裁决归零。

// TestClosureDismissAPI dismiss 动作与搁置投影。
func TestClosureDismissAPI(t *testing.T) {
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
		_ = mgr.Stop("p423-dismiss")
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	post(t, h, http.MethodPost, "/api/tasks",
		`{"goals":["测试目标"],"assets":[{"class":"host","title":"10.0.0.2"}],"id":"p423-dismiss","title":"搁置"}`,
		http.StatusOK, nil)

	// 无待决:dismiss 如实 409(与继续/结束同口径)。
	post(t, h, http.MethodPost, "/api/tasks/p423-dismiss/closure", `{"action":"dismiss"}`, http.StatusConflict, nil)

	// 待决在:直接关闭 → 待决保留 + parked(任务行红点数据面)。
	seedClosurePrompt(t, store, "p423-dismiss")
	var out map[string]any
	post(t, h, http.MethodPost, "/api/tasks/p423-dismiss/closure", `{"action":"dismiss"}`,
		http.StatusOK, &out)
	if out["status"] != "dismissed" {
		t.Fatalf("搁置应返回 dismissed: %+v", out)
	}
	v := taskOf(t, h, "p423-dismiss")
	c, _ := v["closure"].(map[string]any)
	if c == nil || c["parked"] != true {
		t.Fatalf("搁置后任务视图应带 closure.parked=true: %+v", c)
	}
	// 再关一次:幂等,仍 200、仍搁置。
	post(t, h, http.MethodPost, "/api/tasks/p423-dismiss/closure", `{"action":"dismiss"}`, http.StatusOK, nil)
	v = taskOf(t, h, "p423-dismiss")
	if c, _ = v["closure"].(map[string]any); c == nil || c["parked"] != true {
		t.Fatalf("重复搁置应幂等: %+v", c)
	}

	// 处理才消:「继续」后待决解除(红点随之消失)。
	post(t, h, http.MethodPost, "/api/tasks/p423-dismiss/closure", `{"action":"continue"}`, http.StatusOK, nil)
	if v = taskOf(t, h, "p423-dismiss"); v["closure"] != nil {
		t.Fatalf("继续后应无待决: %+v", v["closure"])
	}

	// 新提醒弹出时 parked 从头计(新窗口未关闭过)。
	seedClosurePrompt(t, store, "p423-dismiss")
	v = taskOf(t, h, "p423-dismiss")
	c, _ = v["closure"].(map[string]any)
	if c == nil || c["parked"] != false {
		t.Fatalf("新提醒不应带搁置: %+v", c)
	}
}

// TestTasksPendingApprovals 任务视图的待审批笔数(审批门现读):
// 进入待决即涨、裁决即归零;多任务互不串。
func TestTasksPendingApprovals(t *testing.T) {
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
		_ = mgr.Stop("p423-appr")
		mgr.Close()
	})
	srv, err := New(store, mgr, gate)
	if err != nil {
		t.Fatalf("构建面板后端失败: %v", err)
	}
	h := srv.Handler()

	post(t, h, http.MethodPost, "/api/tasks",
		`{"goals":["g"],"assets":[{"class":"host","title":"10.0.0.3"}],"id":"p423-appr","title":"计数"}`,
		http.StatusOK, nil)
	if v := taskOf(t, h, "p423-appr"); v["pending_approvals"] != float64(0) {
		t.Fatalf("初始待审批应为 0: %v", v["pending_approvals"])
	}

	// 两笔进入待决 → 计数 2。
	t1, err := gate.Enter(swarm.ApprovalRequest{
		TaskID: "p423-appr", StepID: "s-1", UnitID: "u-1", Intent: "读取配置",
		ActionLevel: rings.ActionLevel("只读探测"), ToolName: "bash", ToolArgs: `{"cmd":"cat a"}`,
	})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := gate.Enter(swarm.ApprovalRequest{
		TaskID: "p423-appr", StepID: "s-2", UnitID: "u-1", Intent: "读取配置2",
		ActionLevel: rings.ActionLevel("只读探测"), ToolName: "bash", ToolArgs: `{"cmd":"cat b"}`,
	}); err != nil {
		t.Fatalf("Enter 2: %v", err)
	}
	if v := taskOf(t, h, "p423-appr"); v["pending_approvals"] != float64(2) {
		t.Fatalf("两笔待决应计 2: %v", v["pending_approvals"])
	}

	// 裁决一笔 → 计数 1;再裁决 → 归零。
	if err := gate.Decide(context.Background(), "p423-appr", t1.Req.ID, swarm.GateDecision{Approved: true}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if v := taskOf(t, h, "p423-appr"); v["pending_approvals"] != float64(1) {
		t.Fatalf("裁决一笔后应计 1: %v", v["pending_approvals"])
	}
	pend := gate.Pending("p423-appr")
	if len(pend) != 1 {
		t.Fatalf("待决快照应余 1: %d", len(pend))
	}
	if err := gate.Decide(context.Background(), "p423-appr", pend[0].ID, swarm.GateDecision{Approved: false, By: "creator"}); err != nil {
		t.Fatalf("Decide 2: %v", err)
	}
	if v := taskOf(t, h, "p423-appr"); v["pending_approvals"] != float64(0) {
		t.Fatalf("全部裁决后应归零: %v", v["pending_approvals"])
	}
}
