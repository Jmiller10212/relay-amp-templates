package directmessages

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestDirectMessageHTTPFlowAndPrivacy(t *testing.T) {
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
	friendRequest, _, err := store.CreateFriendRequest(ctx, "friend-request", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptFriendRequest(ctx, friendRequest.ID, "b"); err != nil {
		t.Fatal(err)
	}

	hub := realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0))
	module := New(headerAuth{profiles}, store, hub)
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

	w := request("POST", "/api/v1/direct-conversations", "a", `{"userId":"c"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("non-friend create status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/direct-conversations", "a", `{"userId":"b"}`)
	if w.Code != http.StatusCreated || bytes.Contains(w.Body.Bytes(), []byte("private@example.test")) {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Conversation model.DirectConversation `json:"conversation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/v1/direct-conversations", "b", `{"userId":"a"}`)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"created":false`)) {
		t.Fatalf("reuse status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/direct-conversations", "c", "")
	if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte(created.Conversation.ID)) {
		t.Fatalf("third-party list status=%d body=%s", w.Code, w.Body.String())
	}
	message, err := store.InsertConversation(ctx, created.Conversation.ID, "user", "a", "alice", "Alice", "private hello")
	if err != nil {
		t.Fatal(err)
	}
	w = request("PUT", "/api/v1/conversations/"+created.Conversation.ID+"/read", "b", `{"messageId":`+jsonNumber(message.ID)+`}`)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"unreadCount":0`)) {
		t.Fatalf("read status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("PUT", "/api/v1/conversations/"+created.Conversation.ID+"/read", "c", `{"messageId":`+jsonNumber(message.ID)+`}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("third-party read status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDirectMessageMutationRejectsCrossOrigin(t *testing.T) {
	p := model.Profile{PublicUser: model.PublicUser{ID: "a", Username: "alice", DisplayName: "Alice"}}
	module := New(headerAuth{map[string]model.Profile{"a": p}}, nil, realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0)))
	mux := http.NewServeMux()
	module.RegisterHTTP(mux)
	r := httptest.NewRequest("POST", "http://relay.test/api/v1/direct-conversations", bytes.NewBufferString(`{"userId":"b"}`))
	r.Host = "relay.test"
	r.Header.Set("X-Test-User", "a")
	r.Header.Set("Origin", "https://evil.test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func jsonNumber(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}
