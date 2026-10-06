package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"relay/internal/persistence"
)

type fakeProvider struct {
	user         AuthUser
	session      Session
	signupErr    error
	logoutScopes []string
	updated      string
	refreshes    int
	refreshErr   error
	currentErr   error
	refreshDelay time.Duration
	mu           sync.Mutex
}

func (f *fakeProvider) SignUp(context.Context, string, string, string) (AuthUser, *Session, error) {
	return f.user, nil, f.signupErr
}
func (f *fakeProvider) PasswordLogin(context.Context, string, string) (Session, error) {
	return f.session, nil
}
func (f *fakeProvider) Refresh(context.Context, string) (Session, error) {
	f.mu.Lock()
	f.refreshes++
	session, err, delay := f.session, f.refreshErr, f.refreshDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return session, err
}
func (f *fakeProvider) CurrentUser(context.Context, string) (AuthUser, error) {
	if f.currentErr != nil {
		return AuthUser{}, f.currentErr
	}
	if f.user.ID == "" {
		return AuthUser{}, errors.New("invalid")
	}
	return f.user, nil
}

func TestRefreshCookieRestoresSessionWithoutAccessCookie(t *testing.T) {
	m, store, provider := testModule(t)
	if _, err := store.CreateProfile(context.Background(), "auth-1", "person", "Person"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://relay.test/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "refresh-secret"})
	w := httptest.NewRecorder()
	id, err := m.Authenticate(w, req)
	if err != nil || id.User.ID != "auth-1" || provider.refreshes != 1 {
		t.Fatalf("id=%+v refreshes=%d err=%v", id, provider.refreshes, err)
	}
	if len(w.Result().Cookies()) != 2 {
		t.Fatalf("rotated cookies=%+v", w.Result().Cookies())
	}
}

func TestRefreshCookiePersistsAndClearUsesConfiguredSecureMode(t *testing.T) {
	transport := NewCookieSessionTransport("auto", "https://relay.example")
	req := httptest.NewRequest("GET", "http://relay.internal/", nil)
	w := httptest.NewRecorder()
	transport.Write(w, req, Session{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	var refresh *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == refreshCookie {
			refresh = cookie
		}
	}
	if refresh == nil || !refresh.HttpOnly || !refresh.Secure || refresh.MaxAge < 399*24*60*60 {
		t.Fatalf("refresh cookie=%+v", refresh)
	}
	clear := httptest.NewRecorder()
	transport.Clear(clear, req)
	for _, cookie := range clear.Result().Cookies() {
		if !cookie.Secure || cookie.MaxAge != -1 {
			t.Fatalf("clear cookie=%+v", cookie)
		}
	}
}

func TestAuthProviderOutageIsServiceUnavailable(t *testing.T) {
	m, _, provider := testModule(t)
	provider.currentErr = errors.New("network unavailable")
	req := httptest.NewRequest("GET", "http://relay.test/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: accessCookie, Value: testJWT("session-1", time.Now().Add(time.Hour))})
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "refresh-secret"})
	w := httptest.NewRecorder()
	m.me(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "auth_unavailable") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestConcurrentRefreshesAreCoalesced(t *testing.T) {
	m, store, provider := testModule(t)
	if _, err := store.CreateProfile(context.Background(), "auth-1", "person", "Person"); err != nil {
		t.Fatal(err)
	}
	provider.refreshDelay = 50 * time.Millisecond
	const clients = 8
	start := make(chan struct{})
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		go func() {
			<-start
			req := httptest.NewRequest("GET", "http://relay.test/api/v1/me", nil)
			req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "same-refresh-token"})
			_, err := m.Authenticate(httptest.NewRecorder(), req)
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < clients; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	provider.mu.Lock()
	refreshes := provider.refreshes
	provider.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refresh calls=%d, want 1", refreshes)
	}
}

func TestRevokedRefreshTokenReturnsSessionExpired(t *testing.T) {
	m, _, provider := testModule(t)
	provider.refreshErr = &ProviderError{Status: http.StatusBadRequest, Code: "refresh_token_not_found"}
	req := httptest.NewRequest("GET", "http://relay.test/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "revoked-refresh-token"})
	w := httptest.NewRecorder()
	m.me(w, req)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "session_expired") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLogoutWithRefreshOnlyRevokesAndClearsSession(t *testing.T) {
	m, _, provider := testModule(t)
	req := httptest.NewRequest("POST", "http://relay.test/api/auth/logout", nil)
	req.Header.Set("Origin", "http://relay.test")
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: "refresh-secret"})
	w := httptest.NewRecorder()
	m.logout(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(provider.logoutScopes) != 1 || provider.logoutScopes[0] != "local" {
		t.Fatalf("logout scopes=%v", provider.logoutScopes)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge != -1 {
			t.Fatalf("cookie not cleared: %+v", cookie)
		}
	}
}
func (f *fakeProvider) Verify(context.Context, string, string) (Session, error) {
	return f.session, nil
}
func (*fakeProvider) RequestRecovery(context.Context, string, string) error    { return nil }
func (*fakeProvider) ResendVerification(context.Context, string, string) error { return nil }
func (f *fakeProvider) UpdatePassword(_ context.Context, _ string, password string) (AuthUser, error) {
	f.updated = password
	return f.user, nil
}
func (f *fakeProvider) Logout(_ context.Context, _ string, scope string) error {
	f.logoutScopes = append(f.logoutScopes, scope)
	return nil
}

func testModule(t *testing.T) (*Module, *persistence.Module, *fakeProvider) {
	t.Helper()
	store := persistence.New(t.TempDir())
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Stop(context.Background()) })
	token := testJWT("session-1", time.Now().Add(time.Hour))
	provider := &fakeProvider{user: AuthUser{ID: "auth-1", Email: "person@example.test"}, session: Session{AccessToken: token, RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: AuthUser{ID: "auth-1", Email: "person@example.test"}}}
	m := New(provider, store, Config{RegistrationEnabled: true, PublicBaseURL: "http://relay.test", CookieSecureMode: "never", UsernameMin: 3, UsernameMax: 32, DisplayNameMin: 1, DisplayNameMax: 32, UsernameCooldown: 7 * 24 * time.Hour, ReservationLifetime: 24 * time.Hour, RecoveryGrantLifetime: 15 * time.Minute, RateLimitAttempts: 100, RateLimitWindow: time.Minute}, log.New(io.Discard, "", 0))
	return m, store, provider
}

func TestLoginSetsHttpOnlyCookiesAndReturnsSQLiteProfile(t *testing.T) {
	m, store, _ := testModule(t)
	if _, err := store.CreateProfile(context.Background(), "auth-1", "person", "Person"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	req := httptest.NewRequest("POST", "http://relay.test/api/auth/login", strings.NewReader(`{"email":"person@example.test","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://relay.test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookies=%+v", cookies)
	}
	if strings.Contains(w.Body.String(), "refresh-secret") || strings.Contains(w.Body.String(), "access_token") {
		t.Fatal("token leaked in response")
	}
	if !strings.Contains(w.Body.String(), `"username":"person"`) {
		t.Fatalf("profile missing: %s", w.Body.String())
	}
}

func TestRegistrationProviderFailureIsGenericAndReleasesUsername(t *testing.T) {
	m, _, provider := testModule(t)
	provider.signupErr = &ProviderError{Status: 400, Code: "user_exists"}
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://relay.test/api/auth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://relay.test")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	first := request(`{"email":"person@example.test","username":"Re\u200bserved","displayName":"Person","password":"secret123","passwordConfirmation":"secret123"}`)
	if first.Code != 202 || !strings.Contains(first.Body.String(), "check_email") {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	second := request(`{"email":"other@example.test","username":"reserved","displayName":"Other","password":"secret123","passwordConfirmation":"secret123"}`)
	if second.Code != 202 || !strings.Contains(second.Body.String(), "check_email") {
		t.Fatalf("second=%d %s", second.Code, second.Body.String())
	}
	if strings.Contains(first.Body.String(), "user_exists") {
		t.Fatal("provider account state leaked")
	}
}

func TestRegistrationObfuscatedDuplicateEmailReleasesUsername(t *testing.T) {
	m, _, provider := testModule(t)
	provider.user = AuthUser{ID: "decoy-id", Email: "person@example.test", Identities: []json.RawMessage{}}
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	request := func(email, username string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"email":%q,"username":%q,"displayName":"Person","password":"secret123","passwordConfirmation":"secret123"}`, email, username)
		req := httptest.NewRequest("POST", "http://relay.test/api/auth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://relay.test")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if first := request("person@example.test", "available"); first.Code != http.StatusAccepted {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	if second := request("other@example.test", "available"); second.Code != http.StatusAccepted {
		t.Fatalf("username remained locked: %d %s", second.Code, second.Body.String())
	}
}

func TestRegistrationRealSignupKeepsUsernameReserved(t *testing.T) {
	m, _, provider := testModule(t)
	provider.user = AuthUser{ID: "new-auth-id", Email: "new@example.test", Identities: []json.RawMessage{json.RawMessage(`{"id":"identity-1"}`)}}
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	request := func(email string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"email":%q,"username":"reserved","displayName":"Person","password":"secret123","passwordConfirmation":"secret123"}`, email)
		req := httptest.NewRequest("POST", "http://relay.test/api/auth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://relay.test")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if first := request("new@example.test"); first.Code != http.StatusAccepted {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	if second := request("other@example.test"); second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "username_taken") {
		t.Fatalf("second=%d %s", second.Code, second.Body.String())
	}
}

func TestVerificationCallbackProvisionsProfileAndCleansURL(t *testing.T) {
	m, store, _ := testModule(t)
	if err := store.ReserveRegistration(context.Background(), persistence.PendingRegistration{Nonce: "nonce", EmailHash: emailHash("person@example.test"), AuthUserID: "auth-1", Username: "person", DisplayName: "Person", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	req := httptest.NewRequest("GET", "http://relay.test/auth/callback?token_hash=sensitive&type=email", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?auth=verified" {
		t.Fatalf("status=%d location=%s", w.Code, w.Header().Get("Location"))
	}
	profile, err := store.Profile(context.Background(), "auth-1")
	if err != nil || profile.Username != "person" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if strings.Contains(w.Header().Get("Location"), "sensitive") {
		t.Fatal("token hash remained in redirect")
	}
}

func TestAuthMutationRejectsCrossOriginAndOversizedJSON(t *testing.T) {
	m, _, _ := testModule(t)
	mux := http.NewServeMux()
	m.RegisterHTTP(mux)
	req := httptest.NewRequest("POST", "http://relay.test/api/auth/login", strings.NewReader(`{"email":"person@example.test","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", w.Code)
	}
	large := `{"email":"` + strings.Repeat("a", 17000) + `","password":"x"}`
	req = httptest.NewRequest("POST", "http://relay.test/api/auth/login", strings.NewReader(large))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://relay.test")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized status=%d body=%s", w.Code, w.Body.String())
	}
}

func testJWT(session string, expiry time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	raw, _ := json.Marshal(map[string]any{"session_id": session, "exp": expiry.Unix()})
	return header + "." + base64.RawURLEncoding.EncodeToString(raw) + "."
}
