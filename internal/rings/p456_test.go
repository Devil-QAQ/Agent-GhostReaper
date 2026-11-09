package rings

import (
	"context"
	"testing"
	"time"
)

// P4-56(问题清单 #77):四页签显示键推导层——机器状态 + 步骤现值 →
// 等待反证/反证中/反证暂停/证据不足/已废弃,悬停细分(重派轮次/换法/
// 等待主宰判定/任务完成中断)。与 arbiter.operatorKilled 同口径。

func flight(status string, mut ...func(*ClaimFlight)) *ClaimFlight {
	f := &ClaimFlight{Status: status}
	for _, m := range mut {
		m(f)
	}
	return f
}

func TestClaimDisplayMatrix(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		flight  *ClaimFlight
		pending string
		wantKey string
		wantHit string
	}{
		{"候选=等待反证", TestClaimCandidate, nil, "", ClaimDisplayWaitFalsify, ""},
		{"待下轮=等待反证", TestClaimAwaitingRound, nil, "", ClaimDisplayWaitFalsify, ""},
		{"反证步骤在跑=反证中", TestClaimTesting, flight("running"), "", ClaimDisplayFalsifying, ""},
		{"反证步骤排队=等待反证", TestClaimTesting, flight("dispatched"), "", ClaimDisplayWaitFalsify, ""},
		{"反证步骤待开始=等待反证", TestClaimTesting, flight("pending"), "", ClaimDisplayWaitFalsify, ""},
		{"等范围批准=反证暂停", TestClaimTesting, flight(StepScopeWait), "", ClaimDisplayFalsifyPaused, ""},
		{"模型受阻窗口=等待反证", TestClaimTesting, flight("blocked"), "", ClaimDisplayWaitFalsify, ""},
		{"操作者取消=等待主宰判定", TestClaimTesting, flight("cancelled",
			func(f *ClaimFlight) { f.OperatorCancelled = true }), "", ClaimDisplayWaitFalsify, ClaimHintRulingWait},
		{"舵杆杀单位=等待主宰判定", TestClaimTesting, flight("aborted",
			func(f *ClaimFlight) { f.SteerKilled = true }), "", ClaimDisplayWaitFalsify, ClaimHintRulingWait},
		{"系统中断窗口=等待反证无细分", TestClaimTesting, flight("aborted"), "", ClaimDisplayWaitFalsify, ""},
		{"重派在跑=反证中+第N轮", TestClaimTesting, flight("running",
			func(f *ClaimFlight) { f.Redispatch = 2 }), "", ClaimDisplayFalsifying, ClaimHintRedispatch},
		{"弹门拒绝换法=反证中+换法", TestClaimTesting, flight("running",
			func(f *ClaimFlight) { f.Redispatch = 1; f.RedispatchReason = "步骤终态: 审批拒绝终止" }),
			"", ClaimDisplayFalsifying, ClaimHintRedispatchAlt},
		{"重派排队=等待反证+第N轮", TestClaimTesting, flight("dispatched",
			func(f *ClaimFlight) { f.Redispatch = 1 }), "", ClaimDisplayWaitFalsify, ClaimHintRedispatch},
		{"挂起=证据不足", TestClaimPending, nil, "", ClaimDisplayInsufficient, ""},
		{"终局中断挂起=证据不足+任务完成中断", TestClaimPending, nil,
			"任务完成中断反证，可被唤醒", ClaimDisplayInsufficient, ClaimHintTaskDone},
		{"被推翻=已废弃", TestClaimRefuted, nil, "", ClaimDisplayDiscarded, ""},
		{"摘除连坐=已废弃", TestClaimDiscarded, nil, "", ClaimDisplayDiscarded, ""},
		{"确认=成果", TestClaimConfirmed, nil, "", ClaimDisplayConfirmed, ""},
		{"已修复=成果", TestClaimFixed, nil, "", ClaimDisplayFixed, ""},
		{"未知状态原样退回", "weird", nil, "", "weird", ""},
	}
	for _, c := range cases {
		key, hint := ClaimDisplay(c.status, c.flight, c.pending)
		if key != c.wantKey || hint != c.wantHit {
			t.Errorf("%s: ClaimDisplay = (%q,%q), want (%q,%q)", c.name, key, hint, c.wantKey, c.wantHit)
		}
	}
}

func TestHypDisplayMatrix(t *testing.T) {
	cases := []struct {
		name   string
		status string
		flight *HypFlight
		want   string
	}{
		{"候选=等待验证", HypothesisCandidate, nil, HypDisplayWaitVerify},
		{"验证在跑=验证中", HypothesisTesting, &HypFlight{Status: "running"}, HypDisplayVerifying},
		{"验证排队=等待验证", HypothesisTesting, &HypFlight{Status: "dispatched"}, HypDisplayWaitVerify},
		{"等范围批准=验证暂停", HypothesisTesting, &HypFlight{Status: StepScopeWait}, HypDisplayVerifyPaused},
		{"挂账=搁置", HypothesisHeld, nil, HypDisplayHeld},
		{"成立=迁入事实页", HypothesisConfirmed, nil, HypDisplayConfirmed},
		{"废弃=已废弃", HypothesisDiscarded, nil, HypDisplayDiscarded},
	}
	for _, c := range cases {
		if got := HypDisplay(c.status, c.flight); got != c.want {
			t.Errorf("%s: HypDisplay = %q, want %q", c.name, got, c.want)
		}
	}
}

func stepFor(id string, rev int64, status string, detail map[string]any) NodeFull {
	return NodeFull{NodeView: NodeView{ID: id, Status: status, CreatedRev: rev}, Detail: detail}
}

// TestClaimFlightOfSteps 当前轮取步与 arbiter.currentRound 同口径:轮次
// 号最大,并列取创建序最晚;redispatch/detail 键解析;无步骤 nil。
func TestClaimFlightOfSteps(t *testing.T) {
	d := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	if f := ClaimFlightOfSteps(nil, nil); f != nil {
		t.Fatalf("无步骤应 nil,得 %+v", f)
	}
	steps := []NodeFull{
		stepFor("s-r1", 10, "done", d("kind", "falsify", "round", 1.0)),
		stepFor("s-r2a", 20, "aborted", d("kind", "falsify", "round", 2.0)),
		// 同轮并列取创建序最晚(与 arbiter 一致:后创建者胜)。
		stepFor("s-r2b", 30, "running", d("kind", "falsify", "round", 2.0,
			"redispatch", 1.0, "redispatch_reason", "步骤终态: 审批拒绝终止")),
	}
	f := ClaimFlightOfSteps(steps, map[string]bool{"s-r2a": true})
	if f == nil || f.StepID != "s-r2b" || f.Status != "running" || f.Round != 2 {
		t.Fatalf("当前轮取步不符: %+v", f)
	}
	if f.Redispatch != 1 || f.RedispatchReason != "步骤终态: 审批拒绝终止" {
		t.Fatalf("重派详情解析不符: %+v", f)
	}
	if f.SteerKilled {
		t.Fatalf("s-r2b 未被舵杆终止,SteerKilled 应假")
	}
	f2 := ClaimFlightOfSteps(steps, map[string]bool{"s-r2b": true})
	if f2 == nil || !f2.SteerKilled {
		t.Fatalf("舵杆终止判定不符: %+v", f2)
	}
}

// TestSteerKilledStepsAndUnread 舵杆终止集合的 kind 定向查询(v13 索引)
// 与未读计数的事实侧扩容(假设成立迁入事实页同口径计数)。
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

	// 未读:confirmed 事实 1 + confirmed 假设 1 → facts 2;claims 0。
	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "f-1", NodeType: NodeFact, Title: "事实", Status: "confirmed"}},
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "h-1", NodeType: NodeHypothesis, Title: "假设", Status: "confirmed"}},
		Event{Kind: EventNodeAdded, Actor: "swarm:u1", Payload: NodeAdded{
			NodeID: "h-2", NodeType: NodeHypothesis, Title: "挂起假设", Status: HypothesisHeld}},
	)
	claims, facts, err := s.UnreadConfirms(ctx, task, now.Add(-time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("未读计数: %v", err)
	}
	if claims != 0 || facts != 2 {
		t.Fatalf("未读应 claims=0 facts=2(事实+成立假设),得 claims=%d facts=%d", claims, facts)
	}
}
