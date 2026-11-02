package overmind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueAdmissionConcurrentAndExemptions(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const task = "queue-admission"
	// 机制测试与默认值解耦:显式设 6(默认随 P4-29 调优为 8),
	// 断言数字钉在显式值上,默认值另有断言。
	if err := s.SetSetting(ctx, SettingKeyQueueLimit, "6", "creator"); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := DispatchStep(ctx, s, task, fmt.Sprintf("s%d", i), "普通探索", DispatchOptions{ActionLevel: "只读探测", Priority: PriorityHigh, Detail: map[string]any{"urgent": true, "kind": "falsify"}})
			if err == nil {
				accepted.Add(1)
				return
			}
			var full *QueueFullError
			if !errors.As(err, &full) {
				t.Errorf("异常拒收: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 6 {
		t.Fatalf("并发写前准入应恰收6，实际%d", accepted.Load())
	}
	steps, _ := s.NodesByType(ctx, task, rings.NodeStep, "")
	for _, step := range steps {
		if protectedStep(step) {
			t.Fatal("普通探索伪造豁免标记")
		}
	}
	for i, source := range []dispatchSource{sourceFalsify, sourceVerify, sourceUrgent} {
		if err := DispatchStep(ctx, s, task, fmt.Sprintf("exempt%d", i), "受控豁免", DispatchOptions{source: source, ActionLevel: "只读探测"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := countNodes(t, s, task, rings.NodeStep, ""); got != 9 {
		t.Fatalf("满队豁免应收3，实际%d", got)
	}
	if got := countNodes(t, s, "other-task", rings.NodeStep, ""); got != 0 {
		t.Fatal("跨任务串队")
	}
}

func TestQueueWaitingReservesCapacity(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const task = "queue-waiting"
	if err := s.SetSetting(ctx, SettingKeyQueueLimit, "3", "creator"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		opt := DispatchOptions{ActionLevel: "只读探测"}
		if i > 1 {
			opt.DependsOn = []string{"s1"}
		}
		if err := DispatchStep(ctx, s, task, fmt.Sprintf("s%d", i), "等待前置", opt); err != nil {
			t.Fatal(err)
		}
	}
	_, steps, edges, err := queueView(ctx, s, task)
	if err != nil {
		t.Fatal(err)
	}
	w := QueueWatermarkOf(steps, edges, QueueLimit(ctx, s))
	if w.Running != 0 || w.Queued != 1 || w.Waiting != 2 || w.Available != 0 {
		t.Fatalf("等待水位不符: %+v", w)
	}
	err = DispatchStep(ctx, s, task, "overflow", "不可增长", DispatchOptions{ActionLevel: "只读探测"})
	var full *QueueFullError
	if !errors.As(err, &full) {
		t.Fatalf("预留容量未拒收: %v", err)
	}
	_, err = s.Append(ctx, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1", Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	_, steps, edges, err = queueView(ctx, s, task)
	if err != nil {
		t.Fatal(err)
	}
	w = QueueWatermarkOf(steps, edges, 3)
	if w.Queued != 2 || w.Waiting != 0 || w.Available != 1 {
		t.Fatalf("前置完成后水位不符: %+v", w)
	}
}

func TestQueueCancellationAuditDependenciesAndProtection(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const task = "queue-cancel"
	_, err := s.Append(ctx, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u0", Payload: rings.NodeAdded{NodeID: "f1", NodeType: rings.NodeFact, Status: "confirmed", Title: "新证据"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		opt := DispatchOptions{ActionLevel: "只读探测"}
		if i > 1 {
			opt.DependsOn = []string{fmt.Sprintf("s%d", i-1)}
		}
		if err := DispatchStep(ctx, s, task, fmt.Sprintf("s%d", i), "过时探索", opt); err != nil {
			t.Fatal(err)
		}
	}
	l := &Loop{TaskID: task, Store: s}
	rev, _ := s.CurrentRevision(ctx, task)
	p := cancelPlan{StepID: "s1", Reason: "新证据已覆盖该路径，取消重复探索", Evidence: []string{"f1"}}
	if err := l.cancelQueued(ctx, p, rev); err != nil {
		t.Fatal(err)
	}
	if countNodes(t, s, task, rings.NodeStep, "cancelled") != 3 {
		t.Fatal("依赖后继未同批取消")
	}
	evs, _ := s.Events(ctx, task, rev, 0)
	if len(evs) != 3 {
		t.Fatalf("应保留三条取消轨迹: %d", len(evs))
	}
	for _, ev := range evs {
		var p rings.NodeStateChanged
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Note == "" || len(p.Evidence) != 1 {
			t.Fatalf("取消缺审计依据: %s", ev.Payload)
		}
	}
	if err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := s.Node(ctx, task, "s3")
	if err != nil || n.Detail["cancellation_reason"] == nil {
		t.Fatalf("重放丢取消原因: %+v %v", n, err)
	}
	// 被反证依赖的普通前置不可取消。
	if err := DispatchStep(ctx, s, task, "s4", "关键前置", DispatchOptions{ActionLevel: "只读探测"}); err != nil {
		t.Fatal(err)
	}
	if err := DispatchStep(ctx, s, task, "s5", "反证", DispatchOptions{source: sourceFalsify, ActionLevel: "只读探测", DependsOn: []string{"s4"}, Detail: map[string]any{"kind": "falsify"}}); err != nil {
		t.Fatal(err)
	}
	rev, _ = s.CurrentRevision(ctx, task)
	p.StepID = "s4"
	if err := l.cancelQueued(ctx, p, rev); err == nil {
		t.Fatal("取消了反证的关键前置")
	}
	n, _ = s.Node(ctx, task, "s4")
	if n.Status != "dispatched" {
		t.Fatal("保护失败")
	}
	// 挑选到但尚未异步领取的步骤同样受保护。
	l.launching = map[string]struct{}{"s4": {}}
	if err := l.cancelQueued(ctx, p, rev); err == nil {
		t.Fatal("取消了已挑选步骤")
	}
	if err := l.cancelQueued(ctx, cancelPlan{StepID: "s4", Reason: "无引用"}, rev); err == nil {
		t.Fatal("无证据取消获准")
	}
}

func TestQueueNoSelfPlanningAndNoFaultOnCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := openStore(t)
	const task = "queue-no-self"
	// 机制测试与默认值解耦:显式并发 3 / 派发上限 6(默认随 P4-29
	// 调优为 5/8),断言与快照文案钉在显式值上。
	if err := s.SetSetting(ctx, SettingKeyQueueLimit, "6", "creator"); err != nil {
		t.Fatal(err)
	}
	model := newGatedScriptedModel(func(rr *recordedRequest) scriptedReply {
		if len(rr.ToolsOffered) == 0 {
			if !strings.Contains(rr.LastUser, "步骤:\n  (无)") {
				return scriptedReply{Text: `{"assessment":{"note":"等待新结果"},"new_intents":[]}`, Finish: "stop"}
			}
			var plans []string
			for i := 0; i < 10; i++ {
				plans = append(plans, fmt.Sprintf(`{"intent":"探索第%d条独立路径","action_level":"只读探测"}`, i))
			}
			return scriptedReply{Text: `{"assessment":{"note":"分批探索"},"new_intents":[` + strings.Join(plans, ",") + `]}`, Finish: "stop"}
		}
		return scriptedReply{Text: "完成", Finish: "stop"}
	})
	t.Cleanup(model.Close)
	prov, err := model.provider()
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{TaskID: task, Store: s, Provider: prov, Swarm: swarm.NewExecutor(s, prov, t.TempDir(), swarm.Budget{}), HeartbeatInterval: 40 * time.Millisecond, MaxConcurrentUnits: 3}
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { l.Run(ctx, wake); close(done) }()
	wake <- struct{}{}
	waitFor(t, 10*time.Second, "首轮满队且三单位在跑", func() bool { return countNodes(t, s, task, rings.NodeStep, "running") == 3 })
	time.Sleep(200 * time.Millisecond)
	if got := len(model.decomposeRequests()); got != 1 {
		t.Fatalf("派发、领取、心跳不能自我规划，实际%d轮", got)
	}
	if countNodes(t, s, task, rings.NodeStep, "") != 6 {
		t.Fatal("普通探索超限")
	}
	if l.LastErr() != nil || l.llmFails != 0 {
		t.Fatalf("满队被当作故障: %v", l.LastErr())
	}
	snap, _, err := l.snapshot(ctx)
	if err != nil || !strings.Contains(snap, "执行中 3 / 排队 3 / 等前置 0 / 本轮还可新增 0") || !strings.Contains(snap, "派发已满") {
		t.Fatalf("快照缺水位/拒因: %s %v", snap, err)
	}
	// 急活满队准入，并构成新指令；没有单位结果也允许规划一次。
	if _, err := UrgentDispatch(ctx, s, task, "操作者急活", "观察", "只读探测", nil); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	waitFor(t, 5*time.Second, "新指令触发规划", func() bool { return len(model.decomposeRequests()) == 2 })
	time.Sleep(120 * time.Millisecond)
	if len(model.decomposeRequests()) != 2 {
		t.Fatal("新指令被重复消费")
	}
	// 正式新增目标入口由主宰记账，仍是操作者增量，不能被自事件过滤掉。
	if err := SeedGoal(ctx, s, task, "g-new", "新增检查目标", nil); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	waitFor(t, 5*time.Second, "新增目标触发规划", func() bool { return len(model.decomposeRequests()) == 3 })
	time.Sleep(120 * time.Millisecond)
	if len(model.decomposeRequests()) != 3 {
		t.Fatal("新增目标被重复消费")
	}
	cancel()
	model.release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("循环未退出")
	}
	evs, _ := s.Events(context.Background(), task, 0, 0)
	plans := 0
	for _, ev := range evs {
		if ev.Kind != rings.EventOvermindRecord {
			continue
		}
		var p rings.OvermindRecord
		_ = json.Unmarshal(ev.Payload, &p)
		if p.Type != rings.OvermindRecPlanning {
			continue
		}
		plans++
		if p.Queue == nil || (plans == 2 && (p.Bootstrap || len(p.TriggerRevisions) == 0)) {
			t.Fatalf("规划缺触发依据: %+v", p)
		}
	}
	if plans != 3 {
		t.Fatalf("规划账本次数%d", plans)
	}
}
