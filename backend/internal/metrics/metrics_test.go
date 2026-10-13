package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

type teams map[string][]string

func (t teams) TeamsForGroups(_ context.Context, groups []string) ([]string, error) {
	var out []string
	for _, g := range groups {
		out = append(out, t[g]...)
	}
	return out, nil
}

func podSelector(t *testing.T) authz.PodSelector {
	t.Helper()
	p, err := authz.NewResolver(teams{"g": {"catalog"}}, "admin").Resolve(t.Context(), "s", "u", []string{"g"})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := p.PodSelector(authz.Target{Namespace: "staging", App: "catalog-api", Team: "catalog"})
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

func TestQueriesAreScopedToTheWorkload(t *testing.T) {
	qs, err := Queries(podSelector(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 6 {
		t.Fatalf("expected 6 queries, got %d", len(qs))
	}
	scope := `kube_pod_labels{namespace="staging", label_app_kubernetes_io_name="catalog-api", label_team="catalog"}`
	for name, q := range qs {
		if !strings.Contains(q, scope) || !strings.Contains(q, `namespace="staging"`) {
			t.Errorf("%s is not scoped: %s", name, q)
		}
	}
	want := `sum(rate(container_cpu_usage_seconds_total{namespace="staging", container!="", container!="POD"}[5m])` +
		` * on(namespace, pod) group_left() max by (namespace, pod) (` + scope + `))`
	if qs[SeriesCPU] != want {
		t.Errorf("cpu query:\n got %s\nwant %s", qs[SeriesCPU], want)
	}
	if _, err := Queries(authz.PodSelector{}); !errors.Is(err, authz.ErrInvalidParam) {
		t.Errorf("zero selector accepted: %v", err)
	}
}

func TestStep(t *testing.T) {
	now := time.Now()
	if s := Step(authz.TimeRange{Start: now.Add(-time.Hour), End: now}); s != 30*time.Second {
		t.Errorf("1h step = %v", s)
	}
	if s := Step(authz.TimeRange{Start: now.Add(-time.Minute), End: now}); s != minStep {
		t.Errorf("1m step = %v", s)
	}
}

func TestWorkload(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.ParseForm() != nil {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		queries = append(queries, r.PostForm.Get("query"))
		mu.Unlock()
		if strings.Contains(r.PostForm.Get("query"), "limits") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{},"values":[[100,"0.5"],[130,"NaN"],[160,"0.25"]]}]}}`)
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	now := time.Now()
	series, err := NewClient(base, 5*time.Second).Workload(t.Context(), podSelector(t),
		authz.TimeRange{Start: now.Add(-time.Hour), End: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 6 || series[0].Name != SeriesCPU || len(series[0].Points) != 2 ||
		series[0].Points[1] != (Point{160, 0.25}) || len(series[2].Points) != 0 || series[2].Points == nil {
		t.Fatalf("unexpected series %+v", series)
	}
	if len(queries) != 6 {
		t.Fatalf("expected 6 upstream queries, got %d", len(queries))
	}
}

func TestWorkloadUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	now := time.Now()
	_, err := NewClient(base, time.Second).Workload(t.Context(), podSelector(t),
		authz.TimeRange{Start: now.Add(-time.Hour), End: now})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v", err)
	}
}
