package config

import (
	"strings"
	"testing"
)

func TestLoad_CloudHappyPath(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com/")
	t.Setenv("JIRA_AUTH_EMAIL", "svc@yildizozan.com")
	t.Setenv("JIRA_API_TOKEN", "tok")
	t.Setenv("MCP_AUTH_TOKEN", "secret")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"Payments":{"projectKey":"PAY"}},"defaultTeam":"Payments"}`)

	cfg, err := Load(TransportHTTP)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AuthMode != AuthCloud {
		t.Fatalf("authMode: %s", cfg.AuthMode)
	}
	if cfg.BaseURL != "https://jira.yildizozan.com" {
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
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `
teams:
  dosd:
    projectKey: DOSD
    defaultIssueType: Service Request
    labels: [jiramcp]
defaultTeam: dosd
`)
	cfg, err := Load(TransportStdio)
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
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  p:\n    projectKey: P\n    bogus: x\n")
	if _, err := Load(TransportStdio); err == nil {
		t.Fatal("expected error for unknown mapping field")
	}
}

func TestLoad_RejectsReservedFieldOverride(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	// A mapping must not set core fields (here `project`) via team `fields`.
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  dosd:\n    projectKey: DOSD\n    fields:\n      project: {key: DPS}\n")
	if _, err := Load(TransportStdio); err == nil {
		t.Fatal("expected error when team fields override a reserved core field")
	}
}

func TestLoad_RejectsDuplicateNormalizedTeam(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  DOSD:\n    projectKey: DOSD\n  dosd:\n    projectKey: DPS\n")
	if _, err := Load(TransportStdio); err == nil {
		t.Fatal("expected error for case-insensitive duplicate team keys")
	}
}

func TestLoad_RejectsDuplicateProjectKey(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	// Two teams routing to the same project (case-insensitively) is rejected so
	// the defaults applied for that project stay deterministic.
	t.Setenv("JIRA_TEAM_MAPPING_YAML", "teams:\n  alpha:\n    projectKey: PAY\n  beta:\n    projectKey: pay\n")
	if _, err := Load(TransportStdio); err == nil {
		t.Fatal("expected error when two teams map to the same project key")
	}
}

func TestLoad_RequiresAuthTokenForHTTP(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_AUTH_EMAIL", "svc@yildizozan.com")
	t.Setenv("JIRA_API_TOKEN", "tok")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}}}`)
	// No MCP_AUTH_TOKEN and not allowing unauthenticated => error.
	if _, err := Load(TransportHTTP); err == nil {
		t.Fatal("expected error when http transport has no auth token")
	}
}

func TestLoad_PATInfersDC(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}}}`)
	cfg, err := Load(TransportStdio)
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
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}}}`)
	if _, err := Load(TransportStdio); err == nil {
		t.Fatal("expected error for non-absolute base url")
	}
}

func TestLoad_DefaultTeamMustExist(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
	t.Setenv("JIRA_TEAM_MAPPING_YAML", `{"teams":{"p":{"projectKey":"P"}},"defaultTeam":"ghost"}`)
	if _, err := Load(TransportStdio); err == nil {
		t.Fatal("expected error when defaultTeam is absent from teams")
	}
}

// localDC sets the minimum environment for a developer running the binary
// with their own Data Center PAT and no team mapping.
func localDC(t *testing.T) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_PAT", "pat")
}

func TestLoad_StdioWithoutMappingAllowsAllProjects(t *testing.T) {
	localDC(t)
	cfg, err := Load(TransportStdio)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Teams.Teams) != 0 {
		t.Fatalf("expected an empty mapping, got %v", cfg.Teams.Teams)
	}
	if p := cfg.ProjectPolicy(); !p.Allowed("ANY") || p.Projects() != nil {
		t.Fatal("without a mapping or JIRA_PROJECTS, stdio must leave projects to Jira's permissions")
	}
}

func TestLoad_ProjectsAllowList(t *testing.T) {
	localDC(t)
	t.Setenv("JIRA_PROJECTS", " dosd, PAY ,,dosd")
	t.Setenv("JIRA_DEFAULT_PROJECT", "pay")
	cfg, err := Load(TransportStdio)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if strings.Join(cfg.Projects, ",") != "DOSD,PAY" {
		t.Fatalf("JIRA_PROJECTS parsed as %v, want [DOSD PAY]", cfg.Projects)
	}
	if cfg.DefaultProject != "PAY" {
		t.Fatalf("default project %q, want PAY", cfg.DefaultProject)
	}
	p := cfg.ProjectPolicy()
	if !p.Allowed("dosd") || p.Allowed("HR") {
		t.Fatal("policy must follow JIRA_PROJECTS")
	}
}

func TestLoad_ProjectsValidation(t *testing.T) {
	cases := map[string]struct {
		transport Transport
		env       map[string]string
	}{
		"http without any project bound": {TransportHTTP, map[string]string{"MCP_AUTH_TOKEN": "s"}},
		"mapping outside JIRA_PROJECTS": {TransportStdio, map[string]string{
			"JIRA_PROJECTS": "PAY", "JIRA_TEAM_MAPPING_YAML": `{"teams":{"hr":{"projectKey":"HR"}}}`,
		}},
		"default project not allowed": {TransportStdio, map[string]string{
			"JIRA_PROJECTS": "PAY", "JIRA_DEFAULT_PROJECT": "HR",
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			localDC(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(tc.transport); err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}

func TestLoad_HTTPWithProjectsOnly(t *testing.T) {
	localDC(t)
	t.Setenv("MCP_AUTH_TOKEN", "s")
	t.Setenv("JIRA_PROJECTS", "PAY")
	if _, err := Load(TransportHTTP); err != nil {
		t.Fatalf("JIRA_PROJECTS alone must be enough to bound the http service: %v", err)
	}
}

func TestLoad_JiraAuthModeNeedsNoServerCredentials(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
	t.Setenv("JIRA_AUTH_MODE", "dc")
	t.Setenv("MCP_AUTH_MODE", "jira")
	cfg, err := Load(TransportHTTP)
	if err != nil {
		t.Fatalf("jira mode needs no PAT, MCP token or project bound: %v", err)
	}
	if !cfg.ActsAsCaller() || cfg.AuthMode != AuthDC {
		t.Fatalf("ActsAsCaller=%v AuthMode=%s; want true, dc", cfg.ActsAsCaller(), cfg.AuthMode)
	}
	if !cfg.ProjectPolicy().Allowed("ANY") {
		t.Fatal("without a list, callers acting as themselves are bounded by Jira only")
	}
}

func TestLoad_JiraAuthModeValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"dialect must be explicit": {"MCP_AUTH_MODE": "jira"},
		"unknown mcp auth mode":    {"MCP_AUTH_MODE": "magic", "JIRA_PAT": "p", "MCP_AUTH_TOKEN": "s", "JIRA_PROJECTS": "PAY"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("JIRA_BASE_URL", "https://jira.yildizozan.com")
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(TransportHTTP); err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}

func TestLoad_StdioIgnoresMCPAuthMode(t *testing.T) {
	localDC(t)
	t.Setenv("MCP_AUTH_MODE", "jira")
	cfg, err := Load(TransportStdio)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ActsAsCaller() {
		t.Fatal("stdio always uses the configured token, never per-caller credentials")
	}
}

// oidcEnv sets an OIDC-mode HTTP server on Data Center with a service account.
func oidcEnv(t *testing.T) {
	t.Helper()
	localDC(t)
	t.Setenv("JIRA_AUTH_MODE", "dc")
	t.Setenv("MCP_AUTH_MODE", "oidc")
	t.Setenv("OIDC_ISSUER_URL", "https://dex.yildizozan.com/")
	t.Setenv("OIDC_AUDIENCE", "jiramcp")
	t.Setenv("OIDC_GROUP_PROJECTS_YAML", "pay-devs: [pay, DOSD]\nleads: []\n")
}

func TestLoad_OIDC(t *testing.T) {
	oidcEnv(t)
	t.Setenv("OIDC_ON_BEHALF_GROUP", "leads")
	cfg, err := Load(TransportHTTP)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	o := cfg.OIDC
	if o == nil || o.IssuerURL != "https://dex.yildizozan.com" || o.UserClaim != "email" || o.GroupsClaim != "groups" {
		t.Fatalf("unexpected oidc config: %+v", o)
	}
	if got := strings.Join(o.ProjectsFor([]string{"pay-devs", "leads", "other"}), ","); got != "PAY,DOSD" {
		t.Fatalf("ProjectsFor = %q, want PAY,DOSD", got)
	}
	if cfg.ActsAsCaller() {
		t.Fatal("oidc mode acts with the service account, not caller credentials")
	}
}

func TestLoad_OIDCValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"no issuer":          {"OIDC_ISSUER_URL": ""},
		"no audience":        {"OIDC_AUDIENCE": ""},
		"no group mapping":   {"OIDC_GROUP_PROJECTS_YAML": ""},
		"bad group mapping":  {"OIDC_GROUP_PROJECTS_YAML": "pay-devs: PAY"},
		"bad resource url":   {"OIDC_RESOURCE_URL": "mcp.example.com/mcp"},
		"no service account": {"JIRA_PAT": ""},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			oidcEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(TransportHTTP); err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}
