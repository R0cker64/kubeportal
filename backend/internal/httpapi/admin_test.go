package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/R0cker64/kubeportal/backend/internal/store"
)

// send issues a request as user with the CSRF header and a JSON body.
func (f *fixture) send(t *testing.T, user, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, target, r)
	if user != "" {
		req.AddCookie(sessionCookieFor(user))
	}
	req.Header.Set(csrfHeader, csrfValue)
	req.Header.Set("Origin", "http://localhost:8080")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

var adminRequests = []struct{ method, target, body string }{
	{http.MethodGet, "/api/admin/teams", ""},
	{http.MethodGet, "/api/admin/nodes", ""},
	{http.MethodGet, "/api/admin/workloads/unassigned", ""},
	{http.MethodPost, "/api/admin/teams", `{"name":"search","displayName":"Search","groups":["team-search"]}`},
	{http.MethodPut, "/api/admin/teams/payments", `{"displayName":"P","groups":["team-catalog"]}`},
	{http.MethodDelete, "/api/admin/teams/payments", ""},
}

func TestAdminEndpointsRequireAdmin(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	before, _ := f.teams.ListTeams(t.Context())
	for _, req := range adminRequests {
		if rec := f.send(t, "", req.method, req.target, req.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s: %d", req.method, req.target, rec.Code)
		}
		for _, user := range []string{"alice", "bob", "carol", "dave"} {
			rec := f.send(t, user, req.method, req.target, req.body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: %d", user, req.method, req.target, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "node-a") || strings.Contains(rec.Body.String(), "team-") {
				t.Errorf("%s %s %s leaked data: %s", user, req.method, req.target, rec.Body)
			}
		}
	}
	after, _ := f.teams.ListTeams(t.Context())
	if !slices.EqualFunc(before, after, func(a, b store.Team) bool {
		return a.Name == b.Name && slices.Equal(a.Groups, b.Groups)
	}) {
		t.Fatalf("non-admins changed teams: %v -> %v", before, after)
	}
}

func TestAdminMutationsRequireCSRFHeader(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/admin/teams",
		strings.NewReader(`{"name":"search","displayName":"Search","groups":[]}`))
	req.AddCookie(sessionCookieFor("admin"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF header: %d", rec.Code)
	}
	if teams, _ := f.teams.TeamsForGroups(t.Context(), []string{"team-search"}); len(teams) != 0 {
		t.Fatal("team created without CSRF header")
	}
}

// TestAdminCreatesTeamAndMembersSeeApps is the Phase 3 exit criterion: an
// administrator creates a team and its members see their apps without any
// change in the cluster.
func TestAdminCreatesTeamAndMembersSeeApps(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	dave := func() []string { return decodeServices(t, f.get(t, "dave", "/api/services").Body.Bytes()) }
	searchAPI := "/api/services/staging/deployment/search-api"

	if got := dave(); len(got) != 0 {
		t.Fatalf("dave sees %v before the team exists", got)
	}
	if rec := f.get(t, "dave", searchAPI+"/logs"); rec.Code != http.StatusNotFound {
		t.Fatalf("dave reads logs before the team exists: %d", rec.Code)
	}

	rec := f.send(t, "admin", http.MethodPost, "/api/admin/teams",
		`{"name":"search","displayName":"Search","groups":["team-search","team-search"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team: %d %s", rec.Code, rec.Body)
	}
	var created teamResponse
	_ = json.NewDecoder(rec.Body).Decode(&created)
	if created.Workloads != 1 || !slices.Equal(created.Groups, []string{"team-search"}) {
		t.Errorf("created = %+v", created)
	}

	if got := dave(); !slices.Equal(got, []string{"staging/deployment/search-api"}) {
		t.Fatalf("dave sees %v after the team was created", got)
	}
	for _, suffix := range []string{"", "/logs", "/metrics"} {
		if rec := f.get(t, "dave", searchAPI+suffix); rec.Code != http.StatusOK {
			t.Errorf("dave %s: %d", suffix, rec.Code)
		}
	}
	// Other teams are unaffected.
	if got := decodeServices(t, f.get(t, "alice", "/api/services").Body.Bytes()); slices.Contains(got, "staging/deployment/search-api") {
		t.Fatal("alice sees the new team's workload")
	}

	// Removing the group revokes access immediately.
	if rec := f.send(t, "admin", http.MethodPut, "/api/admin/teams/search",
		`{"displayName":"Search","groups":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("update team: %d %s", rec.Code, rec.Body)
	}
	if got := dave(); len(got) != 0 {
		t.Fatalf("dave still sees %v after losing the group", got)
	}

	if rec := f.send(t, "admin", http.MethodDelete, "/api/admin/teams/search", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete team: %d", rec.Code)
	}
	if rec := f.send(t, "admin", http.MethodDelete, "/api/admin/teams/search", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing team: %d", rec.Code)
	}
}

func TestAdminTeamValidation(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	cases := []struct {
		method, target, body string
		want                 int
	}{
		{http.MethodPost, "/api/admin/teams", `{"name":"Search","displayName":"x","groups":[]}`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `{"name":"x\"}","displayName":"x","groups":[]}`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `{"name":"x","displayName":"","groups":[]}`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `{"name":"x","displayName":"x","groups":["a\nb"]}`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `{"name":"x","displayName":"x","groups":[],"admin":true}`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `{"name":"x","displayName":"x","groups":[]} {}`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `not json`, http.StatusBadRequest},
		{http.MethodPost, "/api/admin/teams", `{"name":"payments","displayName":"x","groups":[]}`, http.StatusConflict},
		{http.MethodPut, "/api/admin/teams/payments", `{"name":"catalog","displayName":"x","groups":[]}`, http.StatusBadRequest},
		{http.MethodPut, "/api/admin/teams/missing", `{"displayName":"x","groups":[]}`, http.StatusNotFound},
		{http.MethodDelete, "/api/admin/teams/Bad_Name", "", http.StatusNotFound},
		{http.MethodPost, "/api/admin/teams", `{"name":"ok","displayName":"Ok","groups":["g"]}`, http.StatusCreated},
	}
	for _, c := range cases {
		if rec := f.send(t, "admin", c.method, c.target, c.body); rec.Code != c.want {
			t.Errorf("%s %s %q: %d, want %d", c.method, c.target, c.body, rec.Code, c.want)
		}
	}

	// A JSON body must be declared as such.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/admin/teams",
		strings.NewReader(`{"name":"plain","displayName":"x","groups":[]}`))
	req.AddCookie(sessionCookieFor("admin"))
	req.Header.Set(csrfHeader, csrfValue)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("text/plain body: %d", rec.Code)
	}
}

func TestAdminNodes(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	var resp struct {
		Nodes []struct {
			Name  string `json:"name"`
			Ready bool   `json:"ready"`
			Usage *struct {
				CPU  float64 `json:"cpu"`
				Pods int     `json:"pods"`
			} `json:"usage"`
		} `json:"nodes"`
		UsageAvailable bool `json:"usageAvailable"`
	}
	if err := json.NewDecoder(f.get(t, "admin", "/api/admin/nodes").Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.UsageAvailable || len(resp.Nodes) != 1 || resp.Nodes[0].Name != "node-a" || !resp.Nodes[0].Ready ||
		resp.Nodes[0].Usage == nil || resp.Nodes[0].Usage.Pods != 3 {
		t.Fatalf("nodes = %+v", resp)
	}
}

func TestAdminUnassignedWorkloads(t *testing.T) {
	f := newFixture(t, "http://localhost:8080")
	read := func() (unlabelled, unknown []string) {
		var resp struct {
			Unlabelled  []serviceSummary `json:"unlabelled"`
			UnknownTeam []serviceSummary `json:"unknownTeam"`
		}
		if err := json.NewDecoder(f.get(t, "admin", "/api/admin/workloads/unassigned").Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		for _, s := range resp.Unlabelled {
			unlabelled = append(unlabelled, s.Name)
		}
		for _, s := range resp.UnknownTeam {
			unknown = append(unknown, s.Name)
		}
		return unlabelled, unknown
	}
	unlabelled, unknown := read()
	if !slices.Equal(unlabelled, []string{"legacy-batch"}) || !slices.Equal(unknown, []string{"search-api"}) {
		t.Fatalf("unlabelled=%v unknown=%v", unlabelled, unknown)
	}
	f.send(t, "admin", http.MethodPost, "/api/admin/teams", `{"name":"search","displayName":"Search","groups":[]}`)
	if _, unknown := read(); len(unknown) != 0 {
		t.Fatalf("registered team still listed as unknown: %v", unknown)
	}
}
