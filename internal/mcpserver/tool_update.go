package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"jiramcp/internal/jira"
)

// updateTicketTool edits fields on an existing issue.
func updateTicketTool() mcp.Tool {
	return mcp.NewTool("update_jira_ticket",
		mcp.WithDescription("Update fields on an existing Jira issue. Only the fields you pass are changed; labels/components replace the existing values."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Issue key, e.g. DOSD-1036.")),
		mcp.WithString("summary", mcp.Description("New summary/title."), mcp.MaxLength(255)),
		mcp.WithString("description", mcp.Description("New description (plain text; converted to ADF on Cloud)."), mcp.MaxLength(32000)),
		mcp.WithString("assignee", mcp.Description("New assignee: accountId/username or an email/name resolved to one active user.")),
		mcp.WithString("priority", mcp.Description("New priority name (e.g. High).")),
		mcp.WithArray("labels", mcp.WithStringItems(), mcp.Description("Replace labels with this list.")),
		mcp.WithArray("components", mcp.WithStringItems(), mcp.Description("Replace components with this list.")),
		mcp.WithString("due_date", mcp.Description("New due date, YYYY-MM-DD."), mcp.Pattern(`^\d{4}-\d{2}-\d{2}$`)),
	)
}

func (s *Server) handleUpdateTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	if err := s.requireMappedIssue(key); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var assigneeID string
	if ref := req.GetString("assignee", ""); ref != "" {
		assigneeID, err = resolveUserID(ctx, s.client, ref)
		if err != nil {
			return mcp.NewToolResultError("assignee: " + err.Error()), nil
		}
	}

	in := jira.UpdateIssueInput{
		Summary:     req.GetString("summary", ""),
		Description: req.GetString("description", ""),
		AssigneeID:  assigneeID,
		Priority:    req.GetString("priority", ""),
		Labels:      req.GetStringSlice("labels", nil),
		Components:  req.GetStringSlice("components", nil),
		DueDate:     req.GetString("due_date", ""),
	}
	if !in.HasChanges() {
		return mcp.NewToolResultError("no fields to update; pass at least one of summary/description/assignee/priority/labels/components/due_date"), nil
	}

	if err := s.client.UpdateIssue(ctx, key, in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.logger.Info("ticket updated", "key", key)
	url := s.client.BrowseURL(key)
	return mcp.NewToolResultStructured(
		map[string]any{"key": key, "url": url},
		fmt.Sprintf("Updated %s: %s", key, url)), nil
}

// transitionTool lists or applies a workflow transition.
func transitionTool() mcp.Tool {
	return mcp.NewTool("transition_jira_ticket",
		mcp.WithDescription("Move an issue through its workflow. Omit `to` to list the transitions currently available; pass `to` (a transition name or target status) to apply one."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Issue key, e.g. DOSD-1036.")),
		mcp.WithString("to", mcp.Description("Transition name (e.g. \"Start Progress\") or target status (e.g. \"In Progress\"). Omit to just list available transitions.")),
		mcp.WithString("comment", mcp.Description("Optional comment to add as part of the transition.")),
	)
}

func (s *Server) handleTransition(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	if err := s.requireMappedIssue(key); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	available, err := s.client.Transitions(ctx, key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	to := strings.TrimSpace(req.GetString("to", ""))
	if to == "" {
		// Discovery mode: list what is possible.
		return mcp.NewToolResultStructured(
			map[string]any{"key": key, "transitions": available},
			fmt.Sprintf("%s available transitions: %s", key, describeTransitions(available))), nil
	}

	var match *jira.Transition
	for i := range available {
		if strings.EqualFold(available[i].Name, to) || strings.EqualFold(available[i].ToName, to) {
			match = &available[i]
			break
		}
	}
	if match == nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"no transition matches %q for %s; available: %s", to, key, describeTransitions(available))), nil
	}

	if err := s.client.TransitionIssue(ctx, key, match.ID, req.GetString("comment", "")); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.logger.Info("ticket transitioned", "key", key, "transition", match.Name, "to", match.ToName)
	url := s.client.BrowseURL(key)
	return mcp.NewToolResultStructured(
		map[string]any{"key": key, "transition": match.Name, "status": match.ToName, "url": url},
		fmt.Sprintf("Transitioned %s via %q to %s: %s", key, match.Name, match.ToName, url)), nil
}

// describeTransitions renders transitions as "Name -> Status" pairs for the
// human-readable tool text.
func describeTransitions(ts []jira.Transition) string {
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		names = append(names, fmt.Sprintf("%s -> %s", t.Name, t.ToName))
	}
	return strings.Join(names, "; ")
}
