package failover

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type returnedUsageProvider struct {
	llm.Provider
	failed bool
}

func (p returnedUsageProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	u := llm.Usage{InputTokens: 300, OutputTokens: 12}
	if p.failed {
		return llm.Message{}, "", u, errors.New("openai: status 503: local failure")
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("completed")}}, "end_turn", u, nil
}

func TestNonStreamingUsageSurvivesEarlyStopAndFailedReplay(t *testing.T) {
	for _, failed := range []bool{false, true} {
		c, _, backup, _ := newTestChain(t)
		c.FailThreshold = 1
		c.lead.prov = nonStreaming{inner: returnedUsageProvider{failed: failed}}
		backup.script = func(int64) ([]llm.StreamEvent, error) {
			return []llm.StreamEvent{{Type: llm.SEMessageDelta, Usage: llm.Usage{InputTokens: 100, OutputTokens: 20}}}, nil
		}
		var models []string
		var usage []llm.Usage
		var complete []bool
		ctx, finish := ObserveUsage(context.Background(), func(m string, u llm.Usage, done bool) {
			models = append(models, m)
			usage = append(usage, u)
			complete = append(complete, done)
		})
		for _, err := range c.Stream(ctx, llm.CompletionRequest{}) {
			if err != nil {
				t.Fatal(err)
			}
			if !failed {
				break
			} // 首个事件尚未携带用量,原 Complete 已返回全量。
		}
		finish("outer", llm.Usage{}, false)
		if len(usage) == 0 || usage[0].InputTokens != 300 || usage[0].OutputTokens != 12 || complete[0] {
			t.Fatalf("known non-streaming usage lost: failed=%v usage=%+v complete=%v", failed, usage, complete)
		}
		if failed {
			if len(usage) != 2 || models[1] != "model-b" || usage[1].InputTokens != 100 || !complete[1] || backup.hits.Load() != 1 {
				t.Fatalf("usage observation changed replay: models=%v usage=%+v complete=%v", models, usage, complete)
			}
		} else if len(usage) != 1 || backup.hits.Load() != 0 {
			t.Fatalf("consumer stop replayed or duplicated: usage=%+v", usage)
		}
	}
}

func TestNoRouteDoesNotFabricateUsageAttempt(t *testing.T) {
	for _, stream := range []bool{false, true} {
		c, _, _, _ := newTestChain(t)
		c.lead, c.members = nil, nil
		calls := 0
		ctx, finish := ObserveUsage(context.Background(), func(string, llm.Usage, bool) { calls++ })
		var callErr error
		if stream {
			for _, err := range c.Stream(ctx, llm.CompletionRequest{}) {
				if err != nil {
					callErr = err
					break
				}
			}
		} else {
			_, _, _, callErr = c.Complete(ctx, llm.CompletionRequest{})
		}
		finish("outer", llm.Usage{}, false)
		if callErr == nil || calls != 0 {
			t.Fatalf("stream=%v local rejection counted as a model request: err=%v rows=%d", stream, callErr, calls)
		}
	}
}

func TestAuxiliaryUsageObserverAcrossFailoverAttempts(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[stream], func(t *testing.T) {
			c, primary, backup, _ := newTestChain(t)
			c.FailThreshold = 1
			primary.script = func(int64) ([]llm.StreamEvent, error) { return nil, errTransient(1) }
			backup.script = func(int64) ([]llm.StreamEvent, error) {
				return []llm.StreamEvent{
					{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 100, CacheReadTokens: 40}},
					{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 20}},
				}, nil
			}
			var models []string
			var usages []llm.Usage
			var completed []bool
			ctx, finish := ObserveUsage(context.Background(), func(model string, u llm.Usage, complete bool) {
				models = append(models, model)
				usages = append(usages, u)
				completed = append(completed, complete)
			})
			if stream {
				acc := llm.NewAccumulator()
				for ev, err := range c.Stream(ctx, llm.CompletionRequest{MaxTokens: 8}) {
					if err != nil {
						t.Fatal(err)
					}
					acc.Add(ev)
				}
				finish("outer", acc.Usage, true)
			} else {
				_, _, u, err := c.Complete(ctx, llm.CompletionRequest{MaxTokens: 8})
				if err != nil {
					t.Fatal(err)
				}
				finish("outer", u, true)
			}
			if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" || usages[0] != (llm.Usage{}) || usages[1].InputTokens == 0 {
				t.Fatalf("attempts lost or duplicated: models=%v usages=%+v", models, usages)
			}
			if completed[0] || !completed[1] {
				t.Fatalf("failed attempt marked successful: %v", completed)
			}
			if primary.hits.Load() != 1 || backup.hits.Load() != 1 {
				t.Fatalf("routing changed: primary=%d backup=%d", primary.hits.Load(), backup.hits.Load())
			}
		})
	}
}

func TestAuxiliaryUsageObserverPartialStreamAndConsumerStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		c, primary, _, _ := newTestChain(t)
		primary.script = func(int64) ([]llm.StreamEvent, error) {
			return []llm.StreamEvent{{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 77}}}, errTransient(1)
		}
		var reported []llm.Usage
		ctx, finish := ObserveUsage(context.Background(), func(_ string, u llm.Usage, _ bool) { reported = append(reported, u) })
		for _, err := range c.Stream(ctx, llm.CompletionRequest{}) {
			if stop || err != nil {
				break
			}
		}
		finish("outer", llm.Usage{}, false)
		if len(reported) != 1 || reported[0].InputTokens != 77 {
			t.Fatalf("stop=%v usage=%+v", stop, reported)
		}
	}
}
