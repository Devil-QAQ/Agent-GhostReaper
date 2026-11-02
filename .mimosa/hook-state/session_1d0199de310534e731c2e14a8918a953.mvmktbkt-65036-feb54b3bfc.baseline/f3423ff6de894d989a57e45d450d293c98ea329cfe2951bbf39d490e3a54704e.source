package server

import (
	"net/http"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P4-02:单位列表的排队中条目 / 拒绝来源 / 会话时长账面 ----

// seedQueuedDenyTask 造三类事实:①派发未领取的步骤(排队中)②硬规则
// 拒绝终结的单位(拒绝来源随行)③原会话续跑链的两段 billable(账面合计)。
func seedQueuedDenyTask(t *testing.T, srv *Server) string {
	t.Helper()
	const task = "p402-task"
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P4-02 验证"}},
		// s1:派发未领取 → 排队中条目。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "全端口扫描与服务指纹识别", Status: "dispatched"}},
		// s2:单位领取 → 硬规则拒绝终结(原会话续跑链两段 billable)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "直连对照复测观测数据", Status: "running"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s2", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionStarted}},
		// 第一段落账(受阻保留会话,billable=3.5s)。
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s2", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionEnd, Resumable: true,
				Note: "reason=model_error turns=3 status=blocked billable=3.5s gate_wait=0s block_wait=2.0s pause_wait=0s"}},
		// 拒绝事件(拒绝来源溯源)。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1",
			Payload: rings.ApprovalRecord{RequestID: "ap-1", StepID: "s2", UnitID: "u1",
				Type: rings.ApprovalRecDenied, ActionLevel: "主动测试",
				Intent: "直连对照复测观测数据", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl --noproxy '*' http://127.0.0.1/"}`),
				DenyLine: rings.DenyLineHardRule, DecidedBy: "swarm:u1",
				Reason: "内置硬规则命中:代理旁路 · curl 绕过代理(命中片段 \"--noproxy\")"}},
		// 步骤终态 approval_denied + 第二段落账(billable=1.5s,同会话续跑)。
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s2", Status: "approval_denied"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s2", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionEnd,
				Note: "reason=approval_denied turns=5 status=approval_denied billable=1.5s gate_wait=0s block_wait=0s pause_wait=0s"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	return task
}

func TestUnitsListQueuedDenyBillable(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := seedQueuedDenyTask(t, srv)

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != http.StatusOK {
		t.Fatalf("单位列表应 200: %d %v", code, out)
	}
	units, _ := out["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("应列 2 行(1 会话 + 1 排队): %v", out)
	}

	// 会话行:拒绝来源 + billable 合计(3.5 + 1.5)。
	var row map[string]any
	for _, u := range units {
		if u.(map[string]any)["session_id"] == "swarm-u1" {
			row = u.(map[string]any)
		}
	}
	if row == nil {
		t.Fatalf("缺会话行: %v", units)
	}
	if row["status"] != "approval_denied" || row["deny_line"] != "hard_rule" {
		t.Fatalf("拒绝来源应随行: %v", row)
	}
	if want := "内置硬规则命中:代理旁路 · curl 绕过代理(命中片段 \"--noproxy\")"; row["deny_reason"] != want {
		t.Fatalf("拒绝原因应原样随行: %v", row["deny_reason"])
	}
	if row["billable_seconds"] != 5.0 {
		t.Fatalf("续跑链 billable 应合计 3.5+1.5=5.0: %v", row["billable_seconds"])
	}

	// 排队行:派发即可见,步骤状态 + 意图标题,无会话锚。
	var queued map[string]any
	for _, u := range units {
		if u.(map[string]any)["step_id"] == "s1" {
			queued = u.(map[string]any)
		}
	}
	if queued == nil || queued["queued"] != true {
		t.Fatalf("缺排队中条目: %v", units)
	}
	if queued["status"] != "dispatched" || queued["step_title"] != "全端口扫描与服务指纹识别" {
		t.Fatalf("排队条目应带意图摘要与真实步骤状态: %v", queued)
	}
	if queued["running"] != false || queued["billable_seconds"] != 0.0 {
		t.Fatalf("排队条目不应在跑: %v", queued)
	}
}

// TestHibernateSecondsInView 牧者/主宰视图携带冬眠抵扣(含进行中的
// 冬眠):无冬眠为 0,入眠后 >0 且随时间增长。
func TestHibernateSecondsInView(t *testing.T) {
	srv, mux, gate := newInterjectTestServer(t)
	const task = "hib-view-task"
	seedIJUnit(t, srv, task)

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/overmind", "")
	if code != http.StatusOK || out["hibernate_seconds"] != 0.0 {
		t.Fatalf("无冬眠抵扣应为 0: %d %v", code, out["hibernate_seconds"])
	}

	// 入眠(先记账后关闸,与 Manager 同纪律)。
	if _, err := srv.Store.Append(t.Context(), task, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "creator",
		Payload: rings.TaskLifecycle{Action: "hibernated"},
	}); err != nil {
		t.Fatalf("入眠记账失败: %v", err)
	}
	gate.Enter(task, "started")
	time.Sleep(120 * time.Millisecond)

	code, out = callJSON(t, mux, "GET", "/api/tasks/"+task+"/shepherd", "")
	if code != http.StatusOK {
		t.Fatalf("牧者视图应 200: %d", code)
	}
	if sec, ok := out["hibernate_seconds"].(float64); !ok || sec < 0.1 {
		t.Fatalf("冬眠中抵扣应计入进行中的区间: %v", out["hibernate_seconds"])
	}
	if out["hibernated"] != true {
		t.Fatalf("应呈冬眠中: %v", out["hibernated"])
	}
}
