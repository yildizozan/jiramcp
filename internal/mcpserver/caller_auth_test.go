package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

// callerCfg is an HTTP server on Data Center that acts with each caller's
// own PAT and has no project allow-list.
func callerCfg() *config.Config {
	return &config.Config{
		AuthMode:         config.AuthDC,
		MCPAuth:          config.MCPAuthJira,
		Transport:        config.TransportHTTP,
		HTTPPath:         "/mcp",
		DefaultIssueType: "Task",
		Teams:            &config.TeamMapping{Teams: map[string]config.TeamConfig{}},
	}
}

func newCallerServer(base *fakeClient) *Server {
	return New(callerCfg(), base, "", slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

// probeCaller runs one request through the caller middleware and returns the
// status and the principal the wrapped handler saw (nil if not reached).
func probeCaller(t *testing.T, s *Server, authorization string) (int, *principal) {
	t.Helper()
	var seen *principal
	h := s.callerAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = s.principal(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, seen
}

func TestCallerAuth_RejectsMissingOrWrongScheme(t *testing.T) {
	s := newCallerServer(&fakeClient{})
	for _, header := range []string{"", "Basic abc", "Bearer ", "bearer pat", "Bearer two words"} {
		code, seen := probeCaller(t, s, header)
		if code != http.StatusUnauthorized || seen != nil {
			t.Fatalf("header %q: status=%d reached=%v; want 401, not reached", header, code, seen != nil)
		}
	}
}

func TestCallerAuth_JiraRejectsCredentials(t *testing.T) {
	s := newCallerServer(&fakeClient{}) // unknown header -> Jira 401
	if code, seen := probeCaller(t, s, "Bearer stolen"); code != http.StatusUnauthorized || seen != nil {
		t.Fatalf("status=%d reached=%v; want 401", code, seen != nil)
	}
}

func TestCallerAuth_JiraUnavailableIsBadGateway(t *testing.T) {
	base := &fakeClient{callers: map[string]*fakeClient{
		"Bearer pat": {meErr: errors.New("dial tcp: connection refused")},
	}}
	if code, _ := probeCaller(t, newCallerServer(base), "Bearer pat"); code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", code)
	}
}

func TestCallerAuth_PrincipalActsAsCaller(t *testing.T) {
	ozan := &fakeClient{me: &jira.User{Name: "ozan", Active: true}}
	s := newCallerServer(&fakeClient{callers: map[string]*fakeClient{"Bearer ozan-pat": ozan}})

	code, p := probeCaller(t, s, "Bearer ozan-pat")
	if code != http.StatusOK || p == nil {
		t.Fatalf("status=%d; want 200 with a principal", code)
	}
	if p.client != jira.Client(ozan) || p.self != "ozan" {
		t.Fatalf("principal must act as the caller: self=%q", p.self)
	}
}

func TestCallerAuth_CachesVerifiedCallers(t *testing.T) {
	ozan := &fakeClient{me: &jira.User{Name: "ozan", Active: true}}
	s := newCallerServer(&fakeClient{callers: map[string]*fakeClient{"Bearer ozan-pat": ozan}})
	now := time.Now()
	s.callers.now = func() time.Time { return now }

	probeCaller(t, s, "Bearer ozan-pat")
	probeCaller(t, s, "Bearer ozan-pat")
	if ozan.myselfCalls != 1 {
		t.Fatalf("Myself called %d times; a verified caller must be cached", ozan.myselfCalls)
	}

	now = now.Add(callerTTL + time.Second)
	probeCaller(t, s, "Bearer ozan-pat")
	if ozan.myselfCalls != 2 {
		t.Fatalf("Myself called %d times; an expired entry must be verified again", ozan.myselfCalls)
	}
}

// End to end through the real streamable HTTP handler: two callers with their
// own PATs each reach Jira as themselves, concurrently, and never as the
// other.
func TestCallerAuth_EndToEndPerCallerClients(t *testing.T) {
	ozan := &fakeClient{me: &jira.User{Name: "ozan", Active: true}, issue: &jira.Issue{Key: "PAY-1", Status: "Open"}}
	ayse := &fakeClient{me: &jira.User{Name: "ayse", Active: true}, issue: &jira.Issue{Key: "DOSD-2", Status: "Open"}}
	base := &fakeClient{callers: map[string]*fakeClient{"Bearer ozan-pat": ozan, "Bearer ayse-pat": ayse}}
	srv := httptest.NewServer(newCallerServer(base).httpHandler())
	defer srv.Close()

	call := func(pat, key string) string {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_jira_ticket","arguments":{"key":"` + key + `"}}}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+pat)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("request: %v", err)
			return ""
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		return string(out)
	}

	var wg sync.WaitGroup
	var ozanOut, ayseOut string
	wg.Add(2)
	go func() { defer wg.Done(); ozanOut = call("ozan-pat", "PAY-1") }()
	go func() { defer wg.Done(); ayseOut = call("ayse-pat", "DOSD-2") }()
	wg.Wait()

	if !strings.Contains(ozanOut, "PAY-1 [Open]") || !strings.Contains(ayseOut, "DOSD-2 [Open]") {
		t.Fatalf("unexpected responses:\nozan: %s\nayse: %s", ozanOut, ayseOut)
	}
	if ozan.gotKey != "PAY-1" || ayse.gotKey != "DOSD-2" || base.gotKey != "" {
		t.Fatalf("each call must reach Jira as its own caller (ozan=%q ayse=%q base=%q)", ozan.gotKey, ayse.gotKey, base.gotKey)
	}
}

func TestCallerCache_StaysBounded(t *testing.T) {
	c := newCallerCache()
	verify := func(context.Context, string) (*principal, time.Time, error) { return &principal{}, time.Time{}, nil }
	for i := 0; i < maxCachedCallers+10; i++ {
		_, _ = c.get(context.Background(), "Bearer "+strings.Repeat("x", i+1), verify)
	}
	if len(c.entries) > maxCachedCallers {
		t.Fatalf("cache holds %d entries, max %d", len(c.entries), maxCachedCallers)
	}
}
