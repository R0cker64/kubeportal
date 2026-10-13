package authz

import (
	"errors"
	"slices"
)

// ErrForbidden is returned when a non-administrator requests an admin scope.
var ErrForbidden = errors.New("authz: forbidden")

// AdminScope proves that the caller is an administrator. Admin-only queries
// (nodes, team management, unlabelled workloads) require one. It can only be
// obtained through Principal.AdminScope.
type AdminScope struct {
	granted bool
}

// Granted reports whether the scope was issued by Principal.AdminScope.
func (s AdminScope) Granted() bool { return s.granted }

// AdminScope returns an AdminScope for administrators and ErrForbidden for
// everybody else.
func (p Principal) AdminScope() (AdminScope, error) {
	if !p.Admin {
		return AdminScope{}, ErrForbidden
	}
	return AdminScope{granted: true}, nil
}

// Limits on team definitions managed by administrators.
const (
	MaxTeamDisplayName = 200
	MaxGroupName       = 255
	MaxGroupsPerTeam   = 50
)

// TeamInput is a team definition submitted by an administrator.
type TeamInput struct {
	Name        string
	DisplayName string
	Groups      []string
}

// NormalizeTeam validates a team definition and returns it with groups
// sorted and deduplicated. The name must be a valid `team` label value.
func NormalizeTeam(in TeamInput) (TeamInput, error) {
	if !ValidName(in.Name) || !validText(in.DisplayName, MaxTeamDisplayName) {
		return TeamInput{}, ErrInvalidParam
	}
	groups := slices.Clone(in.Groups)
	for _, g := range groups {
		if !validText(g, MaxGroupName) {
			return TeamInput{}, ErrInvalidParam
		}
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	if len(groups) > MaxGroupsPerTeam {
		return TeamInput{}, ErrInvalidParam
	}
	if groups == nil {
		groups = []string{}
	}
	return TeamInput{Name: in.Name, DisplayName: in.DisplayName, Groups: groups}, nil
}

// validText accepts non-empty, printable UTF-8 without leading or trailing
// spaces, up to maxLen bytes.
func validText(s string, maxLen int) bool {
	if s == "" || s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	return ValidSearchText(s, maxLen)
}
