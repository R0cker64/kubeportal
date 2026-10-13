package httpapi

// Team isolation tests (CLAUDE.md rule 4, Phase 2 exit criterion): for every
// endpoint that returns team data, a user of team A gets nothing of team B,
// a user without a team gets nothing, and only the admin sees workloads
// without the team label.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/R0cker64/kubeportal/backend/internal/auth"
	"github.com/R0cker64/kubeportal/backend/internal/authz"
	"github.com/R0cker64/kubeportal/backend/internal/config"
	"github.com/R0cker64/kubeportal/backend/internal/k8s"
	"github.com/R0cker64/kubeportal/backend/internal/logs"
	"github.com/R0cker64/kubeportal/backend/internal/metrics"
	"github.com/R0cker64/kubeportal/backend/internal/store"
)

// --- fakes -------------------------------------------------------------------

// fakeAuth maps the session cookie value directly to a session.
type fakeAuth struct {
	mu       sync.Mutex
	sessions map[string]store.Session
}

func (f *fakeAuth) BeginLogin(w http.ResponseWriter) (string, error) {
	return "https://idp.example.com/auth?state=x", nil
}

func (f *fakeAuth) CompleteLogin(_ context.Context, _ http.ResponseWriter, r *http.Request) error {
	if r.URL.Query().Get("code") != "ok" {
		return auth.ErrLoginFailed
	}
	return nil
}

func (f *fakeAuth) Session(_ context.Context, r *http.Request) (store.Session, error) {
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return store.Session{}, auth.ErrNoSession
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[c.Value]
	if !ok {
		return store.Session{}, auth.ErrNoSession
	}
	return s, nil
}

func (f *fakeAuth) Logout(context.Context, http.ResponseWriter, *http.Request) (string, error) {
	return "https://idp.example.com/logout", nil
}

// memTeams is an in-memory team store. It backs both the authz resolver
// and the admin API, like store.Store does in production.
type memTeams struct {
	mu    sync.Mutex
	teams map[string]store.Team
}

func newMemTeams(teams ...store.Team) *memTeams {
	m := &memTeams{teams: map[string]store.Team{}}
	for _, t := range teams {
		m.teams[t.Name] = t
	}
	return m
}

func (m *memTeams) TeamsForGroups(_ context.Context, groups []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, t := range m.teams {
		for _, g := range t.Groups {
			if slices.Contains(groups, g) {
				out = append(out, t.Name)
				break
			}
		}
	}
	return out, nil
}

func (m *memTeams) ListTeams(context.Context) ([]store.Team, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Team, 0, len(m.teams))
	for _, t := range m.teams {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b store.Team) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *memTeams) CreateTeam(_ context.Context, t store.Team) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.teams[t.Name]; ok {
		return store.ErrConflict
	}
	m.teams[t.Name] = t
	return nil
}

func (m *memTeams) UpdateTeam(_ context.Context, t store.Team) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.teams[t.Name]; !ok {
		return store.ErrNotFound
	}
	m.teams[t.Name] = t
	return nil
}

func (m *memTeams) DeleteTeam(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.teams[name]; !ok {
		return store.ErrNotFound
	}
	delete(m.teams, name)
	return nil
}

// fakeLogs records every selector it receives.
type fakeLogs struct {
	mu        sync.Mutex
	selectors []string
}

func (f *fakeLogs) record(sel authz.LogSelector) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selectors = append(f.selectors, sel.String())
}

func (f *fakeLogs) Query(_ context.Context, sel authz.LogSelector, _ logs.Filter, _ authz.TimeRange, _ int) ([]logs.Line, error) {
	f.record(sel)
	return []logs.Line{{Time: time.Now(), Pod: "p", Line: "line of " + sel.String()}}, nil
}

func (f *fakeLogs) Tail(ctx context.Context, sel authz.LogSelector, _ logs.Filter, emit func([]logs.Line) error) error {
	f.record(sel)
	if err := emit([]logs.Line{{Time: time.Now(), Pod: "p", Line: "live " + sel.String()}}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

type fakeMetrics struct {
	mu        sync.Mutex
	selectors []string
}

func (f *fakeMetrics) Nodes(_ context.Context, scope authz.AdminScope) (map[string]metrics.NodeUsage, error) {
	if !scope.Granted() {
		return nil, authz.ErrForbidden
	}
	return map[string]metrics.NodeUsage{"node-a": {CPU: 0.5, Pods: 3}}, nil
}

func (f *fakeMetrics) Workload(_ context.Context, sel authz.PodSelector, _ authz.TimeRange) ([]metrics.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selectors = append(f.selectors, sel.KubePodLabels())
	return []metrics.Series{{Name: metrics.SeriesCPU, Points: []metrics.Point{{1, 0.1}}}}, nil
}

// --- fixture -----------------------------------------------------------------

type fixture struct {
	handler http.Handler
	cfg     *config.Config
	teams   *memTeams
	logs    *fakeLogs
	metrics *fakeMetrics
}

type wl struct {
	env, kind, name, team string
}

// Workloads in the fixture cluster. legacy-batch has no team label.
var fixtureWorkloads = []wl{
	{"prod", k8s.KindDeployment, "payments-api", "payments"},
	{"dev", k8s.KindDeployment, "payments-api", "payments"},
	{"prod", k8s.KindDeployment, "catalog-api", "catalog"},
	{"prod", k8s.KindStatefulSet, "catalog-db", "catalog"},
	{"prod", k8s.KindDeployment, "legacy-batch", ""},
	// Labelled for a team that is not registered yet (see admin tests).
	{"staging", k8s.KindDeployment, "search-api", "search"},
}

func ptr[T any](v T) *T { return &v }

func objects() []runtime.Object {
	objs := []runtime.Object{&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}}
	for i, w := range fixtureWorkloads {
		labels := map[string]string{authz.AppLabel: w.name}
		if w.team != "" {
			labels[authz.TeamLabel] = w.team
		}
		uid := types.UID(fmt.Sprintf("uid-%d", i))
		meta := metav1.ObjectMeta{Namespace: w.env, Name: w.name, UID: uid, Labels: labels}
		tmpl := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}
		owner := []metav1.OwnerReference{{Kind: "StatefulSet", Name: w.name, UID: uid, Controller: ptr(true)}}
		if w.kind == k8s.KindDeployment {
			objs = append(objs, &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1)), Template: tmpl}})
			rsUID := types.UID(fmt.Sprintf("rs-%d", i))
			objs = append(objs, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
				Namespace: w.env, Name: w.name + "-rs", UID: rsUID,
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: w.name, UID: uid, Controller: ptr(true)}},
			}})
			owner = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: w.name + "-rs", UID: rsUID, Controller: ptr(true)}}
		} else {
			objs = append(objs, &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(1)), Template: tmpl}})
		}
		pod := w.name + "-pod"
		objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace: w.env, Name: pod, Labels: labels, OwnerReferences: owner,
		}})
		objs = append(objs, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: w.env, Name: pod + "-event"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: pod},
			Type:           corev1.EventTypeWarning, Reason: "BackOff", Message: "secret of " + w.name,
			LastTimestamp: metav1.NewTime(time.Now()),
		})
	}
	return objs
}

func newFixture(t *testing.T, publicURL string) *fixture {
	t.Helper()
	cfg := testConfig(publicURL)
	cfg.MaxBodyBytes = 1 << 20
	cfg.Environments = []string{"dev", "staging", "prod"}
	cfg.UpstreamTimeout = 5 * time.Second
	cfg.LogMaxRange = 7 * 24 * time.Hour
	cfg.LogMaxLines = 1000
	cfg.LogMaxSearchLen = 64
	cfg.LiveTailsPerUser = 2
	cfg.MetricsMaxRange = 7 * 24 * time.Hour

	cache, err := k8s.NewCache(fake.NewClientset(objects()...), cfg.Environments, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := cache.Start(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}

	f := &fixture{cfg: cfg, logs: &fakeLogs{}, metrics: &fakeMetrics{}}
	fa := &fakeAuth{sessions: map[string]store.Session{
		"alice": {Subject: "alice", Username: "alice", Groups: []string{"team-payments"}},
		"bob":   {Subject: "bob", Username: "bob", Groups: []string{"team-catalog"}},
		"carol": {Subject: "carol", Username: "carol", Groups: []string{"unmapped"}},
		"admin": {Subject: "admin", Username: "admin", Groups: []string{"kubeportal-admin"}},
		"dave":  {Subject: "dave", Username: "dave", Groups: []string{"team-search"}},
	}}
	f.teams = newMemTeams(
		store.Team{Name: "payments", DisplayName: "Payments", Groups: []string{"team-payments"}},
		store.Team{Name: "catalog", DisplayName: "Catalog", Groups: []string{"team-catalog"}},
	)
	resolver := authz.NewResolver(f.teams, "kubeportal-admin")
	f.handler = NewHandler(cfg, testLogger(), nil, Deps{
		Auth: fa, Resolver: resolver, Workloads: cache, Logs: f.logs, Metrics: f.metrics, Teams: f.teams,
	})
	return f
}

// visibleTo is the expected visibility matrix.
func visibleTo(user string, w wl) bool {
	switch user {
	case "admin":
		return true
	case "alice":
		return w.team == "payments"
	case "bob":
		return w.team == "catalog"
	default:
		return false
	}
}

var users = []string{"alice", "bob", "carol", "dave", "admin"}

func (f *fixture) get(t *testing.T, user, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if user != "" {
		req.AddCookie(sessionCookieFor(user))
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func sessionCookieFor(v string) *http.Cookie {
	return &http.Cookie{Name: auth.SessionCookie, Value: v, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

func servicePath(w wl) string {
	return "/api/services/" + w.env + "/" + w.kind + "/" + w.name
}

// --- tests -------------------------------------------------------------------

func TestAPIRequiresSession(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	w := fixtureWorkloads[0]
	for _, target := range []string{
		"/api/me", "/api/services", "/api/services/watch", servicePath(w),
		servicePath(w) + "/metrics", servicePath(w) + "/logs", servicePath(w) + "/logs/live",
	} {
		for _, user := range []string{"", "forged"} {
			if rec := f.get(t, user, target); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s as %q: status %d", target, user, rec.Code)
			}
		}
	}
}

func TestMe(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	cases := map[string]meResponse{
		"alice": {Username: "alice", Teams: []string{"payments"}},
		"carol": {Username: "carol", Teams: []string{}},
		"admin": {Username: "admin", Teams: []string{}, Admin: true},
	}
	for user, want := range cases {
		var got meResponse
		if err := json.NewDecoder(f.get(t, user, "/api/me").Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Username != want.Username || !slices.Equal(got.Teams, want.Teams) || got.Admin != want.Admin ||
			!slices.Equal(got.Environments, []string{"dev", "staging", "prod"}) {
			t.Errorf("%s: got %+v", user, got)
		}
	}
}

func decodeServices(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Services []serviceSummary `json:"services"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Services == nil {
		t.Fatal("services must be an empty array, not null")
	}
	var names []string
	for _, s := range resp.Services {
		names = append(names, s.Environment+"/"+s.Kind+"/"+s.Name)
	}
	return names
}

func expectedServices(user, env string) []string {
	var want []string
	for _, w := range fixtureWorkloads {
		if visibleTo(user, w) && (env == "" || w.env == env) {
			want = append(want, w.env+"/"+w.kind+"/"+w.name)
		}
	}
	slices.Sort(want)
	return want
}

func TestServiceListIsolation(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	for _, user := range users {
		for _, env := range []string{"", "prod", "dev", "staging"} {
			target := "/api/services"
			if env != "" {
				target += "?environment=" + env
			}
			rec := f.get(t, user, target)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: status %d", user, target, rec.Code)
			}
			got := decodeServices(t, rec.Body.Bytes())
			slices.Sort(got)
			if want := expectedServices(user, env); !slices.Equal(got, want) {
				t.Errorf("%s %s: got %v, want %v", user, target, got, want)
			}
		}
	}
	if rec := f.get(t, "alice", "/api/services?environment=kube-system"); rec.Code != http.StatusBadRequest {
		t.Errorf("environment outside the allowlist: status %d", rec.Code)
	}
}

func TestServiceEndpointsIsolation(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	for _, user := range users {
		for _, w := range fixtureWorkloads {
			for _, suffix := range []string{"", "/metrics", "/logs"} {
				target := servicePath(w) + suffix
				rec := f.get(t, user, target)
				body := rec.Body.String()
				if visibleTo(user, w) {
					if rec.Code != http.StatusOK {
						t.Errorf("%s %s: status %d, want 200", user, target, rec.Code)
					}
					continue
				}
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s %s: status %d, want 404", user, target, rec.Code)
				}
				if strings.Contains(body, w.name) || strings.Contains(body, "secret") {
					t.Errorf("%s %s: leaked data: %s", user, target, body)
				}
			}
		}
	}

	// Every upstream query carried an exact team matcher (team="" for the
	// admin on the unlabelled workload); which team is checked by the 404s
	// above and by the authz tests.
	for _, sel := range append(slices.Clone(f.logs.selectors), f.metrics.selectors...) {
		if !strings.Contains(sel, `team="`) {
			t.Errorf("selector without team scope: %s", sel)
		}
	}
}

func TestServiceDetailOnlyContainsOwnPodsAndEvents(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	rec := f.get(t, "alice", "/api/services/prod/deployment/payments-api")
	var d serviceDetail
	if err := json.NewDecoder(rec.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	if len(d.Pods) != 1 || d.Pods[0].Name != "payments-api-pod" {
		t.Errorf("pods = %+v", d.Pods)
	}
	if len(d.Events) != 1 || d.Events[0].Message != "secret of payments-api" {
		t.Errorf("events = %+v", d.Events)
	}
}

func TestParameterValidation(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	base := "/api/services/prod/deployment/payments-api"
	now := time.Now().UTC()
	rfc := func(t time.Time) string { return url.QueryEscape(t.Format(time.RFC3339)) }

	cases := map[string]int{
		"/api/services/kube-system/deployment/coredns":           http.StatusNotFound,
		"/api/services/prod/daemonset/payments-api":              http.StatusNotFound,
		"/api/services/prod/deployment/Payments_API":             http.StatusNotFound,
		base + "/logs?q=" + strings.Repeat("a", 65):              http.StatusBadRequest,
		base + "/logs?q=a%0Ab":                                   http.StatusBadRequest,
		base + "/logs?level=fatal":                               http.StatusBadRequest,
		base + "/logs?limit=0":                                   http.StatusBadRequest,
		base + "/logs?limit=1001":                                http.StatusBadRequest,
		base + "/logs?start=yesterday":                           http.StatusBadRequest,
		base + "/logs?start=" + rfc(now.Add(-8*24*time.Hour)):    http.StatusBadRequest,
		base + "/logs?end=" + rfc(now.Add(time.Hour)):            http.StatusBadRequest,
		base + "/logs?q=%22%7D%20or%20%7Bteam%3D~%22.%2B&level=": http.StatusOK,
		base + "/logs?start=" + rfc(now.Add(-6*24*time.Hour)):    http.StatusOK,
		base + "/metrics?range=30d":                              http.StatusBadRequest,
		base + "/metrics?range=7d":                               http.StatusOK,
	}
	for target, want := range cases {
		if rec := f.get(t, "alice", target); rec.Code != want {
			t.Errorf("%s: status %d, want %d", target, rec.Code, want)
		}
	}
}

func TestCSRFOnLogout(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	post := func(header, origin string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/logout", nil)
		req.AddCookie(sessionCookieFor("alice"))
		if header != "" {
			req.Header.Set(csrfHeader, header)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post("", ""); c != http.StatusForbidden {
		t.Errorf("without header: %d", c)
	}
	if c := post("other", ""); c != http.StatusForbidden {
		t.Errorf("wrong header: %d", c)
	}
	if c := post(csrfValue, "https://evil.example.com"); c != http.StatusForbidden {
		t.Errorf("foreign origin: %d", c)
	}
	if c := post(csrfValue, "http://localhost:8080"); c != http.StatusOK {
		t.Errorf("valid request: %d", c)
	}
}

func TestLoginRedirects(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	rec := f.get(t, "", "/auth/login")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://idp.example.com/") {
		t.Errorf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = f.get(t, "", "/auth/callback?code=ok")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("callback ok: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = f.get(t, "", "/auth/callback?code=bad")
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/?error=login&ref=") {
		t.Errorf("callback error: %d %s", rec.Code, loc)
	}
}

// --- WebSocket ---------------------------------------------------------------

// dialWS connects as user and returns the connection, or the HTTP status of
// the refused handshake.
func dialWS(t *testing.T, srv *httptest.Server, user, path, origin string) (*websocket.Conn, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set("Cookie", auth.SessionCookie+"="+user)
	h.Set("Origin", origin)
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path, &websocket.DialOptions{HTTPHeader: h})
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	return c, status, err
}

func wsFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	t.Cleanup(srv.Close)
	srv.Config.Handler = newFixture(t, srv.URL).handler
	return srv, srv.URL
}

func readJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestLiveLogsIsolation(t *testing.T) {
	srv, origin := wsFixture(t)
	for _, user := range users {
		for _, w := range fixtureWorkloads {
			c, status, err := dialWS(t, srv, user, servicePath(w)+"/logs/live", origin)
			if !visibleTo(user, w) {
				if err == nil {
					_ = c.CloseNow()
					t.Errorf("%s %s: websocket accepted", user, w.name)
				} else if status != http.StatusNotFound {
					t.Errorf("%s %s: expected 404, got %v", user, w.name, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s %s: %v", user, w.name, err)
			}
			var msg struct {
				Lines []logs.Line `json:"lines"`
			}
			readJSON(t, c, &msg)
			if len(msg.Lines) != 1 || !strings.Contains(msg.Lines[0].Line, `team="`+w.team+`"`) {
				t.Errorf("%s %s: unexpected lines %+v", user, w.name, msg.Lines)
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		}
	}
}

func TestWebSocketRejectsForeignOrigin(t *testing.T) {
	srv, _ := wsFixture(t)
	for _, path := range []string{"/api/services/watch", servicePath(fixtureWorkloads[0]) + "/logs/live"} {
		c, status, err := dialWS(t, srv, "alice", path, "https://evil.example.com")
		if err == nil {
			_ = c.CloseNow()
			t.Fatalf("%s: cross-origin websocket accepted", path)
		}
		if status != http.StatusForbidden {
			t.Errorf("%s: expected 403, got %v", path, err)
		}
	}
}

func TestLiveTailLimitPerUser(t *testing.T) {
	srv, origin := wsFixture(t)
	path := servicePath(fixtureWorkloads[0]) + "/logs/live"
	var open []*websocket.Conn
	defer func() {
		for _, c := range open {
			_ = c.CloseNow()
		}
	}()
	for i := range 2 {
		c, _, err := dialWS(t, srv, "alice", path, origin)
		if err != nil {
			t.Fatalf("tail %d: %v", i, err)
		}
		open = append(open, c)
	}
	if c, status, err := dialWS(t, srv, "alice", path, origin); err == nil {
		_ = c.CloseNow()
		t.Fatal("third tail accepted")
	} else if status != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %v", err)
	}
	// Other users have their own budget.
	c, _, err := dialWS(t, srv, "admin", path, origin)
	if err != nil {
		t.Fatalf("admin tail: %v", err)
	}
	_ = c.CloseNow()
}

func TestWatchServicesIsolation(t *testing.T) {
	srv, origin := wsFixture(t)
	for _, user := range users {
		c, _, err := dialWS(t, srv, user, "/api/services/watch", origin)
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		_, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		got := decodeServices(t, data)
		slices.Sort(got)
		if want := expectedServices(user, ""); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", user, got, want)
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
}
