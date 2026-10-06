package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"relay/internal/accounts"
	"relay/internal/model"
	"relay/internal/persistence"
	"relay/internal/realtime"
)

type headerAuth struct{ profiles map[string]model.Profile }

func (a headerAuth) Authenticate(_ http.ResponseWriter, r *http.Request) (accounts.Principal, error) {
	p := a.profiles[r.Header.Get("X-Test-User")]
	return accounts.Principal{User: accounts.AuthUser{ID: p.ID, Email: "private@example.test"}, Profile: &p}, nil
}

func TestServerHTTPMembershipLifecycle(t *testing.T) {
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
	request, _, err := store.CreateFriendRequest(ctx, "friends", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AcceptFriendRequest(ctx, request.ID, "b"); err != nil {
		t.Fatal(err)
	}
	hub := realtime.New(nil, realtime.Config{}, log.New(io.Discard, "", 0))
	module := New(headerAuth{profiles}, store, hub, Config{})
	mux := http.NewServeMux()
	module.RegisterHTTP(mux)
	call := func(method, path, user, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("X-Test-User", user)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/v1/servers", "a", `{"name":"Test Server"}`)
	if w.Code != 201 {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Server model.Server `json:"server"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/v1/servers", "a", `{"name":"test server"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "server_name_taken") {
		t.Fatalf("duplicate create status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/v1/servers", "c", `{"name":"TEST SERVER"}`)
	if w.Code != 201 {
		t.Fatalf("different owner matching name status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/v1/servers/"+created.Server.ID+"/invites", "a", `{"userId":"b"}`)
	if w.Code != 201 {
		t.Fatalf("invite status=%d body=%s", w.Code, w.Body.String())
	}
	var invitation struct {
		Invite model.ServerInvite `json:"invite"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &invitation); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/v1/server-invites/"+invitation.Invite.ID+"/accept", "b", `{}`)
	if w.Code != 200 {
		t.Fatalf("accept status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/servers/"+created.Server.ID+"/members", "b", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "private@example.test") {
		t.Fatalf("members status=%d body=%s", w.Code, w.Body.String())
	}
	channel := created.Server.Channels[0]
	message, err := store.InsertConversation(ctx, channel.ConversationID, "user", "a", "alice", "alice", "searchable @bob message")
	if err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/api/v1/servers/"+created.Server.ID+"/search?q=searchable&mentionsUserId=b", "b", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "searchable @bob message") {
		t.Fatalf("member search status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/servers/"+created.Server.ID+"/search?q=searchable", "c", "")
	if w.Code != 404 {
		t.Fatalf("outsider search status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("PUT", "/api/v1/channels/"+channel.ID+"/pins/"+strconv.FormatInt(message.ID, 10), "b", `{}`)
	if w.Code != 403 {
		t.Fatalf("member pin status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("PUT", "/api/v1/channels/"+channel.ID+"/pins/"+strconv.FormatInt(message.ID, 10), "a", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "searchable @bob message") {
		t.Fatalf("owner pin status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/channels/"+channel.ID+"/pins", "b", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "private@example.test") {
		t.Fatalf("pins status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("PUT", "/api/v1/channels/"+channel.ID+"/notification-preference", "b", `{"mode":"nothing"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"nothing"`) {
		t.Fatalf("notification preference status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("PATCH", "/api/v1/servers/"+created.Server.ID, "b", `{"name":"Not Allowed"}`)
	if w.Code != 404 {
		t.Fatalf("member renamed server status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/v1/servers/"+created.Server.ID+"/ownership", "a", `{"userId":"b","confirm":true}`)
	if w.Code != 200 {
		t.Fatalf("transfer status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/v1/servers/"+created.Server.ID+"/leave", "a", `{}`)
	if w.Code != 200 {
		t.Fatalf("leave status=%d body=%s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/servers/"+created.Server.ID, "a", "")
	if w.Code != 404 {
		t.Fatalf("former member status=%d", w.Code)
	}
	w = call("DELETE", "/api/v1/servers/"+created.Server.ID, "b", `{"name":"Test Server"}`)
	if w.Code != 200 {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
}
