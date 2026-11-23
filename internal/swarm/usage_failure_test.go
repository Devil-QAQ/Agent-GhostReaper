package swarm

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"

	"ghostreaper/internal/rings"
)

func TestRequestUsageInterruptedNormaPrompt(t *testing.T) {
	for _, mode := range []string{"stream_error", "canceled", "consumer_stop", "missing_usage", "success"} {
		t.Run(mode, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := auxTestProvider{stream: func(context.Context) iter.Seq2[llm.StreamEvent, error] {
				return func(yield func(llm.StreamEvent, error) bool) {
					if mode != "missing_usage" {
						if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 300, CacheReadTokens: 100, CacheWriteTokens: 20}}, nil) {
							return
						}
						if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial"}, nil) {
							return
						}
					}
					if mode == "success" {
						yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 12}}, nil)
						return
					}
					if mode == "canceled" {
						cancel()
					}
					yield(llm.StreamEvent{}, errors.New("local interrupted stream"))
				}
			}}
			const task = "main-boundary"
			sess := agentcore.NewSession(agentcore.Options{
				Provider:  MeterProvider(p, store, rings.TokenUsage{TaskID: task, Agent: "overmind", Model: "local", TranscriptID: "main-sid"}),
				SessionID: "main-sid", Tools: []tool.CoreTool{}, MaxTurns: 1, DisableBackgroundTasks: true,
			})
			defer sess.Close()
			budget := &turnBudgetTracker{Store: store, TaskID: task, Agent: "overmind", MaxTurns: 4}
			var terminalUsage llm.Usage
			for ev, err := range sess.Prompt(ctx, "local accounting audit") {
				if err != nil {
					continue
				}
				if mode == "consumer_stop" && ev.Kind == harness.KindText {
					break
				}
				if ev.Kind == harness.KindUsage {
					if err := budget.OnTurn(); err != nil {
						t.Fatal(err)
					}
				}
				if ev.Kind == harness.KindResult && ev.Terminal != nil {
					terminalUsage = ev.Terminal.Usage
				}
			}
			rows, err := store.TokenUsageRows(context.Background(), task)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			r := rows[0]
			if mode == "missing_usage" {
				if !r.UsageMissing || r.InputTokens != 0 {
					t.Fatalf("unknown cost fabricated: %+v", r)
				}
			} else if r.InputTokens != 300 || r.CacheReadTokens != 100 || r.CacheWriteTokens != 20 || r.UsageMissing {
				t.Fatalf("reported partial usage lost: %+v terminal=%+v", r, terminalUsage)
			}
			if mode == "success" {
				if r.Purpose != "" || r.Turn != 1 || budget.Turns() != 1 || r.OutputTokens != 12 {
					t.Fatalf("normal usage/budget altered: %+v budget=%d", r, budget.Turns())
				}
			} else if r.Purpose != "interrupted" || r.Turn != 0 || budget.Turns() != 0 {
				t.Fatalf("failed request became a completed turn: %+v budget=%d", r, budget.Turns())
			}
			t.Logf("mode=%s input=%d output=%d purpose=%s rows=%d budget_turns=%d", mode, r.InputTokens, r.OutputTokens, r.Purpose, len(rows), budget.Turns())
		})
	}
}

func TestExecutorFailedRequestIsMetered(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root)
	const task = "failed-executor"
	seedGraph(t, store, task, "local accounting review")
	p := auxTestProvider{stream: func(context.Context) iter.Seq2[llm.StreamEvent, error] {
		return func(yield func(llm.StreamEvent, error) bool) {
			if yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 300}}, nil) {
				yield(llm.StreamEvent{}, errors.New("openai: status 400: invalid request"))
			}
		}
	}}
	ex := NewExecutor(store, p, root, Budget{MaxTurns: 4, RunSeconds: 60})
	pack, err := ex.Claim(context.Background(), task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := ex.Run(context.Background(), pack)
	if res == nil || res.StepStatus != "blocked" {
		t.Fatalf("unexpected failed execution: %+v", res)
	}
	rows, err := store.TokenUsageRows(t.Context(), task)
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 300 || rows[0].Purpose != "interrupted" || rows[0].TranscriptID != res.TranscriptID || rows[0].Agent != "swarm:"+res.UnitID {
		t.Fatalf("executor failure usage lost/duplicated: rows=%+v err=%v", rows, err)
	}
}

func TestRequestUsageWriteFailureIsReportedWithoutChangingModelResult(t *testing.T) {
	store := openStore(t, t.TempDir())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var reported error
	p := MeterProvider(auxTestProvider{complete: func(context.Context) (llm.Message, string, llm.Usage, error) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("completed")}}, "end_turn", llm.Usage{InputTokens: 300}, nil
	}}, store, rings.TokenUsage{TaskID: "write-failure", Agent: "overmind"}, func(err error) { reported = err })
	msg, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil || msg.Text() != "completed" || !errors.Is(reported, rings.ErrClosed) {
		t.Fatalf("model result=%s err=%v audit error=%v", msg.Text(), err, reported)
	}
}

func TestRequestUsageReactiveRetryDoesNotDoubleCountCumulativeEvent(t *testing.T) {
	store := openStore(t, t.TempDir())
	const task = "reactive-accounting"
	calls, summaries := 0, 0
	p := auxTestProvider{stream: func(context.Context) iter.Seq2[llm.StreamEvent, error] {
		return func(yield func(llm.StreamEvent, error) bool) {
			calls++
			input := 100
			if calls > 1 {
				input = 200
			}
			if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: input}}, nil) {
				return
			}
			if calls == 1 {
				yield(llm.StreamEvent{}, errors.New("status 413: request too large"))
				return
			}
			if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "completed"}, nil) {
				return
			}
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn", Usage: llm.Usage{OutputTokens: 20}}, nil)
		}
	}}
	ts := transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
	w := ts.NewWriter("reactive-sid", "")
	for range 6 {
		w.RecordMessage(llm.UserText(strings.Repeat("history ", 1000)), llm.Usage{})
		w.RecordMessage(llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("observed")}}, llm.Usage{})
	}
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	sess := agentcore.NewSession(agentcore.Options{
		Provider:  MeterProvider(p, store, rings.TokenUsage{TaskID: task, Agent: "swarm:u1", TranscriptID: "reactive-sid", Model: "local"}),
		SessionID: "reactive-sid", Transcript: ts, Tools: []tool.CoreTool{}, MaxTurns: 2, DisableBackgroundTasks: true,
		Compactor: compaction.New(compaction.Config{ContextWindow: 200000, KeepRecent: 2}, func(context.Context, []llm.Message) (string, error) {
			summaries++
			return "deterministic test summary", nil
		}),
	})
	defer sess.Close()
	if err := sess.Resume("reactive-sid"); err != nil {
		t.Fatal(err)
	}
	var cumulative llm.Usage
	for ev, err := range sess.Prompt(context.Background(), "continue") {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == harness.KindUsage && ev.Usage != nil {
			cumulative = *ev.Usage
		}
		if ev.Kind == harness.KindResult && ev.Terminal != nil && ev.Terminal.Err != nil {
			t.Fatal(ev.Terminal.Err)
		}
	}
	rows, err := store.TokenUsageRows(t.Context(), task)
	if err != nil || len(rows) != 2 || calls != 2 || summaries != 1 {
		t.Fatalf("rows=%+v calls=%d summaries=%d err=%v", rows, calls, summaries, err)
	}
	if cumulative.InputTokens != 300 || rows[0].InputTokens != 100 || rows[0].Purpose != "interrupted" || rows[1].InputTokens != 200 || rows[1].OutputTokens != 20 || rows[1].Turn != 1 {
		t.Fatalf("cumulative retry usage duplicated: cumulative=%+v rows=%+v", cumulative, rows)
	}
}
