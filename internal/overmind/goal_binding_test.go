package overmind

import (
	"context"
	"errors"
	"testing"

	"ghostreaper/internal/rings"
)

// The goal is metadata selected from the actual planning snapshot. It must
// neither silently become the newest goal nor introduce execution dependencies.
func TestPlanningGoalBinding(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	const task = "goal-binding"
	mkTask(t, store, task)
	for _, id := range []string{"g1", "g2", "g3"} {
		p454Goal(t, store, task, id, id, nil)
	}
	l := &Loop{TaskID: task, Store: store}
	reply, err := parseIntents(`{"new_intents":[{"goal_id":"g1","intent":"核对第一份材料","action_level":"只读探测"},{"goal_id":"g2","intent":"核对第二份材料","action_level":"只读探测"},{"goal_id":"g3","intent":"比对两份已得材料","action_level":"只读探测"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range reply.NewIntents {
		id, err := l.dispatch(ctx, p, sourceExplore)
		if err != nil {
			t.Fatal(err)
		}
		n, err := store.Node(ctx, task, id)
		if err != nil || n.Detail["goal_id"] != p.GoalID {
			t.Fatalf("plan %d lost its actual goal: %+v, %v", i, n, err)
		}
	}
	deps, err := store.Edges(ctx, task, rings.EdgeDependsOn)
	if err != nil || len(deps) != 0 {
		t.Fatalf("goal binding created execution dependencies: %+v, %v", deps, err)
	}
	for _, goal := range []string{"", "missing", "s1"} {
		_, err := l.dispatch(ctx, IntentPlan{GoalID: goal, Intent: "不得错误归属", ActionLevel: "只读探测"}, sourceExplore)
		if err == nil {
			t.Fatalf("invalid/ambiguous goal %q accepted", goal)
		}
	}
	if got := countNodes(t, store, task, rings.NodeStep, ""); got != 3 {
		t.Fatalf("rejected plans changed the graph: %d steps", got)
	}
	if _, err := DeleteGoal(ctx, store, nil, task, "g3", "测试删除", "creator"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.dispatch(ctx, IntentPlan{GoalID: "g3", Intent: "已删除目标", ActionLevel: "只读探测"}, sourceExplore); !errors.Is(err, ErrGoalDiscarded) {
		t.Fatalf("deleted goal must retain its typed rejection: %v", err)
	}
}

func TestPlanningGoalFallbackAndFocus(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	const task = "goal-fallback"
	mkTask(t, store, task)
	p454Goal(t, store, task, "g1", "唯一目标", nil)
	l := &Loop{TaskID: task, Store: store}
	p := IntentPlan{Intent: "单目标的普通意图", ActionLevel: "只读探测"}
	id, err := l.dispatch(ctx, p, sourceExplore)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := store.Node(ctx, task, id)
	if n.Detail["goal_id"] != "g1" {
		t.Fatalf("single goal lost: %+v", n)
	}
	p454Goal(t, store, task, "g2", "其他目标", nil)
	if _, err := FocusGoal(ctx, store, nil, task, "g1", "creator"); err != nil {
		t.Fatal(err)
	}
	p.Intent = "聚焦方向普通意图"
	id, err = l.dispatch(ctx, p, sourceExplore)
	if err != nil {
		t.Fatal(err)
	}
	n, _ = store.Node(ctx, task, id)
	if n.Detail["goal_id"] != "g1" {
		t.Fatalf("focus lost: %+v", n)
	}
	p.GoalID, p.Intent = "g2", "聚焦之外"
	if _, err := l.dispatch(ctx, p, sourceExplore); !errors.Is(err, ErrFocusOutside) {
		t.Fatalf("explicit goal bypassed focus: %v", err)
	}
	if _, err := l.dispatch(ctx, p, sourceUrgent); err != nil {
		t.Fatalf("creator's urgent instruction lost its existing exception: %v", err)
	}
}

func TestPlanningCannotDispatchAfterGoalAchieved(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	const task = "goal-late-planning"
	mkTask(t, store, task)
	p454Goal(t, store, task, "g1", "已核对的目标", nil)
	loop := &Loop{TaskID: task, Store: store}
	if _, err := store.Append(ctx, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: "g1", Status: GoalStatusAchieved, Note: "验收已核对实际证据"}}); err != nil {
		t.Fatal(err)
	}
	for _, goal := range []string{"g1", ""} {
		if _, err := loop.dispatch(ctx, IntentPlan{GoalID: goal, Intent: "来自标记达成前的旧规划", ActionLevel: "只读探测"}, sourceExplore); err == nil {
			t.Fatal("stale plan created work after goal completion")
		}
	}
	if got := countNodes(t, store, task, rings.NodeStep, ""); got != 0 {
		t.Fatalf("stale plan added %d steps", got)
	}
	// Existing claim repair remains allowed through its own machine-controlled
	// dispatch; a completed goal must not discard already submitted evidence.
	if err := DispatchStep(ctx, store, task, "repair", "核对已交卷证据", DispatchOptions{GoalID: "g1", ActionLevel: "只读探测"}); err != nil {
		t.Fatal(err)
	}
}
