package k8s

import (
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Status is the overall health of a workload.
type Status string

// Health statuses (docs/architecture.md, "Módulo de salud").
const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// Reason codes explain a degraded or down status. The frontend translates
// them; they are stable API values.
const (
	ReasonNoReadyReplicas   = "no_ready_replicas"
	ReasonReplicasNotReady  = "replicas_not_ready"
	ReasonCrashLoopBackOff  = "crash_loop_back_off"
	ReasonImagePull         = "image_pull_error"
	ReasonConfigError       = "config_error"
	ReasonOOMKilled         = "oom_killed"
	ReasonPendingTooLong    = "pending"
	ReasonRecentRestarts    = "recent_restarts"
	ReasonPodFailed         = "pod_failed"
	ReasonRolloutInProgress = "rollout_in_progress"
)

const (
	// pendingGrace is how long a pod may stay Pending before it counts.
	pendingGrace = 5 * time.Minute
	// recentWindow is how far back a termination counts as recent.
	recentWindow = 15 * time.Minute
)

// Health summarizes a workload.
type Health struct {
	Status   Status   `json:"status"`
	Desired  int32    `json:"desired"`
	Ready    int32    `json:"ready"`
	Restarts int32    `json:"restarts"`
	Reasons  []string `json:"reasons"`
}

// ContainerInfo describes one container of a pod.
type ContainerInfo struct {
	Name         string     `json:"name"`
	Ready        bool       `json:"ready"`
	Restarts     int32      `json:"restarts"`
	State        string     `json:"state"`            // running, waiting, terminated
	Reason       string     `json:"reason,omitempty"` // e.g. CrashLoopBackOff
	LastReason   string     `json:"lastTerminationReason,omitempty"`
	LastFinished *time.Time `json:"lastTerminatedAt,omitempty"`
}

// PodInfo describes one pod of a workload.
type PodInfo struct {
	Name       string          `json:"name"`
	Phase      string          `json:"phase"`
	Ready      bool            `json:"ready"`
	Restarts   int32           `json:"restarts"`
	Node       string          `json:"node,omitempty"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	Containers []ContainerInfo `json:"containers"`
}

// Assess computes the health of w from its pods. It is a pure function so
// it can be tested without a cluster.
func Assess(w Workload, pods []*corev1.Pod, now time.Time) (Health, []PodInfo) {
	h := Health{Desired: w.Desired, Ready: w.Ready, Reasons: []string{}}
	reasons := map[string]bool{}
	infos := make([]PodInfo, 0, len(pods))

	for _, p := range pods {
		info := podInfo(p)
		h.Restarts += info.Restarts
		infos = append(infos, info)

		switch {
		case p.Status.Phase == corev1.PodFailed:
			reasons[ReasonPodFailed] = true
		case p.Status.Phase == corev1.PodPending && now.Sub(p.CreationTimestamp.Time) > pendingGrace:
			reasons[ReasonPendingTooLong] = true
		}
		for _, cs := range allStatuses(p) {
			if wt := cs.State.Waiting; wt != nil {
				switch wt.Reason {
				case "CrashLoopBackOff":
					reasons[ReasonCrashLoopBackOff] = true
				case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
					reasons[ReasonImagePull] = true
				case "CreateContainerConfigError", "CreateContainerError":
					reasons[ReasonConfigError] = true
				}
			}
			if t := cs.LastTerminationState.Terminated; t != nil && now.Sub(t.FinishedAt.Time) <= recentWindow {
				if t.Reason == "OOMKilled" {
					reasons[ReasonOOMKilled] = true
				} else {
					reasons[ReasonRecentRestarts] = true
				}
			}
		}
	}

	switch {
	case w.Desired > 0 && w.Ready == 0:
		h.Status = StatusDown
		reasons[ReasonNoReadyReplicas] = true
	case w.Ready < w.Desired:
		h.Status = StatusDegraded
		reasons[ReasonReplicasNotReady] = true
	case len(reasons) > 0:
		h.Status = StatusDegraded
	default:
		h.Status = StatusHealthy
	}
	if w.Updated < w.Desired {
		reasons[ReasonRolloutInProgress] = true
	}

	for r := range reasons {
		h.Reasons = append(h.Reasons, r)
	}
	slices.Sort(h.Reasons)
	return h, infos
}

func allStatuses(p *corev1.Pod) []corev1.ContainerStatus {
	return append(slices.Clone(p.Status.InitContainerStatuses), p.Status.ContainerStatuses...)
}

func podInfo(p *corev1.Pod) PodInfo {
	info := PodInfo{
		Name:       p.Name,
		Phase:      string(p.Status.Phase),
		Node:       p.Spec.NodeName,
		Containers: []ContainerInfo{},
	}
	if p.Status.StartTime != nil {
		t := p.Status.StartTime.Time
		info.StartedAt = &t
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			info.Ready = c.Status == corev1.ConditionTrue
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		ci := ContainerInfo{Name: cs.Name, Ready: cs.Ready, Restarts: cs.RestartCount}
		switch {
		case cs.State.Running != nil:
			ci.State = "running"
		case cs.State.Waiting != nil:
			ci.State, ci.Reason = "waiting", cs.State.Waiting.Reason
		case cs.State.Terminated != nil:
			ci.State, ci.Reason = "terminated", cs.State.Terminated.Reason
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			ci.LastReason = t.Reason
			at := t.FinishedAt.Time
			ci.LastFinished = &at
		}
		info.Restarts += cs.RestartCount
		info.Containers = append(info.Containers, ci)
	}
	return info
}
