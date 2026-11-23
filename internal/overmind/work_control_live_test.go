package overmind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ghostreaper/internal/rings"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Real model decisions against synthetic ledger states. This uses the actual
// decompose path; it does not execute target tools or prove autonomous findings.
func TestWorkControlLiveDecisions(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_WORK_REVIEW_MODEL_FILE")
	if file == "" {
		t.Skip("private decision-model configuration not supplied")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("cannot read private model configuration")
	}
	var cfg struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		Format  string `json:"format"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private model configuration")
	}
	provider, err := llm.NewProvider(llm.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model, Format: llm.Format(cfg.Format), MaxRetries: -1})
	if err != nil {
		t.Fatal("cannot initialize private model provider")
	}
	type sample struct {
		Round     int               `json:"round"`
		Scenario  string            `json:"scenario"`
		Expected  string            `json:"expected"`
		Controls  []workControlPlan `json:"controls"`
		Snapshot  string            `json:"actual_snapshot"`
		ModelText string            `json:"model_text"`
		Thinking  string            `json:"returned_thinking,omitempty"`
		Calls     int               `json:"calls"`
		Input     int               `json:"input_tokens"`
		Output    int               `json:"output_tokens"`
		CacheRead int               `json:"cache_read_tokens"`
		Unknown   int               `json:"unknown_usage"`
		ElapsedMS int64             `json:"elapsed_ms"`
		Error     string            `json:"error,omitempty"`
	}
	var report []sample
	save := func() {
		if path := os.Getenv("GHOSTREAPER_WORK_REVIEW_EVIDENCE"); path != "" {
			body, _ := json.MarshalIndent(report, "", "  ")
			if os.WriteFile(path, append(body, '\n'), 0600) != nil {
				t.Fatal("cannot save sanitized model evidence")
			}
		}
	}
	for round := 1; round <= 3; round++ {
		for _, scenario := range []struct{ name, expected string }{{"progress", "continue"}, {"repeat", "steer"}, {"invalid", "stop"}} {
			l, _ := workReviewFixture(t, scenario.name)
			l.Provider = provider
			l.Model = cfg.Model
			l.AssessWall = 45 * time.Second
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			start := time.Now()
			reply, _, callErr := l.decompose(ctx)
			cancel()
			row := sample{Round: round, Scenario: scenario.name, Expected: scenario.expected, ElapsedMS: time.Since(start).Milliseconds()}
			if reply != nil {
				row.Controls = reply.WorkControls
			}
			if callErr != nil {
				row.Error = "planning request or review contract failed; private error details withheld"
			}
			// Read the actual user input and model text from the formal transcript.
			events, err := l.Store.Events(t.Context(), l.TaskID, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			sid := ""
			for _, ev := range events {
				if ev.Kind == rings.EventOvermindRecord {
					var rec rings.OvermindRecord
					if json.Unmarshal(ev.Payload, &rec) == nil && rec.Type == rings.OvermindRecPlanning {
						sid = rec.SessionID
					}
				}
			}
			ts := transcript.NewStore(filepath.Join(l.RootDir, l.TaskID, "transcripts"))
			records, err := ts.Load(ts.MainPath(sid))
			if err != nil {
				t.Fatal("cannot read fixed-scenario transcript")
			}
			for _, record := range records {
				if record.Message == nil {
					continue
				}
				if record.Message.Role == llm.RoleUser {
					row.Snapshot += record.Message.Text()
				}
				if record.Message.Role == llm.RoleAssistant {
					row.ModelText += record.Message.Text()
					for _, block := range record.Message.Content {
						if block.Type == llm.BlockThinking {
							row.Thinking += block.Thinking
						}
					}
				}
			}
			usage, err := l.Store.TokenUsageRows(t.Context(), l.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range usage {
				row.Calls++
				row.Input += u.InputTokens
				row.Output += u.OutputTokens
				row.CacheRead += u.CacheReadTokens
				if u.UsageMissing {
					row.Unknown++
				}
			}
			report = append(report, row)
			save()
			if callErr != nil {
				t.Fatal(row.Error)
			}
			if len(row.Controls) != 1 || row.Controls[0].Action != scenario.expected || row.Controls[0].StepID != "s1" || row.Controls[0].UnitID != "u1" || row.Controls[0].Reason == "" || row.Unknown > 0 || row.Calls == 0 {
				t.Fatalf("model decision/attribution did not satisfy scenario %s; actual transcript and usage preserved", scenario.name)
			}
			t.Logf("round=%d scenario=%s action=%s calls=%d input=%d output=%d", round, scenario.name, row.Controls[0].Action, row.Calls, row.Input, row.Output)
		}
	}
}
