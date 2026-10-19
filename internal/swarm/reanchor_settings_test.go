package swarm

import (
	"context"
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- 重锚定默认间隔的全局覆盖(P2-08d,Agent 栏可写):消费点在 Claim ----

// claimAnchor 领取并返回任务包的重锚定间隔(测试捷径)。
func claimAnchor(t *testing.T, dir string, store *rings.Store, task string) int {
	t.Helper()
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), Budget{})
	pack, err := ex.Claim(context.Background(), task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	return pack.AnchorEverySteps
}

// seedTaskWithAnchor 建带创建选项的任务(created 锚点带 anchor_every_steps;
// 0 = 不带该键,等价建任务表单留空)。
func seedTaskWithAnchor(t *testing.T, store *rings.Store, task string, anchor int) {
	t.Helper()
	var detail map[string]any
	if anchor > 0 {
		detail = map[string]any{"anchor_every_steps": float64(anchor)}
	}
	if _, err := store.Append(context.Background(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "重锚定任务", Detail: detail},
	}); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	seedGraph(t, store, task, "探测靶标")
}

// TestClaimReanchorGlobalDefault 任务未显式设定间隔时取全局默认(Agent
// 栏写入);显式设定优先于全局;删键回零值(门卫归一到代码默认)。
func TestClaimReanchorGlobalDefault(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "reanchor-global-task"
	seedTaskWithAnchor(t, store, task, 0)

	// 无全局键:零值(门卫随后归一到代码默认 10)。
	if got := claimAnchor(t, dir, store, task); got != 0 {
		t.Fatalf("无覆盖时应为零值,实际 %d", got)
	}

	// 全局键生效:新领取按全局值注入任务锚。
	if err := store.SetSetting(ctx, SettingKeyReanchorSteps, "7", "creator"); err != nil {
		t.Fatalf("写全局默认失败: %v", err)
	}
	if got := claimAnchor(t, dir, store, task); got != 7 {
		t.Fatalf("全局默认应为 7,实际 %d", got)
	}

	// 删键 = 恢复零值(同 fail-safe 的消费语义)。
	if err := store.DeleteSetting(ctx, SettingKeyReanchorSteps); err != nil {
		t.Fatalf("删全局默认失败: %v", err)
	}
	if got := claimAnchor(t, dir, store, task); got != 0 {
		t.Fatalf("删键后应为零值,实际 %d", got)
	}
}

// TestClaimReanchorExplicitTaskWins 任务显式设定(建任务表单)优先于
// 全局默认;脏全局值静默退零值(fail-safe 不挡领取)。
func TestClaimReanchorExplicitTaskWins(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "reanchor-explicit-task"
	seedTaskWithAnchor(t, store, task, 5)

	if err := store.SetSetting(ctx, SettingKeyReanchorSteps, "9", "creator"); err != nil {
		t.Fatalf("写全局默认失败: %v", err)
	}
	if got := claimAnchor(t, dir, store, task); got != 5 {
		t.Fatalf("任务显式值 5 应优先,实际 %d", got)
	}

	// 脏值(非正整数):静默退零值。
	for _, bad := range []string{"abc", "-3", "0", "  "} {
		if err := store.SetSetting(ctx, SettingKeyReanchorSteps, bad, "creator"); err != nil {
			t.Fatalf("写脏值 %q 失败: %v", bad, err)
		}
	}
	const task2 = "reanchor-dirty-task"
	seedTaskWithAnchor(t, store, task2, 0)
	if got := claimAnchor(t, dir, store, task2); got != 0 {
		t.Fatalf("脏全局值应退零值,实际 %d", got)
	}
}
