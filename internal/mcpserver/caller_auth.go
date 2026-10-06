package mcpserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"jiramcp/internal/jira"
)

// callerAuthorizer derives a client that acts with a caller's own Jira
// credentials. *jira.RESTClient implements it.
type callerAuthorizer interface {
	WithAuthorization(header string) jira.Client
}

// Credential cache policy: a verified caller is trusted for callerTTL before
// Jira is asked again, so a revoked token stops working within that time.
const (
	callerTTL        = 5 * time.Minute
	maxCachedCallers = 1024
)

// callerCache remembers callers whose Jira credentials were verified, keyed by
// a SHA-256 of the Authorization header so no raw token is kept as a key.
type callerCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[[32]byte]cachedCaller
}

type cachedCaller struct {
	p       *principal
	expires time.Time
}

func newCallerCache() *callerCache {
	return &callerCache{now: time.Now, entries: map[[32]byte]cachedCaller{}}
}

// get returns the principal for header, verifying it with verify on a miss or
// after expiry. A failed verification is not cached.
func (c *callerCache) get(ctx context.Context, header string,
	verify func(context.Context, string) (*principal, error)) (*principal, error) {
	key := sha256.Sum256([]byte(header))
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && c.now().Before(e.expires) {
		return e.p, nil
	}

	p, err := verify(ctx, header)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCachedCallers {
		c.evict()
	}
	c.entries[key] = cachedCaller{p: p, expires: c.now().Add(callerTTL)}
	return p, nil
}

// evict drops expired entries, and every entry if none had expired, so the
// cache stays bounded. Dropped callers are simply verified again.
func (c *callerCache) evict() {
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= maxCachedCallers {
		c.entries = map[[32]byte]cachedCaller{}
	}
}

// callerScheme is the Authorization scheme a caller's Jira credentials use:
// a Bearer PAT on Server/DC, Basic email:api_token on Cloud.
func (s *Server) callerScheme() string {
	if s.dc() {
		return "Bearer"
	}
	return "Basic"
}

// callerAuthMiddleware authenticates each HTTP caller with their own Jira
// credentials and puts a principal acting as them into the request context.
func (s *Server) callerAuthMiddleware(next http.Handler) http.Handler {
	scheme := s.callerScheme()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred, ok := strings.CutPrefix(r.Header.Get("Authorization"), scheme+" ")
		if !ok || cred == "" || strings.ContainsAny(cred, " \t") {
			unauthorized(w, scheme, "send your Jira credentials as Authorization: "+scheme+" ...")
			return
		}
		p, err := s.callers.get(r.Context(), scheme+" "+cred, s.verifyCaller)
		if err != nil {
			var apiErr *jira.APIError
			if errors.As(err, &apiErr) &&
				(apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
				unauthorized(w, scheme, "Jira rejected the credentials")
				return
			}
			// Never echo err: it can carry details of the caller's request.
			s.logger.Warn("cannot verify caller credentials with Jira", "error", err.Error())
			http.Error(w, "cannot verify credentials with Jira", http.StatusBadGateway)
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// verifyCaller asks Jira who the credentials belong to and builds the
// principal that acts as them.
func (s *Server) verifyCaller(ctx context.Context, header string) (*principal, error) {
	a, ok := s.base.client.(callerAuthorizer)
	if !ok {
		return nil, errors.New("jira client cannot act with caller credentials")
	}
	client := a.WithAuthorization(header)
	me, err := client.Myself(ctx)
	if err != nil {
		return nil, err
	}
	self := me.Ref()
	if self == "" {
		return nil, errors.New("jira returned no usable user id for the caller")
	}
	s.logger.Debug("caller verified", "caller", self)
	return &principal{client: client, policy: s.base.policy, self: self}, nil
}

func unauthorized(w http.ResponseWriter, scheme, msg string) {
	w.Header().Set("WWW-Authenticate", scheme)
	http.Error(w, "unauthorized: "+msg, http.StatusUnauthorized)
}
