package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/shepherd"
)

func TestAuxiliaryUsageIncludedInTotalsButNotContextRatio(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "aux-info"
	seedIJUnit(t, srv, task)
	if err := failover.SaveModelConfig(t.Context(), srv.Store, failover.ModelConfig{
		Name: "main", Format: "openai", BaseURL: "http://local.invalid", APIKey: "test", Model: "main-model", ContextWindow: 10000,
	}, "creator"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []struct{ agent, sid string }{
		{"overmind", "overmind-main"}, {"swarm:u1", "swarm-u1"}, {"shepherd", shepherd.SessionID(task)},
	} {
		if err := srv.Store.RecordTokens(t.Context(),
			rings.TokenUsage{TaskID: task, Agent: who.agent, TranscriptID: who.sid, Model: "main-model", Turn: 1, InputTokens: 3000, OutputTokens: 50},
			rings.TokenUsage{TaskID: task, Agent: who.agent, TranscriptID: who.sid, Model: "aux-model", Purpose: "compaction", InputTokens: 100000, OutputTokens: 10, CacheReadTokens: 400},
			rings.TokenUsage{TaskID: task, Agent: who.agent, TranscriptID: who.sid, Model: "aux-model", Purpose: "triage", UsageMissing: true},
		); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	check := func(name string, calls, input, output int, model string, window int, ratio float64) {
		t.Helper()
		if calls != 3 || input != 103000 || output != 60 || model != "main-model" || window != 10000 || ratio != 0.3 {
			t.Fatalf("%s costs/context mixed: calls=%d in=%d out=%d model=%s window=%d ratio=%f", name, calls, input, output, model, window, ratio)
		}
	}
	i := srv.overmindInfo(r, task)
	check("overmind", i.Calls, i.InputTokens, i.OutputTokens, i.Model, i.ContextWindow, i.ContextRatio)
	s := srv.shepherdInfo(r, task)
	check("shepherd", s.Calls, s.InputTokens, s.OutputTokens, s.Model, s.ContextWindow, s.ContextRatio)
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != 200 {
		t.Fatalf("units status=%d out=%v", code, out)
	}
	var units struct {
		Units []struct {
			Info sessionInfoBar `json:"info"`
		} `json:"units"`
	}
	b, _ := json.Marshal(out)
	if err := json.Unmarshal(b, &units); err != nil || len(units.Units) != 1 {
		t.Fatalf("units=%s err=%v", b, err)
	}
	u := units.Units[0].Info
	check("unit", u.Calls, u.InputTokens, u.OutputTokens, u.Model, u.ContextWindow, u.ContextRatio)
	code, out = callJSON(t, mux, "GET", "/api/obs/tokens", "")
	if code != 200 {
		t.Fatal(out)
	}
	totals := out["totals"].(map[string]any)
	if totals["calls"] != float64(9) || totals["input"] != float64(309000) || totals["output"] != float64(180) || totals["usage_missing"] != float64(3) {
		t.Fatalf("wrong global totals: %v", totals)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/tasks/"+task+"/tokens", nil))
	var rows []struct {
		Purpose      string `json:"purpose"`
		UsageMissing bool   `json:"usage_missing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 9 || rows[1].Purpose != "compaction" || !rows[2].UsageMissing {
		t.Fatalf("purpose/missing absent from official API: %s err=%v", w.Body.String(), err)
	}
}

func TestInterruptedInputUpdatesContextButUnknownDoesNot(t *testing.T) {
	srv, mux, _ := newInterjectTestServer(t)
	const task = "interrupted-info"
	seedIJUnit(t, srv, task)
	if err := failover.SaveModelConfig(t.Context(), srv.Store, failover.ModelConfig{Name: "main", Format: "openai", BaseURL: "http://local.invalid", APIKey: "test", Model: "main-model", ContextWindow: 10000}, "creator"); err != nil {
		t.Fatal(err)
	}
	if err := failover.SaveModelConfig(t.Context(), srv.Store, failover.ModelConfig{Name: "other", Format: "openai", BaseURL: "http://local.invalid", APIKey: "test", Model: "other-model", ContextWindow: 20000}, "creator"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct{ agent, sid string }{
		{"overmind", "overmind-main"}, {"shepherd", shepherd.SessionID(task)}, {"swarm:u1", "swarm-u1"}, {"interject:overmind", "interject-overmind"},
	} {
		if err := srv.Store.RecordTokens(t.Context(),
			rings.TokenUsage{TaskID: task, Agent: owner.agent, TranscriptID: owner.sid, Model: "main-model", InputTokens: 1000, Turn: 1},
			rings.TokenUsage{TaskID: task, Agent: owner.agent, TranscriptID: owner.sid, Model: "other-model", InputTokens: 300, Purpose: "interrupted"},
			rings.TokenUsage{TaskID: task, Agent: owner.agent, TranscriptID: owner.sid, Model: "missing-model", UsageMissing: true, Turn: 2},
		); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	check := func(name string, calls, input, window int, ratio float64, model string) {
		t.Helper()
		if calls != 3 || input != 1300 || window != 20000 || ratio != 0.015 || model != "other-model" {
			t.Fatalf("%s costs/context mixed: calls=%d input=%d window=%d ratio=%f model=%s", name, calls, input, window, ratio, model)
		}
	}
	a := srv.overmindInfo(r, task)
	check("overmind", a.Calls, a.InputTokens, a.ContextWindow, a.ContextRatio, a.Model)
	b := srv.shepherdInfo(r, task)
	check("shepherd", b.Calls, b.InputTokens, b.ContextWindow, b.ContextRatio, b.Model)
	c := srv.interjectInfo(r, task, "overmind", "")
	check("interject", c.Calls, c.InputTokens, c.ContextWindow, c.ContextRatio, c.Model)
	code, out := callJSON(t, mux, "GET", "/api/tasks/"+task+"/units", "")
	if code != 200 {
		t.Fatal(out)
	}
	var units struct {
		Units []struct {
			Info sessionInfoBar `json:"info"`
		} `json:"units"`
	}
	data, _ := json.Marshal(out)
	if err := json.Unmarshal(data, &units); err != nil || len(units.Units) != 1 {
		t.Fatalf("units=%s err=%v", data, err)
	}
	d := units.Units[0].Info
	check("unit", d.Calls, d.InputTokens, d.ContextWindow, d.ContextRatio, d.Model)
}
