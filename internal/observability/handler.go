package observability

import (
	"context"
	"log/slog"

	"github.com/darioajr/skupper-scope/internal/redact"
)

// Placeholder is re-exported so callers of this package need not import the
// redaction package to recognise a redacted value.
const Placeholder = redact.Placeholder

// RedactingHandler wraps a slog.Handler and rewrites sensitive attributes
// before they reach the output. Responses are scrubbed independently in the
// application layer; this is the same guarantee applied to the audit trail,
// and it also catches material arriving inside a third-party error.
type RedactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps h.
func NewRedactingHandler(h slog.Handler) *RedactingHandler { return &RedactingHandler{inner: h} }

// Enabled implements slog.Handler.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *RedactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, redact.String(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

// WithAttrs implements slog.Handler.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

// WithGroup implements slog.Handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	// The authenticated identity is required for investigation, even when an
	// issuer uses an opaque subject identifier that resembles a credential.
	if a.Key == "subject" || a.Key == "session_hash" {
		return a
	}
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		cleaned := make([]any, 0, len(attrs))
		for _, sub := range attrs {
			cleaned = append(cleaned, redactAttr(sub))
		}
		return slog.Group(a.Key, cleaned...)
	}
	if redact.IsSensitiveKey(a.Key) {
		return slog.String(a.Key, redact.Placeholder)
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, redact.String(a.Value.String()))
	}
	if s, ok := a.Value.Any().(string); ok {
		return slog.String(a.Key, redact.String(s))
	}
	return a
}

// IsSensitiveKey reports whether an attribute name should have its value
// replaced outright.
func IsSensitiveKey(key string) bool { return redact.IsSensitiveKey(key) }
