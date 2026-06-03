// Package log configures the process-wide slog logger. In stdio transport
// mode logs MUST go to stderr because stdout carries MCP protocol traffic.
package log

import (
	"log/slog"
	"os"
	"strings"
)

// Setup builds a slog.Logger writing to stderr with the given level/format.
// format is "json" or "text"; level is debug|info|warn|error.
func Setup(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
