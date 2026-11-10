package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-48(问题清单 #67):操作者深入指令 ----
// ① 深入指令是有效规划触发(硬承诺),触发标记驱动急活来源;
// ② 快照注入锚定节点完整内容与血缘(事实与发现两形态),触发行在变化集;
// ③ 深入轮派生意图按急活来源落账:队列准入豁免、取消保护,审批门照走
//    (分级/危害声明校验不豁免)。

// p448Task 建任务 + 种子资产 + 一个已确认事实(带产出步骤与资产锚点)
// + 一条已确认发现(带发现步骤血缘与资产锚点)。
func p448Task(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	append := func(evs ...rings.Event) {
		t.Helper()
		if _, err := store.Append(ctx, task, evs...); err != nil {
			t.Fatalf("铺垫事件失败: %v", err)
		}
	}
	append(
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator",
			Payload: rings.TaskLifecycle{Action: "created", Detail: map[string]any{"title": "深入验证"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a1", NodeType: rings.NodeAsset, Title: "http://mock.local", Status: "active",
			AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "对 mock.local 做登录口令尝试", Status: "done",
			Detail: map[string]any{"action_level": "主动测试"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "f-weak", NodeType: rings.NodeFact, Title: "admin 只试了三个默认口令就放弃,登录口未锁",
			Status: "confirmed",
			Detail: map[string]any{"evidence": "http_probe 实测 3 次尝试", "unit_id": "u1"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: "f-weak"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "f-weak", ToNode: "a1"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u2", Payload: rings.NodeAdded{
			NodeID: "tc-sql", NodeType: rings.NodeTestClaim, Title: "登录接口存在 SQL 注入",
			Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u2", "impact": "可读全部用户表", "vulnclass": "SQL注入"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u2",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDerivedFrom, FromNode: "tc-sql", ToNode: "s1"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u2",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeAbout, FromNode: "tc-sql", ToNode: "a1"}},
	)
}

// TestOperatorDiveTriggersPlanning 深入指令是有效规划触发并带急活标记。
func TestOperatorDiveTriggersPlanning(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p448-trigger"
	p448Task(t, store, task)

	l := &Loop{Store: store, TaskID: task}
	cur, err := store.CurrentRevision(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	l.lastPlanRev = cur
	revs, dive, err := l.planningTriggers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 0 || dive {
		t.Fatalf("无深入指令时不应触发: %v dive=%v", revs, dive)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventOperatorDive, Actor: "creator",
		Payload: rings.OperatorDive{NodeID: "f-weak", Direction: "继续暴力破解"},
	}); err != nil {
		t.Fatal(err)
	}
	revs, dive, err = l.planningTriggers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) == 0 || !dive {
		t.Fatalf("深入指令应触发规划并带急活标记: %v dive=%v", revs, dive)
	}
}

// TestOperatorDiveSnapshotInjection 快照注入:触发行(变化集)+「操作者
// 深入指令」区锚定节点完整内容与血缘——事实形态(内容/证据/单位/产出
// 步骤/资产锚点)与发现形态(判断/影响面/类别/发现步骤/资产锚点),
// 方向留空如实呈"由你自行判断"。
func TestOperatorDiveSnapshotInjection(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p448-snapshot"
	p448Task(t, store, task)

	l := &Loop{Store: store, TaskID: task}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventOperatorDive, Actor: "creator",
			Payload: rings.OperatorDive{NodeID: "f-weak", Direction: "继续暴力破解"}},
		rings.Event{Kind: rings.EventOperatorDive, Actor: "creator",
			Payload: rings.OperatorDive{NodeID: "tc-sql"}},
	); err != nil {
		t.Fatal(err)
	}
	text, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"操作者深入指令:节点 f-weak(方向:继续暴力破解)",
		"操作者深入指令:节点 tc-sql(方向留空,由你自行判断)",
		"操作者深入指令(造物主从该节点出发的推动;本轮必须逐条处理)",
		"锚定事实 f-weak [confirmed]",
		"内容:admin 只试了三个默认口令就放弃,登录口未锁",
		"证据:http_probe 实测 3 次尝试",
		"发现单位:u1",
		"产出步骤:s1 对 mock.local 做登录口令尝试",
		"资产锚点:a1 http://mock.local",
		"锚定发现 tc-sql [confirmed]",
		"判断:登录接口存在 SQL 注入",
		"影响面:可读全部用户表",
		"类别:SQL注入",
		"发现步骤:s1 对 mock.local 做登录口令尝试",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("快照缺 %q:\n%s", want, text)
		}
	}
	// 已被研判消费的指令不再注入(自上轮研判以来的窗口)。
	cur, err := store.CurrentRevision(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventOvermindRecord, Actor: ActorOvermind,
		Payload: rings.OvermindRecord{Type: rings.OvermindRecAssessment, EvaluatedRev: cur, QAccess: "无"}}); err != nil {
		t.Fatal(err)
	}
	text2, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text2, "锚定事实 f-weak") || strings.Contains(text2, "锚定发现 tc-sql") {
		t.Fatalf("上轮研判已消费的指令不应再注入快照区")
	}
}

// TestOperatorDiveDispatchExemption 深入轮派生意图按急活来源落账:
// 队列满时普通探索拒收、深入轮照派(背压豁免),urgent 标记落 detail
// (受控来源),取消保护生效。
func TestOperatorDiveDispatchExemption(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p448-dispatch"
	p448Task(t, store, task)
	if err := store.SetSetting(ctx, SettingKeyQueueLimit, "1", "creator"); err != nil {
		t.Fatal(err)
	}

	l := &Loop{Store: store, TaskID: task}
	plan := IntentPlan{Intent: "普通探索", ActionLevel: "只读探测"}
	if _, err := l.dispatch(ctx, plan, sourceExplore); err != nil {
		t.Fatalf("首个普通探索应可派: %v", err)
	}
	if _, err := l.dispatch(ctx, plan, sourceExplore); err == nil {
		t.Fatal("队列满时普通探索应拒收")
	}
	// 深入轮(sourceUrgent 受控来源):满队照派,豁免标记入账。
	if _, err := l.dispatch(ctx, IntentPlan{Intent: "从 f-weak 继续深入", ActionLevel: "主动测试"}, sourceUrgent); err != nil {
		t.Fatalf("深入轮派发应豁免背压: %v", err)
	}
	steps, err := store.NodesByType(ctx, task, rings.NodeStep, "")
	if err != nil {
		t.Fatal(err)
	}
	var urgent int
	for _, s := range steps {
		if u, _ := s.Detail["urgent"].(bool); u {
			urgent++
			if by, _ := s.Detail["by"].(string); by != "dive" {
				t.Fatalf("急活来源标记应记 by=dive,实际 %v", s.Detail["by"])
			}
			if !protectedStep(s) {
				t.Fatal("深入派生步骤应受取消保护")
			}
		}
	}
	if urgent != 1 {
		t.Fatalf("深入轮应恰一条急活步骤,实际 %d", urgent)
	}
}

// TestDiveRevisionsBetween 竞态补正窗口:(from, to] 半开区间,窗口外
// 与非深入事件不计。
func TestDiveRevisionsBetween(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p448-window"
	p448Task(t, store, task)
	revs, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventOperatorDive, Actor: "creator", Payload: rings.OperatorDive{NodeID: "f-weak"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator", Payload: rings.RoeUpdated{}},
		rings.Event{Kind: rings.EventOperatorDive, Actor: "creator", Payload: rings.OperatorDive{NodeID: "tc-sql"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	d1, other, d2 := revs[0], revs[1], revs[2]
	got := diveRevisionsBetween(ctx, store, task, d1-1, d2)
	if len(got) != 2 || got[0] != d1 || got[1] != d2 {
		t.Fatalf("窗口应含两条深入指令 %v(中间事件 %d 不计)", got, other)
	}
	if got := diveRevisionsBetween(ctx, store, task, d1, d2); len(got) != 1 || got[0] != d2 {
		t.Fatalf("from 应排除自身: %v", got)
	}
	if got := diveRevisionsBetween(ctx, store, task, d2, d2); got != nil {
		t.Fatalf("空窗口应返回 nil: %v", got)
	}
}
