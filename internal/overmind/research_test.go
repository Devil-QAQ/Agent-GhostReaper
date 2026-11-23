package overmind

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
)

func TestDispatchPriorityAndDepends(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "t-dispatch"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "web1", NodeType: rings.NodeAsset, Title: "http://mock", Status: "active", AssetClass: rings.AssetService}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "s1", NodeType: rings.NodeStep, Title: "前置意图", Status: "done",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: "g1", NodeType: rings.NodeGoal, Title: "目标", Status: "active"}},
	); err != nil {
		t.Fatalf("铺底失败: %v", err)
	}

	// 非法优先级拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", Priority: "urgent"}); err == nil {
		t.Fatalf("非法优先级应拒派")
	}
	// 依赖自身拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", DependsOn: []string{"sx"}}); err == nil {
		t.Fatalf("依赖自身应拒派")
	}
	// 幽灵前置拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", DependsOn: []string{"s9"}}); err == nil {
		t.Fatalf("幽灵前置应拒派")
	}
	// 依赖非 Step 节点拒绝。
	if err := DispatchStep(ctx, store, task, "sx", "意图", DispatchOptions{
		ActionLevel: "只读探测", DependsOn: []string{"web1"}}); err == nil {
		t.Fatalf("依赖非 Step 节点应拒派")
	}

	// 合法派发:高优先级 + 依赖既有步骤。
	if err := DispatchStep(ctx, store, task, "s2", "后续意图", DispatchOptions{
		GoalID: "g1", ActionLevel: "只读探测", Priority: "high", DependsOn: []string{"s1"}}); err != nil {
		t.Fatalf("合法派发失败: %v", err)
	}
	node, err := store.Node(ctx, task, "s2")
	if err != nil {
		t.Fatalf("查步骤: %v", err)
	}
	if p, _ := node.Detail["priority"].(string); p != "high" {
		t.Fatalf("优先级应落 detail: %+v", node.Detail)
	}
	deps, _ := store.Edges(ctx, task, rings.EdgeDependsOn)
	found := false
	for _, e := range deps {
		if e.From == "s2" && e.To == "s1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("depends_on 边应落账: %v", deps)
	}

	// normal 缺省不落(快照与 detail 的降噪口径:normal 是常态)。
	if err := DispatchStep(ctx, store, task, "s3", "常规意图", DispatchOptions{
		ActionLevel: "只读探测"}); err != nil {
		t.Fatalf("常规派发失败: %v", err)
	}
	n3, _ := store.Node(ctx, task, "s3")
	if _, ok := n3.Detail["priority"]; ok {
		t.Fatalf("normal 优先级不应落 detail: %+v", n3.Detail)
	}
}

// TestSelectRunnable 派发策略的确定性选步:依赖闸(前置 done 才可执行,
// 前置 running/failed/不存在均等待)+ 优先级排序(high > normal > low,
// 同级创建序)。
func TestSelectRunnable(t *testing.T) {
	node := func(id, status string, pri string, rev int64) rings.NodeFull {
		detail := map[string]any{}
		if pri != "" {
			detail["priority"] = pri
		}
		return rings.NodeFull{NodeView: rings.NodeView{ID: id, Status: status, CreatedRev: rev}, Detail: detail}
	}
	dispatched := []rings.NodeFull{
		node("slow", "dispatched", "low", 1),
		node("norm", "dispatched", "", 2),
		node("quick", "dispatched", "high", 3),
		node("waiter", "dispatched", "high", 4), // 依赖 running 前置 → 等待
		node("follow", "dispatched", "low", 5),  // 依赖 done 前置 → 可执行
	}
	all := append(append([]rings.NodeFull{}, dispatched...),
		node("front-run", "running", "", 6),
		node("front-done", "done", "", 7),
	)
	deps := []rings.EdgeView{
		{Type: rings.EdgeDependsOn, From: "waiter", To: "front-run"},
		{Type: rings.EdgeDependsOn, From: "follow", To: "front-done"},
		{Type: rings.EdgeDependsOn, From: "follow", To: "ghost"}, // 前置不存在 → 等待
	}
	got := selectRunnable(dispatched, all, deps)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	// quick(high) 先于 norm;slow(low) 最后;waiter/follow 等待不选。
	if len(ids) != 3 || ids[0] != "quick" || ids[1] != "norm" || ids[2] != "slow" {
		t.Fatalf("选步不符: %v", ids)
	}
}
