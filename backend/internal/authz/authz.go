// Package authz is the only place that decides which teams a user can see
// and that builds the team-scoped selectors used against Kubernetes, Loki
// and Prometheus (CLAUDE.md rule 1, ADR 0003).
//
// Deny by default: a user without mapped teams sees nothing, and a workload
// without the team label is visible to administrators only.
package authz

import (
	"context"
	"fmt"
	"slices"
)

// TeamLabel is the workload and pod label that carries the owning team.
const TeamLabel = "team"

// AppLabel is the pod label propagated to Loki as `app` and to
// kube-state-metrics as `label_app_kubernetes_io_name`.
const AppLabel = "app.kubernetes.io/name"

// maxGroups bounds the groups accepted from an identity token.
const maxGroups = 256

// Principal is an authenticated user and what they may see. Build it only
// through Resolver.Resolve.
type Principal struct {
	Subject  string
	Username string
	// Teams is sorted and contains only valid label values.
	Teams []string
	Admin bool
}

// HasAccess reports whether the principal may see any data at all.
func (p Principal) HasAccess() bool {
	return p.Admin || len(p.Teams) > 0
}

// CanSeeTeam reports whether workloads labelled team are visible. The empty
// team (no label) is visible to administrators only.
func (p Principal) CanSeeTeam(team string) bool {
	if p.Admin {
		return true
	}
	return team != "" && slices.Contains(p.Teams, team)
}

// CanSeeWorkload reports whether a workload with the given labels is visible.
func (p Principal) CanSeeWorkload(labels map[string]string) bool {
	return p.CanSeeTeam(labels[TeamLabel])
}

// TeamStore maps identity-provider groups to teams.
type TeamStore interface {
	TeamsForGroups(ctx context.Context, groups []string) ([]string, error)
}

// Resolver turns identity claims into a Principal.
type Resolver struct {
	store      TeamStore
	adminGroup string
}

// NewResolver returns a Resolver. Members of adminGroup are administrators.
func NewResolver(store TeamStore, adminGroup string) *Resolver {
	return &Resolver{store: store, adminGroup: adminGroup}
}

// Resolve maps the user's groups to teams. Teams whose name is not a valid
// label value are dropped, so they can never reach a query.
func (r *Resolver) Resolve(ctx context.Context, subject, username string, groups []string) (Principal, error) {
	if len(groups) > maxGroups {
		groups = groups[:maxGroups]
	}
	p := Principal{
		Subject:  subject,
		Username: username,
		Admin:    r.adminGroup != "" && slices.Contains(groups, r.adminGroup),
	}
	teams, err := r.store.TeamsForGroups(ctx, groups)
	if err != nil {
		return Principal{}, fmt.Errorf("authz: resolve teams: %w", err)
	}
	for _, t := range teams {
		if ValidName(t) {
			p.Teams = append(p.Teams, t)
		}
	}
	slices.Sort(p.Teams)
	p.Teams = slices.Compact(p.Teams)
	return p, nil
}
