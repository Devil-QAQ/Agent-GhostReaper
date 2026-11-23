package swarm

import (
	"context"
	"ghostreaper/internal/rings"
	"testing"
)

func TestP468StopLossIntentCover(t *testing.T) {
	store, _, env, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "example-x.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// s-cover:covers 锚定 a-ex(意图覆盖者,应被取消)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-cover", NodeType: rings.NodeStep, Title: "测试排除目标", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-cover", ToNode: "a-ex"}},
		// s-touch:锚定种子(意图不覆盖,顺带触碰形态,不应被杀)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-touch", NodeType: rings.NodeStep, Title: "范围内常规工作", Status: "dispatched",
			Detail: map[string]any{"action_level": "只读探测"}}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-touch", ToNode: "a-seed"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-ex"}}},
	); err != nil {
		t.Fatal(err)
	}
	_ = env
	killed, aborted, err := StopLossForAssets(ctx, store, nil, task, []string{"a-ex"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(killed) != 0 || len(aborted) != 1 || aborted[0].StepID != "s-cover" {
		t.Fatalf("止损应只取消意图覆盖者: killed=%v aborted=%v", killed, aborted)
	}
	touch, terr := store.Node(ctx, task, "s-touch")
	if terr != nil || touch.Status != "dispatched" {
		t.Fatalf("顺带触碰步骤不应被误杀: %+v err=%v", touch, terr)
	}
}
