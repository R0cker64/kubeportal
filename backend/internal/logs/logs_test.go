package logs

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

	"github.com/coder/websocket"

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

func selector(t *testing.T) authz.LogSelector {
	t.Helper()
	p, err := authz.NewResolver(teams{"g": {"payments"}}, "admin").Resolve(t.Context(), "s", "u", []string{"g"})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := p.LogSelector(authz.Target{Namespace: "prod", App: "payments-api", Team: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

func TestBuildQueryEscapesText(t *testing.T) {
	sel := selector(t)
	cases := map[Filter]string{
		{}:                           `{namespace="prod", app="payments-api", team="payments"}`,
		{Text: "timeout"}:            `{namespace="prod", app="payments-api", team="payments"} |= "timeout"`,
		{Level: "error"}:             `{namespace="prod", app="payments-api", team="payments"} | detected_level="error"`,
		{Text: `"} or {team=~".+`}:   `{namespace="prod", app="payments-api", team="payments"} |= "\"} or {team=~\".+"`,
		{Text: `a\b`, Level: "warn"}: `{namespace="prod", app="payments-api", team="payments"} |= "a\\b" | detected_level="warn"`,
	}
	for f, want := range cases {
		got, err := BuildQuery(sel, f)
		if err != nil || got != want {
			t.Errorf("BuildQuery(%+v) = %s, %v; want %s", f, got, err, want)
		}
	}
	for _, bad := range []Filter{{Level: "fatal"}, {Level: `error"`}} {
		if _, err := BuildQuery(sel, bad); !errors.Is(err, authz.ErrInvalidParam) {
			t.Errorf("BuildQuery(%+v) err = %v", bad, err)
		}
	}
	if _, err := BuildQuery(authz.LogSelector{}, Filter{}); err == nil {
		t.Error("zero selector accepted")
	}
}

func TestQuery(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"pod":"p1","detected_level":"info"},"values":[["1000000000","old\n"],["3000000000","new\n"]]},
			{"stream":{"pod":"p2","detected_level":"error"},"values":[["2000000000","mid"]]}
		]}}`)
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	c := NewClient(base, 5*time.Second)
	end := time.Unix(10, 0)
	lines, err := c.Query(t.Context(), selector(t), Filter{Text: "x"},
		authz.TimeRange{Start: time.Unix(0, 0), End: end}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Line != "new" || lines[1].Pod != "p2" || lines[1].Level != "error" {
		t.Fatalf("unexpected lines %+v", lines)
	}
	if gotQuery.Get("direction") != "backward" || gotQuery.Get("limit") != "2" ||
		gotQuery.Get("end") != "10000000000" || !strings.HasPrefix(gotQuery.Get("query"), `{namespace="prod"`) {
		t.Fatalf("unexpected upstream query %v", gotQuery)
	}
}

func TestQueryUpstreamErrors(t *testing.T) {
	for _, body := range []string{"", `{"status":"error"}`, `not json`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if body == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, body)
		}))
		base, _ := url.Parse(srv.URL)
		_, err := NewClient(base, time.Second).Query(t.Context(), selector(t), Filter{},
			authz.TimeRange{Start: time.Unix(0, 0), End: time.Unix(1, 0)}, 10)
		srv.Close()
		if !errors.Is(err, ErrUpstream) {
			t.Errorf("body %q: err = %v", body, err)
		}
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("é", maxLineBytes)
	got := truncate(long)
	if len(got) > maxLineBytes+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate: len %d", len(got))
	}
}

func TestTail(t *testing.T) {
	var mu sync.Mutex
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotQuery = r.URL.Query().Get("query")
		mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		_ = c.Write(r.Context(), websocket.MessageText,
			[]byte(`{"streams":[{"stream":{"pod":"p1"},"values":[["2000000000","b"],["1000000000","a"]]}]}`))
		<-r.Context().Done()
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	got := make(chan []Line, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- NewClient(base, 5*time.Second).Tail(ctx, selector(t), Filter{Level: "info"}, func(l []Line) error {
			got <- l
			return nil
		})
	}()

	select {
	case lines := <-got:
		if len(lines) != 2 || lines[0].Line != "a" || lines[1].Line != "b" {
			t.Fatalf("unexpected lines %+v", lines)
		}
	case err := <-errc:
		t.Fatalf("tail ended early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no lines received")
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("tail returned %v after cancel", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasSuffix(gotQuery, `| detected_level="info"`) {
		t.Fatalf("unexpected tail query %s", gotQuery)
	}
}
