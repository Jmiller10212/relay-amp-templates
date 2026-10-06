package clientapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
	Conversation(context.Context, string) (model.Conversation, error)
	RecentConversation(context.Context, string, int64, int) ([]model.Message, error)
	InsertConversation(context.Context, string, string, string, string, string, string) (model.Message, error)
	FriendRequests(context.Context, string) ([]model.FriendRequest, error)
}

type LegacyPublisher interface{ PublishStored(model.Message) }

type DirectMessages interface {
	Access(context.Context, string, string) (model.DirectAccess, error)
	ReadState(context.Context, string, string) (model.ReadState, error)
	UnreadTotals(context.Context, string) (int, int, error)
}

type Servers interface {
	Access(context.Context, string, string) (model.ChannelAccess, error)
	List(context.Context, string) ([]model.Server, error)
	Invites(context.Context, string) ([]model.ServerInvite, error)
}

type Config struct {
	RoomName              string
	HistoryLimit          int
	MessageMaxRunes       int
	Rate                  float64
	Burst                 int
	FriendsEnabled        bool
	DirectMessagesEnabled bool
	ServersEnabled        bool
}

type Module struct {
	auth     Authenticator
	store    Store
	realtime *realtime.Module
	legacy   LegacyPublisher
	direct   DirectMessages
	servers  Servers
	cfg      Config
	mu       sync.Mutex
	limiters map[string]*limiter
}

func (m *Module) SetDirectMessages(direct DirectMessages) { m.direct = direct }
func (m *Module) SetServers(servers Servers)              { m.servers = servers }

type limiter struct {
	tokens, rate, burst float64
	last                time.Time
}

func New(auth Authenticator, store Store, hub *realtime.Module, legacy LegacyPublisher, cfg Config) *Module {
	if cfg.HistoryLimit <= 0 {
		cfg.HistoryLimit = 50
	}
	if cfg.MessageMaxRunes <= 0 {
		cfg.MessageMaxRunes = 2000
	}
	if cfg.Rate <= 0 {
		cfg.Rate = 5
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 10
	}
	return &Module{auth: auth, store: store, realtime: hub, legacy: legacy, cfg: cfg, limiters: map[string]*limiter{}}
}

func (m *Module) Name() string { return "client_api" }
func (m *Module) Dependencies() []string {
	return []string{"persistence", "accounts", "realtime", "chat"}
}
func (m *Module) Init(context.Context) error        { return nil }
func (m *Module) Start(context.Context) error       { return nil }
func (m *Module) Stop(context.Context) error        { return nil }
func (m *Module) RegisterWebSockets(*http.ServeMux) {}
func (m *Module) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/bootstrap", m.bootstrap)
	mux.HandleFunc("GET /api/v1/presence", m.presence)
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", m.messages)
	mux.HandleFunc("POST /api/v1/conversations/{id}/messages", m.send)
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

func (m *Module) bootstrap(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	conversation, err := m.store.Conversation(r.Context(), model.GlobalConversationID)
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not load the lobby.", "")
		return
	}
	pendingFriends := 0
	if m.cfg.FriendsEnabled {
		if requests, loadErr := m.store.FriendRequests(r.Context(), p.User.ID); loadErr == nil {
			for _, request := range requests {
				if request.Recipient.ID == p.User.ID {
					pendingFriends++
				}
			}
		}
	}
	unreadMessages, unreadConversations := 0, 0
	if m.cfg.DirectMessagesEnabled && m.direct != nil {
		if messages, conversations, loadErr := m.direct.UnreadTotals(r.Context(), p.User.ID); loadErr == nil {
			unreadMessages, unreadConversations = messages, conversations
		}
	}
	serverItems := []model.Server{}
	serverInvites := []model.ServerInvite{}
	if m.cfg.ServersEnabled && m.servers != nil {
		serverItems, _ = m.servers.List(r.Context(), p.User.ID)
		serverInvites, _ = m.servers.Invites(r.Context(), p.User.ID)
	}
	api.WriteJSON(w, 200, map[string]any{
		"account":       map[string]any{"email": p.User.Email, "profile": p.Profile, "profileRequired": false},
		"capabilities":  map[string]bool{"realtime": true, "globalLobby": true, "friends": m.cfg.FriendsEnabled, "directMessages": m.cfg.DirectMessagesEnabled, "servers": m.cfg.ServersEnabled, "channels": m.cfg.ServersEnabled},
		"globalLobby":   map[string]any{"id": conversation.ID, "name": m.cfg.RoomName},
		"servers":       serverItems,
		"serverInvites": serverInvites,
		"counts":        map[string]int{"pendingFriends": pendingFriends, "unreadMessages": unreadMessages, "unreadConversations": unreadConversations, "homeActivity": pendingFriends + unreadMessages + len(serverInvites), "serverInvites": len(serverInvites), "unread": unreadMessages},
	})
}

func (m *Module) presence(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.principal(w, r); !ok {
		return
	}
	api.WriteJSON(w, 200, map[string]any{"users": m.realtime.OnlineUsers()})
}

func (m *Module) messages(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	conversationID := r.PathValue("id")
	if _, err := m.access(r.Context(), conversationID, p.User.ID, false); err != nil {
		api.WriteError(w, 404, "conversation_not_found", "Conversation not found.", "")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			api.WriteError(w, 400, "invalid_limit", "Limit must be between 1 and 100.", "limit")
			return
		}
		limit = n
	}
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			api.WriteError(w, 400, "invalid_cursor", "Cursor is invalid.", "before")
			return
		}
		before = n
	}
	messages, err := m.store.RecentConversation(r.Context(), conversationID, before, limit+1)
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not load messages.", "")
		return
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[1:]
	}
	var nextBefore int64
	if hasMore && len(messages) > 0 {
		nextBefore = messages[0].ID
	}
	api.WriteJSON(w, 200, map[string]any{"messages": messages, "hasMore": hasMore, "nextBefore": nextBefore})
}

func (m *Module) send(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	if !sameOrigin(r) {
		api.WriteError(w, 403, "origin_rejected", "Request origin is not allowed.", "")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		api.WriteError(w, 415, "json_required", "Content-Type must be application/json.", "")
		return
	}
	conversationID := r.PathValue("id")
	access, err := m.access(r.Context(), conversationID, p.User.ID, true)
	if errors.Is(err, persistence.ErrFriendshipRequired) {
		api.WriteError(w, http.StatusConflict, "friendship_required", "This conversation is read-only because you are no longer friends.", "")
		return
	}
	if err != nil {
		api.WriteError(w, 404, "conversation_not_found", "Conversation not found.", "")
		return
	}
	if !m.allow(p.User.ID) {
		api.WriteError(w, 429, "rate_limited", "You are sending messages too quickly.", "")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	text, err := validation.PlainText(in.Text, "message", m.cfg.MessageMaxRunes)
	if err != nil {
		api.WriteError(w, 400, "invalid_message", err.Error(), "text")
		return
	}
	msg, err := m.store.InsertConversation(r.Context(), conversationID, "user", p.User.ID, p.Profile.Username, p.Profile.DisplayName, text)
	if err != nil {
		api.WriteError(w, 500, "database_error", "Relay could not save the message.", "")
		return
	}
	if conversationID == model.GlobalConversationID {
		m.realtime.PublishAll("message.created", map[string]any{"message": msg})
		m.legacy.PublishStored(msg)
	} else if access.DirectPeerID != "" {
		m.realtime.PublishUsers([]string{p.User.ID, access.DirectPeerID}, "message.created", map[string]any{"message": msg})
		if state, readErr := m.direct.ReadState(r.Context(), conversationID, access.DirectPeerID); readErr == nil {
			m.realtime.PublishUsers([]string{access.DirectPeerID}, "conversation.unread_updated", map[string]any{"read": state})
		}
	} else {
		m.realtime.PublishUsers(access.Audience, "message.created", map[string]any{"message": msg, "serverId": access.ServerID})
	}
	api.WriteJSON(w, 201, map[string]any{"message": msg})
}

type conversationAccess struct {
	DirectPeerID, ServerID string
	Audience               []string
	CanSend                bool
}

func (m *Module) access(ctx context.Context, conversationID, userID string, sending bool) (conversationAccess, error) {
	if conversationID == model.GlobalConversationID {
		return conversationAccess{CanSend: true}, nil
	}
	if m.cfg.DirectMessagesEnabled && m.direct != nil {
		access, err := m.direct.Access(ctx, conversationID, userID)
		if err == nil {
			if sending && !access.CanSend {
				return conversationAccess{}, persistence.ErrFriendshipRequired
			}
			return conversationAccess{DirectPeerID: access.PeerID, Audience: []string{userID, access.PeerID}, CanSend: access.CanSend}, nil
		}
		if !errors.Is(err, persistence.ErrConversationNotFound) {
			return conversationAccess{}, err
		}
	}
	if m.cfg.ServersEnabled && m.servers != nil {
		access, err := m.servers.Access(ctx, conversationID, userID)
		if err == nil {
			return conversationAccess{ServerID: access.ServerID, Audience: access.MemberIDs, CanSend: true}, nil
		}
		if !errors.Is(err, persistence.ErrConversationNotFound) {
			return conversationAccess{}, err
		}
	}
	return conversationAccess{}, persistence.ErrConversationNotFound
}

func (m *Module) allow(userID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	l := m.limiters[userID]
	if l == nil {
		l = &limiter{tokens: float64(m.cfg.Burst), rate: m.cfg.Rate, burst: float64(m.cfg.Burst), last: now}
		m.limiters[userID] = l
	}
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
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
