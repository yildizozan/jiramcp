// Command jiramcp is an MCP server that creates Jira tickets on behalf of a
// named person, routed to a team's project. The subcommand selects the
// transport (`jiramcp` or `jiramcp stdio` for stdio, `jiramcp http` for
// streamable HTTP); all other configuration is supplied via environment
// variables so it runs unmodified in Kubernetes.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"jiramcp/internal/config"
	"jiramcp/internal/health"
	"jiramcp/internal/jira"
	applog "jiramcp/internal/log"
	"jiramcp/internal/mcpserver"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		stop()
		os.Exit(1)
	}
}

// newRootCmd builds the CLI. The root command serves stdio so `jiramcp` alone
// works as a local MCP server; `stdio` and `http` pick the transport explicitly.
func newRootCmd() *cobra.Command {
	serveWith := func(t config.Transport) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error { return run(cmd.Context(), t) }
	}

	root := &cobra.Command{
		Use:   "jiramcp",
		Short: "MCP server that creates and manages Jira tickets routed to a team's project",
		Long: "jiramcp is an MCP server for Jira. Without a subcommand it serves MCP over stdio.\n" +
			"All configuration (Jira credentials, team mapping, addresses) comes from environment variables.",
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          serveWith(config.TransportStdio),
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		&cobra.Command{
			Use:   "stdio",
			Short: "Serve MCP over stdio (default)",
			Args:  cobra.NoArgs,
			RunE:  serveWith(config.TransportStdio),
		},
		&cobra.Command{
			Use:   "http",
			Short: "Serve MCP over streamable HTTP with bearer auth",
			Args:  cobra.NoArgs,
			RunE:  serveWith(config.TransportHTTP),
		},
	)
	return root
}

func run(ctx context.Context, transport config.Transport) error {
	cfg, err := config.Load(transport)
	if err != nil {
		return err
	}

	logger := applog.Setup(cfg.LogLevel, cfg.LogFormat)
	logger.Info("starting jiramcp", "version", version, "config", cfg.Redacted())

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
