package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"jiramcp/internal/access"
	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

func searchReq(args map[string]any) map[string]any {
	if _, ok := args["jql"]; !ok {
		args["jql"] = "status = Open"
	}
	return args
}

func searchResult(t *testing.T, f *fakeClient, cfg *config.Config, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	srv := newServer(f, cfg)
	res, _ := srv.handleSearchTickets(context.Background(), newReq(searchReq(args)))
	out, _ := res.StructuredContent.(map[string]any)
	return out, resultText(res), res.IsError
}

func TestSearch_ScopesJQLToAllowedProjects(t *testing.T) {
	f := &fakeClient{}
	_, msg, isErr := searchResult(t, f, testCfg(), map[string]any{"jql": "assignee = currentUser()", "orderBy": "updated DESC"})
	if isErr {
		t.Fatalf("unexpected error: %s", msg)
	}
	want := `project in ("PAY") AND (assignee = currentUser()) ORDER BY updated DESC`
	if f.searchJQL != want {
		t.Fatalf("jql sent to Jira:\n got %s\nwant %s", f.searchJQL, want)
	}
}

// Caller JQL can close the wrapping parentheses and OR in another project.
// The result filter, not the project clause, must keep those issues out.
func TestSearch_DropsIssuesOutsidePolicy(t *testing.T) {
	f := &fakeClient{searchPage: &jira.SearchPage{Issues: []jira.Issue{
		{Key: "PAY-1", Summary: "allowed"},
		{Key: "HR-9", Summary: "leaked"},
	}}}
	out, msg, isErr := searchResult(t, f, testCfg(), map[string]any{"jql": "x) OR (project = HR"})
	if isErr {
		t.Fatalf("unexpected error: %s", msg)
	}
	hits := out["issues"].([]searchHit)
	if len(hits) != 1 || hits[0].Key != "PAY-1" {
		t.Fatalf("expected only PAY-1, got %+v", hits)
	}
	if msg != "found 1 issue(s)" {
		t.Fatalf("unexpected message: %s", msg)
	}
}

func TestSearch_UnrestrictedPolicyNotScoped(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	srv.base.policy = access.AllowAll()
	res, _ := srv.handleSearchTickets(context.Background(), newReq(map[string]any{"jql": "status = Open"}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.searchJQL != "status = Open" {
		t.Fatalf("unrestricted policy must not add a project clause, got %q", f.searchJQL)
	}
}

func TestSearch_EmptyPolicyRefusedWithoutCallingJira(t *testing.T) {
	f := &fakeClient{}
	srv := newServer(f, testCfg())
	srv.base.policy = access.Projects()
	res, _ := srv.handleSearchTickets(context.Background(), newReq(map[string]any{"jql": "status = Open"}))
	if !res.IsError {
		t.Fatal("expected an empty policy to refuse the search")
	}
	if f.searchCalls != 0 {
		t.Fatal("must not call Jira when no project is allowed")
	}
}

func TestSearch_RejectsOrderByInJQL(t *testing.T) {
	f := &fakeClient{}
	_, msg, isErr := searchResult(t, f, testCfg(), map[string]any{"jql": "status = Open order  BY created"})
	if !isErr || !strings.Contains(msg, "orderBy") {
		t.Fatalf("expected ORDER BY in jql to be rejected, got %q", msg)
	}
	if f.searchCalls != 0 {
		t.Fatal("must not call Jira for rejected jql")
	}
}

func TestSearch_RequiresJQL(t *testing.T) {
	f := &fakeClient{}
	_, _, isErr := searchResult(t, f, testCfg(), map[string]any{"jql": "   "})
	if !isErr || f.searchCalls != 0 {
		t.Fatal("expected blank jql to be rejected without calling Jira")
	}
}

func TestSearch_LimitAndCursor(t *testing.T) {
	cases := map[string]struct {
		args        map[string]any
		wantLimit   int
		wantStartAt int
	}{
		"defaults":      {map[string]any{}, defaultSearchLimit, 0},
		"capped":        {map[string]any{"limit": 500}, maxSearchLimit, 0},
		"cursor offset": {map[string]any{"limit": 10, "cursor": "40"}, 10, 40},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeClient{}
			_, msg, isErr := searchResult(t, f, testCfg(), tc.args)
			if isErr {
				t.Fatalf("unexpected error: %s", msg)
			}
			if f.searchLimit != tc.wantLimit || f.searchStartAt != tc.wantStartAt {
				t.Fatalf("got limit %d startAt %d, want %d %d", f.searchLimit, f.searchStartAt, tc.wantLimit, tc.wantStartAt)
			}
		})
	}
}

func TestSearch_InvalidCursorRejected(t *testing.T) {
	for _, cursor := range []string{"abc", "-5"} {
		f := &fakeClient{}
		_, _, isErr := searchResult(t, f, testCfg(), map[string]any{"cursor": cursor})
		if !isErr || f.searchCalls != 0 {
			t.Fatalf("expected cursor %q to be rejected without calling Jira", cursor)
		}
	}
}

func TestSearch_ReturnsNextCursor(t *testing.T) {
	f := &fakeClient{searchPage: &jira.SearchPage{Issues: []jira.Issue{{Key: "PAY-1"}}, NextStartAt: 20}}
	out, msg, isErr := searchResult(t, f, testCfg(), map[string]any{})
	if isErr {
		t.Fatalf("unexpected error: %s", msg)
	}
	if out["nextCursor"] != "20" {
		t.Fatalf("expected nextCursor 20, got %v", out["nextCursor"])
	}
}

func TestSearch_ClientErrorSurfaces(t *testing.T) {
	f := &fakeClient{searchErr: errors.New(`search issues failed: Error in the JQL Query (HTTP 400)`)}
	_, msg, isErr := searchResult(t, f, testCfg(), map[string]any{})
	if !isErr || !strings.Contains(msg, "Error in the JQL Query") {
		t.Fatalf("expected the Jira error to be surfaced, got %q", msg)
	}
}

func TestSearch_RegisteredOnlyForDataCenter(t *testing.T) {
	cloud := newServer(&fakeClient{}, testCfg())
	if cloud.mcp.GetTool("search_jira_tickets") != nil {
		t.Fatal("search_jira_tickets must not be offered on Cloud")
	}
	cfg := testCfg()
	cfg.AuthMode = config.AuthDC
	dc := newServer(&fakeClient{}, cfg)
	if dc.mcp.GetTool("search_jira_tickets") == nil {
		t.Fatal("search_jira_tickets must be offered on Server/Data Center")
	}
}
