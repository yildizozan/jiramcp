package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

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

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func okProbe(context.Context) error { return nil }

func TestStartHealth_ServesProbes(t *testing.T) {
	// Reserve a free port, then hand it to startHealth.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shutdown, err := startHealth(ctx, addr, okProbe, time.Second, discardLogger())
	if err != nil {
		t.Fatalf("startHealth: %v", err)
	}
	defer shutdown()

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", resp.StatusCode)
	}
}

func TestStartHealth_BusyPortFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	if _, err := startHealth(context.Background(), ln.Addr().String(), okProbe, time.Second, discardLogger()); err == nil {
		t.Fatal("expected an error when the health port is already in use")
	}
}
