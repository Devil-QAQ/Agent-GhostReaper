package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ghostreaper/internal/rings"
)

// ---- P5-06 虫群会话列表四段式与水位:类级数据面断言 ----

// mustAppendP506 落账(失败即 Fatal)。
func mustAppendP506(t *testing.T, srv *Server, task string, evs ...rings.Event) {
	t.Helper()
	if _, err := srv.Store.Append(t.Context(), task, evs...); err != nil {
		t.Fatalf("落账失败: %v", err)
	}
}

// seedP506Session 造单位会话:started 起、endNote 非空时收场(note 的
// status= 字段进 unit_sessions 投影,与会话列表读侧同口径)。
func seedP506Session(t *testing.T, srv *Server, task, step, unit, transcript, endNote string) {
	t.Helper()
	evs := []rings.Event{
		{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit,
			Payload: rings.SwarmRecord{StepID: step, UnitID: unit, TranscriptID: transcript,
				Type: rings.SwarmRecSessionStarted}},
	}
	if endNote != "" {
		evs = append(evs, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit,
			Payload: rings.SwarmRecord{StepID: step, UnitID: unit, TranscriptID: transcript,
				Type: rings.SwarmRecSessionEnd, Note: endNote}})
	}
	mustAppendP506(t, srv, task, evs...)
}

// TestP506WatermarkEndpoint 水位轻量接口类级:六数字多形态(执行中/
// 可领取排队/等前置/停等四族等待)＋载荷极简(只回水位数字,无图节点
// 边——不再全量拉图的请求载荷对照)＋graph 同源对账＋未知任务 404。
func TestP506WatermarkEndpoint(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p506-water"
	mustAppendP506(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "水位"}},
		// 2 执行中。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "r1", NodeType: rings.NodeStep, Status: "running"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "r2", NodeType: rings.NodeStep, Status: "running"}},
		// 1 可领取排队 + 1 等前置(依赖未完成)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "q1", NodeType: rings.NodeStep, Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "q2", NodeType: rings.NodeStep, Status: "dispatched"}},
		rings.Event{Kind: rings.EventEdgeAdded, Actor: "overmind",
			Payload: rings.EdgeAdded{EdgeType: rings.EdgeDependsOn, FromNode: "q2", ToNode: "q1"}},
		// 停等四族各一(挂等让位趴窝 → 等待 N)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "w1", NodeType: rings.NodeStep, Status: rings.StepScopeWait}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "w2", NodeType: rings.NodeStep, Status: rings.StepGateWait}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "w3", NodeType: rings.NodeStep, Status: rings.StepPauseWait}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "w4", NodeType: rings.NodeStep, Status: rings.StepModelWait}},
	)

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/watermark", "")
	if code != http.StatusOK {
		t.Fatalf("水位应 200: %d %v", code, out)
	}
	if got := out["running"]; got != float64(2) {
		t.Fatalf("执行中应 2: %v", out)
	}
	if got := out["queued"]; got != float64(1) {
		t.Fatalf("排队应 1(只计可领取): %v", out)
	}
	if got := out["waiting"]; got != float64(1) {
		t.Fatalf("等前置应 1: %v", out)
	}
	if got := out["hold"]; got != float64(4) {
		t.Fatalf("等待应 4(停等四族): %v", out)
	}
	if got := out["available"]; got != float64(4) { // 8 - 2 - 1 - 1(趴窝不占容量)
		t.Fatalf("还可新增应 4: %v", out)
	}
	if got := out["limit"]; got != float64(8) {
		t.Fatalf("派发上限应默认 8: %v", out)
	}
	// 载荷极简:只回水位六个数字,无 nodes/edges(轻量接口纪律——
	// 请求载荷对照:不再全量拉图)。
	for _, key := range []string{"nodes", "edges", "queue"} {
		if _, ok := out[key]; ok {
			t.Fatalf("水位载荷不应含 %q(只回数字): %v", key, out)
		}
	}
	if len(out) != 6 {
		t.Fatalf("水位载荷应恰 6 个数字键: %v", out)
	}

	// 与 graph 接口同源对账:同一任务 graph.queue 数字一致(拆接口不分叉)。
	gcode, gout := callJSON(t, mux, "GET", "/api/tasks/"+task+"/graph", "")
	if gcode != http.StatusOK {
		t.Fatalf("图投影应 200: %d", gcode)
	}
	gq, _ := gout["queue"].(map[string]any)
	if gq == nil || gq["hold"] != out["hold"] || gq["queued"] != out["queued"] || gq["running"] != out["running"] {
		t.Fatalf("graph.queue 与水位轻量接口数字应同源: %v vs %v", gq, out)
	}

	// 未知任务 404。
	if code, _ = callJSON(t, mux, "GET", "/api/tasks/no-such/watermark", ""); code != http.StatusNotFound {
		t.Fatalf("未知任务应 404: %d", code)
	}
}

// TestP506UnitsQueueSegmentUnified /units 四段式数据面类级:排队段
// (让位停等/已唤醒会话行 + dispatched 排队条目)统一上位序殿后且
// 同步骤不双行(已唤醒单位以会话锚行呈现);其余会话行保持原序在前;
// last_active 活动时刻随行(最近调用/开始/收场取最大)。
func TestP506UnitsQueueSegmentUnified(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p506-units"
	mustAppendP506(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "四段"}},
		// 建步骤序(版本号序:s-run < s-done < s-woken < s-park < q1 < q2;
		// s-woken 随建带队首档 detail——状态迁移不改 detail)。
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-run", NodeType: rings.NodeStep, Title: "在跑", Status: "running"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-done", NodeType: rings.NodeStep, Title: "完成", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-woken", NodeType: rings.NodeStep, Title: "已唤醒待领取",
				Status: "running", Detail: map[string]any{"scope_head": true, "head_seq": float64(42)}}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-park", NodeType: rings.NodeStep, Title: "暂停让位趴窝", Status: "running"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "q1", NodeType: rings.NodeStep, Title: "普通排队", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "q2", NodeType: rings.NodeStep, Title: "普通排队二", Status: "dispatched"}},
	)
	// s-run 造"让位收场后重领上位"形态:会话 end_status=model_wait
	//(让位收场落账),步骤随后 model_wait→dispatched→running(重领
	// 原会话续跑)。tr-done 正常收场。
	seedP506Session(t, srv, task, "s-run", "u1", "tr-run", "reason=模型故障 turns=4 status=model_wait billable=12s")
	seedP506Session(t, srv, task, "s-done", "u2", "tr-done", "reason=completed turns=3 status=done billable=10s")
	seedP506Session(t, srv, task, "s-woken", "u3", "tr-woken", "reason=审批宽限超时 turns=2 status=gate_wait billable=8s")
	seedP506Session(t, srv, task, "s-park", "u4", "tr-park", "reason=暂停 turns=1 status=pause_wait billable=5s")
	// 步骤状态迁移:s-woken 让位(gate_wait)后已批准唤醒回 dispatched
	//(队首档压队首);s-park 仍在停等;s-run2 让位重领已上位(步骤回
	// running,原会话续跑——投影行残留收场列,行须复位执行中)。
	mustAppendP506(t, srv, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-woken", Status: rings.StepGateWait}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-woken", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-park", Status: rings.StepPauseWait}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-run", Status: rings.StepModelWait}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-run", Status: "dispatched"}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-run", Status: "running"}},
	)
	// 在跑单位的最近一次模型调用(活动时刻数据源):未来 1 小时,确保
	// last_active 取调用时刻为最大。
	future := time.Now().Add(time.Hour)
	if err := srv.Store.RecordTokens(t.Context(), rings.TokenUsage{
		TaskID: task, Agent: "swarm:u1", TranscriptID: "tr-run",
		Model: "m", InputTokens: 10, OutputTokens: 2, OccurredAt: future,
	}); err != nil {
		t.Fatalf("铺 token 行失败: %v", err)
	}

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != http.StatusOK {
		t.Fatalf("单位列表应 200: %d %v", code, out)
	}
	raw, _ := json.Marshal(out["units"])
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 6 {
		t.Fatalf("应列 6 行(6 步骤各一行): %d %v", len(rows), out)
	}
	got := stepsOfP506(rows)

	// 非排队会话行保持开始序在前;排队段殿后且统一上位序:
	// s-woken(队首档,会话锚行)→ s-park(停等趴窝,会话锚行;无队首
	// 档按版本号序先于后建排队条目)→ q1 → q2(排队条目)。
	want := []string{"s-run", "s-done", "s-woken", "s-park", "q1", "q2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("行序不符(期望 非排队会话在前+排队段上位序殿后): %v", got)
		}
	}
	// 同步骤不双行:已唤醒的让位单位以会话锚行呈现(带会话锚、无
	// finish),不再另出排队条目。
	woken := rows[2]
	if woken["session_id"] != "tr-woken" || woken["status"] != "dispatched" {
		t.Fatalf("已唤醒单位应为会话锚行(status=dispatched): %v", woken)
	}
	if _, has := woken["finish"]; has {
		t.Fatalf("已唤醒会话行不应带收场信息: %v", woken)
	}
	var queuedSteps []string
	for _, r := range rows {
		if q, _ := r["queued"].(bool); q {
			s, _ := r["step_id"].(string)
			queuedSteps = append(queuedSteps, s)
		}
	}
	if len(queuedSteps) != 2 || queuedSteps[0] != "q1" || queuedSteps[1] != "q2" {
		t.Fatalf("排队条目应恰 q1/q2 两行(会话锚行不双出): %v", queuedSteps)
	}
	park := rows[3]
	if park["session_id"] != "tr-park" || park["status"] != rings.StepPauseWait {
		t.Fatalf("停等趴窝应为会话锚行(status=pause_wait): %v", park)
	}

	// last_active:重领上位单位取 max(最近调用,开始,收场残留)——调用
	// 时刻在未来,应恰为该时刻(finished_at 已随复位清空);已完成单位
	// 取收场时刻(与 finished_at 同值);排队条目无活动时刻。
	if rows[0]["running"] != true {
		t.Fatalf("让位重领上位行应复位 running=true: %v", rows[0])
	}
	if _, has := rows[0]["finish"]; has {
		t.Fatalf("上位行不应残留收场信息: %v", rows[0])
	}
	if fa, _ := rows[0]["finished_at"].(string); fa != "" {
		t.Fatalf("上位行 finished_at 应清空: %q", fa)
	}
	if la, _ := rows[0]["last_active"].(string); la != future.Local().Format("2006-01-02 15:04:05") {
		t.Fatalf("上位行 last_active 应为最近调用时刻: %q", la)
	}
	laDone, _ := rows[1]["last_active"].(string)
	faDone, _ := rows[1]["finished_at"].(string)
	if laDone != faDone || laDone == "" {
		t.Fatalf("已完成行 last_active 应为收场时刻: %q vs %q", laDone, faDone)
	}
	if la, _ := rows[4]["last_active"].(string); la != "" {
		t.Fatalf("排队条目不应带活动时刻: %q", la)
	}
}

// TestP506UnitsQueueDedupOnWokenStep 已唤醒步骤双形态回归:停等会话
// 行代步骤入排队段后,dispatched 排队条目不再重复出该步骤——同一账
// 本形态(旧终态会话 + 步骤重派发)对照:旧终态会话留已完成口径,
// 排队条目照出(历史会话不代表当前排队)。
func TestP506UnitsQueueDedupOnWokenStep(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "p506-dedup"
	mustAppendP506(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "去重"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-old", NodeType: rings.NodeStep, Title: "旧完成", Status: "done"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s-wait", NodeType: rings.NodeStep, Title: "趴窝", Status: "running"}},
	)
	seedP506Session(t, srv, task, "s-old", "u1", "tr-old", "reason=completed turns=2 status=done billable=3s")
	seedP506Session(t, srv, task, "s-wait", "u2", "tr-wait", "reason=暂停 turns=1 status=pause_wait billable=2s")
	// s-old 步骤重派发(旧会话已终态);s-wait 停等中。
	mustAppendP506(t, srv, task,
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-wait", Status: rings.StepPauseWait}},
		rings.Event{Kind: rings.EventNodeStateChanged, Actor: "overmind",
			Payload: rings.NodeStateChanged{NodeID: "s-old", Status: "dispatched"}},
	)

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != http.StatusOK {
		t.Fatalf("单位列表应 200: %d %v", code, out)
	}
	raw, _ := json.Marshal(out["units"])
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 3 {
		t.Fatalf("应列 3 行(旧终态会话 + 重派发排队条目 + 趴窝会话锚行,无重复双出): %d %v", len(rows), out)
	}
	got := stepsOfP506(rows)
	// 旧终态会话行(tr-old,done)留前;排队段殿后:重派发排队条目在前
	//(创建序早于 s-wait)。
	if got[0] != "s-old" || got[1] != "s-old" || got[2] != "s-wait" {
		t.Fatalf("行序不符: %v", got)
	}
	if rows[0]["status"] != "done" || rows[0]["session_id"] != "tr-old" {
		t.Fatalf("旧行应保持终态会话锚: %v", rows[0])
	}
	if q, _ := rows[1]["queued"].(bool); !q || rows[1]["session_id"] != "" {
		t.Fatalf("重派发步骤应以排队条目呈现(无会话锚): %v", rows[1])
	}
	// 趴窝步骤:会话锚行恰一行,无排队条目双出(恰 3 行证明——停等
	// 步骤 s-wait 不进 dispatched 条目集)。
	if rows[2]["session_id"] != "tr-wait" || rows[2]["status"] != rings.StepPauseWait {
		t.Fatalf("趴窝应以会话锚行呈现(status=pause_wait): %v", rows[2])
	}
}

func stepsOfP506(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		s, _ := r["step_id"].(string)
		out = append(out, s)
	}
	return out
}
