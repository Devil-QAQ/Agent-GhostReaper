package failover

import (
	"context"
	"github.com/Autumn-27/norma/llm"
	"testing"
)

func TestThinkingDefaultAndSavedOffReachActualRequest(t *testing.T) {
	for _, mode := range []string{"", "enabled", "disabled"} {
		t.Run("saved="+mode, func(t *testing.T) {
			srv, bodies := openaiStub(t, func(map[string]any) (int, string) { return 200, completionJSON("ok") })
			p, err := BuildProvider(ModelConfig{Format: "openai", BaseURL: srv.URL, APIKey: "local", Model: "fixture", Thinking: mode}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err = p.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
				t.Fatal(err)
			}
			raw, _ := bodies.Load("POST /chat/completions")
			th, _ := raw.(map[string]any)["thinking"].(map[string]any)
			want := mode
			if want == "" {
				want = "enabled"
			}
			if th["type"] != want {
				t.Fatalf("actual wire mode=%v want=%s", th, want)
			}
			if _, _, _, err = p.Complete(context.Background(), llm.CompletionRequest{Thinking: "disabled"}); err != nil {
				t.Fatal(err)
			}
			raw, _ = bodies.Load("POST /chat/completions")
			th, _ = raw.(map[string]any)["thinking"].(map[string]any)
			if th["type"] != "disabled" {
				t.Fatal("explicit auxiliary override lost")
			}
		})
	}
}
