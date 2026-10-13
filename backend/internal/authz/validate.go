package authz

import (
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidParam is returned for client parameters outside the allowlists.
var ErrInvalidParam = errors.New("authz: invalid parameter")

var (
	// dnsLabelRe is an RFC 1123 label: namespaces, workload names, teams.
	dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// labelValueRe is a Kubernetes label value.
	labelValueRe = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
)

// ValidName reports whether s is an RFC 1123 label of at most 63 characters.
func ValidName(s string) bool {
	return len(s) <= 63 && dnsLabelRe.MatchString(s)
}

// validLabelValue reports whether s is a non-empty Kubernetes label value.
func validLabelValue(s string) bool {
	return len(s) <= 63 && labelValueRe.MatchString(s)
}

// ValidEnvironment reports whether env is one of the configured environments.
func ValidEnvironment(env string, environments []string) bool {
	return ValidName(env) && slices.Contains(environments, env)
}

// ValidSearchText reports whether a free-text search is acceptable: valid
// UTF-8, no control characters and at most maxLen bytes. The text is still
// escaped as a literal when it is placed in a query.
func ValidSearchText(s string, maxLen int) bool {
	if len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// TimeRange is a validated, closed time window.
type TimeRange struct {
	Start, End time.Time
}

// NewTimeRange validates start < end, end not in the future beyond a small
// skew, and end - start <= maxRange.
func NewTimeRange(start, end, now time.Time, maxRange time.Duration) (TimeRange, error) {
	const skew = time.Minute
	switch {
	case !start.Before(end):
		return TimeRange{}, ErrInvalidParam
	case end.After(now.Add(skew)):
		return TimeRange{}, ErrInvalidParam
	case end.Sub(start) > maxRange:
		return TimeRange{}, ErrInvalidParam
	}
	return TimeRange{Start: start, End: end}, nil
}
