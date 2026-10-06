// Package mcpserver builds the MCP server, registers the Jira tools, and runs
// the selected transport (streamable HTTP or stdio).
package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"jiramcp/internal/config"
	"jiramcp/internal/jira"
)

const (
	name = "jiramcp"

	defaultShutdownTimeout = 10 * time.Second
)

// Server owns the MCP server and its dependencies.
type Server struct {
	base    *principal // used when the call's context carries none
	callers *callerCache
	teams   *config.TeamMapping
	cfg     *config.Config
	logger  *slog.Logger
	mcp     *server.MCPServer
}

// New builds the MCP server and registers all tools. self is the Jira user id
// the client authenticates as when that is the caller's own account (a local
// developer's token), or "" for a shared service account. version is reported
// to clients as serverInfo.version; the binary passes its build version so
// both `jiramcp --version` and MCP clients see the same value.
func New(cfg *config.Config, client jira.Client, self string, logger *slog.Logger, version string) *Server {
	s := &Server{
		base:    &principal{client: client, policy: cfg.ProjectPolicy(), self: self},
		callers: newCallerCache(),
		teams:   cfg.Teams,
		cfg:     cfg,
		logger:  logger,
	}
	s.mcp = server.NewMCPServer(
		name, version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)
	s.mcp.AddTool(createTicketTool(), s.handleCreateTicket)
	s.mcp.AddTool(getTicketTool(), s.handleGetTicket)
	s.mcp.AddTool(updateTicketTool(), s.handleUpdateTicket)
	s.mcp.AddTool(addCommentTool(), s.handleAddComment)
	s.mcp.AddTool(listCommentsTool(), s.handleListComments)
	s.mcp.AddTool(updateCommentTool(), s.handleUpdateComment)
	s.mcp.AddTool(transitionTool(), s.handleTransition)
	s.mcp.AddTool(searchUsersTool(), s.handleSearchUsers)
	s.mcp.AddTool(listProjectsTool(), s.handleListProjects)
	s.mcp.AddTool(listIssueTypesTool(), s.handleListIssueTypes)
	return s
}

// dc reports whether the server talks to Jira Server/Data Center, where users
// are referenced by username instead of a Cloud accountId.
func (s *Server) dc() bool { return s.cfg.AuthMode == config.AuthDC }

// RunStdio serves MCP over stdio until ctx is cancelled.
func (s *Server) RunStdio(ctx context.Context) error {
	s.logger.Info("starting MCP stdio transport")
	return server.ServeStdio(s.mcp, server.WithStdioContextFunc(func(_ context.Context) context.Context {
		return ctx
	}))
}

// RunHTTP serves MCP over streamable HTTP with bearer auth until ctx is
// cancelled. Stateless mode is used so the service scales horizontally without
// sticky sessions.
func (s *Server) RunHTTP(ctx context.Context) error {
	httpSrv := &http.Server{
		Addr:    s.cfg.HTTPAddr,
		Handler: s.httpHandler(),
		// Slowloris / resource-exhaustion hardening. WriteTimeout is intentionally
		// left unset so streamable-HTTP (SSE) responses are not truncated.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("starting MCP http transport",
			"addr", s.cfg.HTTPAddr, "path", s.cfg.HTTPPath,
			"authMode", s.cfg.MCPAuth,
			"authenticated", s.cfg.ActsAsCaller() || !s.cfg.AllowUnauthenticated)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// httpHandler serves the MCP endpoint behind the configured caller
// authentication.
func (s *Server) httpHandler() http.Handler {
	streamable := server.NewStreamableHTTPServer(
		s.mcp,
		server.WithEndpointPath(s.cfg.HTTPPath),
		server.WithStateLess(true),
	)
	auth := s.authMiddleware
	if s.cfg.ActsAsCaller() {
		auth = s.callerAuthMiddleware
	}
	mux := http.NewServeMux()
	mux.Handle(s.cfg.HTTPPath, auth(streamable))
	return mux
}

// authMiddleware enforces a static bearer token unless explicitly disabled.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	if s.cfg.AllowUnauthenticated {
		s.logger.Warn("MCP HTTP authentication is DISABLED (MCP_ALLOW_UNAUTHENTICATED=true); do not expose this endpoint")
		return next
	}
	want := []byte("Bearer " + s.cfg.AuthToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
