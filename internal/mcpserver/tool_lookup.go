package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

func searchUsersTool() mcp.Tool {
	return mcp.NewTool("search_users",
		mcp.WithDescription("Search Jira users by name or email. Returns `id`, the identifier to pass as reporter/assignee: an accountId on Cloud, a username on Server/Data Center."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Name or email fragment to search for.")),
	)
}

func (s *Server) handleSearchUsers(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("query is required"), nil
	}
	users, err := s.client.SearchUsers(ctx, query)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// `id` is the only identifier callers should pass back as reporter/assignee:
	// the Cloud accountId or the Server/DC username (via User.Ref()). The DC user
	// `key` is deliberately NOT exposed — it is not accepted in the reporter
	// `name` field and would silently file under the wrong identity.
	type userOut struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email,omitempty"`
		Active      bool   `json:"active"`
	}
	out := make([]userOut, 0, len(users))
	for _, u := range users {
		id := u.Ref()
		if id == "" {
			// Skip accounts we cannot reference (e.g. a Server/DC user with only an
			// opaque key and no username): surfacing an empty/unusable id would let
			// a caller file a ticket under the wrong identity.
			continue
		}
		out = append(out, userOut{
			ID:          id,
			DisplayName: u.DisplayName,
			Email:       u.Email,
			Active:      u.Active,
		})
	}
	return mcp.NewToolResultStructured(map[string]any{"users": out},
		fmt.Sprintf("found %d user(s) for %q", len(out), query)), nil
}

func listProjectsTool() mcp.Tool {
	return mcp.NewTool("list_projects",
		mcp.WithDescription("List Jira projects reachable by the service account, optionally filtered by query."),
		mcp.WithString("query", mcp.Description("Optional name/key filter.")),
	)
}

func (s *Server) handleListProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	projects, err := s.client.SearchProjects(ctx, req.GetString("query", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(map[string]any{"projects": projects},
		fmt.Sprintf("found %d project(s)", len(projects))), nil
}

func listIssueTypesTool() mcp.Tool {
	return mcp.NewTool("list_issue_types",
		mcp.WithDescription("List the issue types valid for a project (by key)."),
		mcp.WithString("project", mcp.Required(), mcp.Description("Project key.")),
	)
}

func (s *Server) handleListIssueTypes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	project, err := req.RequireString("project")
	if err != nil {
		return mcp.NewToolResultError("project is required"), nil
	}
	types, err := s.client.IssueTypes(ctx, strings.ToUpper(project))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultStructured(map[string]any{"issueTypes": types},
		fmt.Sprintf("found %d issue type(s) in %s", len(types), project)), nil
}
