package overmind

import (
	"context"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-05 手动新增资产(问题清单 #10) ----
// 摘除资产不再被派发(派发入口守卫)+ 主宰快照/变化集对手动资产与
// 范围外资产的呈现 + 复现目标范围跳过摘除资产。

// seedAssetWith 图上种一个资产(带 detail.source),返回节点 ID。
func seedAssetWith(t *testing.T, store *rings.Store, task, id, title string, detail map[string]any) {
	t.Helper()
	_, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventNodeAdded, Actor: "creator",
		Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeAsset,
			Title: title, Status: "active", AssetClass: rings.AssetHost, Detail: detail},
	})
	if err != nil {
		t.Fatalf("种资产 %s 失败: %v", id, err)
	}
}

func excludeAssets(t *testing.T, store *rings.Store, task string, ids ...string) {
	t.Helper()
	_, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventRoeUpdated, Actor: "creator",
		Payload: rings.RoeUpdated{ExcludedAssets: ids},
	})
	if err != nil {
		t.Fatalf("摘除 %v 失败: %v", ids, err)
	}
}

// TestDispatchRejectsExcludedCover 完成标准"从 RoE 摘除后该资产不再被
// 派发"的确定性直证:covers 命中摘除资产 → 整批拒收且零残留;同批
// 未摘除资产不受误伤。
func TestDispatchRejectsExcludedCover(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p405-dispatch"
	mkTask(t, store, task)
	seedAssetWith(t, store, task, "a-in", "10.10.0.5", nil)
	seedAssetWith(t, store, task, "a-out", "10.10.0.6", nil)
	excludeAssets(t, store, task, "a-out")

	if err := DispatchStep(ctx, store, task, "s1", "探测摘除资产",
		DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{"a-out"}}); err == nil {
		t.Fatal("覆盖摘除资产的意图应被拒收")
	} else if !strings.Contains(err.Error(), "摘除") {
		t.Fatalf("拒因应指明摘除: %v", err)
	}
	if n, err := store.Node(ctx, task, "s1"); err == nil {
		t.Fatalf("拒收后不得残留步骤节点: %+v", n)
	}
	// 未摘除资产照常可派;混合批(含摘除)整批拒。
	if err := DispatchStep(ctx, store, task, "s2", "探测在范围内资产",
		DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{"a-in"}}); err != nil {
		t.Fatalf("范围内资产不应被误伤: %v", err)
	}
	if err := DispatchStep(ctx, store, task, "s3", "混合批",
		DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{"a-in", "a-out"}}); err == nil {
		t.Fatal("混合批含摘除资产应整批拒收")
	}
}

// TestLoadTargetScopeSkipsExcluded 复现目标范围(定稿复现步骤的授权域)
// 跳过摘除资产:摘除后同 URL 不再判为目标流量。
func TestLoadTargetScopeSkipsExcluded(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p405-scope"
	mkTask(t, store, task)
	seedAssetWith(t, store, task, "a-keep", "10.10.0.5:8080", nil)
	seedAssetWith(t, store, task, "a-drop", "10.10.0.6:8080", nil)

	sc, err := loadTargetScope(ctx, store, task)
	if err != nil {
		t.Fatalf("读目标范围失败: %v", err)
	}
	if !sc.allows("http://10.10.0.5:8080/") || !sc.allows("http://10.10.0.6:8080/") {
		t.Fatal("摘除前两个资产都应在范围内")
	}
	excludeAssets(t, store, task, "a-drop")
	sc, err = loadTargetScope(ctx, store, task)
	if err != nil {
		t.Fatalf("读目标范围失败: %v", err)
	}
	if !sc.allows("http://10.10.0.5:8080/") {
		t.Fatal("未摘除资产应仍在范围内")
	}
	if sc.allows("http://10.10.0.6:8080/") {
		t.Fatal("摘除资产应已移出范围")
	}
}

// TestSnapshotMarksManualAndExcluded 主宰态势快照:手动资产带"手动补入"
// 标记(研判一眼可见人刚补了什么);摘除资产标"范围外,不派发"且不占
// 覆盖缺口语义。
func TestSnapshotMarksManualAndExcluded(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	const task = "p405-snap"
	mkTask(t, store, task)
	seedAssetWith(t, store, task, "a-seed", "10.10.0.5", nil)
	seedAssetWith(t, store, task, "a-manual", "10.10.0.9",
		map[string]any{"source": rings.AssetSourceManual})
	seedAssetWith(t, store, task, "a-out", "10.10.0.10", nil)
	excludeAssets(t, store, task, "a-out")

	l := &Loop{Store: store, TaskID: task}
	snap, _, err := l.snapshot(ctx)
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	for _, want := range []string{
		"a-seed [host] 10.10.0.5 (未覆盖)",
		"a-manual [host] 10.10.0.9 (未覆盖,手动补入)",
		"a-out [host] 10.10.0.10 (范围外,不派发)",
	} {
		if !strings.Contains(snap, want) {
			t.Fatalf("快照缺资产行 %q:\n%s", want, snap)
		}
	}
}

// TestDeltaManualAssetLine 变化集:手动补入资产行走"人手动补入资产"
// 措辞(§11 触发事件带预写行动指令);种子资产行保持原措辞。
func TestDeltaManualAssetLine(t *testing.T) {
	store := openStore(t)
	const task = "p405-delta"
	mkTask(t, store, task)
	seedAssetWith(t, store, task, "a-seed", "seed.example.com", nil)
	seedAssetWith(t, store, task, "a-manual", "manual.example.com",
		map[string]any{"source": rings.AssetSourceManual})

	l := &Loop{Store: store, TaskID: task}
	lines := l.deltaSinceLastAssessment(context.Background())
	var seedLine, manualLine string
	for _, ln := range lines {
		if strings.Contains(ln, "a-seed") {
			seedLine = ln
		}
		if strings.Contains(ln, "a-manual") {
			manualLine = ln
		}
	}
	if !strings.Contains(seedLine, "新资产") || strings.Contains(seedLine, "手动") {
		t.Fatalf("种子资产行措辞不符: %q", seedLine)
	}
	if !strings.Contains(manualLine, "人手动补入资产") || !strings.Contains(manualLine, "已默认纳入授权范围") {
		t.Fatalf("手动资产行措辞不符: %q", manualLine)
	}
}
