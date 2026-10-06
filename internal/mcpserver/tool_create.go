package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"jiramcp/internal/access"
	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

// createTicketTool defines the primary tool schema.
func createTicketTool() mcp.Tool {
	return mcp.NewTool("create_jira_ticket",
		mcp.WithDescription(
			"Create a Jira ticket on behalf of a named person, routed to a team's project. "+
				"The service account creates the issue but its reporter is set to the named person "+
				"(requires the 'Modify Reporter' permission in the target project)."),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Issue summary/title."), mcp.MaxLength(255)),
		mcp.WithString("reporter",
			mcp.Description("The person to file on behalf of: a user id (accountId on Cloud, username on Server/DC), or an email/display name resolved to exactly one active user. "+
				"Omit to file as yourself when the server runs with your own Jira token; required when it runs as a shared service account.")),
		mcp.WithString("team", mcp.Description("Team name; resolved to a project via the configured mapping, if any. Omit to use project or the default.")),
		mcp.WithString("project", mcp.Description("Explicit Jira project key (overrides team). Must be an allowed project; see list_projects.")),
		mcp.WithString("issue_type", mcp.Description("Issue type name (e.g. Task, Bug). Defaults to the team or global default.")),
		mcp.WithString("description", mcp.Description("Plain-text description; converted to Atlassian Document Format."), mcp.MaxLength(32000)),
		mcp.WithString("assignee", mcp.Description("Optional assignee: accountId or email/name resolved to one active user.")),
		mcp.WithString("priority", mcp.Description("Optional priority name (e.g. High).")),
		mcp.WithArray("labels", mcp.WithStringItems(), mcp.Description("Optional labels (merged with team defaults).")),
		mcp.WithArray("components", mcp.WithStringItems(), mcp.Description("Optional component names (merged with team defaults).")),
		mcp.WithString("due_date", mcp.Description("Optional due date, YYYY-MM-DD."), mcp.Pattern(`^\d{4}-\d{2}-\d{2}$`)),
		mcp.WithString("parent", mcp.Description("Optional parent/epic key for sub-tasks or team-managed children. Must be in a mapped project.")),
	)
}

// createResult is the structured tool output.
type createResult struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	URL       string `json:"url"`
	Project   string `json:"project"`
	IssueType string `json:"issueType"`
	Reporter  string `json:"reporterId"`
}

func (s *Server) handleCreateTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p := s.principal(ctx)
	summary, err := req.RequireString("summary")
	if err != nil {
		return mcp.NewToolResultError("summary is required"), nil
	}
	reporterRef := strings.TrimSpace(req.GetString("reporter", ""))
	if reporterRef == "" && p.self == "" {
		return mcp.NewToolResultError("reporter is required: this server files tickets as a shared service account, " +
			"so it must be told on whose behalf"), nil
	}

	// 1. Resolve the target project and pick up team defaults.
	projectKey, teamCfg, err := s.resolveProject(p.policy, req.GetString("team", ""), req.GetString("project", ""))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 2. Resolve issue type name -> id.
	issueTypeName := firstNonEmpty(req.GetString("issue_type", ""), teamCfg.DefaultIssueType, s.cfg.DefaultIssueType)
	issueTypeID, err := resolveIssueTypeID(ctx, p.client, projectKey, issueTypeName)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 3. Resolve reporter (required) and assignee (optional) to the
	// dialect-appropriate identifier (accountId on Cloud, username on DC).
	// Filing as the caller needs no reporter field: Jira makes the creator the
	// reporter, and the Modify Reporter permission is not involved.
	reporterID := p.self
	if reporterRef != "" {
		reporterID, err = resolveUserID(ctx, p.client, reporterRef, s.dc())
		if err != nil {
			return mcp.NewToolResultError("reporter: " + err.Error()), nil
		}
	}
	setReporter := reporterID != p.self
	if setReporter && !p.onBehalf {
		return mcp.NewToolResultError("reporter: you may only file tickets as yourself; " +
			"filing on behalf of someone else needs the on-behalf group (OIDC_ON_BEHALF_GROUP)"), nil
	}
	var assigneeID string
	if ref := req.GetString("assignee", ""); ref != "" {
		assigneeID, err = resolveUserID(ctx, p.client, ref, s.dc())
		if err != nil {
			return mcp.NewToolResultError("assignee: " + err.Error()), nil
		}
	}

	// The parent is an issue key like any other, so it must also live in a
	// mapped project; otherwise create could attach the new ticket to an epic
	// or story elsewhere.
	var parentKey string
	if ref := strings.TrimSpace(req.GetString("parent", "")); ref != "" {
		parentKey, err = s.requireAllowedIssue(ctx, ref)
		if err != nil {
			return mcp.NewToolResultError("parent: " + err.Error()), nil
		}
	}

	// 4. Assemble the input, merging team defaults.
	in := jira.CreateIssueInput{
		ProjectKey:  projectKey,
		IssueTypeID: issueTypeID,
		Summary:     summary,
		Description: req.GetString("description", ""),
		AssigneeID:  assigneeID,
		Priority:    req.GetString("priority", ""),
		Labels:      mergeUnique(teamCfg.Labels, req.GetStringSlice("labels", nil)),
		Components:  mergeUnique(teamCfg.Components, req.GetStringSlice("components", nil)),
		DueDate:     req.GetString("due_date", ""),
		ParentKey:   parentKey,
		ExtraFields: teamCfg.Fields,
	}

	if setReporter {
		in.ReporterID = reporterID
	}

	// 5. Validate against the create screen metadata (best effort).
	if err := s.validateAgainstCreateMeta(ctx, in); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// 6. Create.
	issue, err := p.client.CreateIssue(ctx, in)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	s.logger.Info("ticket created",
		"key", issue.Key, "project", projectKey, "issueType", issueTypeName,
		"reporter", reporterID, "assignee", assigneeID)

	res := createResult{
		Key: issue.Key, ID: issue.ID, URL: issue.URL,
		Project: projectKey, IssueType: issueTypeName, Reporter: reporterID,
	}
	fallback := fmt.Sprintf("Created %s (%s) in %s for reporter %s: %s",
		issue.Key, issueTypeName, projectKey, reporterID, issue.URL)
	return mcp.NewToolResultStructured(res, fallback), nil
}

// resolveProject determines the project key from an explicit project or a team
// name, applying the caller's project policy and surfacing team defaults.
func (s *Server) resolveProject(policy access.Policy, team, project string) (string, config.TeamConfig, error) {
	if project != "" {
		key := strings.ToUpper(strings.TrimSpace(project))
		if !policy.Allowed(key) {
			return "", config.TeamConfig{}, fmt.Errorf("project %q %s", key, notAllowed(policy))
		}
		// Surface defaults from the team that maps to this project, if any.
		// Config load rejects duplicate project keys, so at most one team
		// matches and the chosen defaults are deterministic.
		for _, cfg := range s.teams.Teams {
			if strings.EqualFold(cfg.ProjectKey, key) {
				return key, cfg, nil
			}
		}
		return key, config.TeamConfig{}, nil
	}

	name := team
	if name == "" {
		name = s.teams.DefaultTeam
	}
	if name == "" {
		if s.cfg.DefaultProject != "" {
			return s.resolveProject(policy, "", s.cfg.DefaultProject)
		}
		hint := "see list_projects"
		if allowed := policy.Projects(); len(allowed) > 0 {
			hint = "allowed: " + strings.Join(allowed, ", ")
		}
		return "", config.TeamConfig{}, fmt.Errorf("no project given and no default configured; pass project (%s)", hint)
	}
	cfg, ok := s.teams.Lookup(name)
	if !ok {
		return "", config.TeamConfig{}, fmt.Errorf("unknown team %q; known teams: %s", name, strings.Join(s.teamNames(), ", "))
	}
	key := strings.ToUpper(cfg.ProjectKey)
	if !policy.Allowed(key) {
		return "", config.TeamConfig{}, fmt.Errorf("team %q routes to project %q, which %s", name, key, notAllowed(policy))
	}
	return key, cfg, nil
}

// validateAgainstCreateMeta fetches the create screen metadata and enforces
// two invariants: the reporter field must be settable (the core promise), and
// all required fields must be provided. If metadata cannot be fetched the
// check is skipped (best effort) and Jira's own validation applies on create.
func (s *Server) validateAgainstCreateMeta(ctx context.Context, in jira.CreateIssueInput) error {
	p := s.principal(ctx)
	meta, err := p.client.CreateMeta(ctx, in.ProjectKey, in.IssueTypeID)
	if err != nil {
		s.logger.Warn("create metadata unavailable; skipping pre-validation",
			"project", in.ProjectKey, "issueTypeID", in.IssueTypeID, "error", err.Error())
		return nil
	}

	if in.ReporterID != "" && !meta.Allowed("reporter") {
		return fmt.Errorf("the reporter field is not settable on the create screen for %s/%s; "+
			"the service account likely lacks the 'Modify Reporter' permission, so the ticket "+
			"cannot be filed on behalf of someone else", in.ProjectKey, in.IssueTypeID)
	}

	// Jira fills the reporter with the creator when the field is not sent, so
	// it never counts as missing.
	provided := map[string]struct{}{"project": {}, "issuetype": {}, "summary": {}, "reporter": {}}
	markProvided(provided, "description", in.Description != "")
	markProvided(provided, "assignee", in.AssigneeID != "")
	markProvided(provided, "priority", in.Priority != "")
	markProvided(provided, "labels", len(in.Labels) > 0)
	markProvided(provided, "components", len(in.Components) > 0)
	markProvided(provided, "duedate", in.DueDate != "")
	markProvided(provided, "parent", in.ParentKey != "")
	for id := range in.ExtraFields {
		provided[id] = struct{}{}
	}

	missing := meta.MissingRequired(provided)
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, f := range missing {
			names = append(names, fmt.Sprintf("%s (%s)", f.Name, f.FieldID))
		}
		sort.Strings(names)
		return fmt.Errorf("missing required field(s) for %s/%s: %s; "+
			"provide them or set team `fields` defaults in the mapping",
			in.ProjectKey, in.IssueTypeID, strings.Join(names, ", "))
	}
	return nil
}

func (s *Server) teamNames() []string {
	names := make([]string, 0, len(s.teams.Teams))
	for name := range s.teams.Teams {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func markProvided(set map[string]struct{}, key string, present bool) {
	if present {
		set[key] = struct{}{}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func mergeUnique(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if _, ok := seen[strings.ToLower(v)]; ok {
				continue
			}
			seen[strings.ToLower(v)] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}
