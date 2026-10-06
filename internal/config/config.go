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

// MCPAuth selects how the HTTP endpoint authenticates its callers and whose
// Jira credentials it acts with.
type MCPAuth string

const (
	// MCPAuthToken checks one shared bearer token (MCP_AUTH_TOKEN) and acts
	// with the server's own service-account credentials.
	MCPAuthToken MCPAuth = "token"
	// MCPAuthJira takes each caller's own Jira credentials from the
	// Authorization header (Bearer PAT on Server/DC, Basic email:token on
	// Cloud) and acts as that caller.
	MCPAuthJira MCPAuth = "jira"
	// MCPAuthOIDC verifies an OIDC access token (e.g. from Dex), maps the
	// caller's groups to projects, and acts with the service account.
	MCPAuthOIDC MCPAuth = "oidc"
)

// OIDC configures MCP_AUTH_MODE=oidc.
type OIDC struct {
	// IssuerURL is the OIDC issuer (OIDC_ISSUER_URL), e.g. https://dex.example.com.
	IssuerURL string
	// Audience is the client id the tokens must be issued for (OIDC_AUDIENCE).
	Audience string
	// UserClaim names the claim that identifies the caller's Jira user
	// (OIDC_USER_CLAIM, default "email").
	UserClaim string
	// GroupsClaim names the claim listing the caller's groups
	// (OIDC_GROUPS_CLAIM, default "groups").
	GroupsClaim string
	// OnBehalfGroup is the group whose members may file tickets with another
	// person as reporter (OIDC_ON_BEHALF_GROUP). Empty: nobody may.
	OnBehalfGroup string
	// ResourceURL is the public URL of the MCP endpoint (OIDC_RESOURCE_URL).
	// When set, OAuth protected resource metadata (RFC 9728) is served so MCP
	// clients can discover the issuer.
	ResourceURL string
	// GroupProjects maps a group to the projects its members may use.
	GroupProjects map[string][]string
}

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

	// MCPAuth is the HTTP caller authentication mode (MCP_AUTH_MODE).
	MCPAuth MCPAuth
	// OIDC is set when MCPAuth is MCPAuthOIDC.
	OIDC *OIDC
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
		MCPAuth:              MCPAuth(strings.ToLower(strings.TrimSpace(env("MCP_AUTH_MODE", string(MCPAuthToken))))),
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

	// Infer auth mode when not set explicitly: a PAT implies Data Center. A
	// server acting with its callers' credentials holds no PAT to infer from,
	// so there the dialect must be explicit (checked in validate).
	if c.AuthMode == "" && !c.ActsAsCaller() {
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

	if c.Transport == TransportHTTP && c.MCPAuth == MCPAuthOIDC {
		if c.OIDC, err = loadOIDC(); err != nil {
			return nil, err
		}
	}

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

	if c.AuthMode != AuthCloud && c.AuthMode != AuthDC {
		return fmt.Errorf("JIRA_AUTH_MODE must be %q or %q, got %q", AuthCloud, AuthDC, c.AuthMode)
	}
	// A server acting with its callers' credentials holds none of its own.
	if !c.ActsAsCaller() {
		if c.AuthMode == AuthCloud && (c.AuthEmail == "" || c.APIToken == "") {
			return fmt.Errorf("cloud auth requires JIRA_AUTH_EMAIL and JIRA_API_TOKEN")
		}
		if c.AuthMode == AuthDC && c.PAT == "" {
			return fmt.Errorf("dc auth requires JIRA_PAT")
		}
	}

	switch c.Transport {
	case TransportHTTP:
		switch c.MCPAuth {
		case MCPAuthToken:
			if c.AuthToken == "" && !c.AllowUnauthenticated {
				return fmt.Errorf("MCP_AUTH_TOKEN is required for http transport; " +
					"set MCP_ALLOW_UNAUTHENTICATED=true only for local development")
			}
		case MCPAuthJira:
			// Callers authenticate with their own Jira credentials.
		case MCPAuthOIDC:
			if err := c.OIDC.validate(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("MCP_AUTH_MODE must be %q, %q or %q, got %q", MCPAuthToken, MCPAuthJira, MCPAuthOIDC, c.MCPAuth)
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
	// The token-mode HTTP endpoint acts as one shared service account for
	// every caller, so it must not reach every project that account can see:
	// one of the two lists must bound it. A local stdio server, or an HTTP
	// server acting with each caller's own credentials, acts as its user, and
	// Jira's own permissions bound it. In OIDC mode the caller's groups do.
	sharedAccount := c.Transport == TransportHTTP && c.MCPAuth == MCPAuthToken
	if sharedAccount && len(mapped) == 0 && len(c.Projects) == 0 {
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

// ActsAsCaller reports whether the HTTP server uses each caller's own Jira
// credentials instead of holding its own.
func (c *Config) ActsAsCaller() bool {
	return c.Transport == TransportHTTP && c.MCPAuth == MCPAuthJira
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
		"mcpAuthMode":          c.MCPAuth,
		"oidc":                 c.OIDC,
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
