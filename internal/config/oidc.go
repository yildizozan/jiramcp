package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// loadOIDC reads the OIDC settings and the group -> projects mapping from
// OIDC_GROUP_PROJECTS_YAML (inline) or OIDC_GROUP_PROJECTS_FILE (path).
func loadOIDC() (*OIDC, error) {
	o := &OIDC{
		IssuerURL:     strings.TrimRight(strings.TrimSpace(env("OIDC_ISSUER_URL", "")), "/"),
		Audience:      strings.TrimSpace(env("OIDC_AUDIENCE", "")),
		UserClaim:     strings.TrimSpace(env("OIDC_USER_CLAIM", "email")),
		GroupsClaim:   strings.TrimSpace(env("OIDC_GROUPS_CLAIM", "groups")),
		OnBehalfGroup: strings.TrimSpace(env("OIDC_ON_BEHALF_GROUP", "")),
		ResourceURL:   strings.TrimSpace(env("OIDC_RESOURCE_URL", "")),
	}

	var raw []byte
	if inline := env("OIDC_GROUP_PROJECTS_YAML", ""); inline != "" {
		raw = []byte(inline)
	} else if file := env("OIDC_GROUP_PROJECTS_FILE", ""); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading OIDC_GROUP_PROJECTS_FILE %q: %w", file, err)
		}
		raw = b
	}
	var groups map[string][]string
	if err := yaml.UnmarshalStrict(raw, &groups); err != nil {
		return nil, fmt.Errorf("parsing OIDC group projects: %w", err)
	}
	o.GroupProjects = map[string][]string{}
	for g, keys := range groups {
		g = strings.TrimSpace(g)
		for _, k := range keys {
			if k = strings.ToUpper(strings.TrimSpace(k)); k != "" {
				o.GroupProjects[g] = append(o.GroupProjects[g], k)
			}
		}
	}
	return o, nil
}

func (o *OIDC) validate() error {
	u, err := url.Parse(o.IssuerURL)
	if o.IssuerURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("OIDC_ISSUER_URL must be an absolute http(s) URL, got %q", o.IssuerURL)
	}
	if o.Audience == "" {
		return fmt.Errorf("OIDC_AUDIENCE is required: the client id the access tokens are issued for")
	}
	if o.UserClaim == "" || o.GroupsClaim == "" {
		return fmt.Errorf("OIDC_USER_CLAIM and OIDC_GROUPS_CLAIM must not be empty")
	}
	if len(o.GroupProjects) == 0 {
		return fmt.Errorf("oidc mode needs a group -> projects mapping " +
			"(OIDC_GROUP_PROJECTS_YAML or OIDC_GROUP_PROJECTS_FILE)")
	}
	if o.ResourceURL != "" {
		r, err := url.Parse(o.ResourceURL)
		if err != nil || (r.Scheme != "https" && r.Scheme != "http") || r.Host == "" {
			return fmt.Errorf("OIDC_RESOURCE_URL must be an absolute http(s) URL, got %q", o.ResourceURL)
		}
	}
	return nil
}

// ProjectsFor returns the projects the given groups grant, without duplicates.
func (o *OIDC) ProjectsFor(groups []string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, g := range groups {
		for _, k := range o.GroupProjects[g] {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}
