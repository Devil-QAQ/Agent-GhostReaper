package server

import (
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P5-05 挂等让位的面板数据面:在途全景纳入停等态行(2-22/5-1 排序
// 消费面核销的机制面)——停等条目按领取次序排(StepLaunchLess 同源),
// 解除唤醒(scope_head)后压队首。----

// TestP505PanoramaRowsIncludeWaitStates 停等态(scope_wait/gate_wait/
// pause_wait/model_wait)进入在途全景行(排队中口径);被唤醒
//(dispatched+scope_head)的条目压队首。
func TestP505PanoramaRowsIncludeWaitStates(t *testing.T) {
	steps := map[string]rings.NodeFull{
		"s-run":   {NodeView: rings.NodeView{ID: "s-run", CreatedRev: 2}, Detail: map[string]any{"priority": "high"}},
		"s-queue": {NodeView: rings.NodeView{ID: "s-queue", CreatedRev: 3}, Detail: map[string]any{}},
		// 四类停等态:让位后仍在工作线上(排队中)。
		"s-scope": {NodeView: rings.NodeView{ID: "s-scope", CreatedRev: 4}, Detail: map[string]any{}},
		"s-gate":  {NodeView: rings.NodeView{ID: "s-gate", CreatedRev: 5}, Detail: map[string]any{}},
		"s-pause": {NodeView: rings.NodeView{ID: "s-pause", CreatedRev: 6}, Detail: map[string]any{}},
		"s-model": {NodeView: rings.NodeView{ID: "s-model", CreatedRev: 7}, Detail: map[string]any{}},
		"s-done":  {NodeView: rings.NodeView{ID: "s-done", CreatedRev: 1}, Detail: map[string]any{}},
	}
	setStatus := func(id, st string) rings.NodeFull {
		n := steps[id]
		n.Status = st
		steps[id] = n
		return n
	}
	setStatus("s-run", "running")
	setStatus("s-queue", "dispatched")
	setStatus("s-scope", rings.StepScopeWait)
	setStatus("s-gate", rings.StepGateWait)
	setStatus("s-pause", rings.StepPauseWait)
	setStatus("s-model", rings.StepModelWait)
	setStatus("s-done", "done")

	rows := panoramaRows(steps, nil, nil, nil, nil)
	if len(rows) != 6 {
		t.Fatalf("四类停等态应全部入列(执行中1+排队1+停等4),得 %d 行: %+v", len(rows), rows)
	}
	seen := map[string]string{}
	for _, r := range rows {
		seen[r.ID] = r.Status
	}
	for _, id := range []string{"s-run", "s-queue", "s-scope", "s-gate", "s-pause", "s-model"} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("停等/在途步骤 %s 缺席全景行: %+v", id, rows)
		}
	}
	if _, ok := seen["s-done"]; ok {
		t.Fatalf("终态步骤不得入列")
	}

	// 唤醒后压队首:scope_head 条目先于一切普通在途。
	woken := setStatus("s-gate", "dispatched")
	woken.Detail["scope_head"] = true
	woken.Detail["head_seq"] = float64(9)
	steps["s-gate"] = woken
	rows = panoramaRows(steps, nil, nil, nil, nil)
	if len(rows) == 0 || rows[0].ID != "s-gate" {
		t.Fatalf("解除唤醒的让位条目应压队首: %+v", rows)
	}
}
