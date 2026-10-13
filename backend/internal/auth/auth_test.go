package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/R0cker64/kubeportal/backend/internal/store"
)

// fakeIdP is a minimal OIDC provider: discovery, JWKS and a token endpoint
// that checks the PKCE verifier and returns whatever ID token the test sets.
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu        sync.Mutex
	challenge string
	claims    map[string]any
	header    map[string]any // overrides the JWT header
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/auth",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/keys",
			"end_session_endpoint":                  f.srv.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.PostForm.Get("code") != "good-code" || b64(sum[:]) != f.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 300,
			"id_token": f.sign(t),
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *fakeIdP) sign(t *testing.T) string {
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"}
	for k, v := range f.header {
		header[k] = v
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(f.claims)
	signing := b64(h) + "." + b64(c)
	if header["alg"] == "none" {
		return signing + "."
	}
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

type memStore struct {
	mu       sync.Mutex
	sessions map[string]store.Session
}

func newMemStore() *memStore { return &memStore{sessions: map[string]store.Session{}} }

func (m *memStore) CreateSession(_ context.Context, s store.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(s.IDHash)] = s
	return nil
}

func (m *memStore) GetSession(_ context.Context, h []byte) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return s, nil
}

func (m *memStore) TouchSession(_ context.Context, h []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[string(h)]
	s.LastSeenAt = at
	m.sessions[string(h)] = s
	return nil
}

func (m *memStore) DeleteSession(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(h))
	return nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

type harness struct {
	idp   *fakeIdP
	store *memStore
	svc   *Service
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{idp: newFakeIdP(t), store: newMemStore(), now: time.Now()}
	public, _ := url.Parse("http://localhost:8080")
	svc, err := NewService(Config{
		IssuerURL:    h.idp.srv.URL,
		DiscoveryURL: h.idp.srv.URL,
		ClientID:     "kubeportal",
		ClientSecret: "secret",
		PublicURL:    public,
		SessionKey:   make([]byte, 32),
		SessionTTL:   8 * time.Hour,
		IdleTimeout:  time.Hour,
		HTTPClient:   &http.Client{Timeout: 5 * time.Second},
	}, h.store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return h.now }
	h.svc = svc
	return h
}

// begin starts a login and returns the login cookie and the authorize URL
// parameters.
func (h *harness) begin(t *testing.T) (*http.Cookie, url.Values) {
	t.Helper()
	rec := httptest.NewRecorder()
	target, err := h.svc.BeginLogin(rec)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	q := u.Query()
	h.idp.mu.Lock()
	h.idp.challenge = q.Get("code_challenge")
	h.idp.claims = map[string]any{
		"iss": h.idp.srv.URL, "aud": "kubeportal", "sub": "user-1",
		"exp": h.now.Add(5 * time.Minute).Unix(), "iat": h.now.Unix(),
		"nonce": q.Get("nonce"), "preferred_username": "alice",
		"groups": []string{"team-payments"},
	}
	h.idp.mu.Unlock()
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one login cookie, got %d", len(cookies))
	}
	return cookies[0], q
}

func (h *harness) callback(t *testing.T, login *http.Cookie, query string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/callback?"+query, nil)
	if login != nil {
		req.AddCookie(login)
	}
	rec := httptest.NewRecorder()
	return rec, h.svc.CompleteLogin(t.Context(), rec, req)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestLoginFlow(t *testing.T) {
	h := newHarness(t)
	login, q := h.begin(t)

	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorize URL lacks PKCE: %v", q)
	}
	if !strings.HasPrefix(login.Name, "__Host-") || !login.HttpOnly || !login.Secure ||
		login.SameSite != http.SameSiteLaxMode || login.Path != "/" {
		t.Fatalf("bad login cookie: %+v", login)
	}
	if strings.Contains(login.Value, q.Get("state")) {
		t.Fatal("login cookie must be encrypted")
	}

	rec, err := h.callback(t, login, "code=good-code&state="+url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	c := sessionCookie(t, rec)
	if c.Name != "__Host-kp_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("bad session cookie: %+v", c)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/me", nil)
	req.AddCookie(c)
	sess, err := h.svc.Session(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Username != "alice" || sess.Subject != "user-1" || len(sess.Groups) != 1 {
		t.Fatalf("unexpected session %+v", sess)
	}
	// Only the hash of the id is stored.
	for k := range h.store.sessions {
		if strings.Contains(k, c.Value) {
			t.Fatal("raw session id stored")
		}
	}
}

func TestLoginRejections(t *testing.T) {
	cases := map[string]func(h *harness, q url.Values) (cookie bool, query string){
		"state mismatch": func(_ *harness, _ url.Values) (bool, string) {
			return true, "code=good-code&state=forged"
		},
		"missing login cookie": func(_ *harness, q url.Values) (bool, string) {
			return false, "code=good-code&state=" + q.Get("state")
		},
		"provider error": func(_ *harness, q url.Values) (bool, string) {
			return true, "error=access_denied&state=" + q.Get("state")
		},
		"bad code": func(_ *harness, q url.Values) (bool, string) {
			return true, "code=other&state=" + q.Get("state")
		},
		"nonce mismatch": func(h *harness, q url.Values) (bool, string) {
			h.idp.claims["nonce"] = "other"
			return true, "code=good-code&state=" + q.Get("state")
		},
		"wrong audience": func(h *harness, q url.Values) (bool, string) {
			h.idp.claims["aud"] = "another-client"
			return true, "code=good-code&state=" + q.Get("state")
		},
		"wrong issuer": func(h *harness, q url.Values) (bool, string) {
			h.idp.claims["iss"] = "https://evil.example.com"
			return true, "code=good-code&state=" + q.Get("state")
		},
		"expired": func(h *harness, q url.Values) (bool, string) {
			h.idp.claims["exp"] = h.now.Add(-time.Minute).Unix()
			return true, "code=good-code&state=" + q.Get("state")
		},
		"not yet valid": func(h *harness, q url.Values) (bool, string) {
			h.idp.claims["nbf"] = h.now.Add(10 * time.Minute).Unix()
			return true, "code=good-code&state=" + q.Get("state")
		},
		"alg none": func(h *harness, q url.Values) (bool, string) {
			h.idp.header = map[string]any{"alg": "none"}
			return true, "code=good-code&state=" + q.Get("state")
		},
		"alg HS256": func(h *harness, q url.Values) (bool, string) {
			h.idp.header = map[string]any{"alg": "HS256"}
			return true, "code=good-code&state=" + q.Get("state")
		},
		"multiple audiences without azp": func(h *harness, q url.Values) (bool, string) {
			h.idp.claims["aud"] = []string{"kubeportal", "other"}
			return true, "code=good-code&state=" + q.Get("state")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			login, q := h.begin(t)
			h.idp.mu.Lock()
			withCookie, query := mutate(h, q)
			h.idp.mu.Unlock()
			if !withCookie {
				login = nil
			}
			rec, err := h.callback(t, login, query)
			if !errors.Is(err, ErrLoginFailed) {
				t.Fatalf("expected ErrLoginFailed, got %v", err)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == SessionCookie && c.Value != "" {
					t.Fatal("session cookie set on failure")
				}
			}
			if h.store.count() != 0 {
				t.Fatal("session created on failure")
			}
		})
	}
}

func TestLoginStateIsSingleUseAndExpires(t *testing.T) {
	h := newHarness(t)
	login, q := h.begin(t)
	query := "code=good-code&state=" + q.Get("state")

	rec, err := h.callback(t, login, query)
	if err != nil {
		t.Fatal(err)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == loginCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("login cookie not cleared after callback")
	}

	h2 := newHarness(t)
	login2, q2 := h2.begin(t)
	h2.now = h2.now.Add(loginStateTTL + time.Second)
	if _, err := h2.callback(t, login2, "code=good-code&state="+q2.Get("state")); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("expired login state accepted: %v", err)
	}
}

func TestTamperedLoginCookie(t *testing.T) {
	h := newHarness(t)
	login, q := h.begin(t)
	raw, _ := base64.RawURLEncoding.DecodeString(login.Value)
	raw[len(raw)-1] ^= 1
	login.Value = base64.RawURLEncoding.EncodeToString(raw)
	if _, err := h.callback(t, login, "code=good-code&state="+q.Get("state")); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("tampered cookie accepted: %v", err)
	}
}

func login(t *testing.T, h *harness) *http.Cookie {
	t.Helper()
	c, q := h.begin(t)
	rec, err := h.callback(t, c, "code=good-code&state="+q.Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	return sessionCookie(t, rec)
}

func TestSessionExpiry(t *testing.T) {
	h := newHarness(t)
	c := login(t, h)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.AddCookie(c)

	h.now = h.now.Add(59 * time.Minute)
	if _, err := h.svc.Session(t.Context(), req); err != nil {
		t.Fatalf("active session rejected: %v", err)
	}
	// Activity at 59m moved the idle window; 61m of inactivity ends it.
	h.now = h.now.Add(61 * time.Minute)
	if _, err := h.svc.Session(t.Context(), req); !errors.Is(err, ErrNoSession) {
		t.Fatalf("idle session accepted: %v", err)
	}
	if h.store.count() != 0 {
		t.Fatal("idle session not deleted")
	}

	h2 := newHarness(t)
	c2 := login(t, h2)
	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req2.AddCookie(c2)
	for range 9 {
		h2.now = h2.now.Add(55 * time.Minute)
		_, _ = h2.svc.Session(t.Context(), req2)
	}
	if _, err := h2.svc.Session(t.Context(), req2); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session past its absolute TTL accepted: %v", err)
	}
}

func TestSessionRejectsUnknownCookie(t *testing.T) {
	h := newHarness(t)
	for _, v := range []string{"", "unknown", strings.Repeat("a", 500)} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: v, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		if _, err := h.svc.Session(t.Context(), req); !errors.Is(err, ErrNoSession) {
			t.Errorf("cookie %.20q: %v", v, err)
		}
	}
}

func TestLogout(t *testing.T) {
	h := newHarness(t)
	c := login(t, h)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/logout", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	target, err := h.svc.Logout(t.Context(), rec, req)
	if err != nil {
		t.Fatal(err)
	}
	if h.store.count() != 0 {
		t.Fatal("session not deleted on logout")
	}
	u, _ := url.Parse(target)
	if !strings.HasPrefix(target, h.idp.srv.URL+"/logout") ||
		u.Query().Get("post_logout_redirect_uri") != "http://localhost:8080/" {
		t.Fatalf("unexpected logout URL %s", target)
	}
	if strings.Contains(target, "id_token") {
		t.Fatal("logout URL must not carry tokens")
	}
}

func TestOldSessionDiscardedOnLogin(t *testing.T) {
	h := newHarness(t)
	old := login(t, h)
	c, q := h.begin(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/auth/callback?code=good-code&state="+q.Get("state"), nil)
	req.AddCookie(c)
	req.AddCookie(old)
	if err := h.svc.CompleteLogin(t.Context(), httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if h.store.count() != 1 {
		t.Fatalf("expected only the new session, got %d", h.store.count())
	}
}

func TestNewServiceRequiresTimeout(t *testing.T) {
	_, err := NewService(Config{SessionKey: make([]byte, 32), HTTPClient: &http.Client{}}, newMemStore(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected error for an HTTP client without timeout")
	}
}

func TestLoginFailureLogIsSingleLine(t *testing.T) {
	var buf strings.Builder
	h := newHarness(t)
	h.svc.log = slog.New(slog.NewTextHandler(&buf, nil))
	login, q := h.begin(t)
	query := "error=" + url.QueryEscape("x\nlevel=ERROR msg=forged") + "&state=" + q.Get("state")
	if _, err := h.callback(t, login, query); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("expected ErrLoginFailed, got %v", err)
	}
	if strings.Count(buf.String(), "\n") != 1 || strings.Contains(buf.String(), "forged") {
		t.Fatalf("log entry was forged: %q", buf.String())
	}
}
