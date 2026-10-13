// Package auth implements the BFF side of OIDC with Keycloak (ADR 0004,
// ADR 0009): Authorization Code + PKCE as a confidential client, ID token
// validation, and server-side sessions referenced by an HttpOnly cookie.
// Tokens never reach the browser and are not persisted.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/R0cker64/kubeportal/backend/internal/store"
)

// ErrNoSession means the request has no valid session.
var ErrNoSession = errors.New("auth: no valid session")

// ErrLoginFailed means the callback could not complete the login. The cause
// is logged; callers show a generic message.
var ErrLoginFailed = errors.New("auth: login failed")

// allowedAlgs is the signing algorithm allowlist (CLAUDE.md rule 6).
var allowedAlgs = []string{oidc.RS256, oidc.ES256}

const (
	// touchInterval limits last_seen_at writes to one per interval.
	touchInterval = time.Minute
	// clockSkew tolerated for nbf.
	clockSkew   = 30 * time.Second
	maxClaimLen = 255
	maxGroups   = 256
)

// SessionStore persists sessions.
type SessionStore interface {
	CreateSession(ctx context.Context, s store.Session) error
	GetSession(ctx context.Context, idHash []byte) (store.Session, error)
	TouchSession(ctx context.Context, idHash []byte, at time.Time) error
	DeleteSession(ctx context.Context, idHash []byte) error
}

// Config configures the Service.
type Config struct {
	IssuerURL    string // public issuer, validated against `iss`
	DiscoveryURL string // where metadata and keys are fetched from
	ClientID     string
	ClientSecret string
	PublicURL    *url.URL // portal URL; the callback is PublicURL + /auth/callback
	SessionKey   []byte   // 32 bytes, encrypts the login state cookie
	SessionTTL   time.Duration
	IdleTimeout  time.Duration
	// HTTPClient is used for every call to the identity provider. It must
	// have a timeout (CLAUDE.md rule 9).
	HTTPClient *http.Client
}

// Service runs the login flow and validates sessions.
type Service struct {
	cfg    Config
	store  SessionStore
	log    *slog.Logger
	sealer *sealer
	now    func() time.Time

	mu       sync.Mutex
	provider *provider
}

type provider struct {
	oauth      oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string
}

// NewService validates cfg. The identity provider is contacted lazily on the
// first login so the portal can start before Keycloak is ready.
func NewService(cfg Config, s SessionStore, log *slog.Logger) (*Service, error) {
	if cfg.HTTPClient == nil || cfg.HTTPClient.Timeout <= 0 {
		return nil, errors.New("auth: HTTPClient with a timeout is required")
	}
	sl, err := newSealer(cfg.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("auth: session key: %w", err)
	}
	return &Service{cfg: cfg, store: s, log: log, sealer: sl, now: time.Now}, nil
}

// getProvider performs OIDC discovery once it succeeds; failures are retried
// on the next call.
func (s *Service) getProvider() (*provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return s.provider, nil
	}

	// The key set keeps this context for background JWKS refreshes, so it is
	// not cancelled; every request is bounded by HTTPClient.Timeout.
	ctx := oidc.ClientContext(context.Background(), s.cfg.HTTPClient)
	if s.cfg.DiscoveryURL != s.cfg.IssuerURL {
		// Metadata comes from the internal URL but must still announce the
		// public issuer (docs/architecture.md).
		ctx = oidc.InsecureIssuerURLContext(ctx, s.cfg.IssuerURL)
	}
	p, err := oidc.NewProvider(ctx, s.cfg.DiscoveryURL)
	if err != nil {
		return nil, fmt.Errorf("auth: discovery: %w", err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, fmt.Errorf("auth: discovery claims: %w", err)
	}

	s.provider = &provider{
		oauth: oauth2.Config{
			ClientID:     s.cfg.ClientID,
			ClientSecret: s.cfg.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  s.cfg.PublicURL.JoinPath("/auth/callback").String(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "groups"},
		},
		verifier: p.VerifierContext(ctx, &oidc.Config{
			ClientID:             s.cfg.ClientID,
			SupportedSigningAlgs: allowedAlgs,
		}),
		endSession: meta.EndSession,
	}
	return s.provider, nil
}

// BeginLogin stores a fresh login state in an encrypted cookie and returns
// the identity provider URL to redirect to.
func (s *Service) BeginLogin(w http.ResponseWriter) (string, error) {
	p, err := s.getProvider()
	if err != nil {
		return "", err
	}
	st := loginState{
		State:    randomToken(),
		Nonce:    randomToken(),
		Verifier: oauth2.GenerateVerifier(),
		Expires:  s.now().Add(loginStateTTL),
	}
	value, err := s.sealer.seal(st)
	if err != nil {
		return "", err
	}
	setCookie(w, loginCookie, value, http.SameSiteLaxMode, int(loginStateTTL/time.Second))
	return p.oauth.AuthCodeURL(st.State,
		oauth2.S256ChallengeOption(st.Verifier),
		oidc.Nonce(st.Nonce),
	), nil
}

// idClaims are the ID token claims the portal uses.
type idClaims struct {
	Username  string   `json:"preferred_username"`
	Groups    []string `json:"groups"`
	NotBefore *int64   `json:"nbf"`
	AZP       string   `json:"azp"`
}

// CompleteLogin validates the callback, creates a session and sets the
// session cookie. Any failure returns ErrLoginFailed; the cause is logged.
func (s *Service) CompleteLogin(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	// The login state is single use whatever the outcome.
	clearCookie(w, loginCookie, http.SameSiteLaxMode)

	sess, err := s.completeLogin(ctx, r)
	if err != nil {
		s.log.WarnContext(ctx, "login failed", "error", err)
		return ErrLoginFailed
	}

	// Rotate: a session id present before login is discarded (fixation).
	if old, err := r.Cookie(SessionCookie); err == nil {
		_ = s.store.DeleteSession(ctx, hashID(old.Value))
	}

	id := randomToken()
	sess.IDHash = hashID(id)
	if err := s.store.CreateSession(ctx, sess); err != nil {
		s.log.ErrorContext(ctx, "create session", "error", err)
		return ErrLoginFailed
	}
	setCookie(w, SessionCookie, id, http.SameSiteStrictMode, 0)
	s.log.InfoContext(ctx, "login", "user", sess.Username, "groups", len(sess.Groups))
	return nil
}

func (s *Service) completeLogin(ctx context.Context, r *http.Request) (store.Session, error) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return store.Session{}, fmt.Errorf("identity provider returned an error (%.64q)", e)
	}
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return store.Session{}, errors.New("missing login state cookie")
	}
	st, err := s.sealer.open(c.Value, s.now())
	if err != nil {
		return store.Session{}, err
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		return store.Session{}, errors.New("state mismatch")
	}
	code := q.Get("code")
	if code == "" || len(code) > 2048 {
		return store.Session{}, errors.New("missing or oversized code")
	}

	p, err := s.getProvider()
	if err != nil {
		return store.Session{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.cfg.HTTPClient)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return store.Session{}, fmt.Errorf("code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return store.Session{}, errors.New("token response without id_token")
	}

	// Verify checks the signature against the JWKS, the algorithm allowlist,
	// iss, aud and exp.
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return store.Session{}, fmt.Errorf("verify id_token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		return store.Session{}, errors.New("nonce mismatch")
	}
	var claims idClaims
	if err := idt.Claims(&claims); err != nil {
		return store.Session{}, fmt.Errorf("id_token claims: %w", err)
	}
	now := s.now()
	if claims.NotBefore != nil && time.Unix(*claims.NotBefore, 0).After(now.Add(clockSkew)) {
		return store.Session{}, errors.New("id_token not valid yet (nbf)")
	}
	if len(idt.Audience) > 1 && claims.AZP != s.cfg.ClientID {
		return store.Session{}, errors.New("id_token azp does not match the client")
	}
	if !validClaim(idt.Subject) {
		return store.Session{}, errors.New("invalid sub claim")
	}
	username := claims.Username
	if !validClaim(username) {
		username = idt.Subject
	}

	return store.Session{
		Subject:    idt.Subject,
		Username:   username,
		Groups:     cleanGroups(claims.Groups),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.cfg.SessionTTL),
	}, nil
}

// Session returns the valid session referenced by the request cookie, or
// ErrNoSession. Expired or idle sessions are deleted.
func (s *Service) Session(ctx context.Context, r *http.Request) (store.Session, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return store.Session{}, ErrNoSession
	}
	idHash := hashID(c.Value)
	sess, err := s.store.GetSession(ctx, idHash)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, ErrNoSession
	}
	if err != nil {
		return store.Session{}, err
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > s.cfg.IdleTimeout {
		_ = s.store.DeleteSession(ctx, idHash)
		return store.Session{}, ErrNoSession
	}
	if now.Sub(sess.LastSeenAt) > touchInterval {
		if err := s.store.TouchSession(ctx, idHash, now); err != nil {
			s.log.WarnContext(ctx, "touch session", "error", err)
		}
	}
	return sess, nil
}

// Logout deletes the session, clears the cookie and returns the identity
// provider logout URL (empty if the provider has none).
func (s *Service) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		if err := s.store.DeleteSession(ctx, hashID(c.Value)); err != nil {
			return "", err
		}
	}
	clearCookie(w, SessionCookie, http.SameSiteStrictMode)

	p, err := s.getProvider()
	if err != nil || p.endSession == "" {
		return "", nil //nolint:nilerr // local logout already succeeded
	}
	u, err := url.Parse(p.endSession)
	if err != nil {
		return "", nil //nolint:nilerr // local logout already succeeded
	}
	q := u.Query()
	q.Set("client_id", s.cfg.ClientID)
	q.Set("post_logout_redirect_uri", s.cfg.PublicURL.JoinPath("/").String())
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func hashID(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:]
}

func validClaim(v string) bool {
	return v != "" && len(v) <= maxClaimLen && utf8.ValidString(v)
}

func cleanGroups(groups []string) []string {
	out := make([]string, 0, min(len(groups), maxGroups))
	for _, g := range groups {
		if len(out) == maxGroups {
			break
		}
		if validClaim(g) {
			out = append(out, g)
		}
	}
	return out
}
