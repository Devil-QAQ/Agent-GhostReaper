package interject

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

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

func TestInterjectFailedRequestIsMetered(t *testing.T) {
	e := newIJEnv(t, func(*recordedReq) string { return "unused" })
	const task = "failed-interject"
	e.seedUnitTask(t, task)
	e.svc.Provider = interruptedUsageModel{}
	if _, _, err := e.svc.Enqueue(context.Background(), task, TargetOvermind, "", "", "", "ask", "local accounting review"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	failed := false
	for time.Now().Before(deadline) {
		v, err := e.svc.View(context.Background(), task, TargetOvermind, "")
		if err == nil && len(v.Messages) == 1 && v.Messages[0].State == "failed" {
			failed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !failed {
		t.Fatal("failed message not recorded")
	}
	rows, err := e.store.TokenUsageRows(t.Context(), task)
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 300 || rows[0].Purpose != "interrupted" || rows[0].Agent != "interject:overmind" {
		t.Fatalf("failed interject usage lost/duplicated: rows=%+v err=%v", rows, err)
	}
}
