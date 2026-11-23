package overmind

import (
	"context"
	"testing"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P5-05 挂等让位的调度面:停等态不领取 / 队首档进档序 / 模型
// 停等对账唤醒(链就绪)----

// p505WaitStep 种一个停等态步骤(等价让位收场后的投影形态)。
func p505WaitStep(t *testing.T, store *rings.Store, task, stepID, status string) {
	t.Helper()
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "意图 " + stepID,
			Status: "dispatched", Detail: map[string]any{"goal_id": "g1"},
		}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: stepID, Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: stepID, Status: status}},
	); err != nil {
		t.Fatal(err)
	}
}

// p505CoolingProvider 实现 AllCooling 的桩链(模型冷却面探测;llm.Provider
// 空嵌入——冷却分支不发起调用,测试零实现)。
type p505CoolingProvider struct {
	llm.Provider
	cooling bool
}

func (p *p505CoolingProvider) AllCooling() bool { return p.cooling }

// TestP505WaitStatesNotClaimable 完成标准⑥:停等态(scope_wait/gate_wait/
// pause_wait/model_wait)不是可领取步骤——调度器零挑选(排队中不二次
// 挂等:不执行命令就出不了卡),水位计数如实反映让位后状态。
func TestP505WaitStatesNotClaimable(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p505-not-claimable"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	for _, st := range []string{rings.StepScopeWait, rings.StepGateWait, rings.StepPauseWait, rings.StepModelWait} {
		p505WaitStep(t, store, task, "s-"+st, st)
	}
	// 附一个正常 dispatched 步骤对照(可领取)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-run", NodeType: rings.NodeStep, Title: "正常步骤",
			Status: "dispatched", Detail: map[string]any{"goal_id": "g1"}},
		}); err != nil {
		t.Fatal(err)
	}
	dispatched, err := store.NodesByType(ctx, task, rings.NodeStep, "dispatched")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := store.NodesByType(ctx, task, rings.NodeStep, "")
	runnable := selectRunnable(dispatched, all, nil)
	if len(runnable) != 1 || runnable[0].ID != "s-run" {
		t.Fatalf("停等态不得进可领取集: %+v", runnable)
	}
	// 水位:停等不计执行中也不计排队(可领取口径;等待 N 归 P5-06)。
	w := QueueWatermarkOf(all, nil, 8)
	if w.Running != 0 || w.Queued != 1 {
		t.Fatalf("水位应如实反映让位后状态: %+v", w)
	}
}

// TestP505ModelWaitResolveOnReady 完成标准④:模型停等对账——链路冷却中
// 不动(等待模型),链就绪即唤醒回 dispatched+队首档(已批准待上位),
// 幂等。
func TestP505ModelWaitResolveOnReady(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p505-model-resolve"
	p444Task(t, store, task, rings.TaskTypePentest, "www.example.com")
	p505WaitStep(t, store, task, "s1", rings.StepModelWait)

	// 链冷却中(全员冷却):对账不动(带链冷却面的执行器桩;冷却分支
	// 不进调度,桩不发起调用)。
	ex := swarm.NewExecutor(store, &p505CoolingProvider{cooling: true}, t.TempDir(), swarm.Budget{})
	l := &Loop{TaskID: task, Store: store, Swarm: ex}
	l.resolveModelWaits(ctx)
	st, err := store.Node(ctx, task, "s1")
	if err != nil || st.Status != rings.StepModelWait {
		t.Fatalf("冷却中不得唤醒: %v %+v", err, st)
	}

	// 链就绪(不再全员冷却):唤醒回队首档(裸循环无链面=视就绪的
	// fail-open 形态;schedule 对未装配执行器零动作)。
	l2 := &Loop{TaskID: task, Store: store}
	l2.resolveModelWaits(ctx)
	st, _ = store.Node(ctx, task, "s1")
	if st.Status != "dispatched" || st.Detail["scope_head"] != true {
		t.Fatalf("链就绪应唤醒回队首档: %+v", st)
	}
	if _, ok := st.Detail["head_seq"]; !ok {
		t.Fatalf("唤醒应带进档序 head_seq: %+v", st.Detail)
	}
	// 幂等:再跑一轮零落笔(已 dispatched)。
	l2.resolveModelWaits(ctx)
	if st, _ = store.Node(ctx, task, "s1"); st.Status != "dispatched" {
		t.Fatalf("对账应幂等: %+v", st)
	}
}

// TestP505HeadSeqWakeOrder 完成标准①:队首档内按进档序(head_seq=解除
// 唤醒时图版本)先后不抢——多只让位单位解除时按解除顺序上位。
func TestP505HeadSeqWakeOrder(t *testing.T) {
	early := rings.NodeFull{NodeView: rings.NodeView{ID: "s-early"}, Detail: map[string]any{"scope_head": true, "head_seq": 10}}
	late := rings.NodeFull{NodeView: rings.NodeView{ID: "s-late"}, Detail: map[string]any{"scope_head": true, "head_seq": 20}}
	if !StepLaunchLess(early, late) {
		t.Fatal("先解除的让位单位应先上位(队首档内按进档序)")
	}
	normal := rings.NodeFull{NodeView: rings.NodeView{ID: "s-n"}, Detail: map[string]any{"priority": "high"}}
	if !StepLaunchLess(late, normal) {
		t.Fatal("队首档整体先于普通步骤(含 high)")
	}
}
