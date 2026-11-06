package manager

import (
	"context"
	"encoding/json"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

// ---- P4-43 任务终局清点(问题清单 #63) ----

// seedSweepGraph 铺垫终局残留形态(确定性):无主排队步骤(s1 可领取 /
// s3 等前置 s2)、在跑步骤 s2(中止记账归单位,清点不动)、判断四态各一
// (testing / candidate / confirmed / pending——清点只收 testing)。
// c1 带冻结标准与反证步骤绑定,与真实 testing 判断同构。
func seedSweepGraph(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	altList := []any{map[string]any{"id": "alt1", "label": "缓存污染"}}
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "排队中", Status: "dispatched"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s2", NodeType: rings.NodeStep, Title: "在跑", Status: "running"}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s3", NodeType: rings.NodeStep, Title: "等前置", Status: "dispatched"}},
		// s3 等前置 s2(s2 未 done → 等待中,同属无主排队)。
		{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDependsOn, FromNode: "s3", ToNode: "s2"}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "c1", NodeType: rings.NodeTestClaim, Title: "反证中判断", Status: rings.TestClaimTesting,
			Detail: map[string]any{"unit_id": "u1", "impact": "影响",
				"standard": arbiter.DefaultClaimStandard(), "alternatives": altList}}},
		{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "sf1", NodeType: rings.NodeStep, Title: "反证(第 1 轮)", Status: "dispatched",
			Detail: map[string]any{"kind": "falsify", "testclaim_id": "c1", "round": 1}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "c2", NodeType: rings.NodeTestClaim, Title: "候选判断", Status: rings.TestClaimCandidate,
			Detail: map[string]any{"unit_id": "u1", "standard": arbiter.DefaultClaimStandard()}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "c3", NodeType: rings.NodeTestClaim, Title: "已确认判断", Status: rings.TestClaimConfirmed,
			Detail: map[string]any{"unit_id": "u1", "standard": arbiter.DefaultClaimStandard()}}},
		{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: "c4", NodeType: rings.NodeTestClaim, Title: "既有待确认", Status: rings.TestClaimPending,
			Detail: map[string]any{"unit_id": "u1", "standard": arbiter.DefaultClaimStandard()}}},
	}
	if _, err := store.Append(context.Background(), task, events...); err != nil {
		t.Fatalf("铺垫失败: %v", err)
	}
}

// sweepAssertCommon 断言清点后的共同形态:无主排队步骤全部中止(留痕
// "任务收场")、testing 判断回挂起带理由(c1 的 falsify 步骤 sf1 同为
// dispatched 一并中止)、其余判断原态、在跑步骤不动。
func sweepAssertCommon(t *testing.T, store *rings.Store, task, wantReason string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"s1", "s3", "sf1"} {
		n, err := store.Node(ctx, task, id)
		if err != nil {
			t.Fatalf("查步骤 %s: %v", id, err)
		}
		if n.Status != "aborted" {
			t.Fatalf("无主排队步骤 %s 应中止(任务收场),现 %s", id, n.Status)
		}
	}
	if n, _ := store.Node(ctx, task, "s2"); n.Status != "running" {
		t.Fatalf("在跑步骤 s2 清点不动(中止记账归单位),现 %s", n.Status)
	}
	for _, id := range []string{"c2", "c3", "c4"} {
		n, err := store.Node(ctx, task, id)
		if err != nil {
			t.Fatalf("查判断 %s: %v", id, err)
		}
		switch id {
		case "c2":
			if n.Status != rings.TestClaimCandidate {
				t.Fatalf("候选判断不应被清点(仍锤炼中),现 %s", n.Status)
			}
		case "c3":
			if n.Status != rings.TestClaimConfirmed {
				t.Fatalf("已确认判断不动,现 %s", n.Status)
			}
		case "c4":
			if n.Status != rings.TestClaimPending {
				t.Fatalf("既有待确认不动,现 %s", n.Status)
			}
		}
	}
	c1, err := store.Node(ctx, task, "c1")
	if err != nil {
		t.Fatalf("查判断 c1: %v", err)
	}
	if c1.Status != rings.TestClaimPending {
		t.Fatalf("testing 判断应回挂起,现 %s", c1.Status)
	}
	digests, err := store.ClaimDigests(ctx, task)
	if err != nil {
		t.Fatalf("查判断卷宗: %v", err)
	}
	if got := digests["c1"].PendingReason; got != wantReason {
		t.Fatalf("c1 搁置理由不符: %q", got)
	}
	if got := digests["c4"].PendingReason; got != "" {
		t.Fatalf("既有待确认 c4 的旧理由不应被清点改写,现 %q", got)
	}
	// 留痕可查:中止事件带原因"任务收场",verdict 记录带中断理由。
	evs, err := store.Events(ctx, task, 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}
	abortedNotes := 0
	verdicts := 0
	for _, ev := range evs {
		switch ev.Kind {
		case rings.EventNodeStateChanged:
			if ev.Actor != "manager" {
				continue
			}
			var p rings.NodeStateChanged
			if json.Unmarshal(ev.Payload, &p) != nil || p.Status != "aborted" {
				continue
			}
			if p.Note != "任务收场" {
				t.Fatalf("中止事件应带原因\"任务收场\": %q", p.Note)
			}
			abortedNotes++
		case rings.EventTestClaimRecord:
			var r rings.TestClaimRecord
			if json.Unmarshal(ev.Payload, &r) != nil || r.ClaimID != "c1" || r.Type != rings.TestClaimRecVerdict {
				continue
			}
			verdicts++
			if r.Decision != rings.VerdictPending || r.PendingReason != wantReason {
				t.Fatalf("c1 收场裁决不符: %+v", r)
			}
			if len(r.Conditions) == 0 {
				t.Fatalf("verdict 必须逐条列依据条件(R2)")
			}
		}
	}
	if abortedNotes != 3 {
		t.Fatalf("应恰 3 个无主步骤中止留痕(s1/s3/sf1),现 %d", abortedNotes)
	}
	if verdicts != 1 {
		t.Fatalf("应恰 1 笔收场裁决(c1),现 %d", verdicts)
	}
}

// TestP443SweepOnStop 终止路径:熔断落 stopped 后紧邻清点——无主排队
// 步骤批量转中止(原因"任务收场")、testing 判断全部回挂起带理由;
// 事件序紧邻终态生命周期事件;幂等(重复清点零追加)。
func TestP443SweepOnStop(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	if _, err := mgr.CreateTask(context.Background(), "sw-1", "清点验证", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedSweepGraph(t, store, "sw-1")

	if err := mgr.StopBy("sw-1", map[string]any{"reason": "测试熔断"}); err != nil {
		t.Fatalf("熔断失败: %v", err)
	}
	sweepAssertCommon(t, store, "sw-1", "任务终止中断反证，可被唤醒")

	// 紧邻:终态生命周期事件的下一笔即清点事件。
	evs, err := store.Events(context.Background(), "sw-1", 0, 0)
	if err != nil {
		t.Fatalf("查事件流: %v", err)
	}
	var stoppedRev, firstSweepRev int64 = -1, -1
	for _, ev := range evs {
		if ev.Kind == rings.EventTaskLifecycle {
			var p rings.TaskLifecycle
			if json.Unmarshal(ev.Payload, &p) == nil && p.Action == StatusStopped {
				stoppedRev = ev.Revision
			}
			continue
		}
		if firstSweepRev < 0 && ev.Actor == "manager" &&
			(ev.Kind == rings.EventNodeStateChanged || ev.Kind == rings.EventTestClaimRecord) {
			firstSweepRev = ev.Revision
		}
	}
	if stoppedRev < 0 || firstSweepRev != stoppedRev+1 {
		t.Fatalf("清点应紧邻终态记账(终态 rev=%d,首笔清点 rev=%d)", stoppedRev, firstSweepRev)
	}

	// 幂等:重复终态不重复记账(stopBy 早退),直接重跑清点也零追加。
	if err := mgr.StopBy("sw-1", nil); err != nil {
		t.Fatalf("重复熔断应幂等: %v", err)
	}
	before := len(evs)
	if err := mgr.sweepAtTerminal("sw-1", StatusStopped); err != nil {
		t.Fatalf("重复清点失败: %v", err)
	}
	after, _ := store.Events(context.Background(), "sw-1", 0, 0)
	if len(after) != before {
		t.Fatalf("重复清点应零追加(前 %d 笔,后 %d 笔)", before, len(after))
	}
}

// TestP443SweepOnFinishSettling 完成路径:收尾时序终局判定(FinishSettling
// → completed)同样清点一例,理由按完成如实措辞。
func TestP443SweepOnFinishSettling(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	if _, err := mgr.CreateTask(context.Background(), "sw-2", "完成路径清点", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedSweepGraph(t, store, "sw-2")

	if err := mgr.MarkSettling("sw-2", nil); err != nil {
		t.Fatalf("进入收尾失败: %v", err)
	}
	if err := mgr.FinishSettling("sw-2", true, nil); err != nil {
		t.Fatalf("定终态失败: %v", err)
	}
	if st := mgr.Task("sw-2").Status(); st != StatusCompleted {
		t.Fatalf("应落 completed: %s", st)
	}
	sweepAssertCommon(t, store, "sw-2", "任务完成中断反证，可被唤醒")
}

// TestP443SweepNotOnNonTerminal 非终态一律不清:冬眠/暂停(在途状态保留
// 待唤醒续跑——P4-33 后冬眠任务同样无活 goroutine,勿误清);冬眠中熔断
// 落终态则照常清点。
func TestP443SweepNotOnNonTerminal(t *testing.T) {
	mgr, _, _, store := hiberTestEnv(t)
	if _, err := mgr.CreateTask(context.Background(), "sw-3", "非终态不清", rings.RoeUpdated{}, nil); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedSweepGraph(t, store, "sw-3")
	ctx := context.Background()
	stepsUntouched := func() {
		t.Helper()
		for _, id := range []string{"s1", "s3", "sf1"} {
			if n, err := store.Node(ctx, "sw-3", id); err != nil || n.Status != "dispatched" {
				t.Fatalf("非终态下 %s 应保持 dispatched: %v %+v", id, err, n)
			}
		}
		if n, _ := store.Node(ctx, "sw-3", "c1"); n.Status != rings.TestClaimTesting {
			t.Fatalf("非终态下 c1 应保持 testing: %s", n.Status)
		}
	}

	if err := mgr.Hibernate("sw-3", nil); err != nil {
		t.Fatalf("冬眠失败: %v", err)
	}
	stepsUntouched()
	if err := mgr.Wake("sw-3", nil); err != nil {
		t.Fatalf("唤醒失败: %v", err)
	}
	if err := mgr.Pause("sw-3"); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	stepsUntouched()

	// 冬眠中熔断 → 落终态照常清点(区别于冬眠本身)。
	if err := mgr.Resume("sw-3"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if err := mgr.Hibernate("sw-3", nil); err != nil {
		t.Fatalf("再冬眠失败: %v", err)
	}
	if err := mgr.StopBy("sw-3", nil); err != nil {
		t.Fatalf("冬眠中熔断失败: %v", err)
	}
	sweepAssertCommon(t, store, "sw-3", "任务终止中断反证，可被唤醒")
}
