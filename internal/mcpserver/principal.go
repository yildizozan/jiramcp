package mcpserver

import (
	"context"

	"jiramcp/internal/access"
	"jiramcp/internal/jira"
)

// principal is everything a tool call needs to know about who is calling: the
// Jira client that acts for the call and the projects the call may touch.
// stdio and the shared-token HTTP mode use one principal for every call; the
// per-caller HTTP modes build one per request and put it in the context.
type principal struct {
	client jira.Client
	policy access.Policy
	// self is the caller's own Jira user id (accountId on Cloud, username on
	// Server/DC) when the client acts as the caller, so create can default the
	// reporter to them. It is empty when the client is a shared service
	// account: then create must be told who the reporter is.
	self string
	// onBehalf reports whether the caller may file tickets with another
	// person as reporter. Jira's Modify Reporter permission applies on top.
	onBehalf bool
	// caller identifies who is calling, for the audit log; "" when the server
	// cannot tell callers apart (one shared token).
	caller string
}

type principalKey struct{}

// withPrincipal returns a context carrying p for the tool handlers.
func withPrincipal(ctx context.Context, p *principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principal returns the principal for this call: the one in the context, or
// the server's default when the transport set none.
func (s *Server) principal(ctx context.Context) *principal {
	if p, ok := ctx.Value(principalKey{}).(*principal); ok {
		return p
	}
	return s.base
}
