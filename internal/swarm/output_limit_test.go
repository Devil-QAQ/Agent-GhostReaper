package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"ghostreaper/internal/failover"
	"io"
	"net/http"
	"path/filepath"
	"testing"
)

func TestWorkerOutputFollowsLibrary(t *testing.T) {
	for _, limit := range []int{0, 1024, 8192, 65536} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			dir := t.TempDir()
			store := openStore(t, dir)
			ctx := context.Background()
			seedGraph(t, store, "limits", "核对已有资料")
			model := newScriptedModel(func(*recordedRequest) scriptedReply { return scriptedReply{Text: "核对完成", Finish: "stop"} })
			defer model.Close()
			seen := make(chan int, 20)
			model.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
				}
				var req struct {
					MaxTokens int `json:"max_tokens"`
				}
				if e = json.Unmarshal(raw, &req); e != nil {
					t.Error(e)
				}
				seen <- req.MaxTokens
				r.Body = io.NopCloser(bytes.NewReader(raw))
				model.handle(w, r)
			})
			cfg := failover.ModelConfig{Name: "limits", Model: "scripted-swarm-model", Format: "openai", BaseURL: model.srv.URL, APIKey: "test-key", MaxTokens: limit, ContextWindow: 100000}
			if err := failover.SaveModelConfig(ctx, store, cfg, "test"); err != nil {
				t.Fatal(err)
			}
			if err := failover.SetActive(ctx, store, failover.RolePrimary, cfg.Name, "test"); err != nil {
				t.Fatal(err)
			}
			provider, err := failover.BuildProvider(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			ex := NewExecutor(store, provider, filepath.Join(dir, "tasks"), Budget{})
			ex.Model = cfg.Model
			if _, err = ex.RunStep(ctx, "limits", "s1"); err != nil {
				t.Fatal(err)
			}
			close(seen)
			count := 0
			for got := range seen {
				count++
				if got != limit {
					t.Fatalf("configured=%d sent=%d", limit, got)
				}
			}
			if count == 0 {
				t.Fatal("no model request")
			}
			c := newUnitCompactor(store, ex.Blobs, "limits", "s1", "unit", 100000, failover.OutputTokensFor(ctx, store, cfg.Model), nil)
			want := limit
			if want <= 0 || want > 20000 {
				want = 20000
			}
			if c.reserved != want || c.microThreshold() != (100000-want)*6/10 {
				t.Fatalf("reserved=%d threshold=%d", c.reserved, c.microThreshold())
			}
		})
	}
}
