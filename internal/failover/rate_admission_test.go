package failover

import (
	"context"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestIndependentProvidersShareRateAdmission(t *testing.T) {
	srv, _ := openaiStub(t, func(map[string]any) (int, string) { return 200, completionJSON("ok") })
	cfg := ModelConfig{Format: "openai", BaseURL: srv.URL, APIKey: "shared-key", Model: "m", RatePerSecond: 2}
	p, err := BuildProvider(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := BuildProvider(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for _, prov := range []llm.Provider{p, q, p, q} {
		if _, _, _, err := prov.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{llm.UserText("ok")}}); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) < 950*time.Millisecond {
		t.Fatal("independent provider bypassed shared limit")
	}
}

func TestRateAdmissionCancellation(t *testing.T) {
	a := &serviceAdmission{}
	rate := &admissionRate{second: newAdmissionBucket(1, time.Hour)}
	if err := a.wait(t.Context(), rate); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.wait(ctx, rate) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rate wait ignored cancellation")
	}
}
