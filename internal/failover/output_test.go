package failover

import (
	"context"
	"github.com/Autumn-27/norma/llm"
	"iter"
	"testing"
)

type outputCapture struct{ got int }

func (p *outputCapture) Complete(_ context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.got = r.MaxTokens
	return llm.Message{}, "", llm.Usage{}, nil
}
func (p *outputCapture) Stream(_ context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.got = r.MaxTokens
	return func(func(llm.StreamEvent, error) bool) {}
}

func TestLibraryOutputLimits(t *testing.T) {
	for _, limit := range []int{0, 1024, 8192, 65536} {
		for _, request := range []int{0, 4096, 32768, 100000} {
			for _, stream := range []bool{false, true} {
				capture := &outputCapture{}
				p := LibraryOutput(capTokensProvider{inner: capture, cap: limit})
				req := llm.CompletionRequest{MaxTokens: request}
				if stream {
					for range p.Stream(context.Background(), req) {
					}
				} else {
					_, _, _, _ = p.Complete(context.Background(), req)
				}
				if capture.got != limit {
					t.Fatalf("limit=%d request=%d stream=%v got=%d", limit, request, stream, capture.got)
				}
			}
		}
	}
}

func TestModelLimitsUseServingModel(t *testing.T) {
	_, _, _, store := newTestChain(t)
	ctx := context.Background()
	for _, cfg := range []ModelConfig{{Name: "limits-primary", Model: "limits-main", Format: "openai", BaseURL: "http://localhost", APIKey: "k", ContextWindow: 50000, MaxTokens: 8192}, {Name: "limits-backup", Model: "limits-back", Format: "openai", BaseURL: "http://localhost", APIKey: "k", ContextWindow: 100000, MaxTokens: 1024}} {
		if err := SaveModelConfig(ctx, store, cfg, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetActive(ctx, store, RolePrimary, "limits-primary", "test"); err != nil {
		t.Fatal(err)
	}
	if err := SaveModelConfig(ctx, store, ModelConfig{Name: "limits-unset", Model: "limits-unset", Format: "openai", BaseURL: "http://localhost", APIKey: "k", ContextWindow: 100000}, "test"); err != nil {
		t.Fatal(err)
	}
	if got := OutputTokensFor(ctx, store, "limits-unset"); got != 0 {
		t.Fatalf("unlimited backup inherited primary limit: %d", got)
	}
	for _, tc := range []struct {
		model          string
		window, output int
	}{{"limits-main", 50000, 8192}, {"limits-back", 100000, 1024}, {"unknown", 50000, 8192}, {"", 50000, 8192}} {
		if w, o := ContextWindowFor(ctx, store, tc.model), OutputTokensFor(ctx, store, tc.model); w != tc.window || o != tc.output {
			t.Fatalf("%s: %d/%d", tc.model, w, o)
		}
	}
	if OutputTokensFor(ctx, nil, "none") != 0 {
		t.Fatal("nil store must leave limit unset")
	}
}
