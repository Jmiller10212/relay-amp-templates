package friends

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"relay/internal/accounts"
	"relay/internal/api"
	"relay/internal/model"
	"relay/internal/persistence"
	"relay/internal/realtime"
	"relay/internal/validation"
)

type Authenticator interface {
	Authenticate(http.ResponseWriter, *http.Request) (accounts.Principal, error)
}
type Store interface {
	UserByUsername(context.Context, string) (model.PublicUser, error)
	Relationship(context.Context, string, string) (string, string, error)
	CreateFriendRequest(context.Context, string, string, string) (model.FriendRequest, bool, error)
	FriendRequests(context.Context, string) ([]model.FriendRequest, error)
	Friends(context.Context, string) ([]model.PublicUser, error)
	AcceptFriendRequest(context.Context, string, string) (string, error)
	DeleteFriendRequest(context.Context, string, string, string) (string, error)
	RemoveFriend(context.Context, string, string) error
}

type Config struct{ UsernameMin, UsernameMax, LookupsPerMinute, MutationsPerMinute int }
type counter struct {
	start time.Time
	count int
}
type Module struct {
	auth   Authenticator
	store  Store
	hub    *realtime.Module
	cfg    Config
	mu     sync.Mutex
	limits map[string]*counter
}

func New(auth Authenticator, store Store, hub *realtime.Module, cfg Config) *Module {
	if cfg.LookupsPerMinute <= 0 {
		cfg.LookupsPerMinute = 30
	}
	if cfg.MutationsPerMinute <= 0 {
		cfg.MutationsPerMinute = 20
	}
	return &Module{auth: auth, store: store, hub: hub, cfg: cfg, limits: map[string]*counter{}}
}
func (m *Module) Name() string                      { return "friends" }
func (m *Module) Dependencies() []string            { return []string{"persistence", "accounts", "realtime"} }
func (m *Module) Init(context.Context) error        { return nil }
func (m *Module) Start(context.Context) error       { return nil }
func (m *Module) Stop(context.Context) error        { return nil }
func (m *Module) RegisterWebSockets(*http.ServeMux) {}
func (m *Module) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/users/lookup", m.lookup)
	mux.HandleFunc("GET /api/v1/friends", m.list)
	mux.HandleFunc("GET /api/v1/friend-requests", m.requests)
	mux.HandleFunc("POST /api/v1/friend-requests", m.create)
	mux.HandleFunc("POST /api/v1/friend-requests/{id}/accept", m.accept)
	mux.HandleFunc("POST /api/v1/friend-requests/{id}/decline", m.decline)
	mux.HandleFunc("DELETE /api/v1/friend-requests/{id}", m.cancel)
	mux.HandleFunc("DELETE /api/v1/friends/{userId}", m.remove)
}

func (m *Module) principal(w http.ResponseWriter, r *http.Request) (accounts.Principal, bool) {
	api.NoStore(w)
	p, err := m.auth.Authenticate(w, r)
	if err != nil {
		if accounts.IsProviderUnavailable(err) {
			api.WriteError(w, 503, "auth_unavailable", "Authentication is temporarily unavailable.", "")
		} else {
			api.WriteError(w, 401, "session_expired", "Please sign in again.", "")
		}
		return accounts.Principal{}, false
	}
	if p.Profile == nil {
		api.WriteError(w, 409, "profile_required", "Finish your Relay profile first.", "")
		return accounts.Principal{}, false
	}
	return p, true
}
func (m *Module) mutation(w http.ResponseWriter, r *http.Request, p accounts.Principal) bool {
	if !sameOrigin(r) {
		api.WriteError(w, 403, "origin_rejected", "Request origin is not allowed.", "")
		return false
	}
	if r.Method != "DELETE" && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		api.WriteError(w, 415, "json_required", "Content-Type must be application/json.", "")
		return false
	}
	if !m.allow("mutation:"+p.User.ID, m.cfg.MutationsPerMinute) {
		api.WriteError(w, 429, "rate_limited", "Too many friend changes. Try again shortly.", "")
		return false
	}
	return true
}

func (m *Module) lookup(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	if !m.allow("lookup-user:"+p.User.ID, m.cfg.LookupsPerMinute) || !m.allow("lookup-ip:"+clientIP(r), m.cfg.LookupsPerMinute) {
		api.WriteError(w, 429, "rate_limited", "Too many lookups. Try again shortly.", "")
		return
	}
	username, err := validation.Username(r.URL.Query().Get("username"), m.cfg.UsernameMin, m.cfg.UsernameMax)
	if err != nil {
		api.WriteError(w, 400, "invalid_username", err.Error(), "username")
		return
	}
	user, err := m.store.UserByUsername(r.Context(), username)
	if errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, 404, "user_not_found", "No Relay user has that exact username.", "username")
		return
	}
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not look up that user.", "")
		return
	}
	relationship, requestID, err := m.store.Relationship(r.Context(), p.User.ID, user.ID)
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not load that relationship.", "")
		return
	}
	api.WriteJSON(w, 200, model.UserLookup{PublicUser: user, Relationship: relationship, RequestID: requestID})
}
func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	users, err := m.store.Friends(r.Context(), p.User.ID)
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not load friends.", "")
		return
	}
	out := make([]model.Friend, len(users))
	for i, user := range users {
		out[i] = model.Friend{PublicUser: user, Online: m.hub.IsOnline(user.ID)}
	}
	api.WriteJSON(w, 200, map[string]any{"friends": out})
}
func (m *Module) requests(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	requests, err := m.store.FriendRequests(r.Context(), p.User.ID)
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not load friend requests.", "")
		return
	}
	incoming := []model.FriendRequest{}
	outgoing := []model.FriendRequest{}
	for _, request := range requests {
		if request.Recipient.ID == p.User.ID {
			incoming = append(incoming, request)
		} else {
			outgoing = append(outgoing, request)
		}
	}
	api.WriteJSON(w, 200, map[string]any{"incoming": incoming, "outgoing": outgoing})
}
func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !m.mutation(w, r, p) {
		return
	}
	var in struct {
		Username string `json:"username"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	username, err := validation.Username(in.Username, m.cfg.UsernameMin, m.cfg.UsernameMax)
	if err != nil {
		api.WriteError(w, 400, "invalid_username", err.Error(), "username")
		return
	}
	recipient, err := m.store.UserByUsername(r.Context(), username)
	if errors.Is(err, sql.ErrNoRows) {
		api.WriteError(w, 404, "user_not_found", "No Relay user has that exact username.", "username")
		return
	}
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not send the request.", "")
		return
	}
	if recipient.ID == p.User.ID {
		api.WriteError(w, 400, "self_request", "You cannot send a friend request to yourself.", "username")
		return
	}
	id, uuidErr := uuid.NewV7()
	if uuidErr != nil {
		id = uuid.New()
	}
	request, crossed, err := m.store.CreateFriendRequest(r.Context(), id.String(), p.User.ID, recipient.ID)
	if errors.Is(err, persistence.ErrAlreadyFriends) {
		api.WriteError(w, 409, "already_friends", "You are already friends.", "")
		return
	}
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not send the request.", "")
		return
	}
	if crossed {
		data := map[string]any{"users": []string{p.User.ID, recipient.ID}}
		m.hub.PublishUsers([]string{p.User.ID, recipient.ID}, "friendship.created", data)
		api.WriteJSON(w, 200, map[string]any{"status": "friends"})
		return
	}
	m.hub.PublishUsers([]string{recipient.ID}, "friend.request.created", map[string]any{"request": request})
	api.WriteJSON(w, 201, map[string]any{"status": "pending", "request": request})
}
func (m *Module) accept(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !m.mutation(w, r, p) {
		return
	}
	other, err := m.store.AcceptFriendRequest(r.Context(), r.PathValue("id"), p.User.ID)
	if errors.Is(err, persistence.ErrFriendRequestNotFound) {
		api.WriteError(w, 404, "request_not_found", "Friend request not found.", "")
		return
	}
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not accept the request.", "")
		return
	}
	m.hub.PublishUsers([]string{p.User.ID, other}, "friendship.created", map[string]any{"users": []string{p.User.ID, other}})
	api.WriteJSON(w, 200, map[string]string{"status": "friends"})
}
func (m *Module) decline(w http.ResponseWriter, r *http.Request) { m.deleteRequest(w, r, "decline") }
func (m *Module) cancel(w http.ResponseWriter, r *http.Request)  { m.deleteRequest(w, r, "cancel") }
func (m *Module) deleteRequest(w http.ResponseWriter, r *http.Request, mode string) {
	p, ok := m.principal(w, r)
	if !ok || !m.mutation(w, r, p) {
		return
	}
	other, err := m.store.DeleteFriendRequest(r.Context(), r.PathValue("id"), p.User.ID, mode)
	if errors.Is(err, persistence.ErrFriendRequestNotFound) {
		api.WriteError(w, 404, "request_not_found", "Friend request not found.", "")
		return
	}
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not remove the request.", "")
		return
	}
	m.hub.PublishUsers([]string{p.User.ID, other}, "friend.request.removed", map[string]any{"requestId": r.PathValue("id")})
	w.WriteHeader(http.StatusNoContent)
}
func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !m.mutation(w, r, p) {
		return
	}
	other := r.PathValue("userId")
	if err := m.store.RemoveFriend(r.Context(), p.User.ID, other); err != nil {
		api.WriteError(w, 404, "friendship_not_found", "Friendship not found.", "")
		return
	}
	m.hub.PublishUsers([]string{p.User.ID, other}, "friendship.removed", map[string]any{"users": []string{p.User.ID, other}})
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) allow(key string, max int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	c := m.limits[key]
	if c == nil || now.Sub(c.start) >= time.Minute {
		m.limits[key] = &counter{start: now, count: 1}
		return true
	}
	if c.count >= max {
		return false
	}
	c.count++
	return true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}
