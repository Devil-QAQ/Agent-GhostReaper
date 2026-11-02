package swarm

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
)

// TestCancelUnitsOfTaskScoped P4-18 收口结束宽限硬停的任务域取消:
// 只硬停本任务在飞单位,不误伤其他并发任务。直操登记表(白盒),
// 与 CancelRunningUnits 同一收场路径。
func TestCancelUnitsOfTaskScoped(t *testing.T) {
	dir := t.TempDir()
	ex := NewExecutor(openStore(t, dir), nil, filepath.Join(dir, "tasks"), Budget{})
	fired := map[string]bool{}
	mk := func(id string) context.CancelFunc {
		return func() { fired[id] = true }
	}
	ex.mu.Lock()
	ex.unitCancels["u1"] = mk("u1")
	ex.unitCancels["u2"] = mk("u2")
	ex.stepUnits["t-a\x00s1"] = "u1"
	ex.stepUnits["t-b\x00s2"] = "u2"
	ex.mu.Unlock()

	ids := ex.CancelUnitsOf("t-a")
	sort.Strings(ids)
	if len(ids) != 1 || ids[0] != "u1" {
		t.Fatalf("应只硬停本任务单位 u1: %v", ids)
	}
	if !fired["u1"] || fired["u2"] {
		t.Fatalf("他任务单位不得被误伤: %+v", fired)
	}
}
