package overmind

import (
	"context"
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

// Both task types use the same inventory choice. Neither task type implicitly
// adopts new discoveries; the decision does not change action permission.
func TestAssetAdoptionSameForBothTaskTypes(t *testing.T) {
	for _, kind := range []string{rings.TaskTypePentest, rings.TaskTypeRedteam} {
		t.Run(kind, func(t *testing.T) {
			store := openStore(t)
			ctx := t.Context()
			p444Task(t, store, "task", kind, "example.com")
			if err := DispatchStep(ctx, store, "task", "s1", "观察", DispatchOptions{ActionLevel: "只读探测"}); err != nil {
				t.Fatal(err)
			}
			submitDiscoveryFact(t, store, "task", "f", "u1", &swarm.NewAssetDecl{Class: "domain", Title: "new.example.org"})
			if _, err := Harvest(ctx, store, "task"); err != nil {
				t.Fatal(err)
			}
			g, err := store.AssetGraph(ctx, "task")
			if err != nil || len(g.Assets) != 1 {
				t.Fatal("discovery auto-adopted", err)
			}
			if err := DispatchStep(ctx, store, "task", "bad", "未加入对象", DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{"missing"}}); err == nil {
				t.Fatal("unknown inventory accepted")
			}
			adoptPendingAssets(t, store, "task")
			g, err = store.AssetGraph(ctx, "task")
			if err != nil || len(g.Assets) != 2 {
				t.Fatal("adoption missing", err)
			}
			var id string
			for _, a := range g.Assets {
				if a.ID != "a-seed" {
					id = a.ID
				}
			}
			if err := DispatchStep(ctx, store, "task", "good", "已加入对象", DispatchOptions{ActionLevel: "只读探测", CoversAssets: []string{id}}); err != nil {
				t.Fatal(err)
			}
			roe, _ := store.CurrentRoE(ctx, "task")
			if roe.UpdatedRev != 0 {
				t.Fatal("adoption changed action policy")
			}
		})
	}
}
