package friends

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"relay/internal/accounts"
	"relay/internal/model"
	"relay/internal/persistence"
	"relay/internal/realtime"
)

type headerAuth struct{ profiles map[string]model.Profile }

func (f headerAuth) Authenticate(_ http.ResponseWriter, r *http.Request) (accounts.Principal, error) {
	p := f.profiles[r.Header.Get("X-Test-User")]
	return accounts.Principal{User: accounts.AuthUser{ID: p.ID, Email: "private@example.test"}, Profile: &p}, nil
}

func TestFriendHTTPFlowAndAuthorization(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	profiles := map[string]model.Profile{}
	for _, u := range []struct{ id, name string }{{"a", "alice"}, {"b", "bob"}, {"c", "carol"}} {
		p, err := store.CreateProfile(ctx, u.id, u.name, u.name)
		if err != nil {
			t.Fatal(err)
		}
		profiles[u.id] = p
	}
	hub := realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0))
	module := New(headerAuth{profiles}, store, hub, Config{UsernameMin: 3, UsernameMax: 32, LookupsPerMinute: 30, MutationsPerMinute: 20})
	mux := http.NewServeMux()
	module.RegisterHTTP(mux)
	request := func(method, path, user, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("X-Test-User", user)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/api/v1/users/lookup?username=bob", "a", "")
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("private@example.test")) {
		t.Fatalf("lookup status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/friend-requests", "a", `{"username":"bob"}`)
	if w.Code != 201 {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	requests, err := store.FriendRequests(ctx, "b")
	if err != nil || len(requests) != 1 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	w = request("POST", "/api/v1/friend-requests/"+requests[0].ID+"/accept", "c", "{}")
	if w.Code != 404 {
		t.Fatalf("third-party accept status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/friend-requests", "b", `{"username":"alice"}`)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"friends"`)) {
		t.Fatalf("crossed status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/friends", "a", "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"username":"bob"`)) {
		t.Fatalf("friends status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestFriendMutationsRejectCrossOrigin(t *testing.T) {
	p := model.Profile{PublicUser: model.PublicUser{ID: "a", Username: "alice", DisplayName: "Alice"}}
	module := New(headerAuth{map[string]model.Profile{"a": p}}, nil, realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0)), Config{MutationsPerMinute: 20})
	mux := http.NewServeMux()
	module.RegisterHTTP(mux)
	r := httptest.NewRequest("POST", "http://relay.test/api/v1/friend-requests", bytes.NewBufferString(`{"username":"bob"}`))
	r.Host = "relay.test"
	r.Header.Set("X-Test-User", "a")
	r.Header.Set("Origin", "https://evil.test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
