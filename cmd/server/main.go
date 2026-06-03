// Command jiramcp is an MCP server that creates Jira tickets on behalf of a
// named person, routed to a team's project. All configuration is supplied via
// environment variables so it runs unmodified in Kubernetes.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jiramcp/internal/config"
	"jiramcp/internal/health"
	"jiramcp/internal/jira"
	applog "jiramcp/internal/log"
	"jiramcp/internal/mcpserver"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := applog.Setup(cfg.LogLevel, cfg.LogFormat)
	logger.Info("starting jiramcp", "version", version, "config", cfg.Redacted())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := jira.NewCloud(cfg.BaseURL, string(cfg.AuthMode), cfg.AuthEmail, cfg.APIToken, cfg.PAT, cfg.HTTPTimeout)

	// Verify credentials up front (fail fast).
	checkCtx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
	me, err := client.Myself(checkCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("jira credential check failed: %w", err)
	}
	logger.Info("jira credentials ok", "accountId", me.AccountID, "displayName", me.DisplayName)

	// Health server with cached readiness (background probe loop).
	checker := health.NewChecker(
		func(ctx context.Context) error { _, e := client.Myself(ctx); return e },
		30*time.Second, cfg.HTTPTimeout, logger,
	)
	go checker.Run(ctx)

	healthSrv := &http.Server{Addr: cfg.HealthAddr, Handler: checker.Handler()}
	go func() {
		logger.Info("starting health server", "addr", cfg.HealthAddr)
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("health server failed", "error", err)
		}
	}()
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = healthSrv.Shutdown(sctx)
	}()

	srv := mcpserver.New(cfg, client, logger)

	switch cfg.Transport {
	case config.TransportStdio:
		return srv.RunStdio(ctx)
	default:
		return srv.RunHTTP(ctx)
	}
}
