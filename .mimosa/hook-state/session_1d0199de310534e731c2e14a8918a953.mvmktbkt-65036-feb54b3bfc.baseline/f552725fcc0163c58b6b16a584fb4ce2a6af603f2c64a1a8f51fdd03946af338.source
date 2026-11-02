package overmind

// P4-12 ①(问题清单 #21)数据补链——收割入图:候选事实带 new_asset 声明
// 转正时点亮 source=discovered 并挂出生血缘(发现步骤 produces→资产、
// 发现事实 about→资产、发现单位/步骤/事实冻结进 detail);同标识查重
// 不重复入图;无证据声明不入图(不编造出生)。

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// seedDiscoveredGraph 铺底图:目标 + 发现步骤 + 种子资产。
func seedDiscoveredGraph(t *testing.T, store *rings.Store, task string) {
	t.Helper()
	ctx := context.Background()
	if err := SeedGoal(ctx, store, task, "g1", "目标", nil); err != nil {
		t.Fatalf("种目标失败: %v", err)
	}
	if err := DispatchStep(ctx, store, task, "s1", "枚举子域", DispatchOptions{GoalID: "g1", ActionLevel: "只读探测"}); err != nil {
		t.Fatalf("派发失败: %v", err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "a-seed", NodeType: rings.NodeAsset,
			Title: "example.com", Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}},
	}); err != nil {
		t.Fatalf("种资产失败: %v", err)
	}
}

// submitDiscoveryFact 模拟虫群提交带新对象声明的候选事实(与
// swarm.SubmitCandidateFact 同形态:fact 节点 + produces 边)。
func submitDiscoveryFact(t *testing.T, store *rings.Store, task, factID, unit string, decl *swarm.NewAssetDecl) {
	t.Helper()
	ctx := context.Background()
	detail := map[string]any{"evidence": "DNS 应答原文", "unit_id": unit, "transcript_id": "swarm-" + unit}
	if decl != nil {
		detail["new_asset"] = map[string]any{"class": decl.Class, "title": decl.Title, "parent": decl.Parent}
	}
	events := []rings.Event{
		{Kind: rings.EventNodeAdded, Actor: "swarm:" + unit, Payload: rings.NodeAdded{
			NodeID: factID, NodeType: rings.NodeFact, Title: "发现子域 " + decl.Title, Status: "candidate", Detail: detail}},
		{Kind: rings.EventEdgeAdded, Actor: "swarm:" + unit, Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: "s1", ToNode: factID}},
	}
	if _, err := store.Append(ctx, task, events...); err != nil {
		t.Fatalf("提交候选 %s 失败: %v", factID, err)
	}
}

func TestHarvestDiscoveredAsset(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p412-harvest"
	seedDiscoveredGraph(t, store, task)

	submitDiscoveryFact(t, store, task, "f-disc", "u5", &swarm.NewAssetDecl{
		Class: "subdomain", Title: "admin.example.com", Parent: "a-seed"})

	hv, err := Harvest(ctx, store, task)
	if err != nil {
		t.Fatalf("收割失败: %v", err)
	}
	if len(hv.Confirmed) != 1 {
		t.Fatalf("应转正 1 条: %+v", hv)
	}
	// 发现型资产入图:来源点亮 + 出生血缘冻结。
	var born *rings.NodeFull
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	for i, a := range assets {
		if a.Title == "admin.example.com" {
			born = &assets[i]
		}
	}
	if born == nil {
		t.Fatalf("收割应入图新对象 admin.example.com: %+v", assets)
	}
	if born.AssetClass != rings.AssetSubdomain || born.Status != "active" {
		t.Fatalf("新对象类与状态不符: %+v", born)
	}
	if rings.AssetSourceOf(born.Detail) != rings.AssetSourceDiscovered {
		t.Fatalf("来源应点亮 discovered: %+v", born.Detail)
	}
	if born.Detail["unit_id"] != "u5" || born.Detail["step_id"] != "s1" || born.Detail["discovered_fact"] != "f-disc" {
		t.Fatalf("出生血缘应冻结发现单位/步骤/事实: %+v", born.Detail)
	}
	if born.Detail["transcript_id"] != "swarm-u5" {
		t.Fatalf("出生会话锚应随事实带过: %+v", born.Detail)
	}
	// 出生边:发现步骤 produces→资产、发现事实 about→资产、层级 sub_of。
	has := func(et rings.EdgeType, from, to string) bool {
		es, _ := store.Edges(ctx, task, et)
		for _, e := range es {
			if e.From == from && e.To == to {
				return true
			}
		}
		return false
	}
	if !has(rings.EdgeProduces, "s1", born.ID) {
		t.Fatalf("应挂出生边 produces %s→%s", "s1", born.ID)
	}
	if !has(rings.EdgeAbout, "f-disc", born.ID) {
		t.Fatalf("发现事实应锚定新对象 about f-disc→%s", born.ID)
	}
	if !has(rings.EdgeSubOf, born.ID, "a-seed") {
		t.Fatalf("声明的父资产应挂层级 sub_of %s→a-seed", born.ID)
	}

	// 同标识重复发现:不重复入图,发现事实锚定既有对象。
	submitDiscoveryFact(t, store, task, "f-dup", "u6", &swarm.NewAssetDecl{
		Class: "host", Title: "admin.example.com"})
	if _, err := Harvest(ctx, store, task); err != nil {
		t.Fatalf("二次收割失败: %v", err)
	}
	assets2, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	if len(assets2) != len(assets) {
		t.Fatalf("同标识不应重复入图: %d → %d", len(assets), len(assets2))
	}
	if !has(rings.EdgeAbout, "f-dup", born.ID) {
		t.Fatalf("重复发现应锚定既有对象 about f-dup→%s", born.ID)
	}

	// 无证据声明:事实如实跳过,对象不入图(无证据的出生不编造)。
	noEv := map[string]any{"unit_id": "u7", "new_asset": map[string]any{
		"class": "host", "title": "ghost.example.com"}}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "swarm:u7", Payload: rings.NodeAdded{
			NodeID: "f-bare", NodeType: rings.NodeFact, Title: "无证据发现", Status: "candidate", Detail: noEv}}); err != nil {
		t.Fatalf("种无证据候选失败: %v", err)
	}
	hv3, err := Harvest(ctx, store, task)
	if err != nil || len(hv3.Skipped) != 1 || hv3.Skipped[0].FactID != "f-bare" {
		t.Fatalf("无证据候选应如实跳过: %+v err=%v", hv3, err)
	}
	for _, a := range assets2 {
		if a.Title == "ghost.example.com" {
			t.Fatalf("无证据声明不得入图: %+v", a)
		}
	}
}
