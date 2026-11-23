package overmind

import (
	"context"
	"ghostreaper/internal/rings"
	"testing"
)

func TestDispatchProvenanceAtomicAndIndependent(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	task := "source-dispatch"
	events := []rings.Event{}
	for _, n := range []rings.NodeAdded{{NodeID: "asset", NodeType: rings.NodeAsset, Title: "目标", Status: "active", AssetClass: "domain"}, {NodeID: "fact", NodeType: rings.NodeFact, Title: "事实", Status: "confirmed"}, {NodeID: "claim", NodeType: rings.NodeTestClaim, Title: "漏洞", Status: rings.TestClaimConfirmed}} {
		events = append(events, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: n})
	}
	if _, err := store.Append(ctx, task, events...); err != nil {
		t.Fatal(err)
	}
	if err := DispatchStep(ctx, store, task, "next", "组合意图", DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{"asset"}, SourceRefs: []string{"fact", "claim", "asset"}}); err != nil {
		t.Fatal(err)
	}
	sources, err := store.Edges(ctx, task, rings.EdgeSourcedFrom)
	if err != nil || len(sources) != 3 {
		t.Fatalf("来源未完整入账 %v %v", sources, err)
	}
	deps, err := store.Edges(ctx, task, rings.EdgeDependsOn)
	if err != nil || len(deps) != 0 {
		t.Fatal("来源引用被当成调度依赖")
	}
	before, _ := store.CurrentRevision(ctx, task)
	if err := DispatchStep(ctx, store, task, "bad", "幽灵来源", DispatchOptions{ActionLevel: "只读探测", SourceRefs: []string{"missing"}}); err == nil {
		t.Fatal("无效来源未拒绝")
	}
	after, _ := store.CurrentRevision(ctx, task)
	if before != after {
		t.Fatal("无效来源留下部分写入")
	}
	if _, err := store.Node(ctx, task, "bad"); err == nil {
		t.Fatal("无效来源留下意图节点")
	}
}
