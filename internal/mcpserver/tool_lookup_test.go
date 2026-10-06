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
