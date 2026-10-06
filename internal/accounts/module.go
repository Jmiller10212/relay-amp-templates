package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"relay/internal/model"
	"relay/internal/persistence"
	"relay/internal/validation"
)

const accessCookie = "relay_sb_access"
const refreshCookie = "relay_sb_refresh"

type Config struct {
	RegistrationEnabled                                          bool
	PublicBaseURL, CookieSecureMode                              string
	UsernameMin, UsernameMax, DisplayNameMin, DisplayNameMax     int
	UsernameCooldown, ReservationLifetime, RecoveryGrantLifetime time.Duration
	RateLimitAttempts                                            int
	RateLimitWindow, SessionValidation                           time.Duration
}
type Store interface {
	ReserveRegistration(context.Context, persistence.PendingRegistration) error
	ReleaseRegistration(context.Context, string) error
	AttachReservationUser(context.Context, string, string) error
	ProvisionReservedProfile(context.Context, string, string) (model.Profile, error)
	CreateProfile(context.Context, string, string, string) (model.Profile, error)
	Profile(context.Context, string) (model.Profile, error)
	UpdateDisplayName(context.Context, string, string) (model.Profile, error)
	UpdateUsername(context.Context, string, string, time.Duration) (model.Profile, error)
	CreateRecoveryGrant(context.Context, string, string, time.Time) error
	ConsumeRecoveryGrant(context.Context, string, string) error
}
type Identity struct {
	User        AuthUser       `json:"user"`
	Profile     *model.Profile `json:"profile,omitempty"`
	AccessToken string         `json:"-"`
	SessionID   string         `json:"-"`
	ExpiresAt   time.Time      `json:"-"`
}
type Principal = Identity
type Module struct {
	provider       AuthProvider
	store          Store
	cfg            Config
	log            *log.Logger
	limiter        *ipLimiter
	profileChanged func(model.PublicUser)
	transport      SessionTransport
	refreshMu      sync.Mutex
	refreshes      map[string]*refreshCall
}

type refreshCall struct {
	done    chan struct{}
	session Session
	err     error
}

func New(provider AuthProvider, store Store, cfg Config, logger *log.Logger) *Module {
	return &Module{provider: provider, store: store, cfg: cfg, log: logger, limiter: newIPLimiter(cfg.RateLimitAttempts, cfg.RateLimitWindow), transport: NewCookieSessionTransport(cfg.CookieSecureMode, cfg.PublicBaseURL), refreshes: map[string]*refreshCall{}}
}
func (m *Module) Name() string                               { return "accounts" }
func (m *Module) Dependencies() []string                     { return []string{"persistence"} }
func (m *Module) Init(context.Context) error                 { return nil }
func (m *Module) Start(context.Context) error                { return nil }
func (m *Module) Stop(context.Context) error                 { return nil }
func (m *Module) RegisterWebSockets(*http.ServeMux)          {}
func (m *Module) OnProfileChanged(fn func(model.PublicUser)) { m.profileChanged = fn }
func (m *Module) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/config", m.authConfig)
	mux.HandleFunc("POST /api/auth/register", m.guard(m.register))
	mux.HandleFunc("POST /api/auth/login", m.guard(m.login))
	mux.HandleFunc("POST /api/auth/resend-verification", m.guard(m.resend))
	mux.HandleFunc("POST /api/auth/forgot-password", m.guard(m.forgot))
	mux.HandleFunc("POST /api/auth/logout", m.guard(m.logout))
	mux.HandleFunc("GET /auth/callback", m.callback)
	mux.HandleFunc("GET /api/account/me", m.me)
	mux.HandleFunc("POST /api/account/complete-profile", m.guard(m.completeProfile))
	mux.HandleFunc("PATCH /api/account/display-name", m.guard(m.displayName))
	mux.HandleFunc("POST /api/account/username", m.guard(m.username))
	mux.HandleFunc("POST /api/account/password", m.guard(m.password))
	mux.HandleFunc("POST /api/auth/reset-password", m.guard(m.resetPassword))
	mux.HandleFunc("GET /api/v1/me", m.me)
	mux.HandleFunc("PATCH /api/v1/me/display-name", m.guard(m.displayName))
	mux.HandleFunc("POST /api/v1/me/username", m.guard(m.username))
	mux.HandleFunc("POST /api/v1/me/password", m.guard(m.password))
}

func (m *Module) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		if !sameOrigin(r) {
			writeError(w, 403, "origin_rejected", "Request origin is not allowed.", "")
			return
		}
		if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			writeError(w, 415, "json_required", "Content-Type must be application/json.", "")
			return
		}
		if !m.limiter.Allow(clientIP(r)) {
			writeError(w, 429, "rate_limited", "Too many requests. Try again later.", "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		next(w, r)
	}
}
func (m *Module) authConfig(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	writeJSON(w, 200, map[string]any{"registrationEnabled": m.cfg.RegistrationEnabled, "usernameMin": m.cfg.UsernameMin, "usernameMax": m.cfg.UsernameMax, "displayNameMin": m.cfg.DisplayNameMin, "displayNameMax": m.cfg.DisplayNameMax})
}

func (m *Module) register(w http.ResponseWriter, r *http.Request) {
	if !m.cfg.RegistrationEnabled {
		writeError(w, 403, "registration_disabled", "Registration is disabled.", "")
		return
	}
	var in struct{ Email, Username, DisplayName, Password, PasswordConfirmation string }
	if !decodeJSON(w, r, &in) {
		return
	}
	username, err := validation.Username(in.Username, m.cfg.UsernameMin, m.cfg.UsernameMax)
	if err != nil {
		writeError(w, 400, "invalid_username", err.Error(), "username")
		return
	}
	display, err := validation.DisplayName(in.DisplayName, m.cfg.DisplayNameMin, m.cfg.DisplayNameMax)
	if err != nil {
		writeError(w, 400, "invalid_display_name", err.Error(), "displayName")
		return
	}
	if in.Password == "" || in.Password != in.PasswordConfirmation {
		writeError(w, 400, "password_mismatch", "Passwords do not match.", "passwordConfirmation")
		return
	}
	email := normalizeEmail(in.Email)
	if email == "" {
		writeError(w, 400, "invalid_email", "Enter a valid email address.", "email")
		return
	}
	nonce := randomID()
	p := persistence.PendingRegistration{Nonce: nonce, EmailHash: emailHash(email), Username: username, DisplayName: display, ExpiresAt: time.Now().Add(m.cfg.ReservationLifetime)}
	if err := m.store.ReserveRegistration(r.Context(), p); err != nil {
		if errors.Is(err, persistence.ErrUsernameTaken) {
			writeError(w, 409, "username_taken", "That username is unavailable.", "username")
		} else {
			m.internal(w, "reserve registration", err)
		}
		return
	}
	user, _, err := m.provider.SignUp(r.Context(), email, in.Password, m.callbackURL())
	if err != nil || user.ID == "" || user.IsObfuscatedSignup() {
		if releaseErr := m.store.ReleaseRegistration(r.Context(), nonce); releaseErr != nil {
			m.log.Printf("ERROR release registration reservation: %v", releaseErr)
		}
	} else if attachErr := m.store.AttachReservationUser(r.Context(), nonce, user.ID); attachErr != nil {
		m.log.Printf("ERROR attach registration reservation: %v", attachErr)
	}
	if err != nil {
		m.log.Printf("WARN registration provider rejected request status=%s", providerStatus(err))
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "check_email"})
}
func (m *Module) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !decodeJSON(w, r, &in) {
		return
	}
	s, err := m.provider.PasswordLogin(r.Context(), normalizeEmail(in.Email), in.Password)
	if err != nil {
		writeError(w, 401, "invalid_credentials", "Email or password is incorrect.", "")
		return
	}
	m.setSession(w, r, s)
	id, err := m.identity(r.Context(), s.AccessToken)
	if err != nil {
		m.clearSession(w, r)
		writeError(w, 502, "auth_unavailable", "Authentication is temporarily unavailable.", "")
		return
	}
	writeJSON(w, 200, accountResponse(id))
}
func (m *Module) resend(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email string }
	if !decodeJSON(w, r, &in) {
		return
	}
	_ = m.provider.ResendVerification(r.Context(), normalizeEmail(in.Email), m.callbackURL())
	writeJSON(w, 202, map[string]string{"status": "check_email"})
}
func (m *Module) forgot(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email string }
	if !decodeJSON(w, r, &in) {
		return
	}
	_ = m.provider.RequestRecovery(r.Context(), normalizeEmail(in.Email), m.callbackURL())
	writeJSON(w, 202, map[string]string{"status": "check_email"})
}
func (m *Module) logout(w http.ResponseWriter, r *http.Request) {
	if tokens, err := m.transport.Read(r); err == nil {
		access := tokens.AccessToken
		if access == "" {
			if session, refreshErr := m.refresh(r.Context(), tokens.RefreshToken); refreshErr == nil {
				access = session.AccessToken
			}
		}
		if access != "" {
			_ = m.provider.Logout(r.Context(), access, "local")
		}
	}
	m.clearSession(w, r)
	writeJSON(w, 200, map[string]string{"status": "signed_out"})
}

func (m *Module) callback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	token := r.URL.Query().Get("token_hash")
	kind := r.URL.Query().Get("type")
	if token == "" || (kind != "email" && kind != "signup" && kind != "recovery") {
		http.Redirect(w, r, "/?auth=invalid_link", http.StatusSeeOther)
		return
	}
	s, err := m.provider.Verify(r.Context(), token, kind)
	if err != nil {
		http.Redirect(w, r, "/?auth=expired_link", http.StatusSeeOther)
		return
	}
	user, err := m.provider.CurrentUser(r.Context(), s.AccessToken)
	if err != nil {
		http.Redirect(w, r, "/?auth=provider_error", http.StatusSeeOther)
		return
	}
	s.User = user
	m.setSession(w, r, s)
	if kind == "recovery" {
		sid := sessionID(s.AccessToken)
		if sid == "" || m.store.CreateRecoveryGrant(r.Context(), sid, user.ID, time.Now().Add(m.cfg.RecoveryGrantLifetime)) != nil {
			m.clearSession(w, r)
			http.Redirect(w, r, "/?auth=provider_error", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?auth=recovery", http.StatusSeeOther)
		return
	}
	_, err = m.store.ProvisionReservedProfile(r.Context(), user.ID, emailHash(user.Email))
	if err != nil && !errors.Is(err, persistence.ErrNoReservation) {
		m.log.Printf("ERROR profile provisioning: %v", err)
	}
	if errors.Is(err, persistence.ErrNoReservation) {
		http.Redirect(w, r, "/?auth=finish_profile", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/?auth=verified", http.StatusSeeOther)
	}
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	id, err := m.Authenticate(w, r)
	if err != nil {
		m.writeAuthError(w, err)
		return
	}
	writeJSON(w, 200, accountResponse(id))
}
func (m *Module) completeProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := m.require(w, r)
	if !ok {
		return
	}
	if id.Profile != nil {
		writeJSON(w, 200, accountResponse(id))
		return
	}
	var in struct{ Username, DisplayName string }
	if !decodeJSON(w, r, &in) {
		return
	}
	u, err := validation.Username(in.Username, m.cfg.UsernameMin, m.cfg.UsernameMax)
	if err != nil {
		writeError(w, 400, "invalid_username", err.Error(), "username")
		return
	}
	d, err := validation.DisplayName(in.DisplayName, m.cfg.DisplayNameMin, m.cfg.DisplayNameMax)
	if err != nil {
		writeError(w, 400, "invalid_display_name", err.Error(), "displayName")
		return
	}
	p, err := m.store.CreateProfile(r.Context(), id.User.ID, u, d)
	if err != nil {
		if errors.Is(err, persistence.ErrUsernameTaken) {
			writeError(w, 409, "username_taken", "That username is unavailable.", "username")
		} else {
			m.internal(w, "create profile", err)
		}
		return
	}
	id.Profile = &p
	writeJSON(w, 200, accountResponse(id))
}
func (m *Module) displayName(w http.ResponseWriter, r *http.Request) {
	id, ok := m.requireProfile(w, r)
	if !ok {
		return
	}
	var in struct{ DisplayName string }
	if !decodeJSON(w, r, &in) {
		return
	}
	d, err := validation.DisplayName(in.DisplayName, m.cfg.DisplayNameMin, m.cfg.DisplayNameMax)
	if err != nil {
		writeError(w, 400, "invalid_display_name", err.Error(), "displayName")
		return
	}
	p, err := m.store.UpdateDisplayName(r.Context(), id.User.ID, d)
	if err != nil {
		m.internal(w, "update display name", err)
		return
	}
	m.notify(p.PublicUser)
	id.Profile = &p
	writeJSON(w, 200, accountResponse(id))
}
func (m *Module) username(w http.ResponseWriter, r *http.Request) {
	id, ok := m.requireProfile(w, r)
	if !ok {
		return
	}
	var in struct{ Username, Password string }
	if !decodeJSON(w, r, &in) {
		return
	}
	u, err := validation.Username(in.Username, m.cfg.UsernameMin, m.cfg.UsernameMax)
	if err != nil {
		writeError(w, 400, "invalid_username", err.Error(), "username")
		return
	}
	s, err := m.provider.PasswordLogin(r.Context(), id.User.Email, in.Password)
	if err != nil {
		writeError(w, 401, "password_incorrect", "Current password is incorrect.", "password")
		return
	}
	p, err := m.store.UpdateUsername(r.Context(), id.User.ID, u, m.cfg.UsernameCooldown)
	if err != nil {
		if errors.Is(err, persistence.ErrCooldown) {
			writeError(w, 409, "username_cooldown", "Username can only be changed once every seven days.", "username")
		} else if errors.Is(err, persistence.ErrUsernameTaken) {
			writeError(w, 409, "username_taken", "That username is unavailable.", "username")
		} else {
			m.internal(w, "update username", err)
		}
		return
	}
	_ = m.provider.Logout(r.Context(), id.AccessToken, "local")
	m.setSession(w, r, s)
	m.notify(p.PublicUser)
	id.Profile = &p
	writeJSON(w, 200, accountResponse(id))
}
func (m *Module) password(w http.ResponseWriter, r *http.Request) {
	id, ok := m.requireProfile(w, r)
	if !ok {
		return
	}
	var in struct{ CurrentPassword, NewPassword, NewPasswordConfirmation string }
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.NewPassword == "" || in.NewPassword != in.NewPasswordConfirmation {
		writeError(w, 400, "password_mismatch", "Passwords do not match.", "newPasswordConfirmation")
		return
	}
	s, err := m.provider.PasswordLogin(r.Context(), id.User.Email, in.CurrentPassword)
	if err != nil {
		writeError(w, 401, "password_incorrect", "Current password is incorrect.", "currentPassword")
		return
	}
	if _, err = m.provider.UpdatePassword(r.Context(), s.AccessToken, in.NewPassword); err != nil {
		writeError(w, 400, "password_rejected", "The new password was not accepted.", "newPassword")
		return
	}
	_ = m.provider.Logout(r.Context(), s.AccessToken, "others")
	m.setSession(w, r, s)
	writeJSON(w, 200, map[string]string{"status": "password_updated"})
}
func (m *Module) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := m.require(w, r)
	if !ok {
		return
	}
	var in struct{ Password, PasswordConfirmation string }
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Password == "" || in.Password != in.PasswordConfirmation {
		writeError(w, 400, "password_mismatch", "Passwords do not match.", "passwordConfirmation")
		return
	}
	if id.SessionID == "" || m.store.ConsumeRecoveryGrant(r.Context(), id.SessionID, id.User.ID) != nil {
		writeError(w, 403, "recovery_expired", "This recovery link is expired or was already used.", "")
		return
	}
	if _, err := m.provider.UpdatePassword(r.Context(), id.AccessToken, in.Password); err != nil {
		writeError(w, 400, "password_rejected", "The new password was not accepted.", "password")
		return
	}
	_ = m.provider.Logout(r.Context(), id.AccessToken, "others")
	writeJSON(w, 200, map[string]string{"status": "password_updated"})
}

func (m *Module) Authenticate(w http.ResponseWriter, r *http.Request) (Identity, error) {
	tokens, err := m.transport.Read(r)
	if err != nil {
		return Identity{}, err
	}
	access := tokens.AccessToken
	if access == "" || tokenNearExpiry(access) {
		s, err := m.refresh(r.Context(), tokens.RefreshToken)
		if err != nil {
			return Identity{}, err
		}
		m.setSession(w, r, s)
		access = s.AccessToken
	}
	id, err := m.identity(r.Context(), access)
	if err == nil {
		return id, nil
	}
	if IsProviderUnavailable(err) {
		return Identity{}, err
	}
	s, refreshErr := m.refresh(r.Context(), tokens.RefreshToken)
	if refreshErr != nil {
		return Identity{}, refreshErr
	}
	m.setSession(w, r, s)
	return m.identity(r.Context(), s.AccessToken)
}

func (m *Module) refresh(ctx context.Context, token string) (Session, error) {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	m.refreshMu.Lock()
	if existing := m.refreshes[key]; existing != nil {
		m.refreshMu.Unlock()
		select {
		case <-existing.done:
			return existing.session, existing.err
		case <-ctx.Done():
			return Session{}, ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	m.refreshes[key] = call
	m.refreshMu.Unlock()
	call.session, call.err = m.provider.Refresh(ctx, token)
	m.refreshMu.Lock()
	delete(m.refreshes, key)
	close(call.done)
	m.refreshMu.Unlock()
	return call.session, call.err
}
func (m *Module) ValidateSession(ctx context.Context, accessToken, userID string) error {
	u, err := m.provider.CurrentUser(ctx, accessToken)
	if err != nil {
		return err
	}
	if u.ID != userID {
		return errors.New("session identity changed")
	}
	return nil
}
func (m *Module) identity(ctx context.Context, access string) (Identity, error) {
	u, err := m.provider.CurrentUser(ctx, access)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{User: u, AccessToken: access, SessionID: sessionID(access), ExpiresAt: tokenExpiry(access)}
	p, err := m.store.Profile(ctx, u.ID)
	if err == nil {
		id.Profile = &p
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Identity{}, err
	}
	return id, nil
}
func (m *Module) require(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, err := m.Authenticate(w, r)
	if err != nil {
		m.writeAuthError(w, err)
		return Identity{}, false
	}
	return id, true
}

func (m *Module) writeAuthError(w http.ResponseWriter, err error) {
	if IsProviderUnavailable(err) {
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "Authentication is temporarily unavailable.", "")
		return
	}
	writeError(w, http.StatusUnauthorized, "session_expired", "Please sign in again.", "")
}
func (m *Module) requireProfile(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := m.require(w, r)
	if !ok {
		return Identity{}, false
	}
	if id.Profile == nil {
		writeError(w, 409, "profile_required", "Finish your Relay profile first.", "")
		return Identity{}, false
	}
	return id, true
}
func (m *Module) notify(u model.PublicUser) {
	if m.profileChanged != nil {
		m.profileChanged(u)
	}
}
func (m *Module) callbackURL() string {
	return strings.TrimRight(m.cfg.PublicBaseURL, "/") + "/auth/callback"
}
func (m *Module) setSession(w http.ResponseWriter, r *http.Request, s Session) {
	m.transport.Write(w, r, s)
}
func (m *Module) clearSession(w http.ResponseWriter, r *http.Request) {
	m.transport.Clear(w, r)
}
func (m *Module) internal(w http.ResponseWriter, action string, err error) {
	m.log.Printf("ERROR %s: %v", action, err)
	writeError(w, 500, "internal_error", "Relay could not complete that request.", "")
}

func accountResponse(id Identity) map[string]any {
	return map[string]any{"email": id.User.Email, "profile": id.Profile, "profileRequired": id.Profile == nil}
}
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		writeError(w, 400, "invalid_request", "Request body is invalid.", "")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid_request", "Request body must contain one JSON object.", "")
		return false
	}
	return true
}
func writeError(w http.ResponseWriter, status int, code, message, field string) {
	noStore(w)
	payload := map[string]any{"code": code, "message": message}
	if field != "" {
		payload["field"] = field
	}
	writeJSON(w, status, map[string]any{"error": payload})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }
func normalizeEmail(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !strings.Contains(v, "@") || len(v) > 320 {
		return ""
	}
	return v
}
func emailHash(email string) string {
	sum := sha256.Sum256([]byte(normalizeEmail(email)))
	return hex.EncodeToString(sum[:])
}
func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func providerStatus(err error) string {
	var p *ProviderError
	if errors.As(err, &p) {
		return fmt.Sprintf("%d", p.Status)
	}
	return "unavailable"
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func tokenClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
func sessionID(token string) string { v, _ := tokenClaims(token)["session_id"].(string); return v }
func tokenExpiry(token string) time.Time {
	v, _ := tokenClaims(token)["exp"].(float64)
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(int64(v), 0)
}
func tokenNearExpiry(token string) bool {
	t := tokenExpiry(token)
	return !t.IsZero() && time.Until(t) < time.Minute
}

type ipWindow struct {
	start time.Time
	count int
}
type ipLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]ipWindow
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, entries: map[string]ipWindow{}}
}
func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	v := l.entries[ip]
	if v.start.IsZero() || now.Sub(v.start) >= l.window {
		v = ipWindow{start: now}
	}
	v.count++
	l.entries[ip] = v
	return v.count <= l.limit
}
