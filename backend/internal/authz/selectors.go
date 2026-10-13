package authz

import (
	"strconv"
	"strings"
)

// Target identifies the pods of one workload: its namespace (environment),
// the value of its app label and the value of its team label ("" when the
// workload has none).
type Target struct {
	Namespace string
	App       string
	Team      string
}

// LogSelector is a Loki stream selector scoped to one visible workload. It
// can only be built by Principal.LogSelector.
type LogSelector struct {
	expr string
}

// String returns the LogQL stream selector, e.g.
// {namespace="prod", app="api", team="payments"}.
func (s LogSelector) String() string { return s.expr }

// PodSelector selects the pods of one visible workload in kube-state-metrics.
// It can only be built by Principal.PodSelector.
type PodSelector struct {
	namespace string
	matchers  string
}

// Namespace returns the workload namespace (already validated).
func (s PodSelector) Namespace() string { return s.namespace }

// KubePodLabels returns a PromQL selector over kube_pod_labels for the
// workload's pods, e.g.
// kube_pod_labels{namespace="prod", label_app_kubernetes_io_name="api", label_team="payments"}.
func (s PodSelector) KubePodLabels() string { return "kube_pod_labels{" + s.matchers + "}" }

// LogSelector returns the Loki selector for t, or ErrInvalidParam when t is
// malformed or not visible to p. A workload without team label matches only
// streams without the label (team=""), so it never widens to other teams.
func (p Principal) LogSelector(t Target) (LogSelector, error) {
	if err := p.checkTarget(t); err != nil {
		return LogSelector{}, err
	}
	return LogSelector{expr: "{" + matchers(
		[2]string{"namespace", t.Namespace},
		[2]string{"app", t.App},
		[2]string{"team", t.Team},
	) + "}"}, nil
}

// PodSelector returns the kube-state-metrics selector for t, with the same
// rules as LogSelector.
func (p Principal) PodSelector(t Target) (PodSelector, error) {
	if err := p.checkTarget(t); err != nil {
		return PodSelector{}, err
	}
	return PodSelector{
		namespace: t.Namespace,
		matchers: matchers(
			[2]string{"namespace", t.Namespace},
			[2]string{"label_app_kubernetes_io_name", t.App},
			[2]string{"label_team", t.Team},
		),
	}, nil
}

func (p Principal) checkTarget(t Target) error {
	if !ValidName(t.Namespace) || !validLabelValue(t.App) {
		return ErrInvalidParam
	}
	if t.Team != "" && !ValidName(t.Team) {
		return ErrInvalidParam
	}
	if !p.CanSeeTeam(t.Team) {
		return ErrInvalidParam
	}
	return nil
}

// matchers renders name="value" pairs. Values are quoted as Go string
// literals, which is the string syntax of both LogQL and PromQL; they are
// also validated above, so quoting is a second line of defence.
func matchers(pairs ...[2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p[0]+"="+strconv.Quote(p[1]))
	}
	return strings.Join(parts, ", ")
}
