package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"time"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
	"github.com/R0cker64/kubeportal/backend/internal/k8s"
	"github.com/R0cker64/kubeportal/backend/internal/metrics"
	"github.com/R0cker64/kubeportal/backend/internal/store"
)

// requireAdmin runs next only for administrators; everybody else gets 403.
// It must be wrapped by requireSession.
func (a *api) requireAdmin(next func(http.ResponseWriter, *http.Request, authz.Principal, authz.AdminScope)) http.HandlerFunc {
	return a.requireSession(func(w http.ResponseWriter, r *http.Request) {
		p, _ := principalFrom(r.Context())
		scope, err := p.AdminScope()
		if err != nil {
			writeError(w, r, http.StatusForbidden)
			return
		}
		next(w, r, p, scope)
	})
}

// --- teams -------------------------------------------------------------------

type teamBody struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Groups      []string `json:"groups"`
}

type teamResponse struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName"`
	Groups      []string  `json:"groups"`
	CreatedAt   time.Time `json:"createdAt"`
	// Workloads is how many workloads carry this team label.
	Workloads int `json:"workloads"`
}

// teamWorkloads counts workloads per team label value.
func (a *api) teamWorkloads() map[string]int {
	counts := map[string]int{}
	for _, w := range a.deps.Workloads.Workloads() {
		if w.Team != "" {
			counts[w.Team]++
		}
	}
	return counts
}

func (a *api) listTeams(w http.ResponseWriter, r *http.Request, _ authz.Principal, _ authz.AdminScope) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	teams, err := a.deps.Teams.ListTeams(ctx)
	if err != nil {
		a.internalError(w, r, "list teams", err)
		return
	}
	counts := a.teamWorkloads()
	out := make([]teamResponse, 0, len(teams))
	for _, t := range teams {
		out = append(out, teamResponse{
			Name: t.Name, DisplayName: t.DisplayName, Groups: t.Groups,
			CreatedAt: t.CreatedAt, Workloads: counts[t.Name],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out, "adminGroup": a.cfg.AdminGroup})
}

// decodeJSON reads a single JSON object with no unknown fields. The body
// size is already capped by withMaxBody.
func decodeJSON(r *http.Request, v any) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return errInvalidParam
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errInvalidParam
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalidParam
	}
	return nil
}

// teamInput decodes and validates a team body. On PUT the name comes from
// the path and must not be changed by the body.
func teamInput(r *http.Request, pathName string) (authz.TeamInput, error) {
	var body teamBody
	if err := decodeJSON(r, &body); err != nil {
		return authz.TeamInput{}, err
	}
	if pathName != "" {
		if body.Name != "" && body.Name != pathName {
			return authz.TeamInput{}, errInvalidParam
		}
		body.Name = pathName
	}
	return authz.NormalizeTeam(authz.TeamInput{Name: body.Name, DisplayName: body.DisplayName, Groups: body.Groups})
}

func (a *api) createTeam(w http.ResponseWriter, r *http.Request, p authz.Principal, _ authz.AdminScope) {
	in, err := teamInput(r, "")
	if err != nil {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	err = a.deps.Teams.CreateTeam(ctx, store.Team{Name: in.Name, DisplayName: in.DisplayName, Groups: in.Groups})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, r, http.StatusConflict)
		return
	}
	if err != nil {
		a.internalError(w, r, "create team", err)
		return
	}
	a.audit(r, p, "team created", in)
	writeJSON(w, http.StatusCreated, teamResponse{
		Name: in.Name, DisplayName: in.DisplayName, Groups: in.Groups, Workloads: a.teamWorkloads()[in.Name],
	})
}

func (a *api) updateTeam(w http.ResponseWriter, r *http.Request, p authz.Principal, _ authz.AdminScope) {
	in, err := teamInput(r, r.PathValue("name"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	err = a.deps.Teams.UpdateTeam(ctx, store.Team{Name: in.Name, DisplayName: in.DisplayName, Groups: in.Groups})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		a.internalError(w, r, "update team", err)
		return
	}
	a.audit(r, p, "team updated", in)
	writeJSON(w, http.StatusOK, teamResponse{
		Name: in.Name, DisplayName: in.DisplayName, Groups: in.Groups, Workloads: a.teamWorkloads()[in.Name],
	})
}

func (a *api) deleteTeam(w http.ResponseWriter, r *http.Request, p authz.Principal, _ authz.AdminScope) {
	name := r.PathValue("name")
	if !authz.ValidName(name) {
		writeError(w, r, http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	err := a.deps.Teams.DeleteTeam(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		a.internalError(w, r, "delete team", err)
		return
	}
	a.audit(r, p, "team deleted", authz.TeamInput{Name: name})
	w.WriteHeader(http.StatusNoContent)
}

// audit records an administrative change. Every value was validated.
func (a *api) audit(r *http.Request, p authz.Principal, action string, t authz.TeamInput) {
	a.log.InfoContext(r.Context(), "admin: "+action,
		"request_id", RequestID(r.Context()),
		"admin", sanitizeLogValue(p.Username),
		"team", t.Name,
		"groups", len(t.Groups),
	)
}

func (a *api) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", RequestID(r.Context()), "error", err)
	writeError(w, r, http.StatusInternalServerError)
}

// --- nodes -------------------------------------------------------------------

type nodeResponse struct {
	k8s.Node
	Usage *metrics.NodeUsage `json:"usage"`
}

func (a *api) listNodes(w http.ResponseWriter, r *http.Request, _ authz.Principal, scope authz.AdminScope) {
	nodes := a.deps.Workloads.Nodes()
	usage, err := a.deps.Metrics.Nodes(r.Context(), scope)
	if err != nil {
		// The node list is still useful without live usage.
		a.log.WarnContext(r.Context(), "prometheus node usage", "request_id", RequestID(r.Context()), "error", err)
	}
	out := make([]nodeResponse, 0, len(nodes))
	for _, n := range nodes {
		nr := nodeResponse{Node: n}
		if u, ok := usage[n.Name]; ok {
			nr.Usage = &u
		}
		out = append(out, nr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out, "usageAvailable": err == nil})
}

// --- workloads without a known team ------------------------------------------

func (a *api) unassignedWorkloads(w http.ResponseWriter, r *http.Request, _ authz.Principal, _ authz.AdminScope) {
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.UpstreamTimeout)
	defer cancel()
	teams, err := a.deps.Teams.ListTeams(ctx)
	if err != nil {
		a.internalError(w, r, "list teams", err)
		return
	}
	known := make([]string, 0, len(teams))
	for _, t := range teams {
		known = append(known, t.Name)
	}

	unlabelled, unknown := []serviceSummary{}, []serviceSummary{}
	now := time.Now()
	for _, wl := range a.deps.Workloads.Workloads() {
		switch {
		case wl.Team == "":
			h, _ := k8s.Assess(wl, a.deps.Workloads.Pods(wl), now)
			unlabelled = append(unlabelled, summary(wl, h))
		case !slices.Contains(known, wl.Team):
			h, _ := k8s.Assess(wl, a.deps.Workloads.Pods(wl), now)
			unknown = append(unknown, summary(wl, h))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"unlabelled": unlabelled, "unknownTeam": unknown})
}
