package websearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskSearchSharesFailureAndBoundsRecovery(t *testing.T) {
	var clock atomic.Int64
	clock.Store(100)
	var calls atomic.Int64
	probe, release := make(chan struct{}), make(chan struct{})
	s := TaskSearch{now: func() time.Time { return time.Unix(clock.Load(), 0) }, search: func(context.Context, Config, string) ([]Result, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, unavailable("local service unavailable", nil)
		}
		if n == 2 {
			close(probe)
			<-release
		}
		return []Result{}, nil
	}}
	if _, err := s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "first"); err == nil {
		t.Fatal("initial service failure lost")
	}
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "another query")
			if err == nil || !strings.Contains(err.Error(), "不重复请求") {
				t.Error("cooldown did not return actual unavailability")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cooldown repeated %d outbound calls", calls.Load())
	}
	clock.Store(131)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "recover")
		if err != nil {
			t.Error(err)
		}
	}()
	<-probe
	for range 32 {
		_, err := s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "waiting")
		if err == nil || !strings.Contains(err.Error(), "检查恢复") {
			t.Fatal("parallel recovery probe escaped")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("more than one recovery probe")
	}
	close(release)
	wg.Wait()
	for range 5 {
		rs, err := s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "new query")
		if err != nil || len(rs) != 0 {
			t.Fatal("normal empty result treated as unavailable")
		}
	}
	if calls.Load() != 7 {
		t.Fatal("healthy searches were cached or stopped")
	}
}

func TestTaskSearchTaskConfigCancellationAndParameterIsolation(t *testing.T) {
	var calls atomic.Int64
	s := TaskSearch{search: func(ctx context.Context, cfg Config, q string) ([]Result, error) {
		calls.Add(1)
		if q == "parameter" {
			return nil, errors.New("bad current query")
		}
		if q == "cancel" {
			return nil, context.Canceled
		}
		return nil, unavailable("fixture unavailable", nil)
	}}
	for _, q := range []string{"parameter", "parameter", "cancel", "cancel"} {
		_, _ = s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, q)
	}
	if calls.Load() != 4 {
		t.Fatal("parameter errors or cancellation poisoned the service")
	}
	_, _ = s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "outage")
	_, _ = s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "blocked")
	if calls.Load() != 5 {
		t.Fatal("unchanged configuration retried an unavailable service")
	}
	_, _ = s.Search(t.Context(), "two", Config{Endpoint: "https://old.example/search"}, "separate task")
	_, _ = s.Search(t.Context(), "one", Config{Endpoint: "https://new.example/search", APIKey: "fixture-one"}, "changed backend")
	_, _ = s.Search(t.Context(), "one", Config{Endpoint: "https://new.example/search", APIKey: "fixture-two"}, "changed key")
	if calls.Load() != 8 {
		t.Fatal("task or effective configuration isolation failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = s.Search(ctx, "three", Config{Endpoint: "https://old.example/search"}, "unused")
	_, _ = s.Search(t.Context(), "three", Config{Endpoint: "https://old.example/search"}, "   ")
	if calls.Load() != 8 {
		t.Fatal("canceled/empty query went outbound")
	}
}

func TestTaskSearchOldFailureCannotOwnNewConfiguration(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s := TaskSearch{search: func(_ context.Context, cfg Config, _ string) ([]Result, error) {
		if cfg.Endpoint == "https://old.example/search" {
			close(started)
			<-release
			return nil, unavailable("old failure", nil)
		}
		return []Result{{Title: "new backend result"}}, nil
	}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.Search(t.Context(), "one", Config{Endpoint: "https://old.example/search"}, "old")
	}()
	<-started
	newCfg := Config{Endpoint: "https://new.example/search", APIKey: "fixture"}
	if _, err := s.Search(t.Context(), "one", newCfg, "new"); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if _, err := s.Search(t.Context(), "one", newCfg, "again"); err != nil {
		t.Fatal("old failure poisoned the new configuration")
	}
}

func TestTaskSearchHTTPClassificationAndEmptyResults(t *testing.T) {
	var calls atomic.Int64
	var mode atomic.Value
	mode.Store("unavailable")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch mode.Load().(string) {
		case "unavailable":
			w.WriteHeader(503)
			_, _ = w.Write([]byte("private-body-fixture"))
		case "empty":
			_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
		case "bad-shape":
			_, _ = w.Write([]byte(`{}`))
		case "bad-query":
			w.WriteHeader(400)
		case "ddgs-empty":
			_, _ = w.Write([]byte(`<div class="no-results__container">No results</div>`))
		case "ddgs-blocked":
			_, _ = w.Write([]byte(`<div>unrecognized search page</div>`))
		}
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.ResultsPath = "web.results"
	var search TaskSearch
	_, err := search.Search(t.Context(), "outage", cfg, "one")
	if err == nil || strings.Contains(err.Error(), "private-body-fixture") {
		t.Fatal("service error leaked response or disappeared")
	}
	_, _ = search.Search(t.Context(), "outage", cfg, "two")
	if calls.Load() != 1 {
		t.Fatal("actual HTTP failure not shared")
	}
	mode.Store("empty")
	for range 2 {
		rs, err := search.Search(t.Context(), "empty", cfg, "none")
		if err != nil || len(rs) != 0 {
			t.Fatal("valid empty JSON treated as failure")
		}
	}
	mode.Store("bad-shape")
	_, err = Search(t.Context(), cfg, "shape")
	var service *serviceError
	if !errors.As(err, &service) {
		t.Fatal("missing result fields fabricated an empty success")
	}
	mode.Store("bad-query")
	before := calls.Load()
	for range 2 {
		_, _ = search.Search(t.Context(), "query", cfg, "bad")
	}
	if calls.Load() != before+2 {
		t.Fatal("query-specific HTTP error cooled the service")
	}
	mode.Store("ddgs-empty")
	rawCfg := cfg
	rawCfg.ResponseMode = "text"
	rs, err := Search(t.Context(), rawCfg, "none")
	if err != nil || len(rs) != 1 || !rs[0].Raw {
		t.Fatal("raw response was misrepresented as structured results")
	}
	mode.Store("ddgs-blocked")
	_, err = Search(t.Context(), cfg, "unknown")
	if !errors.As(err, &service) {
		t.Fatal("unknown page fabricated an empty success")
	}
}

func TestTaskSearchCooldownIsIsolatedPerService(t *testing.T) {
	var calls atomic.Int32
	s := TaskSearch{search: func(_ context.Context, cfg Config, _ string) ([]Result, error) {
		calls.Add(1)
		if cfg.ServiceID == "failed" {
			return nil, unavailable("unavailable", nil)
		}
		return []Result{}, nil
	}}
	failed := Config{ServiceID: "failed", Endpoint: "https://failed.example/search"}
	healthy := Config{ServiceID: "healthy", Endpoint: "https://healthy.example/search"}
	_, _ = s.Search(t.Context(), "same-task", failed, "first")
	if _, err := s.Search(t.Context(), "same-task", healthy, "different source"); err != nil {
		t.Fatal("another service inherited the failure")
	}
	if _, err := s.Search(t.Context(), "same-task", failed, "still failed"); err == nil || !strings.Contains(err.Error(), "不重复请求") {
		t.Fatal("switching service reset the failed service cooldown")
	}
	if calls.Load() != 2 {
		t.Fatal("cooldown sent an extra request")
	}
}
