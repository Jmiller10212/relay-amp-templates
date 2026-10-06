package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupabaseProviderAcceptsAny2xxAndUsesRedirectQuery(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") != "publishable" {
			t.Error("missing API key")
		}
		paths = append(paths, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/v1/signup":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "auth-1", "email": "person@example.test"})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	p := NewSupabaseAuthProvider(srv.URL, "publishable")
	user, session, err := p.SignUp(context.Background(), "person@example.test", "password", "https://relay.test/auth/callback")
	if err != nil || session != nil || user.ID != "auth-1" {
		t.Fatalf("user=%+v session=%+v err=%v", user, session, err)
	}
	if err := p.RequestRecovery(context.Background(), "person@example.test", "https://relay.test/auth/callback"); err != nil {
		t.Fatal(err)
	}
	if err := p.ResendVerification(context.Background(), "person@example.test", "https://relay.test/auth/callback"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !strings.Contains(path, "redirect_to=https%3A%2F%2Frelay.test%2Fauth%2Fcallback") {
			t.Fatalf("redirect missing from %s", path)
		}
	}
}

func TestSupabaseProviderSanitizesRemoteErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"bad","msg":"sensitive provider detail"}`, 400)
	}))
	defer srv.Close()
	p := NewSupabaseAuthProvider(srv.URL, "publishable")
	_, err := p.PasswordLogin(context.Background(), "person@example.test", "secret")
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe error: %v", err)
	}
}
