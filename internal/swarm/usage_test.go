package swarm

import (
	"context"
	"encoding/json"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/tool"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/rings"
)

func TestRequestUsageAcrossPromptsAndBudget(t *testing.T) {
	store := openStore(t, t.TempDir())
	const task = "usage-segments"
	tracker := &turnBudgetTracker{Store: store, TaskID: task, Agent: "swarm:u1", MaxTurns: 4, StepID: "s1", UnitID: "u1"}
	want := []llm.Usage{
		{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 30},
		{InputTokens: 150, OutputTokens: 30, CacheReadTokens: 100},
		{InputTokens: 70, OutputTokens: 4, CacheWriteTokens: 50},
		{InputTokens: 80, OutputTokens: 5, CacheReadTokens: 40},
	}
	calls := 0
	raw := auxTestProvider{stream: func(context.Context) iter.Seq2[llm.StreamEvent, error] {
		return func(yield func(llm.StreamEvent, error) bool) {
			u := want[calls]
			calls++
			if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "part"}, nil) {
				return
			}
			stop := "end_turn"
			if calls%2 == 1 {
				stop = "max_tokens"
			}
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: u, StopReason: stop}, nil)
		}
	}}
	model := &activeModelProvider{Provider: raw}
	model.now.Store("primary")
	provider := MeterProvider(model, store, rings.TokenUsage{TaskID: task, Agent: "swarm:u1", TranscriptID: "swarm-u1"})
	sess := agentcore.NewSession(agentcore.Options{Provider: provider, SessionID: "swarm-u1", Tools: []tool.CoreTool{}, MaxTurns: 2, DisableBackgroundTasks: true})
	defer sess.Close()
	for i := 0; i < 2; i++ {
		if i == 1 {
			model.now.Store("fallback")
		}
		for ev, err := range sess.Prompt(context.Background(), "continue") {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Kind == harness.KindUsage {
				if err := tracker.OnTurn(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	rows, err := store.TokenUsageRows(context.Background(), tracker.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) || tracker.Turns() != len(want) {
		t.Fatalf("rows=%d turns=%d want=%d", len(rows), tracker.Turns(), len(want))
	}
	for i, row := range rows {
		got := llm.Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheWriteTokens: row.CacheWriteTokens}
		model := "primary"
		if i >= 2 {
			model = "fallback"
		}
		if got != want[i] || row.Turn != i+1 || row.Model != model || row.TranscriptID != "swarm-u1" {
			t.Errorf("call %d: got %+v want usage=%+v model=%s", i+1, row, want[i], model)
		}
	}
	events, err := store.Events(context.Background(), tracker.TaskID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	half := 0
	for _, event := range events {
		if event.Kind != rings.EventBlackboxRecord {
			continue
		}
		var record rings.BlackboxRecord
		if err := json.Unmarshal(event.Payload, &record); err != nil {
			t.Fatal(err)
		}
		if record.Type == rings.BlackboxBudgetHalf {
			half++
		}
	}
	if half != 1 {
		t.Fatalf("budget-half events=%d want=1 across both segments", half)
	}
}
