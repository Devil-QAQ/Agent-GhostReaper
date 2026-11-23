package overmind

import (
	"context"
	"testing"

	"ghostreaper/internal/arbiter"
	"ghostreaper/internal/rings"
)

// ---- P4-55(问题清单 #75)收割与循环侧:中断必重派、重派有终点、
// 操作者取消免除重派(P4-54 衔接)、不成立可重开 ----

// p455Claim 铺一张 testing 声明的最小图:发现步骤(done)+ 资产 + 声明
// (完整反证标准)+ 血缘 + 锚点;falsifyStep 按(状态,轮次)另种。
func p455Claim(t *testing.T, store *rings.Store, task, claimID string) {
	t.Helper()
	ctx := context.Background()
	p454Task(t, store, task)
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "sd-" + claimID, NodeType: rings.NodeStep, Title: "发现步骤", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-" + claimID, NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: claimID, NodeType: rings.NodeTestClaim, Title: "判断 " + claimID,
			Status: rings.TestClaimCandidate,
			Detail: map[string]any{
				"unit_id":      "u1",
				"standard":     arbiter.DefaultClaimStandard(),
				"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存假象"}},
			}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: claimID, ToNode: "a-" + claimID}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeDerivedFrom, FromNode: claimID, ToNode: "sd-" + claimID}},
	); err != nil {
		t.Fatal(err)
	}
}

// p455FalsifyStep 种一轮反证步骤(状态/轮次/可选目标归属)并置声明
// testing。
func p455FalsifyStep(t *testing.T, store *rings.Store, task, claimID, stepID, status string, round int, goalID ...string) {
	t.Helper()
	g := ""
	if len(goalID) > 0 {
		g = goalID[0]
	}
	p454Step(t, store, task, stepID, "反证(第 1 轮)", status, g,
		map[string]any{"kind": "falsify", "testclaim_id": claimID, "round": round,
			"alternatives": []any{map[string]any{"id": "alt1", "label": "缓存假象"}}})
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeStateChanged, Actor: ActorOvermind,
		Payload: rings.NodeStateChanged{NodeID: claimID, Status: "testing"},
	}); err != nil {
		t.Fatal(err)
	}
}

// p455StepsOf 取声明名下反证步骤。
func p455StepsOf(t *testing.T, store *rings.Store, task, claimID string) []rings.NodeFull {
	t.Helper()
	steps, err := store.NodesByType(context.Background(), task, rings.NodeStep, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []rings.NodeFull
	for _, s := range steps {
		if id, _ := s.Detail["testclaim_id"].(string); id == claimID {
			out = append(out, s)
		}
	}
	return out
}

// (TestP455InterruptedRedispatched 已随中断自动重派机制退役删除,
// P5-03③——重派链整体删除,收割侧无声明派发工作,零重派为结构事实。)

// TestP455RedispatchExhaustedFallsPending ③+循环侧:重派满 3 次仍无结论
// → 收割不再派,judgeClaims 判待确认且理由逐字"反证多轮未取得结论";

// TestP455JudgeSkipsInterrupted ①循环侧:预算未尽的中断声明 judgeClaims

// TestP455RefutedReopenable ②+重开:不成立终态可经 ReopenClaim 重开回
// 反证中(P5-01⑤:只落 reopen 记录与状态迁移,不再派重开反证步骤)。
func TestP455RefutedReopenable(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p455-reopen"
	p455Claim(t, store, task, "tc-r")
	p455FalsifyStep(t, store, task, "tc-r", "sf-1", "done", 1)
	// 仲裁判不成立(P5-01③:推翻维度字段已删,不成立由裁决事件直接落态)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: arbiter.ActorArbiter,
			Payload: rings.NodeStateChanged{NodeID: "tc-r", Status: rings.TestClaimRefuted}},
	); err != nil {
		t.Fatal(err)
	}
	stepsBefore := len(p455StepsOf(t, store, task, "tc-r"))
	if err := ReopenClaim(ctx, store, task, "tc-r", rings.ReopenNewEvidence, "新证据:推翻依据的样本已失效", "creator"); err != nil {
		t.Fatalf("不成立应可重开: %v", err)
	}
	claim, _ := store.Node(ctx, task, "tc-r")
	if claim.Status != rings.TestClaimCandidate {
		t.Fatalf("重开应回候选(等新交卷重验): %s", claim.Status)
	}
	if steps := p455StepsOf(t, store, task, "tc-r"); len(steps) != stepsBefore {
		t.Fatalf("重开派的是重验步骤(retake),不是反证步骤: 前 %d 后 %d", stepsBefore, len(steps))
	}
}

// (TestP455UnitKillZeroStateChangeAwaitRuling 已随立判断链(claimruling)
// 退役删除,P5-03①——杀单位后声明零程序改态、待裁决区与裁决两向
// 机制整体不存在;存量 testing 声明停原状态属升级删除路线。)

// (TestP455DeleteGoalStillNeededContinues 已随立判断链退役删除,
// P5-03①——删除目标不再交主宰裁决声明去留,声明状态程序不动。)

// (TestP455StopDirectionRunningFalsifySuspends 已随停向挂起机制退役
// 删除,P5-03①——停向不牵动声明状态,停聚焦语义由 p454_test 的
// FocusGoalStopsOthers 覆盖。)
