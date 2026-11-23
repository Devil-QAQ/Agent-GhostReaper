package swarm

// P4-69(问题清单 #86 D,摘除联动收口与状态一致性)swarm 层:
// ExcludeAssets 摘除时从批准清单同步剔除(排除与批准互斥)、
// StepIntentCoversAssets 导出判定(overmind 对账分族单源)、续跑指令
// "目标已排除"档(意图不覆盖被排除目标的挂等单位唤醒换路反馈)。

import (
	"context"

	"testing"

	"ghostreaper/internal/rings"
)

// TestP469ExcludePurgesApproved 互斥写侧:摘除的资产同步从 scope_approved
// 剔除(实锤 a-72dab0e5 同列两清单归位)——批准列中的不相交成员保留,
// 重复摘除(AlreadyOut)零落账不误清。
func TestP469ExcludePurgesApproved(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x-out.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-keep", NodeType: rings.NodeAsset, Title: "keep.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// 旧形态脏快照:两资产都在批准列。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-x", "a-keep"}}},
	); err != nil {
		t.Fatal(err)
	}
	res, err := ExcludeAssets(ctx, store, nil, task, []string{"a-x"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Excluded) != 1 {
		t.Fatalf("应新摘除 1: %+v", res)
	}
	st, _ := store.CurrentRoE(ctx, task)
	if len(st.ScopeApproved) != 1 || st.ScopeApproved[0] != "a-keep" {
		t.Fatalf("摘除后批准清单零残留(不相交成员保留): %+v", st.ScopeApproved)
	}
	if len(st.ExcludedAssets) != 1 || st.ExcludedAssets[0] != "a-x" {
		t.Fatalf("摘除清单应含新摘除: %+v", st.ExcludedAssets)
	}
	// 重复摘除走 AlreadyOut,零落账、批准列不动。
	res2, err := ExcludeAssets(ctx, store, nil, task, []string{"a-x"}, "creator")
	if err != nil || len(res2.Excluded) != 0 || len(res2.AlreadyOut) != 1 {
		t.Fatalf("重复摘除应 AlreadyOut: %+v %v", res2, err)
	}
	st2, _ := store.CurrentRoE(ctx, task)
	if len(st2.ScopeApproved) != 1 {
		t.Fatalf("重复摘除不误清批准列: %+v", st2.ScopeApproved)
	}
}

func TestP469StepIntentCoversAssets(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-ex", NodeType: rings.NodeAsset, Title: "ex.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-cover", NodeType: rings.NodeStep, Title: "测它", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-cover", ToNode: "a-ex"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-other", NodeType: rings.NodeStep, Title: "测种子", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-other", ToNode: "a-seed"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-bare", NodeType: rings.NodeStep, Title: "裸步骤", Status: "dispatched"}},
	); err != nil {
		t.Fatal(err)
	}
	assets, _ := store.NodesByType(ctx, task, rings.NodeAsset, "")
	target := []string{"a-ex"}
	if !StepIntentCoversAssets(ctx, store, task, "s-cover", target, assets) {
		t.Fatal("covers 锚定目标:应判覆盖")
	}
	if StepIntentCoversAssets(ctx, store, task, "s-other", target, assets) {
		t.Fatal("锚定种子(顺带触碰形态):不应判覆盖")
	}
	if StepIntentCoversAssets(ctx, store, task, "s-bare", target, assets) {
		t.Fatal("无锚定步骤:不应判覆盖")
	}
	if StepIntentCoversAssets(ctx, store, task, "s-cover", nil, assets) {
		t.Fatal("空资产集:不应判覆盖")
	}
}
