package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"jiramcp/internal/jira"
)

// UserResolutionError is returned when a person query cannot be resolved to a
// single active Atlassian account. It deliberately fails closed: ambiguous or
// missing users never silently pick a candidate.
type UserResolutionError struct {
	Query      string
	Candidates []jira.User // empty => not found; >1 => ambiguous
}

func (e *UserResolutionError) Error() string {
	if len(e.Candidates) == 0 {
		return fmt.Sprintf("no active Jira user matches %q; pass an exact user id (accountId on Cloud, username on Server/DC) or a more specific email", e.Query)
	}
	names := make([]string, 0, len(e.Candidates))
	for _, u := range e.Candidates {
		label := u.DisplayName
		if u.Email != "" {
			label += " <" + u.Email + ">"
		}
		names = append(names, fmt.Sprintf("%s (id=%s)", label, u.Ref()))
	}
	return fmt.Sprintf("%q is ambiguous, %d users match: %s — pass an exact user id",
		e.Query, len(e.Candidates), strings.Join(names, "; "))
}

// looksLikeUserID reports whether the input should be treated as a ready-made
// user identifier (passed through) rather than an email/name to search for.
// Both Cloud accountIds and Server/DC usernames contain no '@' and no spaces;
// emails contain '@' and display names usually contain a space.
func looksLikeUserID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, "@ ")
}

// resolveUserID maps a person reference (id, email, or name) to the
// dialect-appropriate identifier (accountId on Cloud, username on Server/DC).
// id-looking inputs are trusted and passed through; anything else is searched
// and must resolve to exactly one active user.
func resolveUserID(ctx context.Context, client jira.Client, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("empty user reference")
	}
	if looksLikeUserID(input) {
		return input, nil
	}

	users, err := client.SearchUsers(ctx, input)
	if err != nil {
		return "", err
	}
	active := make([]jira.User, 0, len(users))
	for _, u := range users {
		if u.Active {
			active = append(active, u)
		}
	}
	switch len(active) {
	case 1:
		ref := active[0].Ref()
		if ref == "" {
			// Fail closed: the matched account has no usable identifier for this
			// dialect (e.g. a Server/DC user with only an opaque key and no
			// username), so we must not silently file under the wrong/no account.
			return "", fmt.Errorf("user %q matched an account with no usable identifier "+
				"(its Server/DC username is empty); pass an exact user id instead", input)
		}
		return ref, nil
	default:
		return "", &UserResolutionError{Query: input, Candidates: active}
	}
}

// resolveIssueTypeID maps an issue type name (case-insensitive) to its id for
// the given project.
func resolveIssueTypeID(ctx context.Context, client jira.Client, projectKey, name string) (string, error) {
	types, err := client.IssueTypes(ctx, projectKey)
	if err != nil {
		return "", err
	}
	for _, t := range types {
		if strings.EqualFold(t.Name, name) {
			return t.ID, nil
		}
	}
	available := make([]string, 0, len(types))
	for _, t := range types {
		available = append(available, t.Name)
	}
	return "", fmt.Errorf("issue type %q not found in project %s; available: %s",
		name, projectKey, strings.Join(available, ", "))
}
