package swarm

import (
	"context"

	"path/filepath"

	"testing"

	"ghostreaper/internal/rings"
)

// p444PentestEnv 渗透任务(种子 www.example.com)+ 执行器与单位环境。
func p444PentestEnv(t *testing.T, approved []string) (*rings.Store, *Executor, *unitEnv, string) {
	t.Helper()
	dir := t.TempDir()
	store := openStore(t, dir)
	const task = "p444-task"
	ctx := context.Background()
	if _, err := store.Append(ctx, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{
			Action: "created", Detail: map[string]any{"task_type": rings.TaskTypePentest}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "creator", Payload: rings.NodeAdded{
			NodeID: "a-seed", NodeType: rings.NodeAsset, Title: "www.example.com",
			Status: "active", AssetClass: rings.AssetDomain,
			Detail: map[string]any{"source": rings.AssetSourceSeed}}},
	); err != nil {
		t.Fatal(err)
	}
	if approved != nil {
		if _, err := store.Append(ctx, task, rings.Event{
			Kind: rings.EventRoeUpdated, Actor: "creator",
			Payload: rings.RoeUpdated{ScopeApproved: approved},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	ex.Gate = NewLedgerGate(store)
	env := &unitEnv{Executor: ex, TaskID: task, StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1"}
	// DNS 打桩:解析恒失败(判定按保守方向)——测试不依赖公网 DNS 的
	// 实际解析结果(P4-50 ②同机扩展下,公网同 IP 会改变判定)。
	return store, ex, env, task
}
