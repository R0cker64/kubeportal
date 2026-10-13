package httpapi

import (
	"net/http"
	"time"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
	"github.com/R0cker64/kubeportal/backend/internal/k8s"
)

var errInvalidParam = authz.ErrInvalidParam

// maxEvents is how many Warning events a service detail returns.
const maxEvents = 50

type serviceSummary struct {
	Environment string     `json:"environment"`
	Kind        string     `json:"kind"`
	Name        string     `json:"name"`
	Team        string     `json:"team"`
	App         string     `json:"app"`
	Health      k8s.Health `json:"health"`
}

type serviceEvent struct {
	Reason   string    `json:"reason"`
	Message  string    `json:"message"`
	Object   string    `json:"object"`
	Count    int32     `json:"count"`
	LastSeen time.Time `json:"lastSeen"`
}

type serviceDetail struct {
	serviceSummary
	CreatedAt time.Time      `json:"createdAt"`
	Pods      []k8s.PodInfo  `json:"pods"`
	Events    []serviceEvent `json:"events"`
}

// visibleServices returns the workloads p may see, optionally in one
// environment, with their health. Deny by default: authz decides each one.
func (a *api) visibleServices(p authz.Principal, env string) []serviceSummary {
	out := []serviceSummary{}
	if !p.HasAccess() {
		return out
	}
	now := time.Now()
	for _, w := range a.deps.Workloads.Workloads() {
		if env != "" && w.Namespace != env {
			continue
		}
		if !p.CanSeeWorkload(w.Labels) {
			continue
		}
		h, _ := k8s.Assess(w, a.deps.Workloads.Pods(w), now)
		out = append(out, summary(w, h))
	}
	return out
}

func summary(w k8s.Workload, h k8s.Health) serviceSummary {
	return serviceSummary{
		Environment: w.Namespace, Kind: w.Kind, Name: w.Name, Team: w.Team, App: w.App, Health: h,
	}
}

func (a *api) listServices(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	env := r.URL.Query().Get("environment")
	if env != "" && !authz.ValidEnvironment(env, a.cfg.Environments) {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": a.visibleServices(p, env)})
}

// lookupService validates the path parameters and returns the workload only
// if p may see it. Invisible and missing workloads are both 404, so a user
// cannot probe other teams' service names.
func (a *api) lookupService(w http.ResponseWriter, r *http.Request) (authz.Principal, k8s.Workload, bool) {
	p, _ := principalFrom(r.Context())
	env, kind, name := r.PathValue("env"), r.PathValue("kind"), r.PathValue("name")
	if !authz.ValidEnvironment(env, a.cfg.Environments) || !authz.ValidName(name) ||
		(kind != k8s.KindDeployment && kind != k8s.KindStatefulSet) {
		writeError(w, r, http.StatusNotFound)
		return p, k8s.Workload{}, false
	}
	wl, ok := a.deps.Workloads.Workload(env, kind, name)
	if !ok || !p.CanSeeWorkload(wl.Labels) {
		writeError(w, r, http.StatusNotFound)
		return p, k8s.Workload{}, false
	}
	return p, wl, true
}

func (a *api) getService(w http.ResponseWriter, r *http.Request) {
	_, wl, ok := a.lookupService(w, r)
	if !ok {
		return
	}
	pods := a.deps.Workloads.Pods(wl)
	h, infos := k8s.Assess(wl, pods, time.Now())
	events := []serviceEvent{}
	for _, e := range a.deps.Workloads.Warnings(wl, pods, maxEvents) {
		events = append(events, serviceEvent(e))
	}
	writeJSON(w, http.StatusOK, serviceDetail{
		serviceSummary: summary(wl, h),
		CreatedAt:      wl.CreatedAt,
		Pods:           infos,
		Events:         events,
	})
}

// metricRanges is the allowlist of chart ranges.
var metricRanges = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

func target(wl k8s.Workload) authz.Target {
	return authz.Target{Namespace: wl.Namespace, App: wl.App, Team: wl.Team}
}

func (a *api) serviceMetrics(w http.ResponseWriter, r *http.Request) {
	p, wl, ok := a.lookupService(w, r)
	if !ok {
		return
	}
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "1h"
	}
	d, ok := metricRanges[rangeParam]
	if !ok {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	now := time.Now()
	tr, err := authz.NewTimeRange(now.Add(-d), now, now, a.cfg.MetricsMaxRange)
	if err != nil {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	sel, err := p.PodSelector(target(wl))
	if err != nil {
		// A visible workload without app label cannot be measured.
		writeError(w, r, http.StatusUnprocessableEntity)
		return
	}
	series, err := a.deps.Metrics.Workload(r.Context(), sel, tr)
	if err != nil {
		a.upstreamError(w, r, "prometheus query", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"start": tr.Start, "end": tr.End, "series": series,
	})
}
