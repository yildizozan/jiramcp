package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"jiramcp/internal/access"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 50
)

// orderByClause finds an ORDER BY in caller JQL. The query is wrapped in
// parentheses to scope it to the allowed projects, and ORDER BY cannot appear
// inside them, so ordering has its own parameter.
var orderByClause = regexp.MustCompile(`(?i)\border\s+by\b`)

// searchTicketsTool searches issues with JQL.
func searchTicketsTool() mcp.Tool {
	return mcp.NewTool("search_jira_tickets",
		mcp.WithDescription("Search Jira issues with JQL. The search is limited to the projects you may use. Returns key, summary, status, type, priority, assignee and last update; use get_jira_ticket for the full issue. Pass the returned nextCursor as cursor to get the next page."),
		mcp.WithString("jql", mcp.Required(), mcp.Description(`JQL filter without ORDER BY, e.g. assignee = currentUser() AND status != Done.`)),
		mcp.WithString("orderBy", mcp.Description("Optional ORDER BY clause body, e.g. updated DESC.")),
		mcp.WithNumber("limit", mcp.Description("Maximum issues per page (default 20, max 50)."), mcp.Min(1), mcp.Max(maxSearchLimit)),
		mcp.WithString("cursor", mcp.Description("nextCursor from a previous call, to get the next page.")),
	)
}

// searchHit is one issue in the search result: the fields needed to pick an
// issue from a list.
type searchHit struct {
	Key       string `json:"key"`
	Summary   string `json:"summary"`
	Status    string `json:"status"`
	IssueType string `json:"issueType"`
	Priority  string `json:"priority,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	Updated   string `json:"updated"`
	URL       string `json:"url"`
}

func (s *Server) handleSearchTickets(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p := s.principal(ctx)
	jql, err := req.RequireString("jql")
	if err != nil {
		return mcp.NewToolResultError("jql is required"), nil
	}
	query, err := scopedJQL(p.policy, jql, req.GetString("orderBy", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	startAt, err := parseCursor(req.GetString("cursor", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := min(max(req.GetInt("limit", defaultSearchLimit), 1), maxSearchLimit)

	page, err := p.client.SearchIssues(ctx, query, startAt, limit)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// scopedJQL keeps the query inside the allowed projects. Filtering the
	// results again guards against any JQL that still escapes that scope.
	hits := make([]searchHit, 0, len(page.Issues))
	dropped := 0
	for _, is := range page.Issues {
		if checkAllowedKey(p.policy, is.Key) != nil {
			dropped++
			continue
		}
		hits = append(hits, searchHit{
			Key: is.Key, Summary: is.Summary, Status: is.Status, IssueType: is.IssueType,
			Priority: is.Priority, Assignee: is.Assignee, Updated: is.Updated, URL: is.URL,
		})
	}
	if dropped > 0 {
		s.logger.Warn("search returned issues outside the caller's projects; dropped",
			"caller", p.auditCaller(), "dropped", dropped)
	}

	out := map[string]any{"issues": hits}
	text := fmt.Sprintf("found %d issue(s)", len(hits))
	if page.NextStartAt > 0 {
		cursor := strconv.Itoa(page.NextStartAt)
		out["nextCursor"] = cursor
		text += "; more available, pass cursor " + cursor
	}
	return mcp.NewToolResultStructured(out, text), nil
}

// scopedJQL builds the query sent to Jira: the caller's filter limited to the
// projects the policy allows, then the optional ordering.
func scopedJQL(policy access.Policy, jql, orderBy string) (string, error) {
	jql = strings.TrimSpace(jql)
	if jql == "" {
		return "", errors.New("jql is required")
	}
	if orderByClause.MatchString(jql) {
		return "", errors.New("jql must not contain ORDER BY; pass the ordering as orderBy")
	}
	if !balancedParens(jql) {
		return "", errors.New("jql has unbalanced parentheses")
	}
	// nil means the policy does not restrict projects; an empty list means it
	// allows none, which notAllowed cannot tell apart.
	switch projects := policy.Projects(); {
	case projects == nil:
	case len(projects) == 0:
		return "", errors.New("no projects are allowed, so there is nothing to search")
	default:
		quoted := make([]string, len(projects))
		for i, k := range projects {
			quoted[i] = strconv.Quote(k)
		}
		jql = "project in (" + strings.Join(quoted, ", ") + ") AND (" + jql + ")"
	}
	if orderBy = strings.TrimSpace(orderBy); orderBy != "" {
		jql += " ORDER BY " + orderBy
	}
	return jql, nil
}

// balancedParens reports whether every parenthesis outside a quoted string
// closes one opened before it. Balanced JQL cannot close the parentheses that
// scope it to the allowed projects; without this, "x) OR (project = HR" would
// widen the query, and even with the results filtered, nextCursor would reveal
// whether hidden issues match.
func balancedParens(jql string) bool {
	depth := 0
	var quote rune
	escaped := false
	for _, r := range jql {
		switch {
		case escaped:
			escaped = false
		case quote != 0:
			if r == '\\' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && quote == 0
}

// parseCursor turns a nextCursor back into the result offset.
func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cursor %q; pass nextCursor from a previous search", cursor)
	}
	return n, nil
}
