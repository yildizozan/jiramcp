package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"jiramcp/internal/access"
)

// projectKeyFromIssueKey extracts the project portion of a Jira issue key
// ("DOSD-1036" -> "DOSD"). Issue keys are PROJECT-NUMBER, so the project is
// everything before the final '-'. Returns "" for a malformed key.
func projectKeyFromIssueKey(issueKey string) string {
	issueKey = strings.TrimSpace(issueKey)
	i := strings.LastIndex(issueKey, "-")
	if i <= 0 || i == len(issueKey)-1 {
		return ""
	}
	return issueKey[:i]
}

// requireAllowedIssue applies, to the issue-key tools (get/update/comment/
// transition) and to create's parent, the same project restriction that create
// enforces: the issue must live in a project the caller's policy allows. The
// policy is the authorization boundary for every issue operation, not merely
// routing for create.
//
// It returns the issue's current key, which callers must use for every further
// request. Checking only the key the caller passed is not enough: Jira resolves
// the old key of a moved issue to the issue in its new project, so "PAY-1" can
// name an issue that now lives in a project the caller may not use.
func (s *Server) requireAllowedIssue(ctx context.Context, issueKey string) (string, error) {
	p := s.principal(ctx)
	if err := checkAllowedKey(p.policy, issueKey); err != nil {
		return "", err
	}
	current, err := p.client.IssueKey(ctx, issueKey)
	if err != nil {
		return "", err
	}
	if err := checkAllowedKey(p.policy, current); err != nil {
		return "", fmt.Errorf("issue %s was moved to %s: %w", issueKey, current, err)
	}
	return current, nil
}

// checkAllowedKey reports an error unless the key is well formed and the
// policy allows its project.
func checkAllowedKey(policy access.Policy, issueKey string) error {
	proj := projectKeyFromIssueKey(issueKey)
	if proj == "" {
		return fmt.Errorf("invalid issue key %q; expected the form PROJECT-NUMBER (e.g. DOSD-1036)", issueKey)
	}
	if !policy.Allowed(proj) {
		return fmt.Errorf("issue %s is in project %q, which %s", issueKey, proj, notAllowed(policy))
	}
	return nil
}

// notAllowed explains a policy rejection and names the projects that are
// allowed, so the caller can correct the request.
func notAllowed(policy access.Policy) string {
	allowed := policy.Projects()
	if len(allowed) == 0 {
		return "is not allowed; no projects are allowed"
	}
	return "is not allowed; allowed projects: " + strings.Join(allowed, ", ")
}
