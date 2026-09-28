package main

import (
	"context"
	"io"
	"testing"

	"jiramcp/internal/config"
)

func TestRootCmd_TransportRouting(t *testing.T) {
	cases := []struct {
		args []string
		want config.Transport
	}{
		{nil, config.TransportStdio},
		{[]string{"stdio"}, config.TransportStdio},
		{[]string{"http"}, config.TransportHTTP},
	}
	for _, tc := range cases {
		var got config.Transport
		calls := 0
		cmd := newRootCmd(func(_ context.Context, tr config.Transport) error {
			got = tr
			calls++
			return nil
		})
		cmd.SetArgs(tc.args)
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("args %q: %v", tc.args, err)
		}
		if calls != 1 || got != tc.want {
			t.Fatalf("args %q: served %q %d time(s), want %q once", tc.args, got, calls, tc.want)
		}
	}
}

func TestRootCmd_RejectsUnknownInput(t *testing.T) {
	for _, args := range [][]string{{"foo"}, {"http", "extra"}, {"stdio", "extra"}} {
		cmd := newRootCmd(func(context.Context, config.Transport) error {
			t.Fatalf("args %q: server must not start", args)
			return nil
		})
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.ExecuteContext(context.Background()); err == nil {
			t.Fatalf("args %q: expected an error", args)
		}
	}
}
