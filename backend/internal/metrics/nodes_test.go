package metrics

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

func adminScope(t *testing.T) authz.AdminScope {
	t.Helper()
	p, err := authz.NewResolver(teams{}, "admin").Resolve(t.Context(), "s", "u", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.AdminScope()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNodeQueries(t *testing.T) {
	qs, err := NodeQueries("instance")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qs["cpu"], `label_replace(rate(container_cpu_usage_seconds_total{id="/"}[5m]), "node", "$1", "instance", "(.+)")`) {
		t.Errorf("cpu query: %s", qs["cpu"])
	}
	for _, bad := range []string{"", "1node", `instance", "x`, "a-b"} {
		if _, err := NodeQueries(bad); !errors.Is(err, authz.ErrInvalidParam) {
			t.Errorf("NodeQueries(%q) err = %v", bad, err)
		}
	}
}

func TestNodesRequiresAdminScope(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1")
	c := NewClient(base, time.Second, DefaultCadvisorNodeLabel)
	if _, err := c.Nodes(t.Context(), authz.AdminScope{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("zero scope accepted: %v", err)
	}
}

func TestNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.ParseForm() != nil {
			http.NotFound(w, r)
			return
		}
		q := r.PostForm.Get("query")
		val := "2"
		switch {
		case strings.Contains(q, "count by"):
			val = "17"
		case strings.Contains(q, "working_set"):
			val = "1073741824"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"node":"n1"},"value":[1,"%s"]},
			{"metric":{},"value":[1,"9"]},
			{"metric":{"node":"n2"},"value":[1,"NaN"]}]}}`, val)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	got, err := NewClient(base, 5*time.Second, DefaultCadvisorNodeLabel).Nodes(t.Context(), adminScope(t))
	if err != nil {
		t.Fatal(err)
	}
	want := NodeUsage{CPU: 2, Memory: 1 << 30, CPURequests: 2, MemoryRequests: 2, Pods: 17}
	if len(got) != 1 || got["n1"] != want {
		t.Fatalf("Nodes = %+v", got)
	}
}
