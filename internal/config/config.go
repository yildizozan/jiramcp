// Package config loads and validates all runtime configuration from the
// environment. Every credential and tunable comes from an env var so the
// binary can run unmodified in Kubernetes with values injected from a Secret
// or ConfigMap.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"jiramcp/internal/access"
)

// AuthMode selects how the server authenticates to Jira.
type AuthMode string

const (
	// AuthCloud uses HTTP Basic auth with email:api_token (Jira Cloud).
	AuthCloud AuthMode = "cloud"
	// AuthDC uses a Bearer Personal Access Token (Jira Server/Data Center).
	AuthDC AuthMode = "dc"
)

// Transport selects the MCP transport.
type Transport string

const (
	// TransportHTTP serves the MCP endpoint over streamable HTTP.
	TransportHTTP Transport = "http"
	// TransportStdio serves MCP over stdio (local dev / embedding).
	TransportStdio Transport = "stdio"
)

// Config is the fully validated runtime configuration.
type Config struct {
	// Jira connection.
	BaseURL          string
	AuthMode         AuthMode
	AuthEmail        string
	APIToken         string
	PAT              string
	HTTPTimeout      time.Duration
	DefaultIssueType string

	// Teams is the resolved team -> project routing table. Optional: empty
	// when no mapping is configured.
	Teams *TeamMapping
	// Projects is the optional JIRA_PROJECTS allow-list (upper-cased).
	Projects []string
	// DefaultProject is used by create when neither project nor team is given
	// and the mapping has no defaultTeam.
	DefaultProject string

	// MCP transport.
	Transport Transport
	HTTPAddr  string
	HTTPPath  string

	// AuthToken is the static bearer token required to call the HTTP MCP
	// endpoint. Required when Transport==http unless AllowUnauthenticated.
	AuthToken string
	// AllowUnauthenticated disables HTTP auth. Intended for local dev only;
	// it must be set explicitly and is loudly logged.
	AllowUnauthenticated bool

	// Health server.
	HealthAddr string

	// Logging.
	LogLevel  string
	LogFormat string
}

// Load reads configuration from the environment and validates it for the
// given transport, which is chosen by the CLI subcommand rather than an env var.
func Load(transport Transport) (*Config, error) {
	c := &Config{
		BaseURL:              strings.TrimRight(env("JIRA_BASE_URL", ""), "/"),
		AuthMode:             AuthMode(env("JIRA_AUTH_MODE", "")),
		AuthEmail:            env("JIRA_AUTH_EMAIL", ""),
		APIToken:             env("JIRA_API_TOKEN", ""),
		PAT:                  env("JIRA_PAT", ""),
		DefaultIssueType:     env("JIRA_DEFAULT_ISSUE_TYPE", "Task"),
		Projects:             splitKeys(env("JIRA_PROJECTS", "")),
		DefaultProject:       strings.ToUpper(strings.TrimSpace(env("JIRA_DEFAULT_PROJECT", ""))),
		Transport:            transport,
		HTTPAddr:             env("MCP_HTTP_ADDR", ":8080"),
		HTTPPath:             env("MCP_HTTP_PATH", "/mcp"),
		AuthToken:            env("MCP_AUTH_TOKEN", ""),
		AllowUnauthenticated: envBool("MCP_ALLOW_UNAUTHENTICATED", false),
		HealthAddr:           env("HTTP_HEALTH_ADDR", ":8081"),
		LogLevel:             env("LOG_LEVEL", "info"),
		LogFormat:            env("LOG_FORMAT", "json"),
	}

	timeout, err := time.ParseDuration(env("JIRA_HTTP_TIMEOUT", "15s"))
	if err != nil {
		return nil, fmt.Errorf("invalid JIRA_HTTP_TIMEOUT: %w", err)
	}
	c.HTTPTimeout = timeout

	// Infer auth mode when not set explicitly: a PAT implies Data Center.
	if c.AuthMode == "" {
		if c.PAT != "" {
			c.AuthMode = AuthDC
		} else {
			c.AuthMode = AuthCloud
		}
	}

	teams, err := loadTeamMapping()
	if err != nil {
		return nil, err
	}
	c.Teams = teams

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("JIRA_BASE_URL is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("JIRA_BASE_URL must be an absolute http(s) URL, got %q", c.BaseURL)
	}

	switch c.AuthMode {
	case AuthCloud:
		if c.AuthEmail == "" || c.APIToken == "" {
			return fmt.Errorf("cloud auth requires JIRA_AUTH_EMAIL and JIRA_API_TOKEN")
		}
	case AuthDC:
		if c.PAT == "" {
			return fmt.Errorf("dc auth requires JIRA_PAT")
		}
	default:
		return fmt.Errorf("JIRA_AUTH_MODE must be %q or %q, got %q", AuthCloud, AuthDC, c.AuthMode)
	}

	switch c.Transport {
	case TransportHTTP:
		if c.AuthToken == "" && !c.AllowUnauthenticated {
			return fmt.Errorf("MCP_AUTH_TOKEN is required for http transport; " +
				"set MCP_ALLOW_UNAUTHENTICATED=true only for local development")
		}
	case TransportStdio:
		// no network exposure; auth not applicable.
	default:
		return fmt.Errorf("transport must be %q or %q, got %q", TransportHTTP, TransportStdio, c.Transport)
	}

	if err := c.Teams.validate(); err != nil {
		return err
	}
	return c.validateProjects()
}

// validateProjects checks the project allow-list against the mapping and the
// default project.
func (c *Config) validateProjects() error {
	mapped := c.Teams.ProjectKeyList()
	// The HTTP endpoint acts as one shared service account, so it must not
	// reach every project that account can see: one of the two lists must
	// bound it. A local stdio server acts as its user, and Jira's own
	// permissions bound it.
	if c.Transport == TransportHTTP && len(mapped) == 0 && len(c.Projects) == 0 {
		return fmt.Errorf("http transport needs JIRA_PROJECTS or a team mapping " +
			"(JIRA_TEAM_MAPPING_YAML / JIRA_TEAM_MAPPING_FILE) to bound the projects it may use")
	}
	if len(c.Projects) > 0 {
		allowed := access.Projects(c.Projects...)
		for _, key := range mapped {
			if !allowed.Allowed(key) {
				return fmt.Errorf("team mapping routes to project %q, which is not in JIRA_PROJECTS", key)
			}
		}
	}
	if c.DefaultProject != "" && !c.ProjectPolicy().Allowed(c.DefaultProject) {
		return fmt.Errorf("JIRA_DEFAULT_PROJECT %q is not an allowed project", c.DefaultProject)
	}
	return nil
}

// ProjectPolicy returns the projects this configuration allows: the
// JIRA_PROJECTS list when set, else the mapping's projects, else every project
// (Jira's own permissions decide).
func (c *Config) ProjectPolicy() access.Policy {
	if len(c.Projects) > 0 {
		return access.Projects(c.Projects...)
	}
	if mapped := c.Teams.ProjectKeyList(); len(mapped) > 0 {
		return access.Projects(mapped...)
	}
	return access.AllowAll()
}

// splitKeys parses a comma-separated list of project keys, upper-casing them
// and dropping blanks and duplicates.
func splitKeys(v string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, k := range strings.Split(v, ",") {
		k = strings.ToUpper(strings.TrimSpace(k))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys
}

// Redacted returns a copy safe for logging, with secrets masked.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"baseURL":              c.BaseURL,
		"authMode":             c.AuthMode,
		"authEmail":            mask(c.AuthEmail),
		"apiToken":             maskSecret(c.APIToken),
		"pat":                  maskSecret(c.PAT),
		"transport":            c.Transport,
		"httpAddr":             c.HTTPAddr,
		"httpPath":             c.HTTPPath,
		"authToken":            maskSecret(c.AuthToken),
		"allowUnauthenticated": c.AllowUnauthenticated,
		"healthAddr":           c.HealthAddr,
		"defaultIssueType":     c.DefaultIssueType,
		"projects":             c.Projects,
		"defaultProject":       c.DefaultProject,
		"teams":                len(c.Teams.Teams),
		"defaultTeam":          c.Teams.DefaultTeam,
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off", "":
		return false
	default:
		return def
	}
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	at := strings.IndexByte(s, '@')
	if at > 1 {
		return s[:1] + "***" + s[at:]
	}
	return "***"
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	return "***redacted***"
}
