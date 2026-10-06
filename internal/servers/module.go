package servers

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
	"relay/internal/validation"
)

type Authenticator interface {
	Authenticate(http.ResponseWriter, *http.Request) (accounts.Principal, error)
}
type Store interface {
	CreateServer(context.Context, string, string, string, string, string, int, int) (model.Server, error)
	Servers(context.Context, string) ([]model.Server, error)
	Server(context.Context, string, string) (model.Server, error)
	RenameServer(context.Context, string, string, string) (model.Server, error)
	ServerChannels(context.Context, string, string) ([]model.ServerChannel, error)
	ServerMembers(context.Context, string, string) ([]model.ServerMember, error)
	CreateServerInvite(context.Context, string, string, string, string, int) (model.ServerInvite, error)
	ServerInvite(context.Context, string, string) (model.ServerInvite, error)
	ServerInvites(context.Context, string) ([]model.ServerInvite, error)
	AcceptServerInvite(context.Context, string, string, int) (model.Server, error)
	DeclineServerInvite(context.Context, string, string) error
	CancelServerInvite(context.Context, string, string) (string, error)
	ServerInvitees(context.Context, string, string) ([]string, error)
	RemoveServerMember(context.Context, string, string, string) error
	LeaveServer(context.Context, string, string) error
	TransferServer(context.Context, string, string, string) error
	DeleteServer(context.Context, string, string, string) error
	ChannelAccess(context.Context, string, string) (model.ChannelAccess, error)
}

type Config struct{ NameMax, MaxOwned, MaxMemberships, InvitesPerHour int }
type Module struct {
	auth  Authenticator
	store Store
	hub   *realtime.Module
	cfg   Config
}

func New(auth Authenticator, store Store, hub *realtime.Module, cfg Config) *Module {
	if cfg.NameMax <= 0 {
		cfg.NameMax = 100
	}
	if cfg.MaxOwned <= 0 {
		cfg.MaxOwned = 20
	}
	if cfg.MaxMemberships <= 0 {
		cfg.MaxMemberships = 100
	}
	if cfg.InvitesPerHour <= 0 {
		cfg.InvitesPerHour = 30
	}
	return &Module{auth: auth, store: store, hub: hub, cfg: cfg}
}
func (m *Module) Name() string { return "servers" }
func (m *Module) Dependencies() []string {
	return []string{"persistence", "accounts", "realtime", "friends", "chat"}
}
func (m *Module) Init(context.Context) error        { return nil }
func (m *Module) Start(context.Context) error       { return nil }
func (m *Module) Stop(context.Context) error        { return nil }
func (m *Module) RegisterWebSockets(*http.ServeMux) {}
func (m *Module) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/servers", m.list)
	mux.HandleFunc("POST /api/v1/servers", m.create)
	mux.HandleFunc("GET /api/v1/servers/{id}", m.get)
	mux.HandleFunc("PATCH /api/v1/servers/{id}", m.rename)
	mux.HandleFunc("DELETE /api/v1/servers/{id}", m.delete)
	mux.HandleFunc("GET /api/v1/servers/{id}/members", m.members)
	mux.HandleFunc("DELETE /api/v1/servers/{id}/members/{userId}", m.remove)
	mux.HandleFunc("POST /api/v1/servers/{id}/ownership", m.transfer)
	mux.HandleFunc("POST /api/v1/servers/{id}/leave", m.leave)
	mux.HandleFunc("GET /api/v1/servers/{id}/channels", m.channels)
	mux.HandleFunc("POST /api/v1/servers/{id}/invites", m.invite)
	mux.HandleFunc("GET /api/v1/server-invites", m.invites)
	mux.HandleFunc("POST /api/v1/server-invites/{id}/accept", m.accept)
	mux.HandleFunc("POST /api/v1/server-invites/{id}/decline", m.decline)
	mux.HandleFunc("DELETE /api/v1/server-invites/{id}", m.cancel)
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
func mutation(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		api.WriteError(w, 403, "origin_rejected", "Request origin is not allowed.", "")
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return o == scheme+"://"+r.Host
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	items, err := m.store.Servers(r.Context(), p.User.ID)
	if err != nil {
		dbError(w)
		return
	}
	api.WriteJSON(w, 200, map[string]any{"servers": items})
}
func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	item, err := m.store.Server(r.Context(), r.PathValue("id"), p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	api.WriteJSON(w, 200, map[string]any{"server": item})
}
func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	name, err := validation.PlainText(in.Name, "server name", m.cfg.NameMax)
	if err != nil {
		api.WriteError(w, 400, "invalid_server_name", err.Error(), "name")
		return
	}
	item, err := m.store.CreateServer(r.Context(), ids.New(), ids.New(), ids.New(), p.User.ID, name, m.cfg.MaxOwned, m.cfg.MaxMemberships)
	if errors.Is(err, persistence.ErrServerNameTaken) {
		api.WriteError(w, 409, "server_name_taken", "You already own a server with that name.", "name")
		return
	}
	if errors.Is(err, persistence.ErrServerLimit) {
		api.WriteError(w, 409, "server_limit", "You have reached the owned-server limit.", "")
		return
	}
	if errors.Is(err, persistence.ErrMembershipLimit) {
		api.WriteError(w, 409, "membership_limit", "You have reached the server membership limit.", "")
		return
	}
	if err != nil {
		dbError(w)
		return
	}
	m.hub.PublishUsers([]string{p.User.ID}, "server.created", map[string]any{"server": item})
	if len(item.Channels) > 0 {
		m.hub.PublishUsers([]string{p.User.ID}, "channel.created", map[string]any{"serverId": item.ID, "channel": item.Channels[0]})
	}
	api.WriteJSON(w, 201, map[string]any{"server": item})
}
func (m *Module) rename(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	name, err := validation.PlainText(in.Name, "server name", m.cfg.NameMax)
	if err != nil {
		api.WriteError(w, 400, "invalid_server_name", err.Error(), "name")
		return
	}
	item, err := m.store.RenameServer(r.Context(), r.PathValue("id"), p.User.ID, name)
	if errors.Is(err, persistence.ErrServerNameTaken) {
		api.WriteError(w, 409, "server_name_taken", "You already own a server with that name.", "name")
		return
	}
	if err != nil {
		notFound(w)
		return
	}
	users := m.memberIDs(r.Context(), item.ID, p.User.ID)
	m.hub.PublishUsers(users, "server.updated", map[string]any{"server": item})
	api.WriteJSON(w, 200, map[string]any{"server": item})
}
func (m *Module) channels(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	items, err := m.store.ServerChannels(r.Context(), r.PathValue("id"), p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	api.WriteJSON(w, 200, map[string]any{"channels": items})
}
func (m *Module) members(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	items, err := m.store.ServerMembers(r.Context(), r.PathValue("id"), p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	for i := range items {
		items[i].Online = m.hub.IsOnline(items[i].User.ID)
	}
	api.WriteJSON(w, 200, map[string]any{"members": items})
}
func (m *Module) invite(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	var in struct {
		UserID string `json:"userId"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	if in.UserID == "" || in.UserID == p.User.ID {
		api.WriteError(w, 400, "invalid_user", "Choose one of your Relay friends.", "userId")
		return
	}
	item, err := m.store.CreateServerInvite(r.Context(), ids.New(), r.PathValue("id"), p.User.ID, in.UserID, m.cfg.InvitesPerHour)
	switch {
	case errors.Is(err, persistence.ErrFriendshipRequired):
		api.WriteError(w, 409, "friendship_required", "You can only invite a current friend.", "userId")
	case errors.Is(err, persistence.ErrAlreadyMember):
		api.WriteError(w, 409, "already_member", "That user is already a member.", "userId")
	case errors.Is(err, persistence.ErrInviteExists):
		api.WriteError(w, 409, "invite_exists", "That user already has a pending invitation.", "userId")
	case errors.Is(err, persistence.ErrInviteRateLimit):
		api.WriteError(w, 429, "rate_limited", "You are sending invitations too quickly.", "")
	case err != nil:
		notFound(w)
	default:
		m.hub.PublishUsers([]string{in.UserID}, "server.invite.created", map[string]any{"invite": item})
		api.WriteJSON(w, 201, map[string]any{"invite": item})
	}
}
func (m *Module) invites(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	items, err := m.store.ServerInvites(r.Context(), p.User.ID)
	if err != nil {
		dbError(w)
		return
	}
	api.WriteJSON(w, 200, map[string]any{"invites": items})
}
func (m *Module) accept(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	if !decodeEmpty(w, r) {
		return
	}
	inv, err := m.store.ServerInvite(r.Context(), r.PathValue("id"), p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	item, err := m.store.AcceptServerInvite(r.Context(), inv.ID, p.User.ID, m.cfg.MaxMemberships)
	if errors.Is(err, persistence.ErrMembershipLimit) {
		api.WriteError(w, 409, "membership_limit", "You have reached the server membership limit.", "")
		return
	}
	if err != nil {
		notFound(w)
		return
	}
	users := m.memberIDs(r.Context(), item.ID, p.User.ID)
	m.hub.PublishUsers(users, "server.membership.changed", map[string]any{"serverId": item.ID, "userId": p.User.ID, "action": "joined"})
	m.hub.PublishUsers([]string{p.User.ID, inv.Inviter.ID}, "server.invite.removed", map[string]any{"inviteId": inv.ID})
	api.WriteJSON(w, 200, map[string]any{"server": item})
}
func (m *Module) decline(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	if !decodeEmpty(w, r) {
		return
	}
	inv, err := m.store.ServerInvite(r.Context(), r.PathValue("id"), p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	if err = m.store.DeclineServerInvite(r.Context(), inv.ID, p.User.ID); err != nil {
		notFound(w)
		return
	}
	m.hub.PublishUsers([]string{p.User.ID, inv.Inviter.ID}, "server.invite.removed", map[string]any{"inviteId": inv.ID})
	api.WriteJSON(w, 200, map[string]string{"status": "declined"})
}
func (m *Module) cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	invitee, err := m.store.CancelServerInvite(r.Context(), r.PathValue("id"), p.User.ID)
	if err != nil {
		notFound(w)
		return
	}
	m.hub.PublishUsers([]string{p.User.ID, invitee}, "server.invite.removed", map[string]any{"inviteId": r.PathValue("id")})
	api.WriteJSON(w, 200, map[string]string{"status": "cancelled"})
}
func (m *Module) remove(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	serverID, target := r.PathValue("id"), r.PathValue("userId")
	invitees, _ := m.store.ServerInvitees(r.Context(), serverID, target)
	if err := m.store.RemoveServerMember(r.Context(), serverID, p.User.ID, target); err != nil {
		notFound(w)
		return
	}
	users := m.memberIDs(r.Context(), serverID, p.User.ID)
	users = append(users, target)
	m.hub.PublishUsers(users, "server.membership.changed", map[string]any{"serverId": serverID, "userId": target, "action": "removed"})
	m.hub.PublishUsers(invitees, "server.invite.removed", map[string]any{"serverId": serverID})
	api.WriteJSON(w, 200, map[string]string{"status": "removed"})
}
func (m *Module) leave(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	if !decodeEmpty(w, r) {
		return
	}
	serverID := r.PathValue("id")
	invitees, _ := m.store.ServerInvitees(r.Context(), serverID, p.User.ID)
	err := m.store.LeaveServer(r.Context(), serverID, p.User.ID)
	if errors.Is(err, persistence.ErrOwnerCannotLeave) {
		api.WriteError(w, 409, "owner_cannot_leave", "Transfer ownership or delete the server first.", "")
		return
	}
	if err != nil {
		notFound(w)
		return
	}
	users := m.memberIDs(r.Context(), serverID, p.User.ID)
	users = append(users, p.User.ID)
	m.hub.PublishUsers(users, "server.membership.changed", map[string]any{"serverId": serverID, "userId": p.User.ID, "action": "left"})
	m.hub.PublishUsers(invitees, "server.invite.removed", map[string]any{"serverId": serverID})
	api.WriteJSON(w, 200, map[string]string{"status": "left"})
}
func (m *Module) transfer(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	var in struct {
		UserID  string `json:"userId"`
		Confirm bool   `json:"confirm"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	if !in.Confirm || in.UserID == "" {
		api.WriteError(w, 400, "confirmation_required", "Confirm the ownership transfer.", "confirm")
		return
	}
	serverID := r.PathValue("id")
	if err := m.store.TransferServer(r.Context(), serverID, p.User.ID, in.UserID); err != nil {
		notFound(w)
		return
	}
	users := m.memberIDs(r.Context(), serverID, p.User.ID)
	m.hub.PublishUsers(users, "server.membership.changed", map[string]any{"serverId": serverID, "userId": in.UserID, "action": "ownership_transferred"})
	api.WriteJSON(w, 200, map[string]string{"status": "transferred"})
}
func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok || !mutation(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !api.DecodeJSON(w, r, &in) {
		return
	}
	serverID := r.PathValue("id")
	users := m.memberIDs(r.Context(), serverID, p.User.ID)
	invitees, _ := m.store.ServerInvitees(r.Context(), serverID, "")
	users = append(users, invitees...)
	if err := m.store.DeleteServer(r.Context(), serverID, p.User.ID, in.Name); errors.Is(err, persistence.ErrConfirmation) {
		api.WriteError(w, 400, "confirmation_mismatch", "Enter the current server name exactly.", "name")
		return
	} else if err != nil {
		notFound(w)
		return
	}
	m.hub.PublishUsers(users, "server.deleted", map[string]any{"serverId": serverID})
	api.WriteJSON(w, 200, map[string]string{"status": "deleted"})
}

func (m *Module) Access(ctx context.Context, conversationID, userID string) (model.ChannelAccess, error) {
	return m.store.ChannelAccess(ctx, conversationID, userID)
}
func (m *Module) List(ctx context.Context, userID string) ([]model.Server, error) {
	return m.store.Servers(ctx, userID)
}
func (m *Module) Invites(ctx context.Context, userID string) ([]model.ServerInvite, error) {
	return m.store.ServerInvites(ctx, userID)
}
func (m *Module) memberIDs(ctx context.Context, serverID, userID string) []string {
	items, err := m.store.ServerMembers(ctx, serverID, userID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, v := range items {
		out = append(out, v.User.ID)
	}
	return out
}
func decodeEmpty(w http.ResponseWriter, r *http.Request) bool {
	var in struct{}
	return api.DecodeJSON(w, r, &in)
}
func notFound(w http.ResponseWriter) {
	api.WriteError(w, 404, "server_not_found", "Server not found.", "")
}
func dbError(w http.ResponseWriter) {
	api.WriteError(w, 500, "database_error", "Relay could not complete that server request.", "")
}
