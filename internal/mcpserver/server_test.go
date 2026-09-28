package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// authProbe runs one request through the auth middleware and reports the
// status code and whether the wrapped handler was reached.
func authProbe(t *testing.T, s *Server, authorization string) (int, http.Header, bool) {
	t.Helper()
	reached := false
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Header(), reached
}

func TestAuthMiddleware_RejectsBadCredentials(t *testing.T) {
	cfg := testCfg()
	cfg.AllowUnauthenticated = false
	cfg.AuthToken = "secret"
	s := newServer(&fakeClient{}, cfg)

	cases := map[string]string{
		"missing header": "",
		"wrong token":    "Bearer secreT",
		"other scheme":   "Basic secret",
		"no scheme":      "secret",
		"token prefix":   "Bearer secre",
		"token suffixed": "Bearer secret2",
		"lowercase kind": "bearer secret",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			code, hdr, reached := authProbe(t, s, header)
			if code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			if reached {
				t.Fatal("wrapped handler must not run for a rejected request")
			}
			if got := hdr.Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}
}

func TestAuthMiddleware_AcceptsExactToken(t *testing.T) {
	cfg := testCfg()
	cfg.AllowUnauthenticated = false
	cfg.AuthToken = "secret"
	s := newServer(&fakeClient{}, cfg)

	code, _, reached := authProbe(t, s, "Bearer secret")
	if code != http.StatusOK || !reached {
		t.Fatalf("status = %d, reached = %v; want 200 and the handler reached", code, reached)
	}
}

func TestAuthMiddleware_DisabledPassesThrough(t *testing.T) {
	cfg := testCfg() // AllowUnauthenticated is true
	cfg.AuthToken = "secret"
	s := newServer(&fakeClient{}, cfg)

	code, _, reached := authProbe(t, s, "")
	if code != http.StatusOK || !reached {
		t.Fatalf("status = %d, reached = %v; want the request passed through", code, reached)
	}
}
