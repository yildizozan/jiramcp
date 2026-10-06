// Package jira is a thin, typed client for the Jira REST API. It targets Jira
// Cloud (REST API v3, ADF, accountId identities) and Jira Server/Data Center
// (REST API v2, plain-text descriptions, username/key identities), selecting
// the dialect from the auth mode. It is intentionally dependency free (net/http
// only) and hidden behind the Client interface so it can be mocked in tests and
// swapped without touching the MCP layer.
package jira

import "context"

// User is a Jira account. On Cloud it is identified by AccountID; on
// Server/Data Center by Name (username) and Key, where AccountID is empty.
type User struct {
	AccountID   string `json:"accountId"`
	Name        string `json:"name"` // DC username
	Key         string `json:"key"`  // DC user key
	DisplayName string `json:"displayName"`
	Email       string `json:"emailAddress,omitempty"`
	Active      bool   `json:"active"`
}

// Ref returns the identifier used to reference this user when setting an issue
// field: the Cloud accountId when present, otherwise the Server/DC username.
//
// The opaque DC user Key (e.g. "JIRAUSER41400") is deliberately NOT used as a
// fallback: it is not valid in the reporter/assignee `name` field, so sending it
// would mis-reference the account or be rejected by Jira. When both AccountID and
// Name are empty Ref returns "", which callers must treat as an unusable identity
// (fail closed) rather than file a ticket under the wrong or no account.
func (u User) Ref() string {
	if u.AccountID != "" {
		return u.AccountID
	}
	return u.Name
}

// Project is a Jira project.
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// IssueType is a project issue type.
type IssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

// FieldMeta describes a single field on the create screen for a project +
// issue type, as returned by the createmeta endpoint.
type FieldMeta struct {
	FieldID  string `json:"fieldId"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// CreateMeta is the set of settable fields for a project + issue type.
type CreateMeta struct {
	Fields map[string]FieldMeta
}

// Allowed reports whether a field id can be set on the create screen.
func (m *CreateMeta) Allowed(fieldID string) bool {
	if m == nil {
		return false
	}
	_, ok := m.Fields[fieldID]
	return ok
}

// MissingRequired returns the ids of required fields that are not present in
// the provided set of fields we intend to send.
func (m *CreateMeta) MissingRequired(provided map[string]struct{}) []FieldMeta {
	if m == nil {
		return nil
	}
	var missing []FieldMeta
	for id, f := range m.Fields {
		if !f.Required {
			continue
		}
		if _, ok := provided[id]; !ok {
			missing = append(missing, f)
		}
	}
	return missing
}

// CreateIssueInput is the normalized input for creating an issue. Identity
// fields are pre-resolved by the caller to the dialect-appropriate reference
// (Cloud accountId or DC username), and the client wraps them accordingly.
type CreateIssueInput struct {
	ProjectKey  string
	IssueTypeID string
	Summary     string
	Description string // plain text; rendered to ADF on Cloud, raw on DC. Empty to omit.
	ReporterID  string // accountId (Cloud) or username (DC); empty to omit
	AssigneeID  string // accountId (Cloud) or username (DC); empty to omit
	Priority    string // priority name, empty to omit
	Labels      []string
	Components  []string // component names
	DueDate     string   // YYYY-MM-DD, empty to omit
	ParentKey   string   // empty to omit
	ExtraFields map[string]any
}

// CreatedIssue is the result of a successful create.
type CreatedIssue struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	URL string `json:"-"` // browse URL, derived from base URL
}

// UpdateIssueInput is the set of fields to change on an existing issue. Only
// non-empty (for slices, non-nil) fields are sent; the rest are left unchanged.
// Identity and rich-text fields follow the active dialect, like CreateIssue.
type UpdateIssueInput struct {
	Summary     string   // empty to leave unchanged
	Description string   // empty to leave unchanged
	AssigneeID  string   // accountId (Cloud) or username (DC); empty to leave unchanged
	Priority    string   // priority name; empty to leave unchanged
	Labels      []string // nil to leave unchanged; otherwise REPLACES the labels
	Components  []string // nil to leave unchanged; otherwise REPLACES the components
	DueDate     string   // YYYY-MM-DD; empty to leave unchanged
	ExtraFields map[string]any
}

// HasChanges reports whether any field is set.
func (in UpdateIssueInput) HasChanges() bool {
	return in.Summary != "" || in.Description != "" || in.AssigneeID != "" ||
		in.Priority != "" || in.Labels != nil || in.Components != nil ||
		in.DueDate != "" || len(in.ExtraFields) > 0
}

// Comment is a comment on an issue. Body is always plain text: the Cloud v3
// API returns an ADF tree, which the client flattens, while Server/DC returns
// the raw string. Author, Created and Updated are only populated when reading
// comments; a create/edit response fills whatever Jira echoes back.
type Comment struct {
	ID      string `json:"id"`
	Body    string `json:"body"`
	Author  string `json:"author"`  // display name
	Created string `json:"created"` // Jira timestamp, as returned
	Updated string `json:"updated"` // Jira timestamp, as returned
	URL     string `json:"-"`       // browse URL of the parent issue
}

// Issue is a read view of an existing issue. Description is plain text, like
// Comment.Body: the Cloud ADF tree is flattened, the Server/DC string is kept.
// People are reported by display name.
type Issue struct {
	Key         string   `json:"key"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	IssueType   string   `json:"issueType"`
	Priority    string   `json:"priority,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	Reporter    string   `json:"reporter,omitempty"`
	Labels      []string `json:"labels"`
	Components  []string `json:"components"`
	DueDate     string   `json:"dueDate,omitempty"`
	Parent      string   `json:"parent,omitempty"` // parent issue key
	Created     string   `json:"created"`          // Jira timestamp, as returned
	Updated     string   `json:"updated"`          // Jira timestamp, as returned
	URL         string   `json:"url"`
}

// Transition is an available workflow transition for an issue.
type Transition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	ToName string `json:"toStatus"` // name of the status this transition moves to
}

// Client is the subset of Jira used by the server.
type Client interface {
	// Myself returns the authenticated account; used as a credential check.
	Myself(ctx context.Context) (*User, error)
	// SearchUsers finds users by name/email query (Cloud user search).
	SearchUsers(ctx context.Context, query string) ([]User, error)
	// SearchProjects lists projects, optionally filtered by query.
	SearchProjects(ctx context.Context, query string) ([]Project, error)
	// IssueTypes returns the issue types valid for a project.
	IssueTypes(ctx context.Context, projectKey string) ([]IssueType, error)
	// CreateMeta returns the settable fields for a project + issue type.
	CreateMeta(ctx context.Context, projectKey, issueTypeID string) (*CreateMeta, error)
	// CreateIssue creates an issue and returns its key/id.
	CreateIssue(ctx context.Context, in CreateIssueInput) (*CreatedIssue, error)
	// GetIssue reads the main fields of an existing issue.
	GetIssue(ctx context.Context, key string) (*Issue, error)
	// UpdateIssue edits fields on an existing issue.
	UpdateIssue(ctx context.Context, key string, in UpdateIssueInput) error
	// AddComment appends a comment to an existing issue.
	AddComment(ctx context.Context, key, body string) (*Comment, error)
	// ListComments returns the most recent comments on an issue, oldest first,
	// capped at limit.
	ListComments(ctx context.Context, key string, limit int) ([]Comment, error)
	// UpdateComment replaces the body of an existing comment.
	UpdateComment(ctx context.Context, key, commentID, body string) (*Comment, error)
	// Transitions lists the workflow transitions currently available for an issue.
	Transitions(ctx context.Context, key string) ([]Transition, error)
	// TransitionIssue applies a transition, optionally with a comment.
	TransitionIssue(ctx context.Context, key, transitionID, comment string) error
	// BrowseURL returns the human-facing URL for an issue key.
	BrowseURL(key string) string
}
