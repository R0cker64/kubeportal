// Package metrics queries Prometheus for CPU and memory of one workload.
// Every query is built from an authz.PodSelector: container metrics are
// joined with kube_pod_labels, so only pods carrying the workload's team and
// app labels contribute (CLAUDE.md rule 1).
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

// ErrUpstream wraps every Prometheus failure. Callers return a generic error.
var ErrUpstream = errors.New("metrics: upstream error")

const (
	maxResponseBytes = 16 << 20
	// targetPoints is roughly how many points each series has.
	targetPoints = 120
	minStep      = 15 * time.Second
)

// Series names returned by Workload.
const (
	SeriesCPU           = "cpu"
	SeriesCPURequest    = "cpuRequest"
	SeriesCPULimit      = "cpuLimit"
	SeriesMemory        = "memory"
	SeriesMemoryRequest = "memoryRequest"
	SeriesMemoryLimit   = "memoryLimit"
)

// Point is a sample: Unix seconds and value. CPU is in cores, memory in bytes.
type Point [2]float64

// Series is one named time series.
type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
}

// Client talks to the Prometheus HTTP API.
type Client struct {
	base              *url.URL
	http              *http.Client
	timeout           time.Duration
	cadvisorNodeLabel string
}

// NewClient returns a client for the Prometheus at base. cadvisorNodeLabel
// is the label holding the node name on cAdvisor series (see
// DefaultCadvisorNodeLabel).
func NewClient(base *url.URL, timeout time.Duration, cadvisorNodeLabel string) *Client {
	return &Client{
		base: base, http: &http.Client{Timeout: timeout}, timeout: timeout,
		cadvisorNodeLabel: cadvisorNodeLabel,
	}
}

// Queries returns the PromQL for each series of the workload selected by sel.
func Queries(sel authz.PodSelector) (map[string]string, error) {
	pods := sel.KubePodLabels()
	if sel.Namespace() == "" || pods == "kube_pod_labels{}" {
		return nil, authz.ErrInvalidParam
	}
	ns := "namespace=" + strconv.Quote(sel.Namespace())
	join := func(expr string) string {
		return "sum(" + expr + " * on(namespace, pod) group_left() max by (namespace, pod) (" + pods + "))"
	}
	containers := ns + `, container!="", container!="POD"`
	resource := func(metric, res string) string {
		return join(metric + "{" + ns + `, resource="` + res + `"}`)
	}
	return map[string]string{
		SeriesCPU:           join("rate(container_cpu_usage_seconds_total{" + containers + "}[5m])"),
		SeriesMemory:        join("container_memory_working_set_bytes{" + containers + "}"),
		SeriesCPURequest:    resource("kube_pod_container_resource_requests", "cpu"),
		SeriesCPULimit:      resource("kube_pod_container_resource_limits", "cpu"),
		SeriesMemoryRequest: resource("kube_pod_container_resource_requests", "memory"),
		SeriesMemoryLimit:   resource("kube_pod_container_resource_limits", "memory"),
	}, nil
}

// Step returns the resolution for a range so each series has about
// targetPoints points.
func Step(r authz.TimeRange) time.Duration {
	step := r.End.Sub(r.Start) / targetPoints
	return max(step.Truncate(time.Second), minStep)
}

// Workload returns the CPU and memory series for sel over r, in a stable
// order. Series without data are returned with no points.
func (c *Client) Workload(ctx context.Context, sel authz.PodSelector, r authz.TimeRange) ([]Series, error) {
	queries, err := Queries(sel)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	step := Step(r)
	order := []string{SeriesCPU, SeriesCPURequest, SeriesCPULimit, SeriesMemory, SeriesMemoryRequest, SeriesMemoryLimit}
	out := make([]Series, 0, len(order))
	for _, name := range order {
		points, err := c.queryRange(ctx, queries[name], r, step)
		if err != nil {
			return nil, err
		}
		out = append(out, Series{Name: name, Points: points})
	}
	return out, nil
}

func (c *Client) queryRange(ctx context.Context, query string, r authz.TimeRange, step time.Duration) ([]Point, error) {
	u := c.base.JoinPath("/api/v1/query_range")
	form := url.Values{}
	form.Set("query", query)
	form.Set("start", strconv.FormatInt(r.Start.Unix(), 10))
	form.Set("end", strconv.FormatInt(r.End.Unix(), 10))
	form.Set("step", strconv.FormatInt(int64(step/time.Second), 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
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
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrUpstream, err)
	}
	if body.Status != "success" || body.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("%w: unexpected result %q", ErrUpstream, body.Data.ResultType)
	}
	points := []Point{}
	if len(body.Data.Result) == 0 {
		return points, nil
	}
	// sum(...) yields at most one series.
	for _, v := range body.Data.Result[0].Values {
		var ts float64
		var raw string
		if json.Unmarshal(v[0], &ts) != nil || json.Unmarshal(v[1], &raw) != nil {
			continue
		}
		val, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
			continue
		}
		points = append(points, Point{ts, val})
	}
	return points, nil
}
