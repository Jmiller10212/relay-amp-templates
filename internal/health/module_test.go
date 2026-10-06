package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthStatus(t *testing.T) {
	m := New(func() bool { return true }, func(context.Context) bool { return true }, func() int { return 2 }, func() []string { return []string{"chat", "health", "persistence"} })
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"connections":2`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHealthUnavailable(t *testing.T) {
	m := New(func() bool { return true }, func(context.Context) bool { return false }, func() int { return 0 }, func() []string { return nil })
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
}
