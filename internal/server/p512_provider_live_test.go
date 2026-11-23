package server

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/swarm"
	"github.com/Autumn-27/norma/llm"
)

// Bounded inert probe to distinguish service/transport silence from the UI.
func TestThinkingProviderLiveProbe(t *testing.T) {
	file := os.Getenv("GHOSTREAPER_PANEL_MODEL_FILE")
	if file == "" {
		t.Skip("private model not supplied")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("private model unavailable")
	}
	var cfg failover.ModelConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid private model")
	}
	cfg.Thinking = "enabled"
	srv, _, _ := newInterjectTestServer(t)
	type probe struct {
		Name       string    `json:"name"`
		Elapsed    int64     `json:"elapsed_ms"`
		Events     int       `json:"events"`
		Thinking   string    `json:"thinking"`
		Text       string    `json:"text"`
		Usage      llm.Usage `json:"usage"`
		ErrorClass string    `json:"error_class,omitempty"`
		Canceled   bool      `json:"canceled"`
	}
	var report []probe
	for _, name := range []string{"direct", "production"} {
		var p llm.Provider
		if name == "direct" {
			p, err = llm.NewProvider(llm.Config{Format: llm.Format(cfg.Format), BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model, ThinkingType: "enabled"})
		} else {
			p, err = failover.BuildProvider(cfg, nil)
		}
		if err != nil {
			t.Fatal("model initialization failed")
		}
		p = swarm.MeterProvider(p, srv.Store, rings.TokenUsage{TaskID: "stream-probe", Agent: "probe", Model: cfg.Model, Purpose: name})
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		start := time.Now()
		rec := probe{Name: name}
		acc := llm.NewAccumulator()
		for ev, e := range p.Stream(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("人工固定材料共49项，其中1项重复，其余材料未检查。只用两句话概括实际观察和未验证边界，不调用工具。")}, MaxTokens: 2048}) {
			if e != nil {
				rec.ErrorClass = string(failover.Classify(ctx, e))
				break
			}
			rec.Events++
			acc.Add(ev)
		}
		rec.Elapsed = time.Since(start).Milliseconds()
		rec.Canceled = ctx.Err() != nil
		cancel()
		rec.Usage = acc.Usage
		msg := acc.Message()
		rec.Text = msg.Text()
		for _, b := range msg.Content {
			if b.Type == llm.BlockThinking {
				rec.Thinking += b.Thinking
			}
		}
		report = append(report, rec)
		if out := os.Getenv("GHOSTREAPER_PANEL_PROBE_EVIDENCE"); out != "" {
			b, _ := json.MarshalIndent(report, "", "  ")
			if os.WriteFile(out, b, 0600) != nil {
				t.Fatal("evidence write failed")
			}
		}
		t.Logf("%s events=%d thinking_chars=%d reply_chars=%d elapsed_ms=%d canceled=%t class=%s", name, rec.Events, len(rec.Thinking), len(rec.Text), rec.Elapsed, rec.Canceled, rec.ErrorClass)
	}
}
