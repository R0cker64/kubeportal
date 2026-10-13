package authz

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestAdminScope(t *testing.T) {
	if _, err := resolve(t, "team-payments").AdminScope(); !errors.Is(err, ErrForbidden) {
		t.Fatalf("team member got an admin scope: %v", err)
	}
	if _, err := resolve(t).AdminScope(); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user without teams got an admin scope: %v", err)
	}
	s, err := resolve(t, "kubeportal-admin").AdminScope()
	if err != nil || !s.Granted() {
		t.Fatalf("admin scope = %+v, %v", s, err)
	}
	if (AdminScope{}).Granted() {
		t.Fatal("zero AdminScope must not be granted")
	}
}

func TestNormalizeTeam(t *testing.T) {
	got, err := NormalizeTeam(TeamInput{Name: "search", DisplayName: "Búsqueda", Groups: []string{"b", "a", "b"}})
	if err != nil || !slices.Equal(got.Groups, []string{"a", "b"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, _ := NormalizeTeam(TeamInput{Name: "x", DisplayName: "X"}); got.Groups == nil {
		t.Fatal("groups must be an empty slice, not nil")
	}

	many := make([]string, MaxGroupsPerTeam+1)
	for i := range many {
		many[i] = strings.Repeat("g", i+1)
	}
	bad := []TeamInput{
		{Name: "Search", DisplayName: "x"},
		{Name: `x"}`, DisplayName: "x"},
		{Name: "", DisplayName: "x"},
		{Name: "x", DisplayName: ""},
		{Name: "x", DisplayName: " padded"},
		{Name: "x", DisplayName: strings.Repeat("a", MaxTeamDisplayName+1)},
		{Name: "x", DisplayName: "x", Groups: []string{""}},
		{Name: "x", DisplayName: "x", Groups: []string{"a\nb"}},
		{Name: "x", DisplayName: "x", Groups: []string{strings.Repeat("g", MaxGroupName+1)}},
		{Name: "x", DisplayName: "x", Groups: many},
	}
	for _, in := range bad {
		if _, err := NormalizeTeam(in); !errors.Is(err, ErrInvalidParam) {
			t.Errorf("NormalizeTeam(%.60q) err = %v", in, err)
		}
	}
}
