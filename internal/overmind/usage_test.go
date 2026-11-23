package overmind

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Same-session proofreading starts a new Prompt for each ask. Its usage is
// cumulative only within that Prompt, even though the conversation is retained.
func TestGenSessionUsageAcrossPrompts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage []llm.Usage
	}{
		{"observed_r80", []llm.Usage{
			{InputTokens: 967, OutputTokens: 1954, CacheReadTokens: 128},
			{InputTokens: 1919, OutputTokens: 1032, CacheReadTokens: 896},
			{InputTokens: 2960, OutputTokens: 6107, CacheReadTokens: 1792},
		}},
		{"equal_calls", []llm.Usage{
			{InputTokens: 200, OutputTokens: 40, CacheReadTokens: 128},
			{InputTokens: 200, OutputTokens: 40, CacheReadTokens: 128},
		}},
		{"smaller_next_prompt", []llm.Usage{
			{InputTokens: 2000, OutputTokens: 900, CacheReadTokens: 1024},
			{InputTokens: 1000, OutputTokens: 80, CacheReadTokens: 128},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openStore(t)
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(calls.Add(1)) - 1
				if i >= len(tc.usage) {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				u := tc.usage[i]
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"显示稿\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":%d}}}\n\ndata: [DONE]\n\n", u.InputTokens, u.OutputTokens, u.CacheReadTokens)
			}))
			defer model.Close()
			provider, err := llm.NewProvider(llm.Config{Format: llm.FormatOpenAI, BaseURL: model.URL, APIKey: "test", Model: "usage-fixture"})
			if err != nil {
				t.Fatal(err)
			}
			loop := &Loop{Store: store, TaskID: "usage-review", RootDir: t.TempDir(), Provider: provider, Model: "usage-fixture"}
			gs := loop.newGenSession("生成显示稿")
			defer gs.close()
			for range tc.usage {
				if _, err := gs.ask(ctx, "核对显示稿"); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := store.TokenUsageRows(ctx, loop.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(tc.usage) || int(calls.Load()) != len(rows) {
				t.Fatalf("calls=%d rows=%d want=%d", calls.Load(), len(rows), len(tc.usage))
			}
			ts := transcript.NewStore(filepath.Join(loop.RootDir, loop.TaskID, "transcripts"))
			recs, err := ts.Load(ts.MainPath(gs.sid))
			if err != nil {
				t.Fatal(err)
			}
			var recorded []llm.Usage
			for _, rec := range recs {
				if rec.Usage != nil {
					recorded = append(recorded, *rec.Usage)
				}
			}
			if !reflect.DeepEqual(recorded, tc.usage) {
				b, _ := json.Marshal(recorded)
				t.Fatalf("transcript usage=%s want=%+v", b, tc.usage)
			}
			for i, row := range rows {
				got := llm.Usage{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheWriteTokens: row.CacheWriteTokens}
				if got != tc.usage[i] || row.Turn != i+1 || row.TranscriptID != gs.sid || row.Agent != ActorOvermind {
					t.Errorf("call %d: row=%+v want usage=%+v", i+1, row, tc.usage[i])
				}
			}
		})
	}
}
