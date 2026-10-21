package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// P4-08 server 侧:② 确认耗时拆解(账本时间戳直读对账)+ ① 面板
// 排队次序与调度器同源(high 探索与反证并存时反证在前)。

// TestClaimTimingBreakdown ②:落地 → 终态耗时拆解与账本时间戳逐段
// 对账——排队 = dispatched→running,执行 = running→终态;轮次 = 当前
// 战役反证步骤数;重开前的旧战役步骤不混计。
func TestClaimTimingBreakdown(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	const task = "p408-timing"
	t0 := time.Date(2026, 10, 20, 10, 31, 0, 0, time.Local)
	at := func(offset time.Duration) time.Time { return t0.Add(offset) }

	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager", OccurredAt: at(0),
			Payload: rings.TaskLifecycle{Action: "created", Title: "耗时拆解"}},
		// 落地:候选声明入账(+0s)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", OccurredAt: at(0),
			Payload: rings.NodeAdded{NodeID: "tc-1", NodeType: rings.NodeTestClaim,
				Title: "未授权访问", Status: rings.TestClaimCandidate,
				Detail: map[string]any{"unit_id": "u1", "step_id": "s0"}}},
		// 旧战役(重开前):第 1 轮反证步骤,排队 30s / 执行 60s——不混计。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(10 * time.Second),
			Payload: rings.NodeAdded{NodeID: "s-old", NodeType: rings.NodeStep, Title: "旧战役反证",
				Status: "done", Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-1", "round": 1}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u9", OccurredAt: at(40 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "s-old", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u9", OccurredAt: at(100 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "s-old", Status: "done"}},
		// 重开(reopen 记录,战役边界)。
		rings.Event{Kind: rings.EventTestClaimRecord, Actor: "creator", OccurredAt: at(110 * time.Second),
			Payload: rings.TestClaimRecord{ClaimID: "tc-1", Type: rings.TestClaimRecReopen,
				Trigger: rings.ReopenManual, Reason: "复验"}},
		// 当前战役:第 2 轮反证步骤,排队 20s / 执行 90s;第 3 轮排队 5s / 执行 45s。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(120 * time.Second),
			Payload: rings.NodeAdded{NodeID: "s-r2", NodeType: rings.NodeStep, Title: "反证(第 2 轮)",
				Status: "done", Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-1", "round": 2}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", OccurredAt: at(140 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "s-r2", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", OccurredAt: at(230 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "s-r2", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", OccurredAt: at(240 * time.Second),
			Payload: rings.NodeAdded{NodeID: "s-r3", NodeType: rings.NodeStep, Title: "反证(第 3 轮)",
				Status: "done", Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-1", "round": 3}}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3", OccurredAt: at(245 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "s-r3", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3", OccurredAt: at(290 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "s-r3", Status: "done"}},
		// 终态:confirmed(+300s)。
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "arbiter", OccurredAt: at(300 * time.Second),
			Payload: rings.NodeStateChanged{NodeID: "tc-1", Status: rings.TestClaimConfirmed}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/claims/tc-1", "")
	if code != http.StatusOK {
		t.Fatalf("发现详情应 200: %d %v", code, out)
	}
	tm, _ := out["timing"].(map[string]any)
	if tm == nil {
		t.Fatalf("详情应携带 timing: %v", out)
	}
	// 对账:排队 = 20 + 5 = 25s;执行 = 90 + 45 = 135s(旧战役 30/60 不计)。
	if tm["queue_seconds"] != 25.0 {
		t.Fatalf("排队合计应 25s(20+5,旧战役不计): %v", tm["queue_seconds"])
	}
	if tm["exec_seconds"] != 135.0 {
		t.Fatalf("执行合计应 135s(90+45,旧战役不计): %v", tm["exec_seconds"])
	}
	if tm["rounds"] != float64(2) {
		t.Fatalf("轮次应为当前战役 2 轮: %v", tm["rounds"])
	}
	if v, _ := tm["landed_at"].(string); v != at(0).Format("2006-01-02 15:04:05") {
		t.Fatalf("落地时刻不符: %v", tm["landed_at"])
	}
	if v, _ := tm["terminal_at"].(string); v != at(300*time.Second).Format("2006-01-02 15:04:05") {
		t.Fatalf("终态时刻不符: %v", tm["terminal_at"])
	}

	// 进行中声明(无终态):terminal_at 缺省,如实不造。
	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventNodeAdded, Actor: "swarm:u1", OccurredAt: at(310 * time.Second),
			Payload: rings.NodeAdded{NodeID: "tc-2", NodeType: rings.NodeTestClaim,
				Title: "进行中", Status: rings.TestClaimTesting,
				Detail: map[string]any{"unit_id": "u1", "step_id": "s0"}}}); err != nil {
		t.Fatalf("铺进行中声明失败: %v", err)
	}
	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/claims/tc-2", "")
	if code != http.StatusOK {
		t.Fatalf("进行中声明详情应 200: %d", code)
	}
	tm2, _ := out["timing"].(map[string]any)
	if _, has := tm2["terminal_at"]; has {
		t.Fatalf("进行中声明不得有终态时刻: %v", tm2)
	}
	if tm2["rounds"] != float64(0) {
		t.Fatalf("无反证步骤轮次为 0: %v", tm2)
	}
}

// TestUnitsQueueFalsifyFirst ①:排队中条目次序与调度器同源——更早派发
// 的 high 探索与反证并存时,反证排在前面(面板排队顺序即领取顺序)。
func TestUnitsQueueFalsifyFirst(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	store := srv.Store
	const task = "p408-queue"

	if _, err := store.Append(context.Background(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "排队次序"}},
		// 先派:high 探索(创建版本更早)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-explore", NodeType: rings.NodeStep,
				Title: "探索指纹", Status: "dispatched",
				Detail: map[string]any{"priority": "high", "action_level": "只读探测"}}},
		// 后派:反证(P4-08 ① 起派发即 high + 同级 tie-break)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-falsify", NodeType: rings.NodeStep,
				Title: "反证(第 1 轮): 未授权访问", Status: "dispatched",
				Detail: map[string]any{"kind": "falsify", "testclaim_id": "tc-1",
					"round": 1, "priority": "high", "action_level": "只读探测"}}},
	); err != nil {
		t.Fatalf("铺账本失败: %v", err)
	}

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != http.StatusOK {
		t.Fatalf("单位列表应 200: %d", code)
	}
	units, _ := out["units"].([]any)
	var order []string
	for _, u := range units {
		m, _ := u.(map[string]any)
		if m["queued"] == true {
			order = append(order, m["step_id"].(string))
		}
	}
	if len(order) != 2 {
		t.Fatalf("应恰 2 条排队条目: %v", units)
	}
	if order[0] != "s-falsify" || order[1] != "s-explore" {
		t.Fatalf("① 反证应排在 high 探索之前(面板排队顺序即领取顺序): %v", order)
	}
}
