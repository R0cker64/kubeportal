package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"

	"github.com/R0cker64/kubeportal/backend/internal/auth"
	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

// csrfHeader must accompany every state-changing request. Browsers cannot
// add custom headers cross-site without a CORS preflight, which this server
// never approves (CLAUDE.md rule 7).
const (
	csrfHeader = "X-Requested-With"
	csrfValue  = "kubeportal"
)

// withCSRF rejects unsafe methods without the custom header or with a
// foreign Origin.
func withCSRF(public *url.URL, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get(csrfHeader) != csrfValue || !sameOrigin(r, public) {
				writeError(w, r, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin accepts a missing Origin (some same-origin requests omit it)
// but rejects any Origin other than the portal's.
func sameOrigin(r *http.Request, public *url.URL) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || origin == publicOrigin(public)
}

func publicOrigin(public *url.URL) string {
	return public.Scheme + "://" + public.Host
}

func principalFrom(ctx context.Context) (authz.Principal, bool) {
	p, ok := ctx.Value(principalKey).(authz.Principal)
	return p, ok
}

// authenticate resolves the session and principal of a request.
func (a *api) authenticate(r *http.Request) (authz.Principal, error) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	sess, err := a.deps.Auth.Session(ctx, r)
	if err != nil {
		return authz.Principal{}, err
	}
	return a.deps.Resolver.Resolve(ctx, sess.Subject, sess.Username, sess.Groups)
}

// requireSession runs next with the principal in the context, or answers
// 401. Infrastructure failures are 503 and logged.
func (a *api) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticate(r)
		if errors.Is(err, auth.ErrNoSession) {
			writeError(w, r, http.StatusUnauthorized)
			return
		}
		if err != nil {
			a.log.ErrorContext(r.Context(), "authenticate", "request_id", RequestID(r.Context()), "error", err)
			writeError(w, r, http.StatusServiceUnavailable)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	}
}

// connLimiter bounds concurrent long-lived connections per user (T8).
type connLimiter struct {
	max int
	mu  sync.Mutex
	n   map[string]int
}

func newConnLimiter(max int) *connLimiter {
	return &connLimiter{max: max, n: map[string]int{}}
}

// acquire reserves a slot for key; release must be called when done.
func (l *connLimiter) acquire(key string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[key] >= l.max {
		return nil, false
	}
	l.n[key]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.n[key]--; l.n[key] <= 0 {
			delete(l.n, key)
		}
	}, true
}
