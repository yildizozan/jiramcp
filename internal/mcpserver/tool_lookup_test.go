package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jiramcp/internal/jira"
)

func TestGetTicket_ReturnsIssue(t *testing.T) {
	f := &fakeClient{issue: &jira.Issue{
		Key: "PAY-1", Summary: "Card declined", Status: "In Progress",
		URL: "https://jira.yildizozan.com/browse/PAY-1",
	}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleGetTicket(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.gotKey != "PAY-1" {
		t.Fatalf("key not forwarded to the client, got %q", f.gotKey)
	}
	want := "PAY-1 [In Progress] Card declined: https://jira.yildizozan.com/browse/PAY-1"
	if resultText(res) != want {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}

func TestGetTicket_UnmappedKeyBlocked(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg()) // only PAY is mapped
	res, _ := srv.handleGetTicket(context.Background(), newReq(map[string]any{"key": "HR-9"}))
	if !res.IsError {
		t.Fatal("expected unmapped issue key to be blocked for get_jira_ticket")
	}
	if f.gotKey != "" {
		t.Fatal("must not call GetIssue for an unmapped project")
	}
}

func TestGetTicket_ClientErrorSurfaces(t *testing.T) {
	f := &fakeClient{getErr: errors.New("get issue failed: issue does not exist (HTTP 404)")}
	srv := newServer(f, testCfg())
	res, _ := srv.handleGetTicket(context.Background(), newReq(map[string]any{"key": "PAY-404"}))
	if !res.IsError {
		t.Fatal("expected the Jira error to be surfaced")
	}
	if !strings.Contains(resultText(res), "does not exist") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}

// A moved issue keeps answering to its old key, so "PAY-1" can name an issue
// that now lives in an unmapped project. Every issue-key tool must check the
// issue's current key, not only the key the caller passed.
func TestIssueTools_MovedToUnmappedProjectBlocked(t *testing.T) {
	cases := map[string]struct {
		call func(*Server, *fakeClient) (bool, bool) // (isError, reachedJira)
	}{
		"get_jira_ticket": {func(s *Server, f *fakeClient) (bool, bool) {
			res, _ := s.handleGetTicket(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
			return res.IsError, f.gotKey != ""
		}},
		"update_jira_ticket": {func(s *Server, f *fakeClient) (bool, bool) {
			res, _ := s.handleUpdateTicket(context.Background(), newReq(map[string]any{"key": "PAY-1", "summary": "x"}))
			return res.IsError, f.updated != nil
		}},
		"add_comment": {func(s *Server, f *fakeClient) (bool, bool) {
			res, _ := s.handleAddComment(context.Background(), newReq(map[string]any{"key": "PAY-1", "body": "x"}))
			return res.IsError, f.commentBody != ""
		}},
		"list_comments": {func(s *Server, f *fakeClient) (bool, bool) {
			res, _ := s.handleListComments(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
			return res.IsError, f.listedLimit != 0
		}},
		"update_comment": {func(s *Server, f *fakeClient) (bool, bool) {
			res, _ := s.handleUpdateComment(context.Background(), newReq(map[string]any{"key": "PAY-1", "comment_id": "9", "body": "x"}))
			return res.IsError, f.editedCommentID != ""
		}},
		"transition_jira_ticket": {func(s *Server, f *fakeClient) (bool, bool) {
			res, _ := s.handleTransition(context.Background(), newReq(map[string]any{"key": "PAY-1", "to": "Done"}))
			return res.IsError, f.appliedID != ""
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeClient{
				moved:       map[string]string{"PAY-1": "HR-7"},
				transitions: []jira.Transition{{ID: "31", Name: "Done", ToName: "Done"}},
			}
			isErr, reached := tc.call(newServer(f, testCfg()), f)
			if !isErr {
				t.Fatal("expected an issue moved to an unmapped project to be blocked")
			}
			if reached {
				t.Fatal("must not act on an issue that now lives in an unmapped project")
			}
		})
	}
}

func TestGetTicket_MovedWithinMappingUsesCurrentKey(t *testing.T) {
	f := &fakeClient{
		moved: map[string]string{"PAY-1": "PAY-42"},
		issue: &jira.Issue{Key: "PAY-42", Summary: "s", Status: "Open"},
	}
	srv := newServer(f, testCfg())
	res, _ := srv.handleGetTicket(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.gotKey != "PAY-42" {
		t.Fatalf("GetIssue should use the current key PAY-42, got %q", f.gotKey)
	}
}

func TestGetTicket_IssueKeyErrorSurfaces(t *testing.T) {
	f := &fakeClient{keyErr: errors.New("get issue key failed: issue does not exist (HTTP 404)")}
	srv := newServer(f, testCfg())
	res, _ := srv.handleGetTicket(context.Background(), newReq(map[string]any{"key": "PAY-404"}))
	if !res.IsError || !strings.Contains(resultText(res), "does not exist") {
		t.Fatalf("expected the Jira error to be surfaced, got: %s", resultText(res))
	}
	if f.gotKey != "" {
		t.Fatal("must not call GetIssue when the key check fails")
	}
}

func TestListProjects_OnlyMappedProjects(t *testing.T) {
	f := &fakeClient{projects: []jira.Project{
		{Key: "PAY", Name: "Payments"}, {Key: "HR", Name: "Human Resources"},
	}}
	res, _ := newServer(f, testCfg()).handleListProjects(context.Background(), newReq(map[string]any{}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if strings.Contains(resultText(res), "found 2") || !strings.Contains(resultText(res), "found 1") {
		t.Fatalf("expected only the mapped project, got: %s", resultText(res))
	}
}

func TestListIssueTypes_UnmappedProjectBlocked(t *testing.T) {
	f := &fakeClient{issueTypes: []jira.IssueType{{ID: "1", Name: "Task"}}}
	srv := newServer(f, testCfg())

	res, _ := srv.handleListIssueTypes(context.Background(), newReq(map[string]any{"project": "HR"}))
	if !res.IsError {
		t.Fatal("expected list_issue_types to reject an unmapped project")
	}
	res, _ = srv.handleListIssueTypes(context.Background(), newReq(map[string]any{"project": " pay "}))
	if res.IsError {
		t.Fatalf("mapped project should be accepted case-insensitively: %s", resultText(res))
	}
}
