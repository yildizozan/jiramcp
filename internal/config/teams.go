package config

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// TeamConfig is the routing entry for a single team.
type TeamConfig struct {
	// ProjectKey is the Jira project tickets for this team are created in.
	ProjectKey string `json:"projectKey"`
	// DefaultIssueType overrides the global default for this team.
	DefaultIssueType string `json:"defaultIssueType,omitempty"`
	// Components are default component names applied to created issues.
	Components []string `json:"components,omitempty"`
	// Labels are default labels applied to created issues.
	Labels []string `json:"labels,omitempty"`
	// Fields are default extra field values (e.g. required custom fields),
	// keyed by Jira field id (e.g. "customfield_10010"). Values are passed
	// through to the create payload as-is.
	Fields map[string]any `json:"fields,omitempty"`
}

// TeamMapping is the full team -> project routing table.
type TeamMapping struct {
	Teams       map[string]TeamConfig `json:"teams"`
	DefaultTeam string                `json:"defaultTeam,omitempty"`
}

// loadTeamMapping reads the mapping from JIRA_TEAM_MAPPING_YAML (inline) or
// JIRA_TEAM_MAPPING_FILE (path, typically a mounted ConfigMap). The document is
// YAML; since YAML is a superset of JSON, inline JSON is still accepted.
func loadTeamMapping() (*TeamMapping, error) {
	inline := env("JIRA_TEAM_MAPPING_YAML", "")
	file := env("JIRA_TEAM_MAPPING_FILE", "")

	var raw []byte
	switch {
	case inline != "":
		raw = []byte(inline)
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading JIRA_TEAM_MAPPING_FILE %q: %w", file, err)
		}
		raw = b
	default:
		return nil, fmt.Errorf("no team mapping configured: set JIRA_TEAM_MAPPING_YAML or JIRA_TEAM_MAPPING_FILE")
	}

	// UnmarshalStrict honors the json struct tags and rejects unknown fields.
	var m TeamMapping
	if err := yaml.UnmarshalStrict(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing team mapping: %w", err)
	}
	// Normalize team keys to lower-case for case-insensitive lookup. Reject
	// collisions (e.g. "DOSD" and "dosd") so routing is deterministic rather
	// than dependent on map iteration order.
	norm := make(map[string]TeamConfig, len(m.Teams))
	for name, cfg := range m.Teams {
		key := strings.ToLower(strings.TrimSpace(name))
		if _, dup := norm[key]; dup {
			return nil, fmt.Errorf("duplicate team %q after case-insensitive normalization", key)
		}
		norm[key] = cfg
	}
	m.Teams = norm
	m.DefaultTeam = strings.ToLower(strings.TrimSpace(m.DefaultTeam))
	return &m, nil
}

// reservedFields are issue fields the server sets itself; a team's custom
// `fields` map must not contain them, or it could override routing/identity
// (e.g. redirect a ticket to another project or change the reporter).
var reservedFields = map[string]struct{}{
	"project": {}, "issuetype": {}, "summary": {}, "description": {},
	"reporter": {}, "assignee": {}, "priority": {}, "labels": {},
	"components": {}, "duedate": {}, "parent": {},
}

func (m *TeamMapping) validate() error {
	byProject := make(map[string]string, len(m.Teams)) // normalized projectKey -> team
	for name, cfg := range m.Teams {
		key := strings.TrimSpace(cfg.ProjectKey)
		if key == "" {
			return fmt.Errorf("team %q has no projectKey", name)
		}
		for id := range cfg.Fields {
			if _, bad := reservedFields[strings.ToLower(strings.TrimSpace(id))]; bad {
				return fmt.Errorf("team %q: fields.%s is a reserved core field and cannot be set via the mapping", name, id)
			}
		}
		// Reject two teams routing to the same project: resolveProject picks a
		// team by project key, so a collision would make the applied defaults
		// (labels/components/fields) depend on Go map iteration order.
		norm := strings.ToUpper(key)
		if other, dup := byProject[norm]; dup {
			a, b := name, other
			if a > b {
				a, b = b, a
			}
			return fmt.Errorf("teams %q and %q both map to project %q; each project may be routed by at most one team", a, b, norm)
		}
		byProject[norm] = name
	}
	if m.DefaultTeam != "" {
		if _, ok := m.Teams[m.DefaultTeam]; !ok {
			return fmt.Errorf("defaultTeam %q is not present in teams", m.DefaultTeam)
		}
	}
	return nil
}

// Lookup returns the config for a team name (case-insensitive).
func (m *TeamMapping) Lookup(name string) (TeamConfig, bool) {
	cfg, ok := m.Teams[strings.ToLower(strings.TrimSpace(name))]
	return cfg, ok
}

// ProjectKeyList returns the project keys reachable via the mapping, upper-
// cased. Config load rejects two teams routing to the same project, so the
// list has no duplicates.
func (m *TeamMapping) ProjectKeyList() []string {
	keys := make([]string, 0, len(m.Teams))
	for _, cfg := range m.Teams {
		keys = append(keys, strings.ToUpper(cfg.ProjectKey))
	}
	return keys
}
