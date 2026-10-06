package directmessages

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"relay/internal/accounts"
	"relay/internal/api"
	"relay/internal/ids"
	"relay/internal/model"
	"relay/internal/persistence"
	"relay/internal/realtime"
)

type Authenticator interface {
	Authenticate(http.ResponseWriter, *http.Request) (accounts.Principal, error)
}

type Store interface {
	CreateDirectConversation(context.Context, string, string, string) (model.DirectConversation, bool, error)
	DirectConversation(context.Context, string, string) (model.DirectConversation, error)
	DirectConversations(context.Context, string) ([]model.DirectConversation, error)
	DirectAccess(context.Context, string, string) (model.DirectAccess, error)
	AdvanceRead(context.Context, string, string, int64) (model.ReadState, error)
	ReadState(context.Context, string, string) (model.ReadState, error)
	DirectUnreadTotals(context.Context, string) (int, int, error)
}

type Module struct {
	auth  Authenticator
	store Store
	hub   *realtime.Module
}

func New(auth Authenticator, store Store, hub *realtime.Module) *Module {
	return &Module{auth: auth, store: store, hub: hub}
}

func (m *Module) Name() string { return "direct_messages" }
func (m *Module) Dependencies() []string {
	return []string{"persistence", "accounts", "realtime", "friends", "chat"}
}
func (m *Module) Init(context.Context) error        { return nil }
func (m *Module) Start(context.Context) error       { return nil }
func (m *Module) Stop(context.Context) error        { return nil }
func (m *Module) RegisterWebSockets(*http.ServeMux) {}
func (m *Module) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/direct-conversations", m.list)
	mux.HandleFunc("POST /api/v1/direct-conversations", m.create)
	mux.HandleFunc("PUT /api/v1/conversations/{id}/read", m.read)
}

func (m *Module) principal(w http.ResponseWriter, r *http.Request) (accounts.Principal, bool) {
	api.NoStore(w)
	p, err := m.auth.Authenticate(w, r)
	if err != nil {
		if accounts.IsProviderUnavailable(err) {
			api.WriteError(w, http.StatusServiceUnavailable, "auth_unavailable", "Authentication is temporarily unavailable.", "")
		} else {
			api.WriteError(w, http.StatusUnauthorized, "session_expired", "Please sign in again.", "")
		}
		return accounts.Principal{}, false
	}
	if p.Profile == nil {
		api.WriteError(w, http.StatusConflict, "profile_required", "Finish your Relay profile first.", "")
		return accounts.Principal{}, false
	}
	return p, true
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	items, err := m.store.DirectConversations(r.Context(), p.User.ID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "database_error", "Relay could not load direct messages.", "")
		return
	}
	for i := range items {
		if items[i].CanSend {
			online := m.hub.IsOnline(items[i].Peer.ID)
			items[i].Online = &online
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"conversations": items})
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !sameOrigin(r) {
		if ok {
			api.WriteError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.", "")
		}
		return
	}
	var in struct {
		UserID string `json:"userId"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	if in.UserID == "" || in.UserID == p.User.ID {
		api.WriteError(w, http.StatusBadRequest, "invalid_user", "Choose one of your Relay friends.", "userId")
		return
	}
	conversation, created, err := m.store.CreateDirectConversation(r.Context(), ids.New(), p.User.ID, in.UserID)
	if errors.Is(err, persistence.ErrFriendshipRequired) {
		api.WriteError(w, http.StatusConflict, "friendship_required", "You can only start a direct message with a friend.", "userId")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "database_error", "Relay could not open that conversation.", "")
		return
	}
	if conversation.CanSend {
		online := m.hub.IsOnline(conversation.Peer.ID)
		conversation.Online = &online
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		m.hub.PublishUsers([]string{p.User.ID, in.UserID}, "conversation.created", map[string]any{"conversationId": conversation.ID})
	}
	api.WriteJSON(w, status, map[string]any{"conversation": conversation, "created": created})
}

func (m *Module) read(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !sameOrigin(r) {
		if ok {
			api.WriteError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.", "")
		}
		return
	}
	var in struct {
		MessageID int64 `json:"messageId"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	if in.MessageID < 1 {
		api.WriteError(w, http.StatusBadRequest, "invalid_read_cursor", "Message cursor is invalid.", "messageId")
		return
	}
	state, err := m.store.AdvanceRead(r.Context(), r.PathValue("id"), p.User.ID, in.MessageID)
	if errors.Is(err, persistence.ErrConversationNotFound) {
		api.WriteError(w, http.StatusNotFound, "conversation_not_found", "Conversation not found.", "")
		return
	}
	if errors.Is(err, persistence.ErrInvalidReadCursor) {
		api.WriteError(w, http.StatusBadRequest, "invalid_read_cursor", "That message does not belong to this conversation.", "messageId")
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "database_error", "Relay could not update read state.", "")
		return
	}
	m.hub.PublishUsers([]string{p.User.ID}, "conversation.unread_updated", map[string]any{"read": state})
	api.WriteJSON(w, http.StatusOK, map[string]any{"read": state})
}

func (m *Module) Access(ctx context.Context, conversationID, userID string) (model.DirectAccess, error) {
	return m.store.DirectAccess(ctx, conversationID, userID)
}

func (m *Module) ReadState(ctx context.Context, conversationID, userID string) (model.ReadState, error) {
	return m.store.ReadState(ctx, conversationID, userID)
}

func (m *Module) UnreadTotals(ctx context.Context, userID string) (int, int, error) {
	return m.store.DirectUnreadTotals(ctx, userID)
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
