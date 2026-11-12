package swarm

// P4-69(问题清单 #86 D,摘除联动收口与状态一致性)swarm 层:
// ExcludeAssets 摘除时从批准清单同步剔除(排除与批准互斥)、
// StepIntentCoversAssets 导出判定(overmind 对账分族单源)、续跑指令
// "目标已排除"档(意图不覆盖被排除目标的挂等单位唤醒换路反馈)。

import (
	"context"
	"strings"
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

// TestP469StepIntentCoversAssets 导出判定(overmind 对账分族复用同一单源):
// covers 锚定命中、锚定他资产不命中、verifies→假设→about 锚定同判、
// 无锚定(顺带触碰形态)不命中、空资产集不命中。
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
		// 验证类步骤:verifies→假设→about 锚定被排除资产(P4-68 单源
		// 的验证形态,P4-69 对账分族同口径)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "h1", NodeType: rings.NodeHypothesis, Title: "猜 ex.com 有洞", Status: "candidate"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeAbout, FromNode: "h1", ToNode: "a-ex"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-verify", NodeType: rings.NodeStep, Title: "验证猜", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeVerifies, FromNode: "s-verify", ToNode: "h1"}},
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
	if !StepIntentCoversAssets(ctx, store, task, "s-verify", target, assets) {
		t.Fatal("verifies→假设→about 锚定目标:应判覆盖(验证类同口径)")
	}
	if StepIntentCoversAssets(ctx, store, task, "s-cover", nil, assets) {
		t.Fatal("空资产集:不应判覆盖")
	}
}

// TestP469ScopeExcludedResumePrompt 续跑指令"目标已排除"档:挂等目标
// 现值被排除 → 注入"目标已排除"反馈换路(不再对该目标发起测试);优先
// 级断言——排除优先于信息源;未排除未授权回落"范围批准"措辞。
func TestP469ScopeExcludedResumePrompt(t *testing.T) {
	store, ex, _, pack := p450Env(t)
	ctx := context.Background()
	// 挂等目标标记 + 范围外资产摘除(命令路顺带触碰被拒、单位冻结后
	// 目标又被排除的形态)。
	if _, err := store.Append(ctx, pack.TaskID,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-tmp", NodeType: rings.NodeAsset, Title: "tmp.org",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: pack.StepID, Status: rings.StepScopeWait,
				Note: "scope_wait tmp.org 443"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-tmp"}, Note: "摘除"}},
	); err != nil {
		t.Fatal(err)
	}
	got := scopeResumePrompt(ex, pack)
	if !strings.Contains(got, "目标已排除 · 续跑") || !strings.Contains(got, "tmp.org") {
		t.Fatalf("已排除目标应走目标已排除措辞: %q", got)
	}
	if !strings.Contains(got, "不要") || !strings.Contains(got, "本步骤其余工作不受影响") {
		t.Fatalf("换路反馈应明示不再测它、其余工作继续: %q", got)
	}
	// 排除优先于信息源:同 host 再入信息源清单,止损语义优先。
	if err := GrantInfoSource(ctx, store, pack.TaskID, "tmp.org", "依据", "creator"); err != nil {
		t.Fatal(err)
	}
	if got := scopeResumePrompt(ex, pack); !strings.Contains(got, "目标已排除 · 续跑") {
		t.Fatalf("排除与信息源并存时排除优先: %q", got)
	}
}

// TestP469ScopeExcludedResumePromptFallback 未排除未授权的目标回落既有
// "范围批准"措辞(新档只在排除命中时介入,不改变既有两档)。
func TestP469ScopeExcludedResumePromptFallback(t *testing.T) {
	store, ex, _, pack := p450Env(t)
	ctx := context.Background()
	if _, err := store.Append(ctx, pack.TaskID,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: pack.StepID, Status: rings.StepScopeWait,
				Note: "scope_wait other.org 443"}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-doesnotmatter"}, Note: "摘除别的"}},
	); err != nil {
		t.Fatal(err)
	}
	if got := scopeResumePrompt(ex, pack); !strings.Contains(got, "范围批准 · 续跑") {
		t.Fatalf("未排除目标应回落范围批准措辞: %q", got)
	}
}
