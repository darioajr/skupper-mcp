// Package observability provides structured logging, tool auditing, and the
// redaction pass that keeps credential material out of both.
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/darioajr/skupper-scope/internal/config"
)

// NewLogger builds the process logger from configuration. Every record passes
// through the redaction handler, so redaction is not something a call site can
// forget to apply.
func NewLogger(cfg config.Log, w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("observability: unknown log level %q", cfg.Level)
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("observability: unknown log format %q", cfg.Format)
	}
	return slog.New(NewRedactingHandler(handler)), nil
}
