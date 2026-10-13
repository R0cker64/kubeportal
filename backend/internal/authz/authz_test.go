package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeTeams map[string][]string

func (f fakeTeams) TeamsForGroups(_ context.Context, groups []string) ([]string, error) {
	var out []string
	for _, g := range groups {
		out = append(out, f[g]...)
	}
	return out, nil
}

var mapping = fakeTeams{
	"team-payments": {"payments"},
	"team-catalog":  {"catalog"},
	"both":          {"payments", "catalog"},
	"broken":        {`payments"}`, "Payments", ""},
}

func resolve(t *testing.T, groups ...string) Principal {
	t.Helper()
	p, err := NewResolver(mapping, "kubeportal-admin").Resolve(t.Context(), "sub", "user", groups)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolve(t *testing.T) {
	cases := []struct {
		groups []string
		teams  []string
		admin  bool
	}{
		{nil, nil, false},
		{[]string{"unmapped"}, nil, false},
		{[]string{"team-payments"}, []string{"payments"}, false},
		{[]string{"both", "team-payments"}, []string{"catalog", "payments"}, false},
		{[]string{"kubeportal-admin"}, nil, true},
		// Invalid team names coming from the store never reach a query.
		{[]string{"broken"}, nil, false},
	}
	for _, c := range cases {
		p := resolve(t, c.groups...)
		if !slices.Equal(p.Teams, c.teams) || p.Admin != c.admin {
			t.Errorf("groups %v: teams=%v admin=%v, want %v %v", c.groups, p.Teams, p.Admin, c.teams, c.admin)
		}
	}
}

func TestResolveStoreError(t *testing.T) {
	_, err := NewResolver(errStore{}, "admin").Resolve(t.Context(), "s", "u", []string{"g"})
	if err == nil {
		t.Fatal("expected error")
	}
}

type errStore struct{}

func (errStore) TeamsForGroups(context.Context, []string) ([]string, error) {
	return nil, errors.New("db down")
}

func TestDenyByDefault(t *testing.T) {
	nobody := resolve(t)
	if nobody.HasAccess() {
		t.Fatal("a user without teams must have no access")
	}
	for _, team := range []string{"", "payments", "catalog"} {
		if nobody.CanSeeTeam(team) {
			t.Errorf("user without teams can see team %q", team)
		}
	}

	alice := resolve(t, "team-payments")
	if !alice.CanSeeWorkload(map[string]string{TeamLabel: "payments"}) {
		t.Error("alice cannot see her own workload")
	}
	for _, labels := range []map[string]string{
		{TeamLabel: "catalog"},
		{},
		{TeamLabel: ""},
		{"app": "payments"},
	} {
		if alice.CanSeeWorkload(labels) {
			t.Errorf("alice can see workload with labels %v", labels)
		}
	}

	admin := resolve(t, "kubeportal-admin")
	if !admin.CanSeeWorkload(map[string]string{}) || !admin.CanSeeTeam("catalog") {
		t.Error("admin must see every workload, including unlabelled ones")
	}
}

func TestLogSelector(t *testing.T) {
	alice := resolve(t, "team-payments")
	sel, err := alice.LogSelector(Target{Namespace: "prod", App: "payments-api", Team: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{namespace="prod", app="payments-api", team="payments"}`
	if sel.String() != want {
		t.Fatalf("got %s, want %s", sel, want)
	}

	admin := resolve(t, "kubeportal-admin")
	sel, err = admin.LogSelector(Target{Namespace: "dev", App: "legacy-batch"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sel.String(), `team=""`) {
		t.Fatalf("unlabelled workload must select streams without team: %s", sel)
	}
}

func TestSelectorsRejectOtherTeamsAndInjection(t *testing.T) {
	alice := resolve(t, "team-payments")
	bad := []Target{
		{Namespace: "prod", App: "catalog-api", Team: "catalog"},
		{Namespace: "prod", App: "legacy-batch", Team: ""},
		{Namespace: "prod", App: `x", team=~".+`, Team: "payments"},
		{Namespace: `prod"}`, App: "a", Team: "payments"},
		{Namespace: "prod", App: "", Team: "payments"},
		{Namespace: "Prod", App: "a", Team: "payments"},
		{Namespace: "prod", App: "a", Team: `payments", team="catalog`},
	}
	for _, target := range bad {
		if _, err := alice.LogSelector(target); !errors.Is(err, ErrInvalidParam) {
			t.Errorf("LogSelector(%+v) err = %v", target, err)
		}
		if _, err := alice.PodSelector(target); !errors.Is(err, ErrInvalidParam) {
			t.Errorf("PodSelector(%+v) err = %v", target, err)
		}
	}
}

func TestPodSelector(t *testing.T) {
	bob := resolve(t, "team-catalog")
	sel, err := bob.PodSelector(Target{Namespace: "staging", App: "catalog-api", Team: "catalog"})
	if err != nil {
		t.Fatal(err)
	}
	want := `kube_pod_labels{namespace="staging", label_app_kubernetes_io_name="catalog-api", label_team="catalog"}`
	if sel.KubePodLabels() != want || sel.Namespace() != "staging" {
		t.Fatalf("got %s (%s)", sel.KubePodLabels(), sel.Namespace())
	}
}

func TestValidators(t *testing.T) {
	envs := []string{"dev", "prod"}
	if !ValidEnvironment("dev", envs) || ValidEnvironment("staging", envs) || ValidEnvironment("", envs) {
		t.Error("ValidEnvironment")
	}
	if !ValidName("payments-api") || ValidName("a_b") || ValidName(strings.Repeat("a", 64)) || ValidName("-a") {
		t.Error("ValidName")
	}
	if !ValidSearchText(`timeout "x" \ y`, 64) || ValidSearchText("a\nb", 64) ||
		ValidSearchText(strings.Repeat("a", 65), 64) || ValidSearchText("\xff", 64) {
		t.Error("ValidSearchText")
	}

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	if _, err := NewTimeRange(now.Add(-time.Hour), now, now, week); err != nil {
		t.Errorf("valid range rejected: %v", err)
	}
	for _, r := range [][2]time.Time{
		{now, now.Add(-time.Hour)},
		{now.Add(-8 * 24 * time.Hour), now},
		{now, now.Add(time.Hour)},
	} {
		if _, err := NewTimeRange(r[0], r[1], now, week); err == nil {
			t.Errorf("range %v accepted", r)
		}
	}
}
