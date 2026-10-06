package log

import (
	"context"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		" DEBUG ": slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"verbose": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSetup_FormatAndLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	cases := []struct {
		format string
		want   string // handler type
	}{
		{"json", "*slog.JSONHandler"},
		{"text", "*slog.TextHandler"},
		{"TEXT", "*slog.TextHandler"},
		{"other", "*slog.JSONHandler"},
	}
	for _, tc := range cases {
		logger := Setup("warn", tc.format)
		if got := typeName(logger.Handler()); got != tc.want {
			t.Errorf("format %q: handler %s, want %s", tc.format, got, tc.want)
		}
		if logger.Enabled(context.Background(), slog.LevelInfo) {
			t.Errorf("format %q: info must be disabled at level warn", tc.format)
		}
		if slog.Default() != logger {
			t.Errorf("format %q: Setup must install the logger as the default", tc.format)
		}
	}
}

func typeName(h slog.Handler) string {
	switch h.(type) {
	case *slog.JSONHandler:
		return "*slog.JSONHandler"
	case *slog.TextHandler:
		return "*slog.TextHandler"
	default:
		return "other"
	}
}
