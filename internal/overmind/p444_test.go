package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
)

// ---- P4-44(问题清单 #61):资产范围归属与任务类型 ----
// ① 收割入图对照种子闭包冻结 scope 标记(渗透/红队都判,红队分组用);
// ③ 渗透任务 covers 待批资产派发被拒(与摘除守卫同款),放行后通过;
// 快照资产行按呈现态标注(待批锁定/红队可测)。

// p444Task 建任务(created 带 task_type)+ 种子域名资产。
func p444Task(t *testing.T, store *rings.Store, task, taskType, seed string) {
	t.Helper()
	ctx := context.Background()
	detail := map[string]any{}
	if taskType != "" {
		detail["task_type"] = taskType
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "test",
		Payload: rings.TaskLifecycle{Action: "created", Detail: detail},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: "a-seed", NodeType: rings.NodeAsset,
			Title: seed, Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}},
	}); err != nil {
		t.Fatal(err)
	}
}

// p444FactWithAsset 种一个带 new_asset 声明的候选事实 + 出生血缘
//(出生步骤节点先落,produces 边端点校验要求在图;步骤 ID 随事实唯一)。
func p444FactWithAsset(t *testing.T, store *rings.Store, task, factID, newTitle string) {
	t.Helper()
	ctx := context.Background()
	stepID := "s-" + factID
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: ActorOvermind, Payload: rings.NodeAdded{
			NodeID: stepID, NodeType: rings.NodeStep, Title: "发现步骤",
			Status: "done",
			Detail: map[string]any{"action_level": "只读探测"},
		}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", Payload: rings.NodeAdded{
			NodeID: factID, NodeType: rings.NodeFact, Title: "发现 " + newTitle,
			Status: "candidate",
			Detail: map[string]any{
				"evidence": "http_probe 实测",
				"new_asset": map[string]any{"class": "host", "title": newTitle},
			},
		}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "swarm:u1", Payload: rings.EdgeAdded{
			EdgeType: rings.EdgeProduces, FromNode: stepID, ToNode: factID}},
	); err != nil {
		t.Fatal(err)
	}
}

func TestP444DiscoveredAssetScopeFrozen(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()

	// 渗透任务:种子域名 www.example.com。
	p444Task(t, store, "t-pen", rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, "t-pen", "f-1", "api.example.com")
	p444FactWithAsset(t, store, "t-pen", "f-2", "www.example.com:8080")
	if _, err := Harvest(ctx, store, "t-pen"); err != nil {
		t.Fatal(err)
	}
	a1 := p444AssetByTitle(t, store, "t-pen", "api.example.com")
	if got := rings.AssetScopeOf(a1.Detail); got != rings.AssetScopeOut {
		t.Errorf("兄弟子域名应冻结 scope=out,得 %q", got)
	}
	a2 := p444AssetByTitle(t, store, "t-pen", "www.example.com:8080")
	if got := rings.AssetScopeOf(a2.Detail); got != rings.AssetScopeIn {
		t.Errorf("种子域名同域任意端口应冻结 scope=in,得 %q", got)
	}
	pending, err := store.PendingScopeAssets(ctx, "t-pen")
	if err != nil || len(pending) != 1 || pending[0].Title != "api.example.com" {
		t.Fatalf("渗透任务应恰 1 项待批: %+v %v", pending, err)
	}

	// 红队任务:同样判定照做(分组用),但不构成待批。
	p444Task(t, store, "t-red", rings.TaskTypeRedteam, "www.example.com")
	p444FactWithAsset(t, store, "t-red", "f-1", "api.example.com")
	if _, err := Harvest(ctx, store, "t-red"); err != nil {
		t.Fatal(err)
	}
	ra := p444AssetByTitle(t, store, "t-red", "api.example.com")
	if got := rings.AssetScopeOf(ra.Detail); got != rings.AssetScopeOut {
		t.Errorf("红队任务范围外判定照做(分组用),得 %q", got)
	}
	if pending, _ := store.PendingScopeAssets(ctx, "t-red"); len(pending) != 0 {
		t.Fatalf("红队任务无待批: %+v", pending)
	}
}

// p444AssetByTitle 按标题查图上资产(收割入图的 ID 是随机后缀)。
func p444AssetByTitle(t *testing.T, store *rings.Store, task, title string) *rings.NodeFull {
	t.Helper()
	assets, err := store.NodesByType(context.Background(), task, rings.NodeAsset, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range assets {
		if a.Title == title {
			return &a
		}
	}
	t.Fatalf("图上无资产 %q", title)
	return nil
}

func TestP444DispatchGuardPending(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	p444Task(t, store, "t-1", rings.TaskTypePentest, "www.example.com")
	p444FactWithAsset(t, store, "t-1", "f-1", "api.example.com")
	if _, err := Harvest(ctx, store, "t-1"); err != nil {
		t.Fatal(err)
	}
	pending := p444AssetByTitle(t, store, "t-1", "api.example.com")
	// 待批资产上派意图:整批拒收,人话指明锁定语义。
	err := DispatchStep(ctx, store, "t-1", "s-1", "探测 api 兄弟域", DispatchOptions{
		ActionLevel: "只读探测", CoversAssets: []string{pending.ID},
	})
	if err == nil || !strings.Contains(err.Error(), "待批") {
		t.Fatalf("渗透任务对待批资产派发应拒且人话指明: %v", err)
	}
	// 放行后:同一次派发通过(闭包纳入)。
	if _, err := store.Append(ctx, "t-1", rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ScopeApproved: []string{pending.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := DispatchStep(ctx, store, "t-1", "s-2", "探测 api 兄弟域", DispatchOptions{
		ActionLevel: "只读探测", CoversAssets: []string{pending.ID},
	}); err != nil {
		t.Fatalf("放行后派发应通过: %v", err)
	}
	// 红队任务:范围外资产直接可派(现状行为)。
	p444Task(t, store, "t-2", rings.TaskTypeRedteam, "www.example.com")
	p444FactWithAsset(t, store, "t-2", "f-1", "api.example.com")
	if _, err := Harvest(ctx, store, "t-2"); err != nil {
		t.Fatal(err)
	}
	red := p444AssetByTitle(t, store, "t-2", "api.example.com")
	if err := DispatchStep(ctx, store, "t-2", "s-1", "探测 api 兄弟域", DispatchOptions{
		ActionLevel: "只读探测", CoversAssets: []string{red.ID},
	}); err != nil {
		t.Fatalf("红队任务范围外资产直接可派: %v", err)
	}
}

func TestP444ScopeStateOfSnapLine(t *testing.T) {
	// 快照资产行的标注词源(ScopeStateOf 口径直接断言;整段快照组装
	// 在 Loop 上,活体验收覆盖端到端)。
	if got := rings.ScopeStateOf(rings.TaskTypePentest, rings.AssetSourceDiscovered, rings.AssetScopeOut, false, false); got != rings.ScopeStatePending {
		t.Fatalf("渗透待批口径: %q", got)
	}
	if got := rings.ScopeStateOf(rings.TaskTypeRedteam, rings.AssetSourceDiscovered, rings.AssetScopeOut, false, false); got != rings.ScopeStateOutOpen {
		t.Fatalf("红队范围外口径: %q", got)
	}
}

// swarm.NewAssetDecl 与收割入图的契约稳定(编译期锚)。
var _ = swarm.NewAssetDecl{}
