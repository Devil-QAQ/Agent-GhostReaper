package rings

import (
	"context"
	"testing"
	"time"
)

// (四页签显示键推导层测试已随推导层退役删除,P5-03③;本文件保留
// 舵杆终止集合与未读计数的 v13 索引测试。)

func TestSteerKilledStepsAndUnread(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "p456-rings"
	now := time.Now().UTC()
	mustAppend(t, s, task,
		Event{Kind: EventTaskLifecycle, Actor: "test", Payload: TaskLifecycle{Action: "created"}},
		Event{Kind: EventSteerRecord, Actor: "creator", Payload: SteerRecord{
			SteerID: "st-1", StepID: "s-1", UnitID: "u1", Command: SteerCommandKill,
			State: SteerStateDelivered, Reason: "演练"}},
		Event{Kind: EventSteerRecord, Actor: "creator", Payload: SteerRecord{
			SteerID: "st-2", StepID: "s-2", UnitID: "u2", Command: SteerCommandSteer,
			State: SteerStateDelivered, Message: "换个方向"}}, // 非终止指令不进集合
		Event{Kind: EventSteerRecord, Actor: "creator", Payload: SteerRecord{
			SteerID: "st-3", StepID: "s-3", UnitID: "u3", Command: SteerCommandKill,
			State: SteerStateQueued}}, // 未送达不进集合
		// 干扰事件:同任务其他 kind 大量存在也不影响 kind 定向查询。
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "s-1", NodeType: NodeStep, Title: "步骤", Status: "aborted"}},
	)
	kills := s.SteerKilledSteps(ctx, task)
	if len(kills) != 1 || !kills["s-1"] {
		t.Fatalf("舵杆终止集合应只含已送达的 kill(s-1),得 %v", kills)
	}
	if kills := s.SteerKilledSteps(ctx, "t-none"); len(kills) != 0 {
		t.Fatalf("未知任务应空集合,得 %v", kills)
	}

	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "f-1", NodeType: NodeFact, Title: "事实", Status: "confirmed"}},
	)
	intents, claims, facts, err := s.UnreadFindings(ctx, task, now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("未读计数: %v", err)
	}
	if intents != 1 || claims != 0 || facts != 1 {
		t.Fatalf("未读应 intents=1(在页步骤) claims=0 facts=1(已确认事实;假设不计,P5-07①),得 %d/%d/%d", intents, claims, facts)
	}
}
