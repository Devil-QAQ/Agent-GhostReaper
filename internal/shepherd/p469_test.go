package shepherd

// P4-69(问题清单 #86 D)牧者通道:摘除资产的瞬间联动收口——
// set_asset_scope exclude 走 swarm.ExcludeAssets + resolveScopeWakes
//(Service.Loops 未装配时裸循环兜底,落账照常),挂等冻结单位分族
// 收口、未决卡拒绝收口,与 RoE 面板/卡上禁止测试三通道同源。

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP469ShepherdExcludeClosesWaiters 牧者摘除的联动收口(Loops 未装配
// =裸循环兜底路径):挂等目标 X 的冻结单位与挂着的卡一并收口——意图
// 覆盖者终止留痕、顺带触碰者唤醒换路;卡消红点消;批准清单互斥剔除。
func TestP469ShepherdExcludeClosesWaiters(t *testing.T) {
	store := openStore(t)
	const task = "sh-p469"
	ctx := context.Background()
	woken := make(chan string, 8)
	// Loops 刻意不装配:验证兜底路径(裸 Loop 对账)同样完成联动收口。
	s := &Service{Store: store, RootDir: t.TempDir(), Wake: func(id string) { woken <- id }}
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		// 目标资产:已被批准入范围(脏起点,摘除端应互斥剔除)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-wait", NodeType: rings.NodeAsset, Title: "wait-target.com:80",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: []string{"a-wait"}, Note: "加入资产"}},
		// 挂等步骤 s-cover(意图覆盖目标)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-cover", NodeType: rings.NodeStep, Title: "测目标", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-cover", ToNode: "a-wait"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s-cover", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s-cover", Status: rings.StepScopeWait,
				Note: "scope_wait wait-target.com 80"}},
		// 挂等步骤 s-touch(锚定种子,顺带触碰)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "s-touch", NodeType: rings.NodeStep, Title: "范围内工作", Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeCovers, FromNode: "s-touch", ToNode: "a-seed"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s-touch", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s-touch", Status: rings.StepScopeWait,
				Note: "scope_wait wait-target.com 80"}},
		// 挂着的卡。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1", Payload: rings.ApprovalRecord{
			RequestID: rings.ScopeCardID("wait-target.com", "80"), StepID: "s-cover", UnitID: "u1",
			Type: rings.ApprovalRecScopeWait, ToolName: "bash"}},
	); err != nil {
		t.Fatal(err)
	}
	out, isErr := runScopeTool(t, s, task, `{"action":"exclude","assets":["a-wait"]}`)
	if isErr {
		t.Fatalf("摘除不应报错: %s", out)
	}
	// 挂等单位分族收口。
	if st, _ := store.Node(ctx, task, "s-cover"); st.Status != "aborted" {
		t.Fatalf("覆盖者应终止留痕: %+v", st.Status)
	}
	st, _ := store.Node(ctx, task, "s-touch")
	if st.Status != "dispatched" {
		t.Fatalf("顺带触碰者应唤醒换路: %+v", st.Status)
	}
	if v, _ := st.Detail["scope_head"].(bool); !v {
		t.Fatalf("唤醒应带队首标记: %+v", st.Detail)
	}
	// 卡收口(卡消红点消)+互斥。
	rows, _ := store.ApprovalByRequest(ctx, task, rings.ScopeCardID("wait-target.com", "80"))
	if last := rows[len(rows)-1]; last.Type != rings.ApprovalRecDenied {
		t.Fatalf("卡应按拒绝语义收口: %+v", last)
	}
	if n, _ := store.ScopePendingCount(ctx, task); n != 0 {
		t.Fatalf("红点应归零: %d", n)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if len(roe.ScopeApproved) != 0 {
		t.Fatalf("摘除后批准清单零残留: %+v", roe.ScopeApproved)
	}
}
