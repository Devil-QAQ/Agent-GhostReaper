package swarm

import (
	"context"
	"path/filepath"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- 全局预算覆盖(P2-06c):消费点在 Claim ----

// TestClaimAppliesBudgetOverrides 装配默认 + 全局覆盖:键值表写入的
// max_turns / run_seconds 覆盖装配值,wrapup 维持只读默认;覆盖删除后
// 回到装配值(消费语义与"恢复默认"一致)。
func TestClaimAppliesBudgetOverrides(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "budget-override-task"
	seedGraph(t, store, task, "探测靶标")

	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"),
		Budget{MaxTurns: 2, RunSeconds: 300, WrapupMaxTurns: 2})

	// 无覆盖:装配默认原样进任务包。
	pack, err := ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.Budget != (Budget{MaxTurns: 2, RunSeconds: 300, WrapupMaxTurns: 2}) {
		t.Fatalf("无覆盖时应为装配预算: %+v", pack.Budget)
	}

	// 全局覆盖:两项可调项生效,wrapup 不受影响(无对应键)。
	if err := store.SetSetting(ctx, SettingKeyMaxTurns, "7", "creator"); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	if err := store.SetSetting(ctx, SettingKeyRunSeconds, "99", "creator"); err != nil {
		t.Fatalf("写覆盖失败: %v", err)
	}
	pack, err = ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.Budget != (Budget{MaxTurns: 7, RunSeconds: 99, WrapupMaxTurns: 2}) {
		t.Fatalf("覆盖后任务包预算不符: %+v", pack.Budget)
	}

	// 删除覆盖 = 恢复装配值(同 fail-safe 的消费语义)。
	if err := store.DeleteSetting(ctx, SettingKeyMaxTurns); err != nil {
		t.Fatalf("删覆盖失败: %v", err)
	}
	pack, err = ex.Claim(ctx, task, "s1")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.Budget != (Budget{MaxTurns: 2, RunSeconds: 99, WrapupMaxTurns: 2}) {
		t.Fatalf("删除 max_turns 覆盖后应回装配值 2: %+v", pack.Budget)
	}
}

// TestClaimBudgetOverridesFailSafe 读失败/值非法退装配默认(fail-safe):
// 配置读故障或脏值不挡领取,单位按装配预算起飞。
func TestClaimBudgetOverridesFailSafe(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir)
	ctx := context.Background()
	const task = "budget-failsafe-task"
	seedGraph(t, store, task, "探测靶标")

	assembly := Budget{MaxTurns: 2, RunSeconds: 300, WrapupMaxTurns: 2}

	// 值非法(非整数/非正):静默退装配默认。
	for _, bad := range []string{"abc", "-5", "0", "  "} {
		if err := store.SetSetting(ctx, SettingKeyMaxTurns, bad, "creator"); err != nil {
			t.Fatalf("写脏值 %q 失败: %v", bad, err)
		}
	}
	pack, err := ex_ClaimWith(t, dir, store, task, assembly)
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if pack.Budget != assembly {
		t.Fatalf("脏值应退装配默认: %+v", pack.Budget)
	}

	// 读失败(库已关):applyBudgetOverrides 直证退默认路径。
	_ = store.Close()
	b := assembly
	applyBudgetOverrides(ctx, store, &b)
	if b != assembly {
		t.Fatalf("读失败应退装配默认: %+v", b)
	}
}

// ex_ClaimWith 用指定装配预算建执行器并领取(避免复用已关 store)。
func ex_ClaimWith(t *testing.T, dir string, store *rings.Store, task string, budget Budget) (*TaskPack, error) {
	t.Helper()
	ex := NewExecutor(store, nil, filepath.Join(dir, "tasks"), budget)
	return ex.Claim(context.Background(), task, "s1")
}

// TestDefaultBudgetUnlimitedSemantics 默认值定稿(P4-01 ①):max_turns /
// run_seconds 默认 0 = 不限(雇佣关系,领了干完为止;想限随时面板配回
// 上限),wrapup 维持只读默认 2——设置视图与未显式装配处按此呈现。
func TestDefaultBudgetUnlimitedSemantics(t *testing.T) {
	d := DefaultBudget()
	if d.MaxTurns != 0 || d.RunSeconds != 0 || d.WrapupMaxTurns != 2 {
		t.Fatalf("默认预算应为 0/0/2(0 = 不限): %+v", d)
	}
}
