// Package logging builds the process-wide structured logger (slog, JSON by
// default) from configuration.
package logging

import (
	"io"
	"log/slog"

	"github.com/strazahq/straza/internal/config"
)

// New returns a logger configured per cfg, writing to w.
func New(cfg config.Log, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}
	var h slog.Handler
	if cfg.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
