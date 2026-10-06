package mcpserver

import (
	"context"
	"strings"
	"testing"

	"jiramcp/internal/jira"
)

func TestUpdate_FieldsAndAssignee(t *testing.T) {
	f := &fakeClient{users: []jira.User{{Name: "ozan.yildiz", Active: true}}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateTicket(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "summary": "new title", "assignee": "ozan.yildiz",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.updatedKey != "PAY-1" || f.updated == nil {
		t.Fatalf("update not recorded: key=%q in=%+v", f.updatedKey, f.updated)
	}
	if f.updated.Summary != "new title" || f.updated.AssigneeID != "ozan.yildiz" {
		t.Fatalf("wrong update payload: %+v", f.updated)
	}
}

func TestUpdate_NoFieldsRejected(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateTicket(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
	if !res.IsError {
		t.Fatal("expected error when no fields are provided")
	}
	if f.updated != nil {
		t.Fatal("must not call UpdateIssue with no changes")
	}
}

func TestAddComment(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleAddComment(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "body": "looks good",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.commentBody != "looks good" {
		t.Fatalf("comment body not passed: %q", f.commentBody)
	}
}

func TestTransition_ListsWhenNoTarget(t *testing.T) {
	f := &fakeClient{transitions: []jira.Transition{{ID: "11", Name: "Start Progress", ToName: "In Progress"}}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleTransition(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
	if res.IsError {
		t.Fatalf("listing should not error: %s", resultText(res))
	}
	if f.appliedID != "" {
		t.Fatal("must not apply a transition in list mode")
	}
	if !strings.Contains(resultText(res), "In Progress") {
		t.Fatalf("listing should mention the target status: %s", resultText(res))
	}
}

func TestTransition_AppliesByStatusName(t *testing.T) {
	f := &fakeClient{transitions: []jira.Transition{{ID: "11", Name: "Start Progress", ToName: "In Progress"}}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleTransition(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "to": "in progress",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.appliedID != "11" {
		t.Fatalf("should apply transition id 11, got %q", f.appliedID)
	}
}

func TestTransition_NoMatch(t *testing.T) {
	f := &fakeClient{transitions: []jira.Transition{{ID: "11", Name: "Start Progress", ToName: "In Progress"}}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleTransition(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "to": "Done",
	}))
	if !res.IsError {
		t.Fatal("expected error for unmatched transition")
	}
	if f.appliedID != "" {
		t.Fatal("must not apply when no transition matches")
	}
}

// H1: update/comment/transition must honor the team-mapping boundary, not just
// create. An issue key whose project is unmapped is rejected (fail closed).
func TestUpdate_UnmappedKeyBlocked(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg()) // only PAY is mapped
	res, _ := srv.handleUpdateTicket(context.Background(), newReq(map[string]any{
		"key": "HR-9", "summary": "tamper",
	}))
	if !res.IsError {
		t.Fatal("expected unmapped issue key to be blocked")
	}
	if !strings.Contains(resultText(res), "is not allowed; allowed projects: PAY") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
	if f.updated != nil {
		t.Fatal("must not call UpdateIssue for an unmapped project")
	}
}

func TestComment_UnmappedKeyBlocked(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleAddComment(context.Background(), newReq(map[string]any{
		"key": "HR-9", "body": "leak",
	}))
	if !res.IsError {
		t.Fatal("expected unmapped issue key to be blocked for add_comment")
	}
	if f.commentBody != "" {
		t.Fatal("must not call AddComment for an unmapped project")
	}
}

func TestTransition_UnmappedKeyBlocked(t *testing.T) {
	f := &fakeClient{transitions: []jira.Transition{{ID: "11", Name: "Start Progress", ToName: "In Progress"}}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleTransition(context.Background(), newReq(map[string]any{
		"key": "HR-9", "to": "in progress",
	}))
	if !res.IsError {
		t.Fatal("expected unmapped issue key to be blocked for transition")
	}
	if f.appliedID != "" {
		t.Fatal("must not apply a transition for an unmapped project")
	}
}

func TestTransition_MalformedKeyRejected(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleTransition(context.Background(), newReq(map[string]any{"key": "NOTAKEY"}))
	if !res.IsError {
		t.Fatal("expected a malformed issue key to be rejected")
	}
	if !strings.Contains(resultText(res), "invalid issue key") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}

// H2: search_users must not surface a Server/DC account that has only an opaque
// key and no username — its Ref() is empty and cannot be used as reporter id.
func TestSearchUsers_SkipsUnreferenceableUsers(t *testing.T) {
	f := &fakeClient{users: []jira.User{
		{Name: "ozan.yildiz", DisplayName: "Ozan", Active: true},
		{Key: "JIRAUSER41400", DisplayName: "Ghost", Active: true}, // no AccountID, no Name
	}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleSearchUsers(context.Background(), newReq(map[string]any{"query": "o"}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "found 1 user") {
		t.Fatalf("key-only user should be filtered out, got: %s", resultText(res))
	}
}

func TestUpdate_ClearFields(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateTicket(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "clear": []any{"assignee", "due_date"},
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.updated == nil || strings.Join(f.updated.Clear, ",") != "assignee,duedate" {
		t.Fatalf("clear should map to Jira ids [assignee duedate], got %+v", f.updated)
	}
}

func TestUpdate_ClearRejectsUnknownAndConflicting(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown field":   {"key": "PAY-1", "clear": []any{"summary"}},
		"set and cleared": {"key": "PAY-1", "assignee": "ozan.yildiz", "clear": []any{"assignee"}},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeClient{users: []jira.User{{Name: "ozan.yildiz", Active: true}}}
			res, _ := newServer(f, testCfg()).handleUpdateTicket(context.Background(), newReq(args))
			if !res.IsError {
				t.Fatal("expected an error")
			}
			if f.updated != nil {
				t.Fatal("must not call UpdateIssue")
			}
		})
	}
}
