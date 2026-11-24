package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"ghostreaper/internal/webui"
)

func TestResumedSessionsRemainVisibleWithHistoryAndQueue(t *testing.T) {
	srv, h, _ := newInterjectTestServer(t)
	const task = "session-resume-panel"
	mustAppendP506(t, srv, task,
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "created", Title: "虫群恢复显示验收"}},
		rings.Event{Kind: rings.EventTaskLifecycle, Actor: "test", Payload: rings.TaskLifecycle{Action: "started"}},
	)
	var latest []string
	for i, reason := range []string{"checkpointed", "model_blocked", "approval_yield", "adopted", "legacy_checkpoint"} {
		step, sid := fmt.Sprintf("s%d", i+1), fmt.Sprintf("swarm-original-%d", i+1)
		mustAppendP506(t, srv, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: step, NodeType: rings.NodeStep, Title: fmt.Sprintf("恢复核对资料 %d", i+1), Status: "running"}})
		note := "reason=" + reason + " turns=4 status=dispatched billable=1.5s"
		if reason == "model_blocked" {
			note = "reason=model_blocked turns=4 status=model_wait billable=1.5s"
		}
		if reason == "approval_yield" {
			note = "reason=approval_yield turns=4 status=gate_wait billable=1.5s"
		}
		if reason == "adopted" {
			note = "" // Previous process exited without a closeout.
		}
		seedP506Session(t, srv, task, step, "u-old-"+step, sid, note)
		if i == 0 {
			// A newer separate conversation must stay historical when the
			// original conversation is subsequently resumed.
			seedP506Session(t, srv, task, step, "u-history", "swarm-history", "reason=completed turns=2 status=done billable=2s")
		}
		if reason != "legacy_checkpoint" {
			seedP506Session(t, srv, task, step, "u-current-"+step, sid, "")
		}
		latest = append(latest, sid)
	}
	for i := 1; i <= 3; i++ {
		mustAppendP506(t, srv, task, rings.Event{Kind: rings.EventNodeAdded, Actor: "test", Payload: rings.NodeAdded{NodeID: fmt.Sprintf("q%d", i), NodeType: rings.NodeStep, Title: fmt.Sprintf("待领取资料 %d", i), Status: "dispatched"}})
	}
	check := func() {
		t.Helper()
		var result struct {
			Units []UnitRow `json:"units"`
		}
		get(t, h, "/api/tasks/"+task+"/units", 200, &result)
		if len(result.Units) != 9 {
			t.Fatal("session was swallowed or queue duplicated it", result.Units)
		}
		bySession := map[string]UnitRow{}
		running, queued := 0, 0
		for _, row := range result.Units {
			if row.SessionID != "" {
				bySession[row.SessionID] = row
			}
			if row.Running {
				running++
			}
			if row.Queued {
				queued++
			}
		}
		if running != 5 || queued != 3 {
			t.Fatal("list diverged from the actual step states", running, queued, result.Units)
		}
		for i, sid := range latest {
			row := bySession[sid]
			if !row.Running || row.Status != "running" || row.Finish != nil || row.FinishedAt != "" {
				t.Fatal("resumed session retained a closeout", row)
			}
			if i < 4 && row.UnitID != fmt.Sprintf("u-current-s%d", i+1) {
				t.Fatal("current unit identity was not refreshed", row)
			}
		}
		if history := bySession["swarm-history"]; history.Running || history.Status != "done" || history.Finish == nil {
			t.Fatal("history was revived as an active session", history)
		}
		_, newest, ok, err := srv.Store.StepLatestSession(t.Context(), task, "s1")
		if err != nil || !ok || newest != latest[0] {
			t.Fatal("latest session still follows first creation order", newest, err)
		}
	}
	check()
	if err := srv.Store.RebuildProjections(t.Context()); err != nil {
		t.Fatal(err)
	}
	check()
	// Opt-in formal UI acceptance, with no scheduler or model attached.
	if ready := os.Getenv("GHOSTREAPER_SESSION_PANEL_READY"); ready != "" {
		h.Handle("/", webui.Handler())
		ts := httptest.NewServer(h)
		defer ts.Close()
		info, _ := json.Marshal(map[string]string{"url": ts.URL, "task": task})
		if err := os.WriteFile(ready, info, 0600); err != nil {
			t.Fatal(err)
		}
		stop := os.Getenv("GHOSTREAPER_SESSION_PANEL_STOP")
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(stop); err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatal("isolated session panel acceptance timed out")
	}
}
