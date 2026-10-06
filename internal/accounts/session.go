package accounts

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

type SessionTokens struct {
	AccessToken  string
	RefreshToken string
}

var ErrMissingSession = errors.New("missing session")

// SessionTransport isolates how credentials arrive from the authenticated
// Relay principal. The embedded browser uses cookies; future native clients
// can add a transport without changing domain authorization code.
type SessionTransport interface {
	Read(*http.Request) (SessionTokens, error)
	Write(http.ResponseWriter, *http.Request, Session)
	Clear(http.ResponseWriter, *http.Request)
}

type CookieSessionTransport struct {
	secureMode    string
	publicBaseURL string
}

func NewCookieSessionTransport(secureMode, publicBaseURL string) *CookieSessionTransport {
	return &CookieSessionTransport{secureMode: secureMode, publicBaseURL: publicBaseURL}
}

func (t *CookieSessionTransport) Read(r *http.Request) (SessionTokens, error) {
	access, accessErr := r.Cookie(accessCookie)
	refresh, refreshErr := r.Cookie(refreshCookie)
	if refreshErr != nil || refresh.Value == "" {
		return SessionTokens{}, ErrMissingSession
	}
	var accessValue string
	if accessErr == nil {
		accessValue = access.Value
	}
	return SessionTokens{AccessToken: accessValue, RefreshToken: refresh.Value}, nil
}

func (t *CookieSessionTransport) Write(w http.ResponseWriter, r *http.Request, s Session) {
	secure := t.secure(r)
	expires := time.Unix(s.ExpiresAt, 0)
	if s.ExpiresAt == 0 && s.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(s.ExpiresIn) * time.Second)
	}
	http.SetCookie(w, &http.Cookie{Name: accessCookie, Value: s.AccessToken, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, Expires: expires})
	const refreshLifetime = 400 * 24 * time.Hour
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: s.RefreshToken, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(refreshLifetime.Seconds()), Expires: time.Now().Add(refreshLifetime)})
}

func (t *CookieSessionTransport) Clear(w http.ResponseWriter, r *http.Request) {
	secure := t.secure(r)
	for _, name := range []string{accessCookie, refreshCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

func (t *CookieSessionTransport) secure(r *http.Request) bool {
	return t.secureMode == "always" || (t.secureMode == "auto" && (r.TLS != nil || strings.HasPrefix(strings.ToLower(t.publicBaseURL), "https://")))
}
