package server

import (
	"fmt"
	"testing"

	"ghostreaper/internal/rings"
)

func TestP512ContinueOfferedOnlyForLatestManualPauseSession(t *testing.T) {
	s, mux, _ := newInterjectTestServer(t)
	const task = "p512-panel"
	events := []rings.Event{{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "created"}}}
	for i, status := range []string{rings.StepScopeWait, rings.StepGateWait, rings.StepPauseWait, rings.StepModelWait, "done"} {
		id := fmt.Sprintf("step%d", i)
		unit := fmt.Sprintf("u%d", i)
		session := "swarm-" + unit
		events = append(events,
			rings.Event{Kind: rings.EventNodeAdded, Actor: "overmind", Payload: rings.NodeAdded{NodeID: id, NodeType: rings.NodeStep, Title: "验收单位", Status: status}},
			rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit, Payload: rings.SwarmRecord{StepID: id, UnitID: unit, TranscriptID: session, Type: rings.SwarmRecSessionStarted}},
			rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:" + unit, Payload: rings.SwarmRecord{StepID: id, UnitID: unit, TranscriptID: session, Type: rings.SwarmRecSessionEnd, Note: "reason=pause_yield status=" + status + " turns=1"}},
		)
	}
	events = append(events, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u20", Payload: rings.SwarmRecord{StepID: "step2", UnitID: "u20", TranscriptID: "swarm-u20", Type: rings.SwarmRecSessionStarted}}, rings.Event{Kind: rings.EventSwarmRecord, Actor: "swarm:u20", Payload: rings.SwarmRecord{StepID: "step2", UnitID: "u20", TranscriptID: "swarm-u20", Type: rings.SwarmRecSessionEnd, Note: "reason=pause_yield status=pause_wait turns=2"}})
	if _, err := s.Store.Append(t.Context(), task, events...); err != nil {
		t.Fatal(err)
	}
	read := func() []any {
		t.Helper()
		code, body := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
		if code != 200 {
			t.Fatal(code)
		}
		return body["units"].([]any)
	}
	canContinue := 0
	for _, item := range read() {
		row := item.(map[string]any)
		if row["can_resume"] == true {
			canContinue++
			if row["step_id"] != "step2" || row["session_id"] != "swarm-u20" {
				t.Fatal("continue offered for another wait source")
			}
		}
	}
	if canContinue != 1 {
		t.Fatal("manual pause has no continue entry")
	}
	code, _ := callJSON(t, mux, "POST", "/api/tasks/"+task+"/units/step2/resume", `{}`)
	if code != 200 {
		t.Fatal("formal continue failed", code)
	}
	for _, item := range read() {
		if item.(map[string]any)["can_resume"] == true {
			t.Fatal("continue remained after its committed transition")
		}
	}
	code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/units/step1/resume", `{}`)
	if code == 200 {
		t.Fatal("action approval wait treated as manual pause")
	}
	appendEvents(t, s.Store, task, rings.Event{Kind: rings.EventNodeStateChanged, Actor: "swarm:u2", Payload: rings.NodeStateChanged{NodeID: "step2", Status: rings.StepPauseWait}}, rings.Event{Kind: rings.EventTaskLifecycle, Actor: "creator", Payload: rings.TaskLifecycle{Action: "hibernated"}})
	for _, item := range read() {
		if item.(map[string]any)["can_resume"] == true {
			t.Fatal("hibernated task exposed a continue action")
		}
	}
	code, _ = callJSON(t, mux, "POST", "/api/tasks/"+task+"/units/step2/resume", `{}`)
	if code == 200 {
		t.Fatal("hibernation was bypassed")
	}
}
