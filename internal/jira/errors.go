package jira

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a structured error parsed from a Jira error response. It maps
// common HTTP statuses to actionable messages so tool callers know what to do.
type APIError struct {
	StatusCode int
	// Messages are top-level errorMessages from Jira.
	Messages []string
	// FieldErrors are per-field errors keyed by field id.
	FieldErrors map[string]string
	// RetryAfter is the parsed Retry-After header value in seconds, or -1
	// when the header is absent or unusable.
	RetryAfter int
	// Op is the logical operation that failed (e.g. "create issue").
	Op string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s failed: %s (HTTP %d)", e.Op, hint(e.StatusCode), e.StatusCode)
	if len(e.Messages) > 0 {
		fmt.Fprintf(&b, ": %s", strings.Join(e.Messages, "; "))
	}
	if e.StatusCode == http.StatusTooManyRequests && e.RetryAfter > 0 {
		fmt.Fprintf(&b, "; retry after %ds", e.RetryAfter)
	}
	if len(e.FieldErrors) > 0 {
		parts := make([]string, 0, len(e.FieldErrors))
		for k, v := range e.FieldErrors {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
		fmt.Fprintf(&b, " [fields: %s]", strings.Join(parts, ", "))
	}
	return b.String()
}

// Retryable reports whether the request may be safely retried (rate limited or
// transient server error). Note: create is not idempotent, so callers must be
// careful even when this is true.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode == http.StatusBadGateway ||
		e.StatusCode == http.StatusServiceUnavailable ||
		e.StatusCode == http.StatusGatewayTimeout
}

func hint(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid request (a field is not settable, missing, or malformed)"
	case http.StatusUnauthorized:
		return "authentication failed (check JIRA credentials)"
	case http.StatusForbidden:
		return "permission denied (service account lacks the required project permission)"
	case http.StatusNotFound:
		return "not found (project, issue type, or user not visible to the service account)"
	case http.StatusTooManyRequests:
		return "rate limited by Jira"
	default:
		return http.StatusText(status)
	}
}

// parseAPIError reads a Jira error body into an APIError.
func parseAPIError(op string, status int, retryAfter int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Op: op, RetryAfter: retryAfter}
	var payload struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		e.Messages = payload.ErrorMessages
		if len(payload.Errors) > 0 {
			e.FieldErrors = payload.Errors
		}
	}
	if len(e.Messages) == 0 && len(e.FieldErrors) == 0 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		if snippet != "" {
			e.Messages = []string{snippet}
		}
	}
	return e
}
