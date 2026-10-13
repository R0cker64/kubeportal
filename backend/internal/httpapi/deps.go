package httpapi

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
	"github.com/R0cker64/kubeportal/backend/internal/k8s"
	"github.com/R0cker64/kubeportal/backend/internal/logs"
	"github.com/R0cker64/kubeportal/backend/internal/metrics"
	"github.com/R0cker64/kubeportal/backend/internal/store"
)

// Authenticator runs the BFF login flow and validates sessions
// (implemented by auth.Service).
type Authenticator interface {
	BeginLogin(w http.ResponseWriter) (string, error)
	CompleteLogin(ctx context.Context, w http.ResponseWriter, r *http.Request) error
	Session(ctx context.Context, r *http.Request) (store.Session, error)
	Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, error)
}

// PrincipalResolver maps a session to what the user may see
// (implemented by authz.Resolver).
type PrincipalResolver interface {
	Resolve(ctx context.Context, subject, username string, groups []string) (authz.Principal, error)
}

// WorkloadSource is the informer cache (implemented by k8s.Cache). Its
// results are unfiltered; handlers must apply authz.
type WorkloadSource interface {
	Workloads() []k8s.Workload
	Workload(namespace, kind, name string) (k8s.Workload, bool)
	Pods(w k8s.Workload) []*corev1.Pod
	Warnings(w k8s.Workload, pods []*corev1.Pod, limit int) []k8s.Event
	Subscribe() (<-chan struct{}, func())
	Synced() bool
	// Nodes is admin only.
	Nodes() []k8s.Node
}

// LogSource queries Loki (implemented by logs.Client).
type LogSource interface {
	Query(ctx context.Context, sel authz.LogSelector, f logs.Filter, r authz.TimeRange, limit int) ([]logs.Line, error)
	Tail(ctx context.Context, sel authz.LogSelector, f logs.Filter, emit func([]logs.Line) error) error
}

// MetricSource queries Prometheus (implemented by metrics.Client).
type MetricSource interface {
	Workload(ctx context.Context, sel authz.PodSelector, r authz.TimeRange) ([]metrics.Series, error)
	Nodes(ctx context.Context, scope authz.AdminScope) (map[string]metrics.NodeUsage, error)
}

// TeamAdmin manages teams and their group mapping (implemented by
// store.Store). Only administrators reach it.
type TeamAdmin interface {
	ListTeams(ctx context.Context) ([]store.Team, error)
	CreateTeam(ctx context.Context, t store.Team) error
	UpdateTeam(ctx context.Context, t store.Team) error
	DeleteTeam(ctx context.Context, name string) error
}

// Deps are the collaborators of the API handlers.
type Deps struct {
	Auth      Authenticator
	Resolver  PrincipalResolver
	Workloads WorkloadSource
	Logs      LogSource
	Metrics   MetricSource
	Teams     TeamAdmin
	// Ready reports whether dependencies needed to serve requests are up.
	Ready func(ctx context.Context) error
}
