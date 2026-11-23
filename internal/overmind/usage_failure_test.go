package overmind

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type interruptedUsageModel struct{ llm.Provider }

func (interruptedUsageModel) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 300}}, nil) {
			yield(llm.StreamEvent{}, errors.New("local interrupted model"))
		}
	}
}

func TestOvermindFailedRequestsAreMetered(t *testing.T) {
	for _, mode := range []string{"generation", "planning"} {
		t.Run(mode, func(t *testing.T) {
			store := openStore(t)
			l := &Loop{Store: store, TaskID: "failed-overmind", Provider: interruptedUsageModel{}, Model: "local"}
			var err error
			if mode == "generation" {
				gs := l.newGenSession("local accounting review")
				defer gs.close()
				_, err = gs.ask(context.Background(), "review")
			} else {
				_, _, err = l.decompose(context.Background())
			}
			if err == nil {
				t.Fatal("interrupted model became successful")
			}
			rows, readErr := store.TokenUsageRows(t.Context(), l.TaskID)
			if readErr != nil || len(rows) != 1 || rows[0].InputTokens != 300 || rows[0].Purpose != "interrupted" || rows[0].Agent != ActorOvermind || rows[0].TranscriptID == "" {
				t.Fatalf("failed %s usage lost/duplicated: rows=%+v err=%v", mode, rows, readErr)
			}
		})
	}
}
