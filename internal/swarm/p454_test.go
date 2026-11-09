package swarm

import (
	"context"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-54:资产标识解析入图(EnsureScopeAsset,牧者操作面与范围卡
// 裁决共用) ----

func TestP454EnsureScopeAsset(t *testing.T) {
	store := openStore(t, t.TempDir())
	ctx := context.Background()
	const task = "p454-ensure"
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "created"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-x", NodeType: rings.NodeAsset, Title: "x.example.com", Status: "active",
			AssetClass: rings.AssetDomain, Detail: map[string]any{"source": rings.AssetSourceSeed}}},
	); err != nil {
		t.Fatal(err)
	}

	// ID 复用。
	id, created, err := EnsureScopeAsset(ctx, store, task, "a-x", rings.AssetSourceManual, "shepherd")
	if err != nil || created || id != "a-x" {
		t.Fatalf("ID 应复用既有资产: %s %v %v", id, created, err)
	}
	// 标题精确复用。
	id, created, err = EnsureScopeAsset(ctx, store, task, "x.example.com", rings.AssetSourceManual, "shepherd")
	if err != nil || created || id != "a-x" {
		t.Fatalf("标题应复用既有资产: %s %v %v", id, created, err)
	}
	// 同锚点不同写法复用(URL 形态 → 同 host)。
	id, created, err = EnsureScopeAsset(ctx, store, task, "http://x.example.com/login", rings.AssetSourceManual, "shepherd")
	if err != nil || created || id != "a-x" {
		t.Fatalf("同锚点写法应复用: %s %v %v", id, created, err)
	}
	// 新标识·手动来源:入图且范围内(手动资产天然可测)。
	id, created, err = EnsureScopeAsset(ctx, store, task, "new.example.org", rings.AssetSourceManual, "shepherd")
	if err != nil || !created {
		t.Fatalf("新标识应入图: %v %v", created, err)
	}
	a, _ := store.Node(ctx, task, id)
	if src := rings.AssetSourceOf(a.Detail); src != rings.AssetSourceManual {
		t.Fatalf("来源应标手动: %s", src)
	}
	if rings.AssetScopeOf(a.Detail) != rings.AssetScopeIn {
		t.Fatalf("手动资产应默认范围内: %+v", a.Detail)
	}
	// 通配写法:独立资产(不与裸域混),发现形态入图默认范围外。
	wid, created, err := EnsureScopeAsset(ctx, store, task, "*.wild.io", rings.AssetSourceDiscovered, "shepherd")
	if err != nil || !created || wid == id {
		t.Fatalf("通配应独立入图: %s %v %v", wid, created, err)
	}
	w, _ := store.Node(ctx, task, wid)
	if w.Title != "*.wild.io" || rings.AssetScopeOf(w.Detail) != rings.AssetScopeOut {
		t.Fatalf("通配资产形态: %+v", w)
	}
	// 通配复用:再次按通配写法解析到同一资产。
	id2, created, err := EnsureScopeAsset(ctx, store, task, "*.wild.io", rings.AssetSourceDiscovered, "shepherd")
	if err != nil || created || id2 != wid {
		t.Fatalf("通配重复解析应复用: %s %v %v", id2, created, err)
	}
}
