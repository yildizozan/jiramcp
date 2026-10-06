package mcpserver

import (
	"context"
	"fmt"
	"strings"
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

// requireMappedIssue applies, to the issue-key tools (get/update/comment/
// transition), the same project restriction that create enforces: the issue
// must live in a project reachable via the team mapping. The curated mapping is
// the authorization boundary for every issue operation, not merely routing for
// create.
//
// It returns the issue's current key, which callers must use for every further
// request. Checking only the key the caller passed is not enough: Jira resolves
// the old key of a moved issue to the issue in its new project, so "PAY-1" can
// name an issue that now lives in an unmapped project.
func (s *Server) requireMappedIssue(ctx context.Context, issueKey string) (string, error) {
	if err := s.checkMappedKey(issueKey); err != nil {
		return "", err
	}
	current, err := s.client.IssueKey(ctx, issueKey)
	if err != nil {
		return "", err
	}
	if err := s.checkMappedKey(current); err != nil {
		return "", fmt.Errorf("issue %s was moved to %s: %w", issueKey, current, err)
	}
	return current, nil
}

// checkMappedKey reports an error unless the key is well formed and its
// project is in the team mapping.
func (s *Server) checkMappedKey(issueKey string) error {
	proj := projectKeyFromIssueKey(issueKey)
	if proj == "" {
		return fmt.Errorf("invalid issue key %q; expected the form PROJECT-NUMBER (e.g. DOSD-1036)", issueKey)
	}
	if !s.teams.IsMappedProject(proj) {
		return fmt.Errorf("issue %s is in project %q, which is not in the team mapping", issueKey, proj)
	}
	return nil
}
