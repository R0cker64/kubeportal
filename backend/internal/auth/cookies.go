package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const (
	// SessionCookie carries the random session id. __Host- forces Secure,
	// Path=/ and no Domain (CLAUDE.md rule 5).
	SessionCookie = "__Host-kp_session"
	// loginCookie carries the encrypted state, nonce and PKCE verifier while
	// the user is at Keycloak. It is SameSite=Lax because the callback is a
	// top-level navigation coming from the identity provider's site.
	loginCookie = "__Host-kp_login"

	loginStateTTL = 10 * time.Minute
)

// loginState is what the callback needs to finish the flow it started.
type loginState struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Expires  time.Time `json:"e"`
}

var errBadLoginState = errors.New("auth: invalid login state")

// sealer encrypts the login state with AES-256-GCM. The cookie name is the
// additional data, so a value cannot be replayed under another cookie.
type sealer struct {
	aead cipher.AEAD
}

func newSealer(key []byte) (*sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(st loginState) (string, error) {
	plain, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	out := s.aead.Seal(nonce, nonce, plain, []byte(loginCookie))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (s *sealer) open(value string, now time.Time) (loginState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return loginState{}, errBadLoginState
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(loginCookie))
	if err != nil {
		return loginState{}, errBadLoginState
	}
	var st loginState
	if err := json.Unmarshal(plain, &st); err != nil {
		return loginState{}, errBadLoginState
	}
	if !now.Before(st.Expires) || st.State == "" || st.Nonce == "" || st.Verifier == "" {
		return loginState{}, errBadLoginState
	}
	return st, nil
}

// setCookie always sets HttpOnly and Secure; sameSite is Strict for the
// session and Lax only for the short-lived login state.
func setCookie(w http.ResponseWriter, name, value string, sameSite http.SameSite, maxAge int) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: attributes are set; SameSite is a parameter
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: sameSite,
	})
}

func clearCookie(w http.ResponseWriter, name string, sameSite http.SameSite) {
	setCookie(w, name, "", sameSite, -1)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error.
	return base64.RawURLEncoding.EncodeToString(b)
}
