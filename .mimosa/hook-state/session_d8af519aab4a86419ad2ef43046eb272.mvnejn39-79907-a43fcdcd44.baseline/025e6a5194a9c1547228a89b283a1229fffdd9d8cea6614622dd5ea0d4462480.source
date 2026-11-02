package server

import (
	"net/http"
	"testing"

	"ghostreaper/internal/rings"
)

// ---- P4-06 ②:收场信息结构化随行,时间账 note 原文不出账本 ----

// seedFinishTask 造三只落地单位:正常完成 / 受阻(终因是含空格的自然
// 语句)/ 总闸到点收场——覆盖 noteReason 不能按空格切的形状。
func seedFinishTask(t *testing.T, srv *Server) string {
	t.Helper()
	const task = "p406-task"
	end := func(step, unit, transcript, note string) rings.Event {
		return rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit,
			Payload: rings.SwarmRecord{StepID: step, UnitID: unit, TranscriptID: transcript,
				Type: rings.SwarmRecSessionEnd, Note: note}}
	}
	start := func(step, unit, transcript string) rings.Event {
		return rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit,
			Payload: rings.SwarmRecord{StepID: step, UnitID: unit, TranscriptID: transcript,
				Type: rings.SwarmRecSessionStarted}}
	}
	_, err := srv.Store.Append(t.Context(), task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "manager",
			Payload: rings.TaskLifecycle{Action: "created", Title: "P4-06 验证"}},
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s1", NodeType: rings.NodeStep,
				Title: "指纹识别", Status: "done"}},
		start("s1", "u1", "swarm-u1"),
		end("s1", "u1", "swarm-u1",
			"reason=completed turns=18 status=done billable=192.0s gate_wait=0s block_wait=0s pause_wait=0s"),
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s2", NodeType: rings.NodeStep,
				Title: "深挖目录", Status: "blocked"}},
		start("s2", "u2", "swarm-u2"),
		end("s2", "u2", "swarm-u2",
			"reason=模型连续失败(2 次): dial tcp 127.0.0.1:9: connect: connection refused turns=7 status=blocked billable=3.5s gate_wait=0s block_wait=2.0s pause_wait=0s"),
		rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind",
			Payload: rings.NodeAdded{NodeID: "s3", NodeType: rings.NodeStep,
				Title: "全端口扫描", Status: "exhausted"}},
		start("s3", "u3", "swarm-u3"),
		end("s3", "u3", "swarm-u3",
			"reason=timeout turns=9 status=exhausted billable=9.9s gate_wait=0s block_wait=0s pause_wait=0s"),
	)
	if err != nil {
		t.Fatalf("造数失败: %v", err)
	}
	return task
}

func TestUnitsFinishView(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	task := seedFinishTask(t, srv)

	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != http.StatusOK {
		t.Fatalf("单位列表应 200: %d %v", code, out)
	}
	units, _ := out["units"].([]any)
	if len(units) != 3 {
		t.Fatalf("应列 3 行: %v", out)
	}
	rows := map[string]map[string]any{}
	for _, u := range units {
		r := u.(map[string]any)
		rows[r["step_id"].(string)] = r
	}

	// note 原文零出账本:行上无 finish_note 键(P4-06 ② 界面零入口)。
	for id, r := range rows {
		if _, has := r["finish_note"]; has {
			t.Fatalf("%s 行不应再携带 note 原文: %v", id, r["finish_note"])
		}
	}

	// 正常完成:轮次与终因随行,状态与计费可组装人话状态行。
	f1, _ := rows["s1"]["finish"].(map[string]any)
	if f1 == nil || f1["turns"] != float64(18) || f1["reason"] != "completed" {
		t.Fatalf("s1 收场信息应结构化随行: %v", rows["s1"]["finish"])
	}
	if rows["s1"]["status"] != "done" || rows["s1"]["billable_seconds"] != 192.0 {
		t.Fatalf("s1 终态与计费: %v", rows["s1"])
	}

	// 受阻终因是含空格的自然语句:整段取回,不能按空格截断。
	f2, _ := rows["s2"]["finish"].(map[string]any)
	if f2 == nil || f2["turns"] != float64(7) {
		t.Fatalf("s2 轮次应随行: %v", rows["s2"]["finish"])
	}
	if want := "模型连续失败(2 次): dial tcp 127.0.0.1:9: connect: connection refused"; f2["reason"] != want {
		t.Fatalf("s2 终因应整段取回(含空格): %q", f2["reason"])
	}

	// 总闸到点:终因 timeout 随行(P4-01 后 exhausted 落地仅剩总闸场景)。
	f3, _ := rows["s3"]["finish"].(map[string]any)
	if f3 == nil || f3["reason"] != "timeout" || f3["turns"] != float64(9) {
		t.Fatalf("s3 收场信息: %v", rows["s3"]["finish"])
	}
	if rows["s3"]["status"] != "exhausted" || rows["s3"]["billable_seconds"] != 9.9 {
		t.Fatalf("s3 终态与计费: %v", rows["s3"])
	}
}
