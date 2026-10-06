package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

// fakeIssuer is a minimal OIDC provider, shaped like Dex: a discovery
// document, a JWKS, and RS256-signed JWT access tokens.
type fakeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fi := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fi.srv.URL,
			"jwks_uri":                              fi.srv.URL + "/keys",
			"authorization_endpoint":                fi.srv.URL + "/auth",
			"token_endpoint":                        fi.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	fi.srv = httptest.NewServer(mux)
	t.Cleanup(fi.srv.Close)
	return fi
}

// token signs claims with key (the issuer's own key unless another is given),
// filling iss/aud/exp defaults.
func (fi *fakeIssuer) token(t *testing.T, claims map[string]any, key ...*rsa.PrivateKey) string {
	t.Helper()
	full := map[string]any{"iss": fi.srv.URL, "aud": "jiramcp", "exp": time.Now().Add(time.Hour).Unix(), "sub": "u1"}
	for k, v := range claims {
		full[k] = v
	}
	signKey := fi.key
	if len(key) > 0 {
		signKey = key[0]
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(full)
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// oidcServer builds an OIDC-mode server on Data Center whose service account
// resolves ozan@ and ayse@ to Jira usernames.
func oidcServer(t *testing.T, fi *fakeIssuer) (*Server, *fakeClient) {
	t.Helper()
	cfg := &config.Config{
		AuthMode:         config.AuthDC,
		MCPAuth:          config.MCPAuthOIDC,
		Transport:        config.TransportHTTP,
		HTTPPath:         "/mcp",
		DefaultIssueType: "Task",
		Teams:            &config.TeamMapping{Teams: map[string]config.TeamConfig{}},
		OIDC: &config.OIDC{
			IssuerURL: fi.srv.URL, Audience: "jiramcp",
			UserClaim: "email", GroupsClaim: "groups",
			OnBehalfGroup: "leads",
			ResourceURL:   "https://mcp.example.com/mcp",
			GroupProjects: map[string][]string{"pay-devs": {"PAY"}, "dosd-devs": {"DOSD"}},
		},
	}
	base := &fakeClient{
		usersByQuery: map[string][]jira.User{
			"ozan@yildizozan.com": {{Name: "ozan", Active: true}},
			"ayse@yildizozan.com": {{Name: "ayse", Active: true}},
		},
		issueTypes: []jira.IssueType{{ID: "1", Name: "Task"}},
		metaErr:    io.EOF, // skip createmeta pre-validation
	}
	v, err := NewOIDCVerifier(context.Background(), fi.srv.URL, "jiramcp")
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	return New(cfg, base, "", slog.New(slog.NewTextHandler(io.Discard, nil)), "test", WithOIDC(v)), base
}

func probeOIDC(t *testing.T, s *Server, authorization string) (*httptest.ResponseRecorder, *principal) {
	t.Helper()
	var seen *principal
	h := s.oidcAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = s.principal(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, seen
}

func TestOIDC_ValidTokenBuildsCallerPrincipal(t *testing.T) {
	fi := newFakeIssuer(t)
	s, base := oidcServer(t, fi)
	tok := fi.token(t, map[string]any{"email": "ozan@yildizozan.com", "groups": []string{"pay-devs", "dosd-devs"}})

	rr, p := probeOIDC(t, s, "Bearer "+tok)
	if rr.Code != http.StatusOK || p == nil {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if p.self != "ozan" || p.caller != "ozan@yildizozan.com" || p.onBehalf {
		t.Fatalf("principal = self %q caller %q onBehalf %v", p.self, p.caller, p.onBehalf)
	}
	if strings.Join(p.policy.Projects(), ",") != "DOSD,PAY" {
		t.Fatalf("projects = %v, want the union of the groups' projects", p.policy.Projects())
	}
	if p.client != jira.Client(base) {
		t.Fatal("oidc callers must reach Jira through the service account")
	}
}

func TestOIDC_RejectsBadTokens(t *testing.T) {
	fi := newFakeIssuer(t)
	s, _ := oidcServer(t, fi)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ok := map[string]any{"email": "ozan@yildizozan.com", "groups": []string{"pay-devs"}}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range ok {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]string{
		"missing header": "",
		"expired":        "Bearer " + fi.token(t, with(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})),
		"wrong audience": "Bearer " + fi.token(t, with(map[string]any{"aud": "other-client"})),
		"wrong issuer":   "Bearer " + fi.token(t, with(map[string]any{"iss": "https://evil.example.com"})),
		"forged":         "Bearer " + fi.token(t, ok, otherKey),
		"not a jwt":      "Bearer abc.def.ghi",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			rr, p := probeOIDC(t, s, header)
			if rr.Code != http.StatusUnauthorized || p != nil {
				t.Fatalf("status %d reached=%v; want 401", rr.Code, p != nil)
			}
			want := `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`
			if got := rr.Header().Get("WWW-Authenticate"); got != want {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
			}
		})
	}
}

func TestOIDC_ForbidsCallersWithoutAccess(t *testing.T) {
	fi := newFakeIssuer(t)
	s, _ := oidcServer(t, fi)
	cases := map[string]map[string]any{
		"no mapped group":    {"email": "ozan@yildizozan.com", "groups": []string{"marketing"}},
		"no user claim":      {"groups": []string{"pay-devs"}},
		"unknown jira user":  {"email": "ghost@yildizozan.com", "groups": []string{"pay-devs"}},
		"groups not in list": {"email": "ozan@yildizozan.com"},
	}
	for name, claims := range cases {
		t.Run(name, func(t *testing.T) {
			rr, p := probeOIDC(t, s, "Bearer "+fi.token(t, claims))
			if rr.Code != http.StatusForbidden || p != nil {
				t.Fatalf("status %d reached=%v; want 403", rr.Code, p != nil)
			}
		})
	}
}

func TestOIDC_CacheNeverOutlivesToken(t *testing.T) {
	fi := newFakeIssuer(t)
	s, _ := oidcServer(t, fi)
	exp := time.Now().Add(30 * time.Second)
	tok := fi.token(t, map[string]any{"email": "ozan@yildizozan.com", "groups": []string{"pay-devs"}, "exp": exp.Unix()})
	if rr, _ := probeOIDC(t, s, "Bearer "+tok); rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	for _, e := range s.callers.entries {
		if e.expires.After(exp) {
			t.Fatalf("cache entry expires %v, after the token's own expiry %v", e.expires, exp)
		}
	}
}

// callCreate runs create_jira_ticket through the real HTTP handler.
func callCreate(t *testing.T, url, token, args string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_jira_ticket","arguments":` + args + `}}`
	req, _ := http.NewRequest(http.MethodPost, url+"/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return string(out)
}

func TestOIDC_EndToEndReporterRules(t *testing.T) {
	fi := newFakeIssuer(t)
	s, base := oidcServer(t, fi)
	srv := httptest.NewServer(s.httpHandler())
	defer srv.Close()

	dev := fi.token(t, map[string]any{"email": "ozan@yildizozan.com", "groups": []string{"pay-devs"}})
	lead := fi.token(t, map[string]any{"email": "ayse@yildizozan.com", "groups": []string{"pay-devs", "leads"}})

	// Without reporter: filed as the caller, no reporter field sent.
	out := callCreate(t, srv.URL, dev, `{"summary":"x","project":"PAY"}`)
	if !strings.Contains(out, "for reporter ozan") || base.created == nil || base.created.ReporterID != "" {
		t.Fatalf("default reporter must be the caller: %s (in=%+v)", out, base.created)
	}

	// Outside the caller's projects.
	base.created = nil
	out = callCreate(t, srv.URL, dev, `{"summary":"x","project":"DOSD"}`)
	if !strings.Contains(out, "is not allowed") || base.created != nil {
		t.Fatalf("DOSD must be refused for pay-devs: %s", out)
	}

	// On behalf of someone else: refused for a plain member...
	out = callCreate(t, srv.URL, dev, `{"summary":"x","project":"PAY","reporter":"ayse@yildizozan.com"}`)
	if !strings.Contains(out, "only file tickets as yourself") || base.created != nil {
		t.Fatalf("a non-lead must not file on behalf of others: %s", out)
	}
	// ...allowed for the on-behalf group.
	out = callCreate(t, srv.URL, lead, `{"summary":"x","project":"PAY","reporter":"ozan@yildizozan.com"}`)
	if base.created == nil || base.created.ReporterID != "ozan" {
		t.Fatalf("a lead may file on behalf of others: %s (in=%+v)", out, base.created)
	}
}

func TestOIDC_ServesProtectedResourceMetadata(t *testing.T) {
	fi := newFakeIssuer(t)
	s, _ := oidcServer(t, fi)
	srv := httptest.NewServer(s.httpHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if meta.Resource != "https://mcp.example.com/mcp" || len(meta.AuthorizationServers) != 1 || meta.AuthorizationServers[0] != fi.srv.URL {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if !strings.Contains(strings.Join(meta.ScopesSupported, " "), "groups") {
		t.Fatalf("clients must be told to ask for the groups scope: %v", meta.ScopesSupported)
	}
}
