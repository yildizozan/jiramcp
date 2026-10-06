package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

// localCfg is a developer's local Data Center setup: no team mapping,
// projects bounded only by an optional allow-list.
func localCfg(projects ...string) *config.Config {
	return &config.Config{
		AuthMode:         config.AuthDC,
		DefaultIssueType: "Task",
		Transport:        config.TransportStdio,
		Teams:            &config.TeamMapping{Teams: map[string]config.TeamConfig{}},
		Projects:         projects,
	}
}

// newLocalServer builds a server whose client acts as the developer "ozan".
func newLocalServer(f jira.Client, cfg *config.Config) *Server {
	return New(cfg, f, "ozan", slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

func localFake() *fakeClient {
	return &fakeClient{
		issueTypes: []jira.IssueType{{ID: "10001", Name: "Task"}},
		// reporter is required on the screen but filled by Jira when omitted.
		meta: &jira.CreateMeta{Fields: map[string]jira.FieldMeta{
			"summary":   {FieldID: "summary", Required: true},
			"reporter":  {FieldID: "reporter", Required: true},
			"project":   {FieldID: "project", Required: true},
			"issuetype": {FieldID: "issuetype", Required: true},
		}},
	}
}

func TestCreateLocal_ReporterDefaultsToSelf(t *testing.T) {
	f := localFake()
	res, _ := newLocalServer(f, localCfg()).handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "project": "dosd",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	if f.created == nil || f.created.ProjectKey != "DOSD" {
		t.Fatalf("expected a ticket in DOSD, got %+v", f.created)
	}
	if f.created.ReporterID != "" {
		t.Fatalf("filing as self must not send a reporter, got %q", f.created.ReporterID)
	}
	if !strings.Contains(resultText(res), "for reporter ozan") {
		t.Fatalf("result should name the effective reporter: %s", resultText(res))
	}
}

func TestCreateLocal_ReporterResolvingToSelfIsNotSent(t *testing.T) {
	f := localFake()
	f.users = []jira.User{{Name: "ozan", Email: "ozan@yildizozan.com", Active: true}}
	res, _ := newLocalServer(f, localCfg()).handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "project": "PAY", "reporter": "ozan@yildizozan.com",
	}))
	if res.IsError || f.created == nil || f.created.ReporterID != "" {
		t.Fatalf("reporter equal to self must not be sent (err=%v in=%+v)", res.IsError, f.created)
	}
}

func TestCreateLocal_OtherReporterIsSent(t *testing.T) {
	f := localFake()
	f.meta.Fields["reporter"] = jira.FieldMeta{FieldID: "reporter"}
	res, _ := newLocalServer(f, localCfg()).handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "project": "PAY", "reporter": "jane",
	}))
	if res.IsError || f.created == nil || f.created.ReporterID != "jane" {
		t.Fatalf("another reporter must be sent (err=%s in=%+v)", resultText(res), f.created)
	}
}

func TestCreateLocal_ProjectsOutsideAllowListBlocked(t *testing.T) {
	f := localFake()
	res, _ := newLocalServer(f, localCfg("PAY", "DOSD")).handleCreateTicket(context.Background(), newReq(map[string]any{
		"summary": "x", "project": "HR",
	}))
	if !res.IsError || !strings.Contains(resultText(res), "allowed projects: DOSD, PAY") {
		t.Fatalf("expected an allow-list error naming the allowed projects, got: %s", resultText(res))
	}
	if f.created != nil {
		t.Fatal("must not create outside the allow-list")
	}
}

func TestCreateLocal_DefaultProject(t *testing.T) {
	cfg := localCfg("PAY", "DOSD")
	cfg.DefaultProject = "DOSD"
	f := localFake()
	res, _ := newLocalServer(f, cfg).handleCreateTicket(context.Background(), newReq(map[string]any{"summary": "x"}))
	if res.IsError || f.created == nil || f.created.ProjectKey != "DOSD" {
		t.Fatalf("expected the default project DOSD (err=%s in=%+v)", resultText(res), f.created)
	}
}

func TestCreateLocal_NoProjectNoDefault(t *testing.T) {
	f := localFake()
	res, _ := newLocalServer(f, localCfg("PAY")).handleCreateTicket(context.Background(), newReq(map[string]any{"summary": "x"}))
	if !res.IsError || !strings.Contains(resultText(res), "allowed: PAY") {
		t.Fatalf("expected a hint naming the allowed projects, got: %s", resultText(res))
	}
}

func TestCreate_ServiceAccountStillRequiresReporter(t *testing.T) {
	f := localFake()
	res, _ := newServer(f, testCfg()).handleCreateTicket(context.Background(), newReq(map[string]any{"summary": "x"}))
	if !res.IsError || !strings.Contains(resultText(res), "reporter is required") {
		t.Fatalf("a service-account server must require reporter, got: %s", resultText(res))
	}
}

func TestListProjectsLocal_AllVisibleWithoutAllowList(t *testing.T) {
	f := &fakeClient{projects: []jira.Project{{Key: "PAY"}, {Key: "DOSD"}, {Key: "HR"}}}
	res, _ := newLocalServer(f, localCfg()).handleListProjects(context.Background(), newReq(map[string]any{}))
	if res.IsError || !strings.Contains(resultText(res), "found 3") {
		t.Fatalf("without an allow-list every project Jira shows must be listed, got: %s", resultText(res))
	}
}
