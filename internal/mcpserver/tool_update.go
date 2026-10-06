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
		mcp.WithDescription("Update fields on an existing Jira issue. Only the fields you pass are changed; labels/components replace the existing values (pass [] to remove all). "+
			"Use `clear` to empty assignee, due_date or description. "+
			"description REPLACES the whole description with plain text: on Jira Cloud any existing rich formatting (headings, tables, links, mentions) is lost."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Issue key, e.g. DOSD-1036.")),
		mcp.WithString("summary", mcp.Description("New summary/title."), mcp.MaxLength(255)),
		mcp.WithString("description", mcp.Description("New description (plain text; converted to ADF on Cloud)."), mcp.MaxLength(32000)),
		mcp.WithString("assignee", mcp.Description("New assignee: accountId/username or an email/name resolved to one active user.")),
		mcp.WithString("priority", mcp.Description("New priority name (e.g. High).")),
		mcp.WithArray("labels", mcp.WithStringItems(), mcp.Description("Replace labels with this list.")),
		mcp.WithArray("components", mcp.WithStringItems(), mcp.Description("Replace components with this list.")),
		mcp.WithString("due_date", mcp.Description("New due date, YYYY-MM-DD."), mcp.Pattern(`^\d{4}-\d{2}-\d{2}$`)),
		mcp.WithArray("clear", mcp.WithStringEnumItems(clearableFieldNames()),
			mcp.Description("Fields to empty: assignee (unassign), due_date, description. A field cannot be both set and cleared.")),
	)
}

// clearable lists the fields `clear` accepts: the tool's field name and the
// Jira field id it empties.
var clearable = []struct{ name, id string }{
	{"assignee", "assignee"},
	{"due_date", "duedate"},
	{"description", "description"},
}

// clearableFieldNames returns the tool field names `clear` accepts.
func clearableFieldNames() []string {
	names := make([]string, 0, len(clearable))
	for _, c := range clearable {
		names = append(names, c.name)
	}
	return names
}

// clearableID returns the Jira field id for a clearable tool field name.
func clearableID(name string) (string, bool) {
	for _, c := range clearable {
		if c.name == name {
			return c.id, true
		}
	}
	return "", false
}

// clearFields turns the `clear` argument into Jira field ids. It rejects an
// unknown name, and a field that the same call also sets, because the result
// would depend on which of the two Jira applies last.
func clearFields(names []string, set map[string]bool) ([]string, error) {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		id, ok := clearableID(name)
		if !ok {
			return nil, fmt.Errorf("clear: unknown field %q; allowed: %s", name, strings.Join(clearableFieldNames(), ", "))
		}
		if set[name] {
			return nil, fmt.Errorf("clear: %s is also being set; pass it in one of the two only", name)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Server) handleUpdateTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p := s.principal(ctx)
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	key, err = s.requireAllowedIssue(ctx, key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var assigneeID string
	if ref := req.GetString("assignee", ""); ref != "" {
		assigneeID, err = resolveUserID(ctx, p.client, ref, s.dc())
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
	in.Clear, err = clearFields(req.GetStringSlice("clear", nil), map[string]bool{
		"assignee":    in.AssigneeID != "",
		"due_date":    in.DueDate != "",
		"description": in.Description != "",
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if !in.HasChanges() {
		return mcp.NewToolResultError("no fields to update; pass at least one of summary/description/assignee/priority/labels/components/due_date/clear"), nil
	}

	if err := p.client.UpdateIssue(ctx, key, in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.logger.Info("ticket updated", "caller", p.auditCaller(), "key", key)
	url := p.client.BrowseURL(key)
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
	p := s.principal(ctx)
	key, err := req.RequireString("key")
	if err != nil {
		return mcp.NewToolResultError("key is required"), nil
	}
	key, err = s.requireAllowedIssue(ctx, key)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	available, err := p.client.Transitions(ctx, key)
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

	if err := p.client.TransitionIssue(ctx, key, match.ID, req.GetString("comment", "")); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	s.logger.Info("ticket transitioned", "caller", p.auditCaller(), "key", key, "transition", match.Name, "to", match.ToName)
	url := p.client.BrowseURL(key)
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
