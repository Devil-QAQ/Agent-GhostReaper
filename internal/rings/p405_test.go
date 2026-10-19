package rings

import (
	"context"
	"testing"
)

// ---- P4-05 手动新增资产(问题清单 #10) ----

// TestRoeExcludedAssets 摘除清单随 RoE 快照整体覆盖:投影持久化与读回、
// 覆盖语义、结构校验(空 ID / 重复)在投影入口拒绝、集合形态判定。
func TestRoeExcludedAssets(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-p405-roe"

	mustAppend(t, s, task,
		Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
			ExcludedAssets: []string{"a-1", "a-2"}, Note: "摘除两个"}},
	)
	r1, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("查询 RoE 失败: %v", err)
	}
	if len(r1.ExcludedAssets) != 2 || r1.ExcludedAssets[0] != "a-1" || r1.ExcludedAssets[1] != "a-2" {
		t.Fatalf("摘除清单投影不符: %+v", r1.ExcludedAssets)
	}
	if set := r1.ExcludedAssetSet(); !set["a-1"] || !set["a-2"] || set["a-3"] {
		t.Fatalf("集合形态判定不符: %+v", set)
	}
	// 覆盖语义:新快照整体替换(纳入回 a-2,另摘 a-3)。
	mustAppend(t, s, task, Event{Kind: EventRoeUpdated, Actor: "creator", Payload: RoeUpdated{
		ExcludedAssets: []string{"a-1", "a-3"}}})
	r2, err := s.CurrentRoE(ctx, task)
	if err != nil {
		t.Fatalf("第二次查询失败: %v", err)
	}
	if len(r2.ExcludedAssets) != 2 || r2.ExcludedAssets[1] != "a-3" {
		t.Fatalf("摘除清单应为覆盖语义: %+v", r2.ExcludedAssets)
	}
	// 结构校验:空 ID 与重复在投影入口拒绝(整批不落)。
	if _, err := s.Append(ctx, task, Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ExcludedAssets: []string{"a-1", ""}}}); err == nil {
		t.Fatal("空资产 ID 应被拒绝")
	}
	if _, err := s.Append(ctx, task, Event{Kind: EventRoeUpdated, Actor: "creator",
		Payload: RoeUpdated{ExcludedAssets: []string{"a-1", "a-1"}}}); err == nil {
		t.Fatal("重复资产 ID 应被拒绝")
	}
	r3, _ := s.CurrentRoE(ctx, task)
	if len(r3.ExcludedAssets) != 2 {
		t.Fatalf("拒收批次不得影响当前值: %+v", r3.ExcludedAssets)
	}
}

// TestAssetSourceProjection 资产来源三态投影:detail.source 缺省(历史
// 与种子)归一 seed;manual/discovered 原样读出;AssetGraph 与 Graph
// 两个读面同源。
func TestAssetSourceProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const task = "t-p405-src"

	mustAppend(t, s, task,
		Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{
			NodeID: "a-legacy", NodeType: NodeAsset, Title: "legacy.example.com",
			Status: "active", AssetClass: AssetDomain}},
		Event{Kind: EventNodeAdded, Actor: "creator", Payload: NodeAdded{
			NodeID: "a-manual", NodeType: NodeAsset, Title: "manual.example.com",
			Status: "active", AssetClass: AssetDomain, Detail: map[string]any{"source": AssetSourceManual}}},
		Event{Kind: EventNodeAdded, Actor: "overmind", Payload: NodeAdded{
			NodeID: "a-found", NodeType: NodeAsset, Title: "found.example.com",
			Status: "active", AssetClass: AssetDomain, Detail: map[string]any{"source": AssetSourceDiscovered}}},
	)

	want := map[string]string{
		"a-legacy": AssetSourceSeed,
		"a-manual": AssetSourceManual,
		"a-found":  AssetSourceDiscovered,
	}
	ag, err := s.AssetGraph(ctx, task)
	if err != nil {
		t.Fatalf("查资产图失败: %v", err)
	}
	for _, a := range ag.Assets {
		if a.Source != want[a.ID] {
			t.Fatalf("AssetGraph 来源不符 %s: %s ≠ %s", a.ID, a.Source, want[a.ID])
		}
	}
	g, err := s.Graph(ctx, task)
	if err != nil {
		t.Fatalf("查图投影失败: %v", err)
	}
	for _, n := range g.Nodes {
		if n.Type != NodeAsset {
			continue
		}
		if n.Source != want[n.ID] {
			t.Fatalf("Graph 来源不符 %s: %s ≠ %s", n.ID, n.Source, want[n.ID])
		}
	}
	// 归一助手:缺省 seed,显式值原样。
	if AssetSourceOf(nil) != AssetSourceSeed || AssetSourceOf(map[string]any{}) != AssetSourceSeed {
		t.Fatal("缺省来源应归一 seed")
	}
	if AssetSourceOf(map[string]any{"source": AssetSourceManual}) != AssetSourceManual {
		t.Fatal("显式来源应原样读出")
	}
}
