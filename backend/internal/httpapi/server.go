// Package httpapi wires HTTP handlers, middleware and security headers.
package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/R0cker64/kubeportal/backend/internal/config"
)

// maxWatchesPerUser bounds concurrent service-list WebSockets per user.
const maxWatchesPerUser = 5

type api struct {
	cfg     *config.Config
	log     *slog.Logger
	deps    Deps
	tails   *connLimiter
	watches *connLimiter
}

// NewHandler builds the root handler. static may be nil when the frontend is
// served separately (e.g. ng serve during development).
func NewHandler(cfg *config.Config, log *slog.Logger, static fs.FS, deps Deps) http.Handler {
	a := &api{
		cfg:     cfg,
		log:     log,
		deps:    deps,
		tails:   newConnLimiter(max(cfg.LiveTailsPerUser, 1)),
		watches: newConnLimiter(maxWatchesPerUser),
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", a.ready)

	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.logout)

	mux.HandleFunc("GET /api/me", a.requireSession(a.me))
	mux.HandleFunc("GET /api/services", a.requireSession(a.listServices))
	mux.HandleFunc("GET /api/services/watch", a.requireSession(a.watchServices))
	mux.HandleFunc("GET /api/services/{env}/{kind}/{name}", a.requireSession(a.getService))
	mux.HandleFunc("GET /api/services/{env}/{kind}/{name}/metrics", a.requireSession(a.serviceMetrics))
	mux.HandleFunc("GET /api/services/{env}/{kind}/{name}/logs", a.requireSession(a.serviceLogs))
	mux.HandleFunc("GET /api/services/{env}/{kind}/{name}/logs/live", a.requireSession(a.serviceLogsLive))

	mux.HandleFunc("GET /api/admin/teams", a.requireAdmin(a.listTeams))
	mux.HandleFunc("POST /api/admin/teams", a.requireAdmin(a.createTeam))
	mux.HandleFunc("PUT /api/admin/teams/{name}", a.requireAdmin(a.updateTeam))
	mux.HandleFunc("DELETE /api/admin/teams/{name}", a.requireAdmin(a.deleteTeam))
	mux.HandleFunc("GET /api/admin/nodes", a.requireAdmin(a.listNodes))
	mux.HandleFunc("GET /api/admin/workloads/unassigned", a.requireAdmin(a.unassignedWorkloads))

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
	h = withCSRF(cfg.PublicURL, h)
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

func (a *api) ready(w http.ResponseWriter, r *http.Request) {
	if a.deps.Ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
		defer cancel()
		if err := a.deps.Ready(ctx); err != nil {
			a.log.WarnContext(r.Context(), "not ready", "error", err)
			writeError(w, r, http.StatusServiceUnavailable)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// loginErrorPath is where the SPA shows a generic login error.
const loginErrorPath = "/?error=login&ref="

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	target, err := a.deps.Auth.BeginLogin(w)
	if err != nil {
		a.log.ErrorContext(r.Context(), "begin login", "request_id", RequestID(r.Context()), "error", err)
		http.Redirect(w, r, loginErrorPath+RequestID(r.Context()), http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *api) callback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := a.deps.Auth.CompleteLogin(ctx, w, r); err != nil {
		http.Redirect(w, r, loginErrorPath+RequestID(r.Context()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	target, err := a.deps.Auth.Logout(ctx, w, r)
	if err != nil {
		a.log.ErrorContext(r.Context(), "logout", "request_id", RequestID(r.Context()), "error", err)
		writeError(w, r, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logoutUrl": target})
}

type meResponse struct {
	Username     string   `json:"username"`
	Teams        []string `json:"teams"`
	Admin        bool     `json:"admin"`
	Environments []string `json:"environments"`
}

func (a *api) me(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	teams := p.Teams
	if teams == nil {
		teams = []string{}
	}
	writeJSON(w, http.StatusOK, meResponse{
		Username: p.Username, Teams: teams, Admin: p.Admin, Environments: a.cfg.Environments,
	})
}

// upstreamError logs an upstream failure and answers with a generic 502, or
// 400 when the failure was a rejected parameter.
func (a *api) upstreamError(w http.ResponseWriter, r *http.Request, what string, err error) {
	if errors.Is(err, errInvalidParam) {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	a.log.ErrorContext(r.Context(), what, "request_id", RequestID(r.Context()), "error", err)
	writeError(w, r, http.StatusBadGateway)
}
