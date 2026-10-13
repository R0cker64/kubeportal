// Package httpapi wires HTTP handlers, middleware and security headers.
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/R0cker64/kubeportal/backend/internal/config"
)

// NewHandler builds the root handler. static may be nil when the frontend is
// served separately (e.g. ng serve during development).
func NewHandler(cfg *config.Config, log *slog.Logger, static fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound)
	})

	var root http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound)
	})
	if static != nil {
		root = spaHandler(static)
	}
	mux.Handle("/", root)

	var h http.Handler = mux
	h = withMaxBody(cfg.MaxBodyBytes, h)
	h = withRecover(log, h)
	h = withAccessLog(log, h)
	h = withSecurityHeaders(cfg.PublicURL.Scheme == "https", h)
	h = withRequestID(h)
	return h
}

// NewServer returns an http.Server with every timeout set.
func NewServer(cfg *config.Config, log *slog.Logger, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}
