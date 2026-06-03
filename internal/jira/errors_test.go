package jira

import (
	"net/http"
	"strings"
	"testing"
)

func TestParseAPIError_FieldErrors(t *testing.T) {
	body := []byte(`{"errorMessages":["boom"],"errors":{"reporter":"not on screen"}}`)
	e := parseAPIError("create issue", http.StatusBadRequest, 0, body)
	if e.StatusCode != 400 {
		t.Fatalf("status: %d", e.StatusCode)
	}
	msg := e.Error()
	if !strings.Contains(msg, "boom") || !strings.Contains(msg, "reporter=not on screen") {
		t.Fatalf("unexpected message: %s", msg)
	}
	if e.Retryable() {
		t.Fatalf("400 should not be retryable")
	}
}

func TestParseAPIError_RetryAfter(t *testing.T) {
	e := parseAPIError("search users", http.StatusTooManyRequests, 7, []byte(`{}`))
	if !e.Retryable() {
		t.Fatalf("429 should be retryable")
	}
	if e.RetryAfter != 7 {
		t.Fatalf("retry-after: %d", e.RetryAfter)
	}
	if !strings.Contains(e.Error(), "rate limited") {
		t.Fatalf("missing hint: %s", e.Error())
	}
}

func TestParseAPIError_PlainBodyFallback(t *testing.T) {
	e := parseAPIError("create issue", http.StatusForbidden, 0, []byte("permission denied raw"))
	if len(e.Messages) != 1 || e.Messages[0] != "permission denied raw" {
		t.Fatalf("expected raw snippet, got %v", e.Messages)
	}
}
