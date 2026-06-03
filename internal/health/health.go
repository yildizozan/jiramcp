// Package health provides liveness and readiness HTTP handlers. Readiness
// reflects a cached Jira reachability state refreshed by a background loop, so
// kubelet probes never hammer Atlassian directly.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Checker probes a dependency and caches the latest result.
type Checker struct {
	ready   atomic.Bool
	lastErr atomic.Pointer[string]
	probe   func(ctx context.Context) error
	logger  *slog.Logger

	interval time.Duration
	timeout  time.Duration
}

// NewChecker builds a Checker. probe should perform a cheap reachability check
// (e.g. Jira /myself). interval is the steady-state refresh period.
func NewChecker(probe func(ctx context.Context) error, interval, timeout time.Duration, logger *slog.Logger) *Checker {
	return &Checker{probe: probe, interval: interval, timeout: timeout, logger: logger}
}

// Run refreshes readiness until ctx is cancelled. It probes immediately, then
// backs off when unhealthy (fast retry) and relaxes to interval when healthy.
func (c *Checker) Run(ctx context.Context) {
	backoff := time.Second
	for {
		c.refresh(ctx)

		wait := c.interval
		if !c.ready.Load() {
			wait = backoff
			if backoff < 15*time.Second {
				backoff *= 2
			}
		} else {
			backoff = time.Second
		}

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (c *Checker) refresh(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	err := c.probe(pctx)
	if err != nil {
		msg := err.Error()
		c.lastErr.Store(&msg)
		if c.ready.Swap(false) {
			c.logger.Warn("readiness degraded", "error", msg)
		}
		return
	}
	c.lastErr.Store(nil)
	if !c.ready.Swap(true) {
		c.logger.Info("readiness restored")
	}
}

// Ready reports the cached readiness state.
func (c *Checker) Ready() bool { return c.ready.Load() }

// Handler returns an http.Handler exposing /healthz, /readyz and /livez.
func (c *Checker) Handler() http.Handler {
	mux := http.NewServeMux()
	// Liveness: process is up; always 200 (kubelet restarts only on hangs).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	// Readiness: reflects cached dependency state.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if c.Ready() {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
			return
		}
		body := map[string]any{"status": "not ready"}
		if p := c.lastErr.Load(); p != nil {
			body["error"] = *p
		}
		writeJSON(w, http.StatusServiceUnavailable, body)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
