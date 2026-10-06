package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AuthUser struct {
	ID         string            `json:"id"`
	Email      string            `json:"email"`
	Identities []json.RawMessage `json:"identities"`
}

// IsObfuscatedSignup reports the privacy-preserving response Supabase returns
// when sign-up is attempted for an existing confirmed email address. A real
// newly-created email user has at least one identity; the decoy has an explicit
// empty identities array. A missing field is not treated as a decoy so Relay
// remains compatible with older/provider-specific responses.
func (u AuthUser) IsObfuscatedSignup() bool {
	return u.Identities != nil && len(u.Identities) == 0
}

type Session struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int64    `json:"expires_in"`
	ExpiresAt    int64    `json:"expires_at"`
	User         AuthUser `json:"user"`
}
type ProviderError struct {
	Status        int
	Code, Message string
}

func IsProviderUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrMissingSession) {
		return false
	}
	var provider *ProviderError
	if errors.As(err, &provider) {
		return provider.Status == http.StatusTooManyRequests || provider.Status >= 500
	}
	return true
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("authentication provider returned status %d", e.Status)
}

type AuthProvider interface {
	SignUp(context.Context, string, string, string) (AuthUser, *Session, error)
	PasswordLogin(context.Context, string, string) (Session, error)
	Refresh(context.Context, string) (Session, error)
	CurrentUser(context.Context, string) (AuthUser, error)
	Verify(context.Context, string, string) (Session, error)
	RequestRecovery(context.Context, string, string) error
	ResendVerification(context.Context, string, string) error
	UpdatePassword(context.Context, string, string) (AuthUser, error)
	Logout(context.Context, string, string) error
}

type SupabaseAuthProvider struct {
	baseURL, key string
	client       *http.Client
}

func NewSupabaseAuthProvider(baseURL, key string) *SupabaseAuthProvider {
	return &SupabaseAuthProvider{baseURL: strings.TrimRight(baseURL, "/"), key: key, client: &http.Client{Timeout: 12 * time.Second}}
}

func (p *SupabaseAuthProvider) SignUp(ctx context.Context, email, password, redirect string) (AuthUser, *Session, error) {
	var out struct {
		ID           string            `json:"id"`
		Email        string            `json:"email"`
		Identities   []json.RawMessage `json:"identities"`
		User         *AuthUser         `json:"user"`
		AccessToken  string            `json:"access_token"`
		RefreshToken string            `json:"refresh_token"`
		ExpiresIn    int64             `json:"expires_in"`
		ExpiresAt    int64             `json:"expires_at"`
	}
	err := p.call(ctx, http.MethodPost, "/auth/v1/signup?redirect_to="+url.QueryEscape(redirect), "", map[string]string{"email": email, "password": password}, &out)
	if err != nil {
		return AuthUser{}, nil, err
	}
	user := AuthUser{ID: out.ID, Email: out.Email, Identities: out.Identities}
	if out.User != nil {
		user = *out.User
	}
	if out.AccessToken == "" {
		return user, nil, nil
	}
	return user, &Session{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresIn: out.ExpiresIn, ExpiresAt: out.ExpiresAt, User: user}, nil
}
func (p *SupabaseAuthProvider) PasswordLogin(ctx context.Context, email, password string) (Session, error) {
	var out Session
	err := p.call(ctx, http.MethodPost, "/auth/v1/token?grant_type=password", "", map[string]string{"email": email, "password": password}, &out)
	return out, err
}
func (p *SupabaseAuthProvider) Refresh(ctx context.Context, refresh string) (Session, error) {
	var out Session
	err := p.call(ctx, http.MethodPost, "/auth/v1/token?grant_type=refresh_token", "", map[string]string{"refresh_token": refresh}, &out)
	return out, err
}
func (p *SupabaseAuthProvider) CurrentUser(ctx context.Context, access string) (AuthUser, error) {
	var out AuthUser
	err := p.call(ctx, http.MethodGet, "/auth/v1/user", access, nil, &out)
	return out, err
}
func (p *SupabaseAuthProvider) Verify(ctx context.Context, tokenHash, kind string) (Session, error) {
	var out Session
	err := p.call(ctx, http.MethodPost, "/auth/v1/verify", "", map[string]string{"token_hash": tokenHash, "type": kind}, &out)
	return out, err
}
func (p *SupabaseAuthProvider) RequestRecovery(ctx context.Context, email, redirect string) error {
	return p.call(ctx, http.MethodPost, "/auth/v1/recover?redirect_to="+url.QueryEscape(redirect), "", map[string]string{"email": email}, nil)
}
func (p *SupabaseAuthProvider) ResendVerification(ctx context.Context, email, redirect string) error {
	return p.call(ctx, http.MethodPost, "/auth/v1/resend?redirect_to="+url.QueryEscape(redirect), "", map[string]string{"type": "signup", "email": email}, nil)
}
func (p *SupabaseAuthProvider) UpdatePassword(ctx context.Context, access, password string) (AuthUser, error) {
	var out AuthUser
	err := p.call(ctx, http.MethodPut, "/auth/v1/user", access, map[string]string{"password": password}, &out)
	return out, err
}
func (p *SupabaseAuthProvider) Logout(ctx context.Context, access, scope string) error {
	if scope == "" {
		scope = "local"
	}
	return p.call(ctx, http.MethodPost, "/auth/v1/logout?scope="+url.QueryEscape(scope), access, nil, nil)
}

func (p *SupabaseAuthProvider) call(ctx context.Context, method, path, bearer string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", p.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("authentication provider unavailable: %w", err)
	}
	defer res.Body.Close()
	limited := io.LimitReader(res.Body, 64*1024)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var v struct {
			Code             string `json:"code"`
			ErrorCode        string `json:"error_code"`
			Message          string `json:"msg"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(limited).Decode(&v)
		if v.Code == "" {
			v.Code = v.ErrorCode
		}
		if v.Message == "" {
			v.Message = v.ErrorDescription
		}
		return &ProviderError{Status: res.StatusCode, Code: v.Code, Message: v.Message}
	}
	if out != nil {
		if err := json.NewDecoder(limited).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("invalid authentication provider response: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, limited)
	}
	return nil
}
