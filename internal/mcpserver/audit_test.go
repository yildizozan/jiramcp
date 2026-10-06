package mcpserver

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"jiramcp/internal/access"
	"jiramcp/internal/jira"
)

// Every change is logged with the caller who made it, so the log answers
// "who did this" even where Jira shows only the service account.
func TestAudit_MutationsNameTheCaller(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeClient{transitions: []jira.Transition{{ID: "31", Name: "Done", ToName: "Done"}}}
	srv := New(testCfg(), f, "", slog.New(slog.NewTextHandler(&buf, nil)), "test")
	ctx := withPrincipal(context.Background(), &principal{
		client: f, policy: access.Projects("PAY"), self: "ozan", onBehalf: true, caller: "ozan@yildizozan.com",
	})

	_, _ = srv.handleAddComment(ctx, newReq(map[string]any{"key": "PAY-1", "body": "x"}))
	_, _ = srv.handleUpdateTicket(ctx, newReq(map[string]any{"key": "PAY-1", "summary": "x"}))
	_, _ = srv.handleTransition(ctx, newReq(map[string]any{"key": "PAY-1", "to": "Done"}))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 audit lines, got %d:\n%s", len(lines), buf.String())
	}
	for _, l := range lines {
		if !strings.Contains(l, "caller=ozan@yildizozan.com") {
			t.Fatalf("audit line without the caller: %s", l)
		}
	}
}

func TestAudit_SharedTokenIsNamed(t *testing.T) {
	var buf bytes.Buffer
	srv := New(testCfg(), &fakeClient{}, "", slog.New(slog.NewTextHandler(&buf, nil)), "test")
	_, _ = srv.handleAddComment(context.Background(), newReq(map[string]any{"key": "PAY-1", "body": "x"}))
	if !strings.Contains(buf.String(), "caller=shared-token") {
		t.Fatalf("a shared-token call must be labeled as such: %s", buf.String())
	}
}
