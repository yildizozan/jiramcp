package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testChecker(probeErr error) *Checker {
	return NewChecker(
		func(context.Context) error { return probeErr },
		time.Second, time.Second,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func get(t *testing.T, c *Checker, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	c.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// /readyz must not leak the upstream (Jira) error detail to its unauthenticated
// caller; the body is generic and the cause is logged server-side only.
func TestReadyz_NotReadyIsGenericAndDoesNotLeak(t *testing.T) {
	upstream := `Get "https://jira.yildizozan.com/rest/api/2/myself": dial tcp 10.0.0.5:443: connect: connection refused`
	c := testChecker(errors.New(upstream))
	c.refresh(context.Background()) // probe fails -> not ready

	rr := get(t, c, "/readyz")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "not ready") {
		t.Fatalf("expected status \"not ready\", got %s", body)
	}
	for _, leak := range []string{"jira.yildizozan.com", "10.0.0.5", "connection refused", "rest/api", "myself"} {
		if strings.Contains(body, leak) {
			t.Fatalf("/readyz leaked upstream detail %q: %s", leak, body)
		}
	}
}

func TestReadyz_OKWhenReady(t *testing.T) {
	c := testChecker(nil)
	c.refresh(context.Background()) // probe succeeds -> ready

	rr := get(t, c, "/readyz")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ready") {
		t.Fatalf("expected ready, got %s", rr.Body.String())
	}
}

// Liveness must stay 200 even when the dependency is down (kubelet restarts only
// on a hung process, not on a Jira outage).
func TestHealthzAndLivez_AlwaysOK(t *testing.T) {
	c := testChecker(errors.New("jira down"))
	c.refresh(context.Background())
	for _, path := range []string{"/healthz", "/livez"} {
		rr := get(t, c, path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s should be 200 regardless of dependency, got %d", path, rr.Code)
		}
	}
}

func TestReady_ReflectsLatestProbe(t *testing.T) {
	c := testChecker(nil)
	c.refresh(context.Background())
	if !c.Ready() {
		t.Fatal("expected ready after a successful probe")
	}
	// Flip the probe to failing and refresh again.
	c.probe = func(context.Context) error { return errors.New("boom") }
	c.refresh(context.Background())
	if c.Ready() {
		t.Fatal("expected not ready after a failing probe")
	}
}
