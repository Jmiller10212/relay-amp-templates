package clientapi

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
	"relay/internal/directmessages"
	"relay/internal/model"
	"relay/internal/persistence"
	"relay/internal/realtime"
	"relay/internal/servers"
)

type fakeAuth struct{ principal accounts.Principal }

func (f fakeAuth) Authenticate(http.ResponseWriter, *http.Request) (accounts.Principal, error) {
	return f.principal, nil
}

type headerAuth struct{ profiles map[string]model.Profile }

func (f headerAuth) Authenticate(_ http.ResponseWriter, r *http.Request) (accounts.Principal, error) {
	p := f.profiles[r.Header.Get("X-Test-User")]
	return accounts.Principal{User: accounts.AuthUser{ID: p.ID, Email: "private@example.test"}, Profile: &p}, nil
}

type legacyRecorder struct{ messages []model.Message }

func (l *legacyRecorder) PublishStored(message model.Message) {
	l.messages = append(l.messages, message)
}

func TestVersionedGlobalMessageAPIUsesAuthenticatedIdentity(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	profile, err := store.CreateProfile(ctx, "auth-1", "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	auth := fakeAuth{accounts.Principal{User: accounts.AuthUser{ID: "auth-1", Email: "private@example.test"}, Profile: &profile}}
	hub := realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0))
	legacy := &legacyRecorder{}
	m := New(auth, store, hub, legacy, Config{RoomName: "general", MessageMaxRunes: 2000, Rate: 100, Burst: 100})
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)

	body := bytes.NewBufferString(`{"text":"hello","userId":"spoofed"}`)
	r := httptest.NewRequest("POST", "/api/v1/conversations/"+model.GlobalConversationID+"/messages", body)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("spoof status=%d body=%s", w.Code, w.Body.String())
	}

	body = bytes.NewBufferString(`{"text":"hello"}`)
	r = httptest.NewRequest("POST", "/api/v1/conversations/"+model.GlobalConversationID+"/messages", body)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("send status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Message model.Message `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Message.UserID != "auth-1" || response.Message.Username != "alice" || response.Message.ConversationID != model.GlobalConversationID {
		t.Fatalf("wrong identity: %+v", response.Message)
	}
	if len(legacy.messages) != 1 {
		t.Fatalf("legacy deliveries=%d", len(legacy.messages))
	}

	r = httptest.NewRequest("GET", "/api/v1/conversations/"+model.GlobalConversationID+"/messages", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"text":"hello"`)) {
		t.Fatalf("history status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBootstrapDoesNotExposeSessionTokens(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	profile, _ := store.CreateProfile(ctx, "auth-1", "alice", "Alice")
	auth := fakeAuth{accounts.Principal{User: accounts.AuthUser{ID: "auth-1", Email: "alice@example.test"}, Profile: &profile, AccessToken: "must-not-leak", SessionID: "must-not-leak-either"}}
	m := New(auth, store, realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0)), &legacyRecorder{}, Config{RoomName: "general"})
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	r := httptest.NewRequest("GET", "/api/v1/bootstrap", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("must-not-leak")) {
		t.Fatal("bootstrap exposed session material")
	}
}

func TestDirectMessageAuthorizationAndFriendshipChanges(t *testing.T) {
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
	friendRequest, _, err := store.CreateFriendRequest(ctx, "friend-1", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptFriendRequest(ctx, friendRequest.ID, "b"); err != nil {
		t.Fatal(err)
	}
	conversation, _, err := store.CreateDirectConversation(ctx, "01900000-0000-7000-8000-000000000001", "a", "b")
	if err != nil {
		t.Fatal(err)
	}

	auth := headerAuth{profiles}
	hub := realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0))
	direct := directmessages.New(auth, store, hub)
	m := New(auth, store, hub, &legacyRecorder{}, Config{RoomName: "general", MessageMaxRunes: 2000, Rate: 100, Burst: 100, DirectMessagesEnabled: true})
	m.SetDirectMessages(direct)
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
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

	w := request("POST", "/api/v1/conversations/"+conversation.ID+"/messages", "a", `{"text":"secret","userId":"c"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("spoof status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/conversations/"+conversation.ID+"/messages", "a", `{"text":"secret"}`)
	if w.Code != http.StatusCreated || bytes.Contains(w.Body.Bytes(), []byte("private@example.test")) {
		t.Fatalf("send status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/conversations/"+conversation.ID+"/messages", "c", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("third-party history status=%d body=%s", w.Code, w.Body.String())
	}
	if err := store.RemoveFriend(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/v1/conversations/"+conversation.ID+"/messages", "b", "")
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"text":"secret"`)) {
		t.Fatalf("former-friend history status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/conversations/"+conversation.ID+"/messages", "b", `{"text":"blocked"}`)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("friendship_required")) {
		t.Fatalf("former-friend send status=%d body=%s", w.Code, w.Body.String())
	}
	friendRequest, _, err = store.CreateFriendRequest(ctx, "friend-2", "b", "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptFriendRequest(ctx, friendRequest.ID, "a"); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/v1/conversations/"+conversation.ID+"/messages", "b", `{"text":"restored"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("re-friend send status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestServerChannelAuthorizationAndPersistence(t *testing.T) {
	ctx := context.Background()
	store := persistence.New(t.TempDir())
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer store.Stop(ctx)
	profiles := map[string]model.Profile{}
	for _, user := range []struct{ id, name string }{{"owner", "owner"}, {"member", "member"}, {"outsider", "outsider"}} {
		profile, err := store.CreateProfile(ctx, user.id, user.name, user.name)
		if err != nil {
			t.Fatal(err)
		}
		profiles[user.id] = profile
	}
	request, _, err := store.CreateFriendRequest(ctx, "friend-server", "owner", "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AcceptFriendRequest(ctx, request.ID, "member"); err != nil {
		t.Fatal(err)
	}
	server, err := store.CreateServer(ctx, "server-api", "conversation-api", "channel-api", "owner", "API Server", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateServerInvite(ctx, "invite-api", server.ID, "owner", "member", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AcceptServerInvite(ctx, invite.ID, "member", 100); err != nil {
		t.Fatal(err)
	}

	auth := headerAuth{profiles}
	hub := realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0))
	serverModule := servers.New(auth, store, hub, servers.Config{})
	m := New(auth, store, hub, &legacyRecorder{}, Config{RoomName: "general", MessageMaxRunes: 2000, Rate: 100, Burst: 100, ServersEnabled: true})
	m.SetServers(serverModule)
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	requestAPI := func(method, user, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/conversations/conversation-api/messages", bytes.NewBufferString(body))
		req.Header.Set("X-Test-User", user)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	if response := requestAPI("POST", "member", `{"text":"member message"}`); response.Code != http.StatusCreated {
		t.Fatalf("member send=%d %s", response.Code, response.Body.String())
	}
	if response := requestAPI("GET", "outsider", ""); response.Code != http.StatusNotFound {
		t.Fatalf("outsider read=%d %s", response.Code, response.Body.String())
	}
	if err = store.RemoveServerMember(ctx, server.ID, "owner", "member"); err != nil {
		t.Fatal(err)
	}
	if response := requestAPI("GET", "member", ""); response.Code != http.StatusNotFound {
		t.Fatalf("removed member read=%d %s", response.Code, response.Body.String())
	}
	history, err := store.RecentConversation(ctx, "conversation-api", 0, 10)
	if err != nil || len(history) != 1 || history[0].Text != "member message" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}
