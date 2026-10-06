package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

// fakeClient is a programmable jira.Client for tests.
type fakeClient struct {
	users      []jira.User
	projects   []jira.Project
	issueTypes []jira.IssueType
	meta       *jira.CreateMeta
	metaErr    error

	created   *jira.CreateIssueInput
	createErr error

	moved  map[string]string // old issue key -> current key, as Jira resolves it
	keyErr error

	issue  *jira.Issue
	gotKey string
	getErr error

	updatedKey  string
	updated     *jira.UpdateIssueInput
	commentBody string

	comments        []jira.Comment
	listedLimit     int
	listErr         error
	editedCommentID string
	editedBody      string
	editErr         error

	transitions []jira.Transition
	appliedID   string
}

func (f *fakeClient) Myself(context.Context) (*jira.User, error) {
	return &jira.User{AccountID: "svc", DisplayName: "Service", Active: true}, nil
}
func (f *fakeClient) SearchUsers(context.Context, string) ([]jira.User, error) {
	return f.users, nil
}
func (f *fakeClient) SearchProjects(context.Context, string) ([]jira.Project, error) {
	return f.projects, nil
}
func (f *fakeClient) IssueTypes(context.Context, string) ([]jira.IssueType, error) {
	return f.issueTypes, nil
}
func (f *fakeClient) CreateMeta(context.Context, string, string) (*jira.CreateMeta, error) {
	return f.meta, f.metaErr
}
func (f *fakeClient) CreateIssue(_ context.Context, in jira.CreateIssueInput) (*jira.CreatedIssue, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = &in
	return &jira.CreatedIssue{ID: "1000", Key: in.ProjectKey + "-123", URL: "https://jira.yildizozan.com/browse/" + in.ProjectKey + "-123"}, nil
}
func (f *fakeClient) IssueKey(_ context.Context, key string) (string, error) {
	if f.keyErr != nil {
		return "", f.keyErr
	}
	if current, ok := f.moved[key]; ok {
		return current, nil
	}
	return key, nil
}
func (f *fakeClient) GetIssue(_ context.Context, key string) (*jira.Issue, error) {
	f.gotKey = key
	return f.issue, f.getErr
}
func (f *fakeClient) UpdateIssue(_ context.Context, key string, in jira.UpdateIssueInput) error {
	f.updatedKey = key
	f.updated = &in
	return nil
}
func (f *fakeClient) AddComment(_ context.Context, key, body string) (*jira.Comment, error) {
	f.commentBody = body
	return &jira.Comment{ID: "9001", URL: f.BrowseURL(key)}, nil
}
func (f *fakeClient) ListComments(_ context.Context, _ string, limit int) ([]jira.Comment, error) {
	f.listedLimit = limit
	return f.comments, f.listErr
}
func (f *fakeClient) UpdateComment(_ context.Context, key, commentID, body string) (*jira.Comment, error) {
	if f.editErr != nil {
		return nil, f.editErr
	}
	f.editedCommentID = commentID
	f.editedBody = body
	return &jira.Comment{ID: commentID, Body: body, URL: f.BrowseURL(key)}, nil
}
func (f *fakeClient) Transitions(context.Context, string) ([]jira.Transition, error) {
	return f.transitions, nil
}
func (f *fakeClient) TransitionIssue(_ context.Context, _, transitionID, _ string) error {
	f.appliedID = transitionID
	return nil
}
func (f *fakeClient) BrowseURL(key string) string { return "https://jira.yildizozan.com/browse/" + key }

func testCfg() *config.Config {
	return &config.Config{
		DefaultIssueType:     "Task",
		AllowUnauthenticated: true,
		Transport:            config.TransportStdio,
		Teams: &config.TeamMapping{
			Teams:       map[string]config.TeamConfig{"payments": {ProjectKey: "PAY", Labels: []string{"from-mcp"}}},
			DefaultTeam: "payments",
		},
	}
}

func metaWith(fields ...string) *jira.CreateMeta {
	m := &jira.CreateMeta{Fields: map[string]jira.FieldMeta{}}
	for _, f := range fields {
		req := f == "summary" || f == "project" || f == "issuetype"
		m.Fields[f] = jira.FieldMeta{FieldID: f, Name: f, Required: req}
	}
	return m
}

func newReq(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "create_jira_ticket", Arguments: args}}
}

func resultText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func newServer(f jira.Client, cfg *config.Config) *Server {
	return New(cfg, f, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

func TestCreate_HappyPath(t *testing.T) {
	f := &fakeClient{
		users:      []jira.User{{AccountID: "acc-1", DisplayName: "Alice", Email: "alice@yildizozan.com", Active: true}},
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		meta:       metaWith("summary", "project", "issuetype", "reporter", "description", "labels"),
	}
	srv := newServer(f, testCfg())

	res, err := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary":     "Fix login",
		"reporter":    "alice@yildizozan.com",
		"team":        "payments",
		"description": "It is broken",
	}))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(res))
	}
	if f.created == nil {
		t.Fatal("CreateIssue was not called")
	}
	if f.created.ProjectKey != "PAY" || f.created.IssueTypeID != "10001" {
		t.Fatalf("wrong routing: %+v", f.created)
	}
	if f.created.ReporterID != "acc-1" {
		t.Fatalf("reporter not resolved to id: %q", f.created.ReporterID)
	}
	if f.created.Description != "It is broken" {
		t.Fatalf("description not passed through: %q", f.created.Description)
	}
	// Team default label merged in.
	if len(f.created.Labels) != 1 || f.created.Labels[0] != "from-mcp" {
		t.Fatalf("team default label not merged: %v", f.created.Labels)
	}
	if res.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
}

// On DC the createmeta endpoint is unreliable; when CreateMeta errors the
// create must still proceed (validation fails open), never block.
func TestCreate_CreateMetaErrorFailsOpen(t *testing.T) {
	f := &fakeClient{
		users:      []jira.User{{Name: "ozan.yildiz", Active: true}},
		issueTypes: []jira.IssueType{{ID: "10202", Name: "Task"}},
		metaErr:    errors.New("Issue Does Not Exist"),
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "reporter": "ozan.yildiz", "team": "payments",
	}))
	if res.IsError {
		t.Fatalf("create should succeed when createmeta is unavailable: %s", resultText(res))
	}
	if f.created == nil {
		t.Fatal("CreateIssue should have been called despite createmeta error")
	}
	if f.created.ReporterID != "ozan.yildiz" {
		t.Fatalf("reporter id passthrough: %q", f.created.ReporterID)
	}
}

func TestCreate_ReporterNotSettable(t *testing.T) {
	f := &fakeClient{
		users:      []jira.User{{AccountID: "acc-1", Active: true}},
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		meta:       metaWith("summary", "project", "issuetype"), // no reporter
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "reporter": "acc-1", "team": "payments",
	}))
	if !res.IsError {
		t.Fatal("expected error when reporter field not settable")
	}
	if !strings.Contains(resultText(res), "Modify Reporter") {
		t.Fatalf("error should mention Modify Reporter: %s", resultText(res))
	}
	if f.created != nil {
		t.Fatal("must not create when reporter cannot be set")
	}
}

func TestCreate_AmbiguousReporter(t *testing.T) {
	f := &fakeClient{
		users: []jira.User{
			{AccountID: "a1", DisplayName: "Alice A", Active: true},
			{AccountID: "a2", DisplayName: "Alice B", Active: true},
		},
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		meta:       metaWith("summary", "project", "issuetype", "reporter"),
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "reporter": "Alice Smith", "team": "payments",
	}))
	if !res.IsError {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(resultText(res), "ambiguous") {
		t.Fatalf("expected ambiguous message: %s", resultText(res))
	}
}

func TestCreate_UnmappedProjectBlocked(t *testing.T) {
	f := &fakeClient{
		users:      []jira.User{{AccountID: "acc-1", Active: true}},
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		meta:       metaWith("summary", "project", "issuetype", "reporter"),
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "reporter": "acc-1", "project": "ZZZ",
	}))
	if !res.IsError {
		t.Fatal("expected unmapped project to be blocked")
	}
	if !strings.Contains(resultText(res), "not in the team mapping") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}

func TestCreate_MissingRequiredCustomField(t *testing.T) {
	f := &fakeClient{
		users:      []jira.User{{AccountID: "acc-1", Active: true}},
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		meta: &jira.CreateMeta{Fields: map[string]jira.FieldMeta{
			"summary":           {FieldID: "summary", Name: "Summary", Required: true},
			"project":           {FieldID: "project", Name: "Project", Required: true},
			"issuetype":         {FieldID: "issuetype", Name: "Issue Type", Required: true},
			"reporter":          {FieldID: "reporter", Name: "Reporter", Required: false},
			"customfield_10010": {FieldID: "customfield_10010", Name: "Team", Required: true},
		}},
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "reporter": "acc-1", "team": "payments",
	}))
	if !res.IsError {
		t.Fatal("expected missing-required-field error")
	}
	if !strings.Contains(resultText(res), "customfield_10010") {
		t.Fatalf("should name the missing field: %s", resultText(res))
	}
}

// H2: a search that resolves to a single active Server/DC account with only an
// opaque key (no username) must fail closed, never file under a blank reporter.
func TestCreate_ReporterWithNoUsableIdentity(t *testing.T) {
	f := &fakeClient{
		users:      []jira.User{{DisplayName: "Ghost", Key: "JIRAUSER41400", Active: true}}, // no AccountID, no Name
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		metaErr:    errors.New("skip"), // fail open; irrelevant to this path
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "reporter": "Ghost Account", "team": "payments", // space => searched, not passed through
	}))
	if !res.IsError {
		t.Fatal("expected error when the matched user has no usable identifier")
	}
	if !strings.Contains(resultText(res), "no usable identifier") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
	if f.created != nil {
		t.Fatal("must not create a ticket when the reporter has no usable id")
	}
}

func createWithParent(t *testing.T, f *fakeClient, parent string) *mcp.CallToolResult {
	t.Helper()
	f.users = []jira.User{{AccountID: "acc-1", Active: true}}
	f.issueTypes = []jira.IssueType{{ID: "10001", Name: "Task"}}
	f.metaErr = errors.New("skip pre-validation")
	res, _ := newServer(f, testCfg()).handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "child", "reporter": "acc-1", "team": "payments", "parent": parent,
	}))
	return res
}

func TestCreate_ParentInUnmappedProjectBlocked(t *testing.T) {
	f := &fakeClient{}
	res := createWithParent(t, f, "HR-5")
	if !res.IsError || !strings.Contains(resultText(res), "parent:") {
		t.Fatalf("expected a parent mapping error, got: %s", resultText(res))
	}
	if f.created != nil {
		t.Fatal("must not create a ticket under a parent in an unmapped project")
	}
}

func TestCreate_ParentMovedOutOfMappingBlocked(t *testing.T) {
	f := &fakeClient{moved: map[string]string{"PAY-5": "HR-5"}}
	res := createWithParent(t, f, "PAY-5")
	if !res.IsError {
		t.Fatal("expected a parent moved to an unmapped project to be blocked")
	}
	if f.created != nil {
		t.Fatal("must not create a ticket under a moved parent")
	}
}

func TestCreate_ParentUsesCurrentKey(t *testing.T) {
	f := &fakeClient{moved: map[string]string{"PAY-5": "PAY-50"}}
	res := createWithParent(t, f, "PAY-5")
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.created == nil || f.created.ParentKey != "PAY-50" {
		t.Fatalf("parent should be the current key PAY-50, got %+v", f.created)
	}
}
