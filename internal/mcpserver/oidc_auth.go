package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/mark3labs/mcp-go/server"

	"jiramcp/internal/access"
)

// OIDCVerifier checks OIDC access tokens: signature, issuer, audience and
// expiry. Dex issues its access tokens as signed JWTs in the ID token format,
// so the ID token verifier applies to them.
type OIDCVerifier struct {
	v *oidc.IDTokenVerifier
}

// NewOIDCVerifier fetches the issuer's discovery document and keys and
// returns a verifier for tokens issued to audience (the client id).
func NewOIDCVerifier(ctx context.Context, issuer, audience string) (*OIDCVerifier, error) {
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", issuer, err)
	}
	return &OIDCVerifier{v: p.Verifier(&oidc.Config{ClientID: audience})}, nil
}

// verify returns the token's claims and expiry.
func (o *OIDCVerifier) verify(ctx context.Context, raw string) (map[string]any, time.Time, error) {
	tok, err := o.v.Verify(ctx, raw)
	if err != nil {
		return nil, time.Time{}, err
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return nil, time.Time{}, err
	}
	return claims, tok.Expiry, nil
}

// Option configures a Server.
type Option func(*Server)

// WithOIDC sets the verifier used when MCP_AUTH_MODE=oidc.
func WithOIDC(v *OIDCVerifier) Option {
	return func(s *Server) { s.oidc = v }
}

// errInvalidToken marks a token the issuer did not sign, or that is expired
// or meant for another audience: the caller must authenticate again (401).
var errInvalidToken = errors.New("invalid or expired access token")

// accessDenied is a valid token whose caller may not use the service (403).
type accessDenied struct{ reason string }

func (e *accessDenied) Error() string { return e.reason }

// oidcAuthMiddleware authenticates each HTTP caller with an OIDC access token
// and puts a principal for them into the request context. Jira is called as
// the service account; the caller's groups decide the projects.
func (s *Server) oidcAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" || strings.ContainsAny(raw, " \t") {
			s.oidcUnauthorized(w, "send an access token as Authorization: Bearer ...")
			return
		}
		p, err := s.callers.get(r.Context(), "Bearer "+raw, s.verifyOIDC)
		if err != nil {
			var denied *accessDenied
			switch {
			case errors.Is(err, errInvalidToken):
				s.oidcUnauthorized(w, errInvalidToken.Error())
			case errors.As(err, &denied):
				http.Error(w, "forbidden: "+denied.reason, http.StatusForbidden)
			default:
				s.logger.Warn("cannot authorize oidc caller", "error", err.Error())
				http.Error(w, "cannot authorize the caller", http.StatusBadGateway)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// verifyOIDC checks the token and builds the caller's principal: projects from
// their groups (narrowed by any server-wide policy), their Jira user as the
// default reporter, and on-behalf filing only for OIDC_ON_BEHALF_GROUP.
func (s *Server) verifyOIDC(ctx context.Context, header string) (*principal, time.Time, error) {
	o := s.cfg.OIDC
	claims, expiry, err := s.oidc.verify(ctx, strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		s.logger.Debug("oidc token rejected", "error", err.Error())
		return nil, time.Time{}, errInvalidToken
	}
	user, _ := claims[o.UserClaim].(string)
	if strings.TrimSpace(user) == "" {
		return nil, time.Time{}, &accessDenied{fmt.Sprintf("the token has no %q claim", o.UserClaim)}
	}
	groups := stringList(claims[o.GroupsClaim])
	policy := access.Intersect(access.Projects(o.ProjectsFor(groups)...), s.base.policy)
	if len(policy.Projects()) == 0 {
		return nil, time.Time{}, &accessDenied{fmt.Sprintf("none of the groups of %s grant access to a Jira project", user)}
	}
	self, err := resolveUserID(ctx, s.base.client, user, s.dc())
	if err != nil {
		var ure *UserResolutionError
		if errors.As(err, &ure) {
			return nil, time.Time{}, &accessDenied{fmt.Sprintf("%s does not match exactly one active Jira user", user)}
		}
		return nil, time.Time{}, err
	}
	onBehalf := o.OnBehalfGroup != "" && slices.Contains(groups, o.OnBehalfGroup)
	s.logger.Debug("oidc caller verified", "caller", user, "jiraUser", self, "projects", policy.Projects())
	return &principal{client: s.base.client, policy: policy, self: self, onBehalf: onBehalf, caller: user}, expiry, nil
}

// stringList reads a claim that holds a list of strings (or a single string).
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// oidcUnauthorized answers 401 and, when OIDC_RESOURCE_URL is set, points the
// client at the protected resource metadata so it can start the OAuth flow.
func (s *Server) oidcUnauthorized(w http.ResponseWriter, msg string) {
	challenge := "Bearer"
	if u := s.resourceMetadataURL(); u != "" {
		challenge += fmt.Sprintf(` resource_metadata=%q`, u)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, "unauthorized: "+msg, http.StatusUnauthorized)
}

// resourceMetadataURL is the absolute URL of the RFC 9728 metadata document,
// or "" when OIDC_RESOURCE_URL is not set.
func (s *Server) resourceMetadataURL() string {
	if s.cfg.OIDC == nil || s.cfg.OIDC.ResourceURL == "" {
		return ""
	}
	u, err := url.Parse(s.cfg.OIDC.ResourceURL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + server.ProtectedResourceMetadataPath(s.cfg.OIDC.ResourceURL)
}

// mountResourceMetadata serves the RFC 9728 document that tells MCP clients
// which issuer to get a token from and which scopes to ask for.
func (s *Server) mountResourceMetadata(mux *http.ServeMux) {
	if s.resourceMetadataURL() == "" {
		return
	}
	o := s.cfg.OIDC
	mux.Handle(server.ProtectedResourceMetadataPath(o.ResourceURL),
		server.NewProtectedResourceMetadataHandler(server.ProtectedResourceMetadataConfig{
			Resource:               o.ResourceURL,
			AuthorizationServers:   []string{o.IssuerURL},
			ScopesSupported:        []string{"openid", "email", "profile", "groups", "offline_access"},
			BearerMethodsSupported: []string{"header"},
			ResourceName:           name,
		}))
}
