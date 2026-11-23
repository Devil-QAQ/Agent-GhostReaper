package manager

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P5-05 挂等让位的终局边界:停等态(gate_wait/pause_wait/model_wait)
// 随终局清点收场(P4-43 纪律扩展);冬眠唤醒放行 gate_wait(HibernateSink
// 次序)。----

// seedP505WaitGraph 铺垫挂等让位停等态形态:gate_wait/pause_wait/
// model_wait 各一(等价让位收场后的投影形态)。
func seedP505WaitGraph(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "wg", NodeType: rings.NodeStep, Title: "弹门让位", Status: "dispatched"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "wg", Status: "running"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "wg", Status: rings.StepGateWait}},

		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "wp", NodeType: rings.NodeStep, Title: "暂停让位", Status: "dispatched"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "wp", Status: "running"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "wp", Status: rings.StepPauseWait}},

		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "wm", NodeType: rings.NodeStep, Title: "模型让位", Status: "dispatched"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3",
			Payload: rings.NodeStateChanged{NodeID: "wm", Status: "running"}},
		{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3",
			Payload: rings.NodeStateChanged{NodeID: "wm", Status: rings.StepModelWait}},
	}
	if _, err := store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("铺垫失败: %v", err)
	}
}

// TestP505SweepWaitStatesOnStop 完成标准⑥:任务终止时停等态一律收场
// (挂等单位已收场让位,终局无解除通道)——三态全部落 aborted"任务
// 收场",幂等。
func TestP505SweepWaitStatesOnStop(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	const task = "sw-p505"
	if _, err := mgr.CreateTask(context.Background(), task, "停等清点", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedP505WaitGraph(t, store, task)
	if err := mgr.StopBy(task, map[string]any{"reason": "测试熔断"}); err != nil {
		t.Fatalf("熔断失败: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"wg", "wp", "wm"} {
		n, err := store.Node(ctx, task, id)
		if err != nil || n.Status != "aborted" {
			t.Fatalf("停等步骤 %s 应随终局收场(aborted): %v %+v", id, err, n)
		}
	}
	// 幂等:重跑清点零追加。
	before := eventCount(t, store, task)
	if err := mgr.sweepAtTerminal(task, StatusStopped); err != nil {
		t.Fatalf("重复清点失败: %v", err)
	}
	if after := eventCount(t, store, task); after != before {
		t.Fatalf("重复清点应零追加(前 %d 后 %d)", before, after)
	}
}

// TestP505SweepNotOnHibernate 冬眠不清停等态:挂等单位与卡一并冻结,
// 唤醒后各归其位(P4-33 纪律——gate_wait 由唤醒放行,pause_wait/
// model_wait 解除通道不经票表,原样保留)。
func TestP505SweepNotOnHibernate(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	const task = "hiber-p505"
	if _, err := mgr.CreateTask(context.Background(), task, "冬眠不清", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedP505WaitGraph(t, store, task)
	if err := mgr.Hibernate(task, nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"wg", "wp", "wm"} {
		want := map[string]string{
			"wg": rings.StepGateWait, "wp": rings.StepPauseWait, "wm": rings.StepModelWait,
		}[id]
		if n, err := store.Node(ctx, task, id); err != nil || n.Status != want {
			t.Fatalf("冬眠中 %s 应保持 %s: %v %+v", id, want, err, n)
		}
	}
}

func eventCount(t *testing.T, store *rings.Store, task string) int {
	t.Helper()
	evs, err := store.Events(context.Background(), task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}
	return len(evs)
}
