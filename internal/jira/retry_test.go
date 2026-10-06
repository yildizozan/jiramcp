package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// sequenceServer answers with the given statuses in order (the last one
// repeats) and records how many requests arrived and the last body.
func sequenceServer(t *testing.T, retryAfter string, statuses ...int) (*httptest.Server, *int32, *string) {
	t.Helper()
	var calls int32
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&calls, 1)) - 1
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		status := statuses[len(statuses)-1]
		if n < len(statuses) {
			status = statuses[n]
		}
		if status == http.StatusTooManyRequests && retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"accountId":"svc","key":"PAY-1","id":"1"}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &lastBody
}

func TestDo_RetriesRateLimitThenSucceeds(t *testing.T) {
	srv, calls, body := sequenceServer(t, "0", 429, 429, 201)
	out, err := cloudClient(srv.URL).CreateIssue(context.Background(), CreateIssueInput{
		ProjectKey: "PAY", IssueTypeID: "1", Summary: "s",
	})
	if err != nil {
		t.Fatalf("expected success after two 429s, got %v", err)
	}
	if out.Key != "PAY-1" || *calls != 3 {
		t.Fatalf("key=%q calls=%d; want PAY-1 after 3 calls", out.Key, *calls)
	}
	if *body == "" {
		t.Fatal("the request body must be sent again on every retry")
	}
}

func TestDo_GivesUpAfterMaxRetries(t *testing.T) {
	srv, calls, _ := sequenceServer(t, "0", 429)
	_, err := cloudClient(srv.URL).Myself(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
		t.Fatalf("expected the 429 APIError, got %v", err)
	}
	if *calls != maxRateLimitRetries+1 {
		t.Fatalf("calls = %d, want %d", *calls, maxRateLimitRetries+1)
	}
}

func TestDo_LongRetryAfterIsNotWaited(t *testing.T) {
	srv, calls, _ := sequenceServer(t, "120", 429, 200)
	start := time.Now()
	_, err := cloudClient(srv.URL).Myself(context.Background())
	if err == nil || *calls != 1 {
		t.Fatalf("err=%v calls=%d; want the 429 returned after one call", err, *calls)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("must not wait when Retry-After exceeds the cap")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter != 120 {
		t.Fatalf("RetryAfter = %d, want 120", apiErr.RetryAfter)
	}
}

func TestDo_ServerErrorNotRetried(t *testing.T) {
	srv, calls, _ := sequenceServer(t, "", 503, 201)
	_, err := cloudClient(srv.URL).CreateIssue(context.Background(), CreateIssueInput{
		ProjectKey: "PAY", IssueTypeID: "1", Summary: "s",
	})
	if err == nil || *calls != 1 {
		t.Fatalf("err=%v calls=%d; a 503 on create must not be retried", err, *calls)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	cases := map[string]int{
		"":                              -1,
		"garbage":                       -1,
		"-3":                            -1,
		"0":                             0,
		"7":                             7,
		"Tue, 06 Oct 2026 12:00:05 GMT": 5,
		"Tue, 06 Oct 2026 11:59:00 GMT": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %d, want %d", in, got, want)
		}
	}
}
