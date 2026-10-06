package chat

import (
	"context"
	"errors"
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
	"relay/internal/persistence"
)

type fakeAuth struct{ next int }

func (f *fakeAuth) Authenticate(http.ResponseWriter, *http.Request) (accounts.Identity, error) {
	f.next++
	u := model.Profile{PublicUser: model.PublicUser{ID: "user-" + string(rune('0'+f.next)), Username: []string{"ada", "lin", "grace"}[(f.next-1)%3], DisplayName: []string{"Ada", "Lin", "Grace"}[(f.next-1)%3]}}
	return accounts.Identity{Profile: &u, AccessToken: "token"}, nil
}
func (*fakeAuth) ValidateSession(context.Context, string, string) error { return nil }

type fixedAuth struct{ reject bool }

func (f *fixedAuth) Authenticate(http.ResponseWriter, *http.Request) (accounts.Identity, error) {
	if f.reject {
		return accounts.Identity{}, errors.New("unauthorized")
	}
	p := model.Profile{PublicUser: model.PublicUser{ID: "auth-one", Username: "ada", DisplayName: "Ada"}}
	return accounts.Identity{Profile: &p, AccessToken: "token"}, nil
}
func (*fixedAuth) ValidateSession(context.Context, string, string) error { return nil }

func TestAuthenticatedClientsExchangeAndBroadcastPersists(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	_, _ = store.CreateProfile(ctx, "user-1", "ada", "Ada")
	_, _ = store.CreateProfile(ctx, "user-2", "lin", "Lin")
	m := New(store, &fakeAuth{}, Config{HistoryLimit: 100, MessageMaxRunes: 2000, Rate: 100, Burst: 100}, log.New(io.Discard, "", 0))
	_ = m.Start(ctx)
	defer m.Stop(ctx)
	mux := http.NewServeMux()
	m.RegisterWebSockets(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	one := dial(t, url)
	defer one.CloseNow()
	two := dial(t, url)
	defer two.CloseNow()
	if err := wsjson.Write(ctx, one, inbound{Type: "message", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*websocket.Conn{one, two} {
		e := readEventType(t, c, "message")
		if e.Message == nil || e.Message.Username != "ada" || e.Message.DisplayName != "Ada" {
			t.Fatalf("unexpected message: %+v", e)
		}
	}
	if err := m.Broadcast(ctx, "maintenance"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*websocket.Conn{one, two} {
		e := readEventType(t, c, "message")
		if e.Message == nil || e.Message.Kind != "system" {
			t.Fatalf("unexpected broadcast: %+v", e)
		}
	}
	got, err := store.Recent(ctx, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("history=%+v err=%v", got, err)
	}
}

func TestIdentityFieldsCannotBeSpoofed(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	_ = store.Init(ctx)
	defer store.Stop(ctx)
	m := New(store, &fakeAuth{}, Config{HistoryLimit: 10, MessageMaxRunes: 100, Rate: 10, Burst: 10}, log.New(io.Discard, "", 0))
	_ = m.Start(ctx)
	defer m.Stop(ctx)
	mux := http.NewServeMux()
	m.RegisterWebSockets(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := dial(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	defer c.CloseNow()
	_ = wsjson.Write(ctx, c, inbound{Type: "message", Text: "bad", Username: "SERVER"})
	e := readEventType(t, c, "error")
	if e.Code != "invalid_event" {
		t.Fatalf("unexpected: %+v", e)
	}
	history, _ := store.Recent(ctx, 10)
	if len(history) != 0 {
		t.Fatalf("spoofed message persisted: %+v", history)
	}
}

func TestCrossOriginRejected(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	_ = store.Init(ctx)
	defer store.Stop(ctx)
	m := New(store, &fakeAuth{}, Config{HistoryLimit: 10, MessageMaxRunes: 100, Rate: 10, Burst: 10}, log.New(io.Discard, "", 0))
	_ = m.Start(ctx)
	defer m.Stop(ctx)
	mux := http.NewServeMux()
	m.RegisterWebSockets(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	header := http.Header{"Origin": []string{"https://evil.example"}}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: header})
	if c != nil {
		c.CloseNow()
	}
	if err == nil {
		t.Fatal("expected cross-origin rejection")
	}
}

func TestUnauthenticatedUpgradeRejectedAndMultiTabPresenceIsUnique(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	_ = store.Init(ctx)
	defer store.Stop(ctx)
	rejected := New(store, &fixedAuth{reject: true}, Config{HistoryLimit: 10, MessageMaxRunes: 100, Rate: 10, Burst: 10}, log.New(io.Discard, "", 0))
	_ = rejected.Start(ctx)
	defer rejected.Stop(ctx)
	mux := http.NewServeMux()
	rejected.RegisterWebSockets(mux)
	srv := httptest.NewServer(mux)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	c, res, err := websocket.Dial(ctx, url, nil)
	if c != nil {
		c.CloseNow()
	}
	if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err=%v status=%v", err, res)
	}
	srv.Close()
	_, _ = store.CreateProfile(ctx, "auth-one", "ada", "Ada")
	m := New(store, &fixedAuth{}, Config{HistoryLimit: 10, MessageMaxRunes: 100, Rate: 10, Burst: 10}, log.New(io.Discard, "", 0))
	_ = m.Start(ctx)
	defer m.Stop(ctx)
	mux = http.NewServeMux()
	m.RegisterWebSockets(mux)
	srv = httptest.NewServer(mux)
	defer srv.Close()
	url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	one := dial(t, url)
	defer one.CloseNow()
	two := dial(t, url)
	defer two.CloseNow()
	if got := m.Users(); len(got) != 1 || m.UserCount() != 2 {
		t.Fatalf("users=%v connections=%d", got, m.UserCount())
	}
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := readEventType(t, c, "welcome")
	if e.ClientID == "" {
		t.Fatal("missing client id")
	}
	return c
}
func readEventType(t *testing.T, c *websocket.Conn, kind string) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var e Event
		if err := wsjson.Read(ctx, c, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == kind {
			return e
		}
	}
}
