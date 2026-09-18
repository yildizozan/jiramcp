package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jiramcp/internal/jira"
)

func TestListComments_DefaultLimitAndPayload(t *testing.T) {
	f := &fakeClient{comments: []jira.Comment{
		{ID: "9001", Body: "first", Author: "Ozan"},
		{ID: "9002", Body: "second", Author: "Jane"},
	}}
	srv := newServer(f, testCfg())
	res, _ := srv.handleListComments(context.Background(), newReq(map[string]any{"key": "PAY-1"}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.listedLimit != defaultCommentLimit {
		t.Fatalf("expected the default limit %d, got %d", defaultCommentLimit, f.listedLimit)
	}
	if !strings.Contains(resultText(res), "found 2 comment(s) on PAY-1") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}

func TestListComments_ExplicitLimit(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleListComments(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "limit": 5,
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.listedLimit != 5 {
		t.Fatalf("limit not forwarded to the client, got %d", f.listedLimit)
	}
}

func TestListComments_UnmappedKeyBlocked(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg()) // only PAY is mapped
	res, _ := srv.handleListComments(context.Background(), newReq(map[string]any{"key": "HR-9"}))
	if !res.IsError {
		t.Fatal("expected unmapped issue key to be blocked for list_comments")
	}
	if f.listedLimit != 0 {
		t.Fatal("must not call ListComments for an unmapped project")
	}
}

func TestUpdateComment_ForwardsIDAndBody(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateComment(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "comment_id": "9001", "body": "corrected text",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.editedCommentID != "9001" || f.editedBody != "corrected text" {
		t.Fatalf("comment id/body not forwarded: %q / %q", f.editedCommentID, f.editedBody)
	}
	if !strings.Contains(resultText(res), "Updated comment 9001 on PAY-1") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}

func TestUpdateComment_MissingCommentID(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateComment(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "body": "corrected text",
	}))
	if !res.IsError {
		t.Fatal("expected an error when comment_id is missing")
	}
	if f.editedBody != "" {
		t.Fatal("must not edit anything without a comment id")
	}
}

func TestUpdateComment_UnmappedKeyBlocked(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateComment(context.Background(), newReq(map[string]any{
		"key": "HR-9", "comment_id": "9001", "body": "tamper",
	}))
	if !res.IsError {
		t.Fatal("expected unmapped issue key to be blocked for update_comment")
	}
	if f.editedCommentID != "" {
		t.Fatal("must not call UpdateComment for an unmapped project")
	}
}

// Jira owns the authorization decision (e.g. 'Edit All Comments'); its refusal
// must reach the caller as a tool error, not be swallowed.
func TestUpdateComment_ClientErrorSurfaces(t *testing.T) {
	f := &fakeClient{editErr: errors.New("update comment failed: permission denied (HTTP 403)")}
	srv := newServer(f, testCfg())
	res, _ := srv.handleUpdateComment(context.Background(), newReq(map[string]any{
		"key": "PAY-1", "comment_id": "9001", "body": "corrected text",
	}))
	if !res.IsError {
		t.Fatal("expected the Jira error to be surfaced")
	}
	if !strings.Contains(resultText(res), "permission denied") {
		t.Fatalf("unexpected message: %s", resultText(res))
	}
}
