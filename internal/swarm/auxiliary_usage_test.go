package swarm

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/norma/llm"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
)

type auxTestProvider struct {
	complete func(context.Context) (llm.Message, string, llm.Usage, error)
	stream   func(context.Context) iter.Seq2[llm.StreamEvent, error]
}

func TestAuxiliaryParallelRequestsKeepTaskAttribution(t *testing.T) {
	store := openStore(t, t.TempDir())
	p := auxTestProvider{complete: func(ctx context.Context) (llm.Message, string, llm.Usage, error) {
		return llm.Message{}, "", llm.Usage{InputTokens: 11, OutputTokens: 3}, ctx.Err()
	}}
	var wg sync.WaitGroup
	for _, task := range []string{"parallel-a", "parallel-b"} {
		metered := MeterProvider(p, store, rings.TokenUsage{TaskID: task, Agent: "swarm:u1", TranscriptID: "swarm-u1", Purpose: "action_review"})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // 任务取消仍须记录模型已经报告的消耗。
				_, _, _, _ = metered.Complete(ctx, llm.CompletionRequest{})
			}()
		}
	}
	wg.Wait()
	for _, task := range []string{"parallel-a", "parallel-b"} {
		rows, err := store.TokenUsageRows(t.Context(), task)
		if err != nil || len(rows) != 8 {
			t.Fatalf("task=%s rows=%+v err=%v", task, rows, err)
		}
		for _, r := range rows {
			if r.TaskID != task || r.InputTokens != 11 || r.OutputTokens != 3 || r.Purpose != "action_review" {
				t.Fatalf("parallel attribution mixed: %+v", r)
			}
		}
	}
}

func (p auxTestProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.complete(ctx)
}
func (p auxTestProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return p.stream(ctx)
}

func TestAuxiliaryActionReviewUsageIncludingParseFailure(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		err         error
		usage       llm.Usage
	}{
		{"valid", `{"decision":"allow","reason":"读取本地资料"}`, nil,
			llm.Usage{InputTokens: 120, OutputTokens: 30, CacheReadTokens: 50, CacheWriteTokens: 20}},
		{"invalid_json", "invalid", nil, llm.Usage{InputTokens: 100, OutputTokens: 20}},
		{"provider_error_with_usage", "", errors.New("local failure"), llm.Usage{InputTokens: 80}},
		{"missing_usage", "", errors.New("no usage returned"), llm.Usage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			calls := 0
			p := auxTestProvider{complete: func(ctx context.Context) (llm.Message, string, llm.Usage, error) {
				calls++
				if failover.TaskFromContext(ctx) != "aux-task" {
					t.Fatalf("lost task routing context")
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(tc.reply)}}, "stop", tc.usage, tc.err
			}}
			env := &unitEnv{TaskID: "aux-task", UnitID: "u7", TranscriptID: "swarm-u2", // 接续沿用旧会话
				Executor: &Executor{Store: store, Provider: p, Model: "local-model"}}
			_, err := env.meteredActionReview()(context.Background(), ActionReviewInput{Tool: "read"})
			if (err != nil) != (tc.name != "valid") {
				t.Fatalf("unexpected triage result: %v", err)
			}
			rows, err := store.TokenUsageRows(t.Context(), env.TaskID)
			if err != nil || len(rows) != 1 || calls != 1 {
				t.Fatalf("rows=%+v calls=%d err=%v", rows, calls, err)
			}
			r := rows[0]
			got := llm.Usage{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens}
			if got != tc.usage || r.Purpose != "action_review" || r.Agent != "swarm:u7" || r.TranscriptID != "swarm-u2" || r.Model != "local-model" || r.Turn != 0 || r.UsageMissing != (tc.usage == (llm.Usage{})) {
				t.Fatalf("wrong usage attribution: %+v", r)
			}
		})
	}
}

func TestAuxiliaryStreamUsageOnErrorStopAndUnusedIterator(t *testing.T) {
	for _, mode := range []string{"success", "error", "stop", "unused"} {
		t.Run(mode, func(t *testing.T) {
			store := openStore(t, t.TempDir())
			calls := 0
			p := auxTestProvider{stream: func(ctx context.Context) iter.Seq2[llm.StreamEvent, error] {
				return func(yield func(llm.StreamEvent, error) bool) {
					calls++
					if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 300, CacheReadTokens: 100}}, nil) {
						return
					}
					if mode == "error" {
						yield(llm.StreamEvent{}, errors.New("stream interrupted"))
						return
					}
					yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 25}}, nil)
				}
			}}
			metered := MeterProvider(p, store, rings.TokenUsage{TaskID: "stream-task", Agent: "shepherd", Purpose: "compaction", TranscriptID: "shepherd-stream-task", Model: "local"})
			seq := metered.Stream(context.Background(), llm.CompletionRequest{})
			if mode != "unused" {
				for _, err := range seq {
					if mode == "stop" || err != nil {
						break
					}
				}
			}
			rows, err := store.TokenUsageRows(t.Context(), "stream-task")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unused" {
				if len(rows) != 0 || calls != 0 {
					t.Fatalf("unconsumed iterator recorded: %+v calls=%d", rows, calls)
				}
				return
			}
			if len(rows) != 1 || calls != 1 || rows[0].InputTokens != 300 || rows[0].CacheReadTokens != 100 {
				t.Fatalf("wrong stream accounting: %+v calls=%d", rows, calls)
			}
			wantOutput := 0
			if mode == "success" {
				wantOutput = 25
			}
			if rows[0].OutputTokens != wantOutput {
				t.Fatalf("output=%d want=%d", rows[0].OutputTokens, wantOutput)
			}
		})
	}
}

func TestAuxiliaryCompactionRetryDoesNotCountAsConversationTurn(t *testing.T) {
	store := openStore(t, t.TempDir())
	calls := 0
	p := auxTestProvider{stream: func(context.Context) iter.Seq2[llm.StreamEvent, error] {
		return func(yield func(llm.StreamEvent, error) bool) {
			calls++
			if !yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 400}}, nil) {
				return
			}
			if calls == 1 {
				yield(llm.StreamEvent{}, errors.New("interrupted summary"))
				return
			}
			if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "<summary>会话摘要</summary>"}, nil) {
				return
			}
			yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 30}}, nil)
		}
	}}
	const task = "compact-task"
	main := &turnBudgetTracker{Store: store, TaskID: task, Agent: "swarm:u7"}
	mainCalls := 0
	mainProvider := MeterProvider(auxTestProvider{complete: func(context.Context) (llm.Message, string, llm.Usage, error) {
		mainCalls++
		u := llm.Usage{InputTokens: 5000, OutputTokens: 70}
		if mainCalls == 2 {
			u = llm.Usage{InputTokens: 3000, OutputTokens: 30}
		}
		return llm.Message{}, "end_turn", u, nil
	}}, store, rings.TokenUsage{TaskID: task, Agent: "swarm:u7", TranscriptID: "swarm-u2", Model: "local"})
	_, _, _, _ = mainProvider.Complete(context.Background(), llm.CompletionRequest{})
	if err := main.OnTurn(); err != nil {
		t.Fatal(err)
	}

	c := newUnitCompactor(store, nil, task, "s1", "u7", 50000, 4096,
		MeterProvider(p, store, rings.TokenUsage{TaskID: task, Agent: "swarm:u7", TranscriptID: "swarm-u2", Model: "local", Purpose: "compaction"}))
	var msgs []llm.Message
	for range 6 {
		msgs = append(msgs, llm.UserText(strings.Repeat("original ", 100)), llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("observed")}})
	}
	if _, ok := c.Reactive(context.Background(), msgs); !ok {
		t.Fatal("summary compaction did not run")
	}
	_, _, _, _ = mainProvider.Complete(context.Background(), llm.CompletionRequest{})
	if err := main.OnTurn(); err != nil {
		t.Fatal(err)
	}
	rows, err := store.TokenUsageRows(t.Context(), task)
	if err != nil || len(rows) != 4 || calls != 2 || main.Turns() != 2 {
		t.Fatalf("rows=%+v calls=%d main turns=%d err=%v", rows, calls, main.Turns(), err)
	}
	if rows[1].Purpose != "compaction" || rows[2].Purpose != "compaction" || rows[1].InputTokens != 400 || rows[2].OutputTokens != 30 || rows[3].Purpose != "" || rows[3].InputTokens != 3000 || rows[3].OutputTokens != 30 {
		t.Fatalf("summary retry mixed into main cumulative baseline: %+v", rows)
	}
}
