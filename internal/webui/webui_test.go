package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("GET / body is not HTML: %q", body)
	}
}

func TestHandlerUnknownPathFallsBackToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/not-a-static-file", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /not-a-static-file status = %d, want 200 (index fallback)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("fallback body is not HTML: %q", body)
	}
}
