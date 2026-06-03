package config

import (
	"testing"
)

func TestLoad_CloudHappyPath(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://acme.atlassian.net/")
	t.Setenv("JIRA_AUTH_EMAIL", "svc@acme.com")
	t.Setenv("JIRA_API_TOKEN", "tok")
	t.Setenv("MCP_AUTH_TOKEN", "secret")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"Payments":{"projectKey":"PAY"}},"defaultTeam":"Payments"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AuthMode != AuthCloud {
		t.Fatalf("authMode: %s", cfg.AuthMode)
	}
	if cfg.BaseURL != "https://acme.atlassian.net" {
		t.Fatalf("baseURL not trimmed: %s", cfg.BaseURL)
	}
	if _, ok := cfg.Teams.Lookup("payments"); !ok {
		t.Fatalf("team lookup should be case-insensitive")
	}
	if cfg.Teams.DefaultTeam != "payments" {
		t.Fatalf("defaultTeam normalized: %s", cfg.Teams.DefaultTeam)
	}
}

func TestLoad_NativeYAMLMapping(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.acme.internal")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `
teams:
  dosd:
    projectKey: DOSD
    defaultIssueType: Service Request
    labels: [jiramcp]
defaultTeam: dosd
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c, ok := cfg.Teams.Lookup("dosd")
	if !ok {
		t.Fatal("dosd team should be present")
	}
	if c.ProjectKey != "DOSD" || c.DefaultIssueType != "Service Request" {
		t.Fatalf("unexpected team config: %+v", c)
	}
}

func TestLoad_RejectsUnknownMappingField(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.acme.internal")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  p:\n    projectKey: P\n    bogus: x\n")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unknown mapping field")
	}
}

func TestLoad_RejectsReservedFieldOverride(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.acme.internal")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	// A mapping must not set core fields (here `project`) via team `fields`.
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  dosd:\n    projectKey: DOSD\n    fields:\n      project: {key: DPS}\n")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when team fields override a reserved core field")
	}
}

func TestLoad_RejectsDuplicateNormalizedTeam(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.acme.internal")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  DOSD:\n    projectKey: DOSD\n  dosd:\n    projectKey: DPS\n")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for case-insensitive duplicate team keys")
	}
}

func TestLoad_RejectsDuplicateProjectKey(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.acme.internal")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	// Two teams routing to the same project (case-insensitively) is rejected so
	// the defaults applied for that project stay deterministic.
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  alpha:\n    projectKey: PAY\n  beta:\n    projectKey: pay\n")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when two teams map to the same project key")
	}
}

func TestLoad_RequiresAuthTokenForHTTP(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://acme.atlassian.net")
	t.Setenv("JIRA_AUTH_EMAIL", "svc@acme.com")
	t.Setenv("JIRA_API_TOKEN", "tok")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}}}`)
	// No MCP_AUTH_TOKEN and not allowing unauthenticated => error.
	if _, err := Load(); err == nil {
		t.Fatal("expected error when http transport has no auth token")
	}
}

func TestLoad_PATInfersDC(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.acme.internal")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AuthMode != AuthDC {
		t.Fatalf("expected dc auth, got %s", cfg.AuthMode)
	}
}

func TestLoad_RejectsBadBaseURL(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "not-a-url")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}}}`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for non-absolute base url")
	}
}

func TestLoad_DefaultTeamMustExist(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://acme.atlassian.net")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("MCP_TRANSPORT", "stdio")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}},"defaultTeam":"ghost"}`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error when defaultTeam is absent from teams")
	}
}
