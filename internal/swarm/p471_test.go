package swarm

// P4-71(问题清单 #87 ④⑤⑥,RoE 复核修正批次第二阶·末阶)swarm 层:
// 退役资产复活(纳入遇 retired 一律复活——写法粒度、互斥、幂等,
// "本就范围内"误答根治)与攻击档反向调档(Executor 在飞登记表:
// 撤销允许=档移除重新出卡 / 转为允许=授档免卡,单位终态档失效)。

import (
	"context"
	"encoding/json"

	"strings"

	"testing"

	"ghostreaper/internal/rings"
)

// p471RetireAssetOfHost 经 AllowScopeAccess 把 host 的图上资产退役
// (host 入信息源清单 + 同主机资产 retired——P4-68 统一入口,测试用)。
func p471RetireAssetOfHost(t *testing.T, store *rings.Store, task, host string) {
	t.Helper()
	assets, err := store.NodesByType(t.Context(), task, rings.NodeAsset, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range assets {
		if anchor, ok := rings.ScopeAnchorOf(a.Title); ok && anchor.Host == host {
			if _, err := store.Append(t.Context(), task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "creator", Payload: rings.NodeStateChanged{NodeID: a.ID, Status: rings.AssetStatusRetired}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestP471IncludeRevivesRetired 完成标准①·牧者入口(IncludeAssets 单源)
// +"本就范围内"误答根治:retired 资产纳入 → 复活(retired→active 留痕
// Note"纳入复活")＋入批准清单,不再落 AlreadyIn;资产图复现(status
// 过滤按现值走)。
func TestP471IncludeRevivesRetired(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	// 范围外发现资产入图 → 允许访问退役(host 级)。
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-src", NodeType: rings.NodeAsset, Title: "gitee.com:443",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	p471RetireAssetOfHost(t, store, task, "gitee.com")
	if a, _ := store.Node(ctx, task, "a-src"); a.Status != rings.AssetStatusRetired {
		t.Fatalf("前置:资产应已退役: %s", a.Status)
	}
	// 资产图已不含它(读面过滤)。
	ag, _ := store.AssetGraph(ctx, task)
	for _, n := range ag.Assets {
		if n.ID == "a-src" {
			t.Fatal("前置:退役资产不应在资产图")
		}
	}

	res, err := IncludeAssets(ctx, store, task, []string{"a-src"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	// 误答根治:retired 不再落 AlreadyIn,归 Revived。
	if len(res.Revived) != 1 || res.Revived[0] != "a-src" || len(res.AlreadyIn) != 0 {
		t.Fatalf("retired 纳入应复活(不落 AlreadyIn): %+v", res)
	}
	a, _ := store.Node(ctx, task, "a-src")
	if a.Status != "active" {
		t.Fatalf("资产应复活为 active: %s", a.Status)
	}
	// 复活留痕:NodeStateChanged retired→active,Note"纳入复活"。
	evs, _ := store.Events(ctx, task, 0, 0)
	found := false
	for _, ev := range evs {
		if ev.Kind != rings.EventNodeStateChanged {
			continue
		}
		var p rings.NodeStateChanged
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.NodeID == "a-src" && p.Status == "active" && strings.Contains(p.Note, "纳入复活") {
			found = true
		}
	}
	if !found {
		t.Fatal("复活应留痕 NodeStateChanged(retired→active,Note 纳入复活)")
	}
	// 复活即入批准清单(条目契约)。
	roe, _ := store.CurrentRoE(ctx, task)
	if !roe.ScopeApprovedSet()["a-src"] {
		t.Fatalf("复活资产应入批准清单: %+v", roe.ScopeApproved)
	}
	// 资产图复现。
	ag2, _ := store.AssetGraph(ctx, task)
	found = false
	for _, n := range ag2.Assets {
		if n.ID == "a-src" {
			found = true
		}
	}
	if !found {
		t.Fatal("复活后资产应在资产图复现")
	}
}

// TestP471ReviveGranularity 复活粒度＝按纳入的资产节点(写法粒度):
// 同 host 其余退役节点不连带复活——要收编逐个纳入、各自复活留痕。
func TestP471ReviveGranularity(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-80", NodeType: rings.NodeAsset, Title: "multi.io:80",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-443", NodeType: rings.NodeAsset, Title: "multi.io:443",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
	); err != nil {
		t.Fatal(err)
	}
	// host 级退役:两端口资产一并退役(P4-68 语义)。
	p471RetireAssetOfHost(t, store, task, "multi.io")
	for _, id := range []string{"a-80", "a-443"} {
		if a, _ := store.Node(ctx, task, id); a.Status != rings.AssetStatusRetired {
			t.Fatalf("前置 %s 应退役", id)
		}
	}
	// 纳入其一:只它复活。
	if _, err := IncludeAssets(ctx, store, task, []string{"a-80"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if a, _ := store.Node(ctx, task, "a-80"); a.Status != "active" {
		t.Fatalf("纳入的 a-80 应复活: %s", a.Status)
	}
	if a, _ := store.Node(ctx, task, "a-443"); a.Status != rings.AssetStatusRetired {
		t.Fatalf("同 host 其余退役节点不连带复活: %s", a.Status)
	}
	// 逐个纳入:各自复活留痕。
	if _, err := IncludeAssets(ctx, store, task, []string{"a-443"}, "creator"); err != nil {
		t.Fatal(err)
	}
	if a, _ := store.Node(ctx, task, "a-443"); a.Status != "active" {
		t.Fatalf("第二个纳入的 a-443 应复活: %s", a.Status)
	}
}

// TestP471RevivedPurgesExcluded 复活＋互斥:retired 资产因历史脏数据
// 并存于摘除清单的,纳入复活时一并移回(排除与批准不并存)。
func TestP471RevivedPurgesExcluded(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{
			NodeID: "a-dirty", NodeType: rings.NodeAsset, Title: "dirty.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceDiscovered, "scope": rings.AssetScopeOut}}},
		// 脏起点:既被摘除又被退役(旧机制写侧无互斥时的并存形态)。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-dirty"}, Note: "摘除"}},
	); err != nil {
		t.Fatal(err)
	}
	p471RetireAssetOfHost(t, store, task, "dirty.com")
	res, err := IncludeAssets(ctx, store, task, []string{"a-dirty"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Revived) != 1 {
		t.Fatalf("retired+excluded 并存也应按复活整体处理: %+v", res)
	}
	roe, _ := store.CurrentRoE(ctx, task)
	if roe.ExcludedAssetSet()["a-dirty"] {
		t.Fatalf("复活应移出摘除清单(互斥): %+v", roe.ExcludedAssets)
	}
	if !roe.ScopeApprovedSet()["a-dirty"] {
		t.Fatalf("复活应入批准清单: %+v", roe.ScopeApproved)
	}
}

// TestP471ReviveRetiredIdempotent ReviveRetiredAssets 幂等:非 retired
// 资产零落笔(重复纳入零追加事件)。
func TestP471ReviveRetiredIdempotent(t *testing.T) {
	store, _, _, task := p444PentestEnv(t, nil)
	ctx := context.Background()
	p471RetireAssetOfHost(t, store, task, "www.example.com") // 种子资产退役
	before, _ := store.CurrentRevision(ctx, task)
	if revived, err := ReviveRetiredAssets(ctx, store, task, []string{"a-seed"}, "creator"); err != nil || len(revived) != 1 {
		t.Fatalf("退役资产应复活: %+v %v", revived, err)
	}
	after1, _ := store.CurrentRevision(ctx, task)
	revived2, err := ReviveRetiredAssets(ctx, store, task, []string{"a-seed"}, "creator")
	if err != nil || len(revived2) != 0 {
		t.Fatalf("非退役资产零复活: %+v %v", revived2, err)
	}
	after2, _ := store.CurrentRevision(ctx, task)
	if !(after1 > before && after2 == after1) {
		t.Fatalf("幂等:第一次落账推进版本,第二次零落笔: %d→%d→%d", before, after1, after2)
	}
}

// seenText 会话历史(任意消息)是否含文本片段(被拒命令无工具结果,
// 注入的审批反馈是阶段推进的判据)。
func seenText(rr *recordedRequest, marker string) bool {
	for _, m := range rr.Messages {
		if strings.Contains(m.Content, marker) {
			return true
		}
	}
	return false
}
