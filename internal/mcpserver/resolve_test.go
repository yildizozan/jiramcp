package mcpserver

import (
	"context"
	"testing"

	"jiramcp/internal/jira"
)

func TestLooksLikeUserID(t *testing.T) {
	cases := []struct {
		in   string
		dc   bool
		want bool
	}{
		{"5b10ac8d82e05b22cc7d4ef5", false, true},
		{"557058:f58131cb-b67d-43c7-b30d-6b58d40bd077", false, true},
		{"ozan", false, false},
		{"ozan.yildiz", false, false},
		{"ozan@yildizozan.com", false, false},
		{"Ozan Yildiz", false, false},
		{"ozan.yildiz", true, true},
		{"ozan@yildizozan.com", true, false},
		{"Ozan Yildiz", true, false},
		{"  ", true, false},
	}
	for _, tc := range cases {
		if got := looksLikeUserID(tc.in, tc.dc); got != tc.want {
			t.Errorf("looksLikeUserID(%q, dc=%v) = %v, want %v", tc.in, tc.dc, got, tc.want)
		}
	}
}

// On Cloud a single word is a name, not an accountId: it must be searched
// and resolved to the matching account.
func TestResolveUserID_CloudSingleWordIsSearched(t *testing.T) {
	f := &fakeClient{users: []jira.User{{AccountID: "5b10ac8d82e05b22cc7d4ef5", DisplayName: "Ozan", Active: true}}}
	got, err := resolveUserID(context.Background(), f, "ozan", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "5b10ac8d82e05b22cc7d4ef5" {
		t.Fatalf("got %q, want the searched accountId", got)
	}
}

func TestResolveUserID_DCUsernamePassesThrough(t *testing.T) {
	f := &fakeClient{} // a search would find nobody
	got, err := resolveUserID(context.Background(), f, "ozan.yildiz", true)
	if err != nil || got != "ozan.yildiz" {
		t.Fatalf("got %q, %v; want the username passed through", got, err)
	}
}
