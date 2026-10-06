// Package access decides which Jira projects a caller may use. The policy is
// the authorization boundary for every issue operation; Jira's own permissions
// apply on top of it.
package access

import (
	"sort"
	"strings"
)

// Policy reports which projects are allowed.
type Policy interface {
	// Allowed reports whether the project key (case-insensitive) may be used.
	Allowed(projectKey string) bool
	// Projects returns the allowed keys, sorted, or nil when the policy does
	// not restrict projects (Jira's permissions alone decide).
	Projects() []string
}

// AllowAll returns a policy that allows every project.
func AllowAll() Policy { return allowAll{} }

type allowAll struct{}

func (allowAll) Allowed(string) bool { return true }
func (allowAll) Projects() []string  { return nil }

// Projects returns a policy that allows only the given project keys. Keys are
// matched case-insensitively; blank keys are ignored. With no keys it allows
// nothing (fail closed).
func Projects(keys ...string) Policy {
	set := make(projectSet, len(keys))
	for _, k := range keys {
		if k = normalize(k); k != "" {
			set[k] = struct{}{}
		}
	}
	return set
}

type projectSet map[string]struct{}

func (s projectSet) Allowed(key string) bool {
	_, ok := s[normalize(key)]
	return ok
}

func (s projectSet) Projects() []string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Intersect returns a policy that allows a project only when both a and b
// allow it.
func Intersect(a, b Policy) Policy {
	ka, kb := a.Projects(), b.Projects()
	switch {
	case ka == nil:
		return b
	case kb == nil:
		return a
	}
	keep := make([]string, 0, len(ka))
	for _, k := range ka {
		if b.Allowed(k) {
			keep = append(keep, k)
		}
	}
	return Projects(keep...)
}

func normalize(key string) string { return strings.ToUpper(strings.TrimSpace(key)) }
