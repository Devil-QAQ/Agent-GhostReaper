package shepherd

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

func TestShepherdFailedRequestIsMetered(t *testing.T) {
	store := openStore(t)
	const task = "failed-shepherd"
	seedTask(t, store, task)
	s := &Service{Store: store, Provider: interruptedUsageModel{}, Model: "local", RootDir: t.TempDir()}
	if _, err := s.Enqueue(context.Background(), task, "local accounting review"); err != nil {
		t.Fatal(err)
	}
	waitView(t, s, task, func(v *View) bool { return len(v.Messages) == 1 && v.Messages[0].State == "failed" && !v.Busy })
	rows, err := store.TokenUsageRows(t.Context(), task)
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 300 || rows[0].Purpose != "interrupted" || rows[0].Agent != ActorShepherd || rows[0].TranscriptID != SessionID(task) {
		t.Fatalf("failed shepherd usage lost/duplicated: rows=%+v err=%v", rows, err)
	}
}
