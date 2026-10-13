package k8s

import (
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// poolLabels are the node labels that name the node pool, by provider.
var poolLabels = []string{
	"cloud.google.com/gke-nodepool",
	"eks.amazonaws.com/nodegroup",
	"kubernetes.azure.com/agentpool",
	"karpenter.sh/nodepool",
	"node.kubernetes.io/pool",
}

const (
	zoneLabel     = "topology.kubernetes.io/zone"
	rolePrefix    = "node-role.kubernetes.io/"
	conditionsMax = 10
)

// NodeCondition is a node condition in a problematic state.
type NodeCondition struct {
	Type    string    `json:"type"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	Since   time.Time `json:"since"`
}

// Node is the administrator view of a cluster node.
type Node struct {
	Name           string          `json:"name"`
	Ready          bool            `json:"ready"`
	Unschedulable  bool            `json:"unschedulable"`
	Roles          []string        `json:"roles"`
	Pool           string          `json:"pool,omitempty"`
	Zone           string          `json:"zone,omitempty"`
	KubeletVersion string          `json:"kubeletVersion"`
	CreatedAt      time.Time       `json:"createdAt"`
	Problems       []NodeCondition `json:"problems"`
	// Allocatable capacity: CPU in cores, memory in bytes.
	AllocatableCPU    float64 `json:"allocatableCpu"`
	AllocatableMemory int64   `json:"allocatableMemory"`
	AllocatablePods   int64   `json:"allocatablePods"`
}

// Nodes returns every node sorted by name. Only administrators may see them;
// callers must hold an authz.AdminScope.
func (c *Cache) Nodes() []Node {
	if c.nodes == nil {
		return nil
	}
	list, _ := c.nodes.List(labels.Everything())
	out := make([]Node, 0, len(list))
	for _, n := range list {
		out = append(out, fromNode(n))
	}
	slices.SortFunc(out, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func fromNode(n *corev1.Node) Node {
	node := Node{
		Name:           n.Name,
		Unschedulable:  n.Spec.Unschedulable,
		Roles:          []string{},
		Zone:           n.Labels[zoneLabel],
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		CreatedAt:      n.CreationTimestamp.Time,
		Problems:       []NodeCondition{},
	}
	for _, l := range poolLabels {
		if v := n.Labels[l]; v != "" {
			node.Pool = v
			break
		}
	}
	for l := range n.Labels {
		if role, ok := strings.CutPrefix(l, rolePrefix); ok && role != "" {
			node.Roles = append(node.Roles, role)
		}
	}
	slices.Sort(node.Roles)

	for _, cond := range n.Status.Conditions {
		problem := false
		switch cond.Type {
		case corev1.NodeReady:
			node.Ready = cond.Status == corev1.ConditionTrue
			problem = !node.Ready
		default:
			// MemoryPressure, DiskPressure, PIDPressure, NetworkUnavailable
			// and custom conditions are problems when true.
			problem = cond.Status == corev1.ConditionTrue
		}
		if problem && len(node.Problems) < conditionsMax {
			node.Problems = append(node.Problems, NodeCondition{
				Type: string(cond.Type), Status: string(cond.Status),
				Reason: cond.Reason, Message: cond.Message, Since: cond.LastTransitionTime.Time,
			})
		}
	}

	alloc := n.Status.Allocatable
	node.AllocatableCPU = alloc.Cpu().AsApproximateFloat64()
	node.AllocatableMemory = alloc.Memory().Value()
	node.AllocatablePods = alloc.Pods().Value()
	return node
}
