package manager

// P4-69 ②(问题清单 #86 D):排除与批准互斥的存量脏数据在重启收养时
// 一次清理(摘除清单成员一律从批准清单剔除;实锤 a-72dab0e5 同列两
// 清单);干净任务零落笔(幂等)。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ghostreaper/internal/rings"
)

// TestP469AdoptionReconcilesExclusivity 存量脏数据升级清理:含脏 RoE 的
// 库重启收养 → 批准清单剔除交集成员且 roe.updated 修正留痕;再次收养
// 零落笔(幂等)。
func TestP469AdoptionReconcilesExclusivity(t *testing.T) {
	store := openStore(t)
	const task = "t-p469-adopt"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-dirty", NodeType: rings.NodeAsset, Title: "dirty.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-clean", NodeType: rings.NodeAsset, Title: "clean.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
		// 旧机制脏快照:a-dirty 同列两清单(先批准后摘除的窗口产物),
		// a-clean 只在批准列。
		rings.Event{Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ExcludedAssets: []string{"a-dirty"},
				ScopeApproved: []string{"a-dirty", "a-clean"}}},
	); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Store: store, RootDir: filepath.Join(t.TempDir(), "tasks")})
	if err != nil {
		t.Fatalf("收养失败: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(task); m.Close() })
	st, _ := store.CurrentRoE(ctx, task)
	if len(st.ScopeApproved) != 1 || st.ScopeApproved[0] != "a-clean" {
		t.Fatalf("收养后批准清单应零残留(不相交成员保留): %+v", st.ScopeApproved)
	}
	if len(st.ExcludedAssets) != 1 || st.ExcludedAssets[0] != "a-dirty" {
		t.Fatalf("摘除清单不动: %+v", st.ExcludedAssets)
	}
	if !strings.Contains(st.Note, "状态互斥清理") {
		t.Fatalf("修正留痕应在案: %q", st.Note)
	}
	// 幂等:干净后再收养零落笔。
	rev := st.UpdatedRev
	m2, err := New(Config{Store: store, RootDir: filepath.Join(t.TempDir(), "tasks")})
	if err != nil {
		t.Fatalf("二次收养失败: %v", err)
	}
	t.Cleanup(m2.Close)
	if st2, _ := store.CurrentRoE(ctx, task); st2.UpdatedRev != rev {
		t.Fatalf("干净任务二次收养应零落笔: %d → %d", rev, st2.UpdatedRev)
	}
}
