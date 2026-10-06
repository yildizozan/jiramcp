// Command jiramcp is an MCP server that creates Jira tickets on behalf of a
// named person, routed to a team's project. The subcommand selects the
// transport (`jiramcp` or `jiramcp stdio` for stdio, `jiramcp http` for
// streamable HTTP); all other configuration is supplied via environment
// variables so it runs unmodified in Kubernetes.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
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

	if err := newRootCmd(run).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		stop()
		os.Exit(1)
	}
}

// newRootCmd builds the CLI. The root command serves stdio so `jiramcp` alone
// works as a local MCP server; `stdio` and `http` pick the transport explicitly.
// serve is injected so tests can check the transport routing without starting
// a server.
func newRootCmd(serve func(context.Context, config.Transport) error) *cobra.Command {
	serveWith := func(t config.Transport) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), t) }
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

	client := jira.NewRESTClient(cfg.BaseURL, string(cfg.AuthMode), cfg.AuthEmail, cfg.APIToken, cfg.PAT, cfg.HTTPTimeout)

	// Verify credentials up front (fail fast).
	checkCtx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
	me, err := client.Myself(checkCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("jira credential check failed: %w", err)
	}
	logger.Info("jira credentials ok", "accountId", me.AccountID, "displayName", me.DisplayName)

	// Health and readiness only matter to an orchestrator probing the HTTP
	// service. A stdio server is a child process of one MCP client; opening a
	// port there would only collide when several clients run it at once.
	if cfg.Transport == config.TransportHTTP {
		probe := func(ctx context.Context) error { _, e := client.Myself(ctx); return e }
		shutdown, err := startHealth(ctx, cfg.HealthAddr, probe, cfg.HTTPTimeout, logger)
		if err != nil {
			return err
		}
		defer shutdown()
	}

	// Over stdio the token belongs to the developer running the binary, so
	// tickets default to them as reporter. The HTTP service authenticates as a
	// shared service account, which must never become the default reporter.
	self := ""
	if cfg.Transport == config.TransportStdio {
		self = me.Ref()
	}
	srv := mcpserver.New(cfg, client, self, logger, version)

	switch cfg.Transport {
	case config.TransportStdio:
		return srv.RunStdio(ctx)
	default:
		return srv.RunHTTP(ctx)
	}
}

// startHealth serves /healthz and /readyz on addr, with readiness cached by a
// background probe loop. The listener is opened before it returns, so a busy
// port fails startup instead of leaving the probes unanswered. shutdown stops
// the server.
func startHealth(ctx context.Context, addr string, probe func(context.Context) error,
	timeout time.Duration, logger *slog.Logger) (shutdown func(), err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("health server: %w", err)
	}
	checker := health.NewChecker(probe, 30*time.Second, timeout, logger)
	go checker.Run(ctx)

	srv := &http.Server{Handler: checker.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("starting health server", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("health server failed", "error", err)
		}
	}()
	return func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}, nil
}
