package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

// DefaultCadvisorNodeLabel is the label that carries the node name on
// cAdvisor series in the prometheus-community chart (job
// kubernetes-nodes-cadvisor). kube-prometheus-stack uses "node".
const DefaultCadvisorNodeLabel = "instance"

var labelNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidLabelName reports whether s is a Prometheus label name.
func ValidLabelName(s string) bool { return labelNameRe.MatchString(s) }

// NodeUsage is the live usage of one node: CPU in cores, memory in bytes.
// Requests count only pods that are Pending or Running.
type NodeUsage struct {
	CPU            float64 `json:"cpu"`
	Memory         float64 `json:"memory"`
	CPURequests    float64 `json:"cpuRequests"`
	MemoryRequests float64 `json:"memoryRequests"`
	Pods           int     `json:"pods"`
}

// activePods selects pods that are Pending or Running.
const activePods = `(max by (namespace, pod) (kube_pod_status_phase{phase=~"Pending|Running"}) == 1)`

// NodeQueries returns the PromQL for each node usage field. nodeLabel is the
// cAdvisor label holding the node name; it must be a valid label name.
func NodeQueries(nodeLabel string) (map[string]string, error) {
	if !ValidLabelName(nodeLabel) {
		return nil, authz.ErrInvalidParam
	}
	// Root cgroup (id="/") is the whole node as seen by the kubelet.
	cadvisor := func(expr string) string {
		return `sum by (node) (label_replace(` + expr + `, "node", "$1", "` + nodeLabel + `", "(.+)"))`
	}
	requests := func(res string) string {
		return `sum by (node) (kube_pod_container_resource_requests{resource="` + res + `"}` +
			` * on(namespace, pod) group_left() ` + activePods + `)`
	}
	return map[string]string{
		"cpu":            cadvisor(`rate(container_cpu_usage_seconds_total{id="/"}[5m])`),
		"memory":         cadvisor(`container_memory_working_set_bytes{id="/"}`),
		"cpuRequests":    requests("cpu"),
		"memoryRequests": requests("memory"),
		"pods":           `count by (node) (kube_pod_info * on(namespace, pod) group_left() ` + activePods + `)`,
	}, nil
}

// Nodes returns the usage of every node, keyed by node name. It requires an
// administrator scope.
func (c *Client) Nodes(ctx context.Context, scope authz.AdminScope) (map[string]NodeUsage, error) {
	if !scope.Granted() {
		return nil, authz.ErrForbidden
	}
	queries, err := NodeQueries(c.cadvisorNodeLabel)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	out := map[string]NodeUsage{}
	for _, field := range []string{"cpu", "memory", "cpuRequests", "memoryRequests", "pods"} {
		values, err := c.queryInstant(ctx, queries[field])
		if err != nil {
			return nil, err
		}
		for node, v := range values {
			u := out[node]
			switch field {
			case "cpu":
				u.CPU = v
			case "memory":
				u.Memory = v
			case "cpuRequests":
				u.CPURequests = v
			case "memoryRequests":
				u.MemoryRequests = v
			case "pods":
				u.Pods = int(v)
			}
			out[node] = u
		}
	}
	return out, nil
}

// queryInstant runs an instant query and returns value by the "node" label.
func (c *Client) queryInstant(ctx context.Context, query string) (map[string]float64, error) {
	form := url.Values{}
	form.Set("query", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base.JoinPath("/api/v1/query").String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrUpstream, err)
	}
	if body.Status != "success" || body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("%w: unexpected result %q", ErrUpstream, body.Data.ResultType)
	}
	out := map[string]float64{}
	for _, r := range body.Data.Result {
		node := r.Metric["node"]
		var raw string
		if node == "" || json.Unmarshal(r.Value[1], &raw) != nil {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out[node] = v
	}
	return out, nil
}
