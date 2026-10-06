package realtime

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"relay/internal/accounts"
	"relay/internal/model"
)

type fakeAuth struct{ principal accounts.Principal }

func (f fakeAuth) Authenticate(http.ResponseWriter, *http.Request) (accounts.Principal, error) {
	return f.principal, nil
}
func (f fakeAuth) ValidateSession(context.Context, string, string) error { return nil }

func TestRealtimeTracksOneUserAcrossMultipleSockets(t *testing.T) {
	profile := model.Profile{PublicUser: model.PublicUser{ID: "user-1", Username: "alice", DisplayName: "Alice"}}
	m := New(fakeAuth{accounts.Principal{Profile: &profile, AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour)}}, Config{PingInterval: time.Hour, SessionValidation: time.Hour}, log.New(io.Discard, "", 0))
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	mux := http.NewServeMux()
	m.RegisterWebSockets(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/realtime"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.CloseNow()
	b, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseNow()
	if got := m.UserCount(); got != 1 {
		t.Fatalf("user count=%d want 1", got)
	}
	m.PublishUsers([]string{"user-1"}, "profile.updated", map[string]string{"value": "ok"})
	seen := false
	for i := 0; i < 4 && !seen; i++ {
		var event Event
		if err := wsjson.Read(ctx, a, &event); err != nil {
			t.Fatal(err)
		}
		seen = event.Type == "profile.updated" && event.Version == 1 && event.EventID != ""
	}
	if !seen {
		t.Fatal("targeted versioned event was not delivered")
	}
}

func TestRealtimeRejectsMissingProfileBeforeUpgrade(t *testing.T) {
	m := New(fakeAuth{accounts.Principal{}}, Config{}, log.New(io.Discard, "", 0))
	_ = m.Start(context.Background())
	mux := http.NewServeMux()
	m.RegisterWebSockets(mux)
	r := httptest.NewRequest("GET", "/api/v1/realtime", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}
