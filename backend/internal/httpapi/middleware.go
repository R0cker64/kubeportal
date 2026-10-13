package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// contentSecurityPolicy returns a strict policy. Angular injects component
// styles at runtime, so style-src allows only the per-request nonce that the
// SPA handler writes into index.html (ngCspNonce). Never add 'unsafe-inline'
// or 'unsafe-eval'.
func contentSecurityPolicy(nonce string) string {
	return "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'nonce-" + nonce + "'; " +
		cspRest
}

const cspRest = "img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

const requestIDHeader = "X-Request-Id"

type ctxKey int

const (
	requestIDKey ctxKey = iota
	cspNonceKey
	principalKey
)

// RequestID returns the correlation id assigned to the request, if any.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func cspNonce(ctx context.Context) string {
	n, _ := ctx.Value(cspNonceKey).(string)
	return n
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error.
	return hex.EncodeToString(b)
}

// withRequestID assigns a server-generated correlation id. Incoming ids are
// ignored so clients cannot inject values into server logs.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := randomHex(16)
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func withSecurityHeaders(hsts bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := randomHex(16)
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy(nonce))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceKey, nonce)))
	})
}

func withMaxBody(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// withRecover turns panics into a generic 500 so stack details never reach
// the client.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic in handler",
					"request_id", RequestID(r.Context()), "panic", v)
				writeError(w, r, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// maxLoggedValueLen caps client-controlled values written to the logs.
const maxLoggedValueLen = 256

// sanitizeLogValue strips line breaks and truncates a client-controlled value
// so it cannot forge log entries, whatever slog handler is in use.
func sanitizeLogValue(v string) string {
	v = strings.ReplaceAll(v, "\n", "")
	v = strings.ReplaceAll(v, "\r", "")
	if len(v) > maxLoggedValueLen {
		v = v[:maxLoggedValueLen]
	}
	return v
}

// withAccessLog logs method, path, status and latency. It deliberately omits
// query strings and headers, which may carry search text, cookies or tokens.
func withAccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.InfoContext(r.Context(), "http request",
			"request_id", RequestID(r.Context()),
			"method", r.Method,
			"path", sanitizeLogValue(r.URL.Path),
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
