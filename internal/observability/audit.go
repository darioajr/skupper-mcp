package observability

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/darioajr/skupper-scope/internal/domain"
)

// Auditor emits one structured record per MCP tool invocation.
//
// What is logged is a fixed allowlist of scalar fields. Tool arguments are
// never logged as a blob, and no Kubernetes object, whole or partial, is ever
// written to the log.
type Auditor struct {
	log *slog.Logger
}

// NewAuditor returns an auditor writing to log.
func NewAuditor(log *slog.Logger) *Auditor { return &Auditor{log: log} }

// Invocation is the audited shape of one tool call.
type Invocation struct {
	Tool string
	// Subject identifies the caller when a verified token carried an identity.
	// It is empty for unauthenticated local use, which is itself a fact worth
	// recording.
	Subject string
	// Scopes are the token scopes that authorised the call.
	Scopes []string
	// Context and Namespace are requested filters, never proof of a read.
	Context    string
	Namespace  string
	ReadScopes []domain.ScopeEntry
	Session    string
	HTTPStatus int
	// RoutingKey is logged for diagnose_connectivity because it is the subject
	// of the query. It is an application-level name, not a credential.
	RoutingKey string
	// ResultCount is the cardinality of the answer, which lets an operator spot
	// a client enumerating the network without logging what it saw.
	ResultCount int
	Outcome     string
	Duration    time.Duration
	Err         error
}

// Outcomes recorded in the audit log.
const (
	OutcomeOK      = "ok"
	OutcomeDenied  = "denied"
	OutcomeInvalid = "invalid"
	OutcomeError   = "error"
	OutcomeLimited = "limited"
)

// Record writes the audit entry. A denied or failed call is logged at warn so
// it stands out from routine reads.
func (a *Auditor) Record(ctx context.Context, inv Invocation) {
	if a == nil || a.log == nil {
		return
	}
	attrs := []any{
		slog.String("event", "tool_invocation"),
		slog.String("tool", inv.Tool),
		slog.String("outcome", inv.Outcome),
		slog.Duration("duration", inv.Duration),
		slog.Int("result_count", inv.ResultCount),
	}
	if inv.ReadScopes != nil {
		attrs = append(attrs, slog.Any("read_scopes", inv.ReadScopes))
	}
	if inv.Session != "" {
		attrs = append(attrs, slog.String("session_hash", inv.Session))
	}
	if inv.HTTPStatus != 0 {
		attrs = append(attrs, slog.Int("http_status", inv.HTTPStatus))
	}
	if inv.Subject != "" {
		attrs = append(attrs, slog.String("subject", inv.Subject))
	} else {
		attrs = append(attrs, slog.String("subject", "anonymous"))
	}
	if len(inv.Scopes) > 0 {
		attrs = append(attrs, slog.Any("scopes", inv.Scopes))
	}
	if inv.Context != "" {
		attrs = append(attrs, slog.String("requested_context", inv.Context))
	}
	if inv.Namespace != "" {
		attrs = append(attrs, slog.String("requested_namespace", inv.Namespace))
	}
	if inv.RoutingKey != "" {
		attrs = append(attrs, slog.String("routing_key", inv.RoutingKey))
	}
	if inv.Err != nil {
		// The message is redacted by the handler; a client library error can
		// carry a token in a URL.
		attrs = append(attrs, slog.String("error", inv.Err.Error()))
	}

	level := slog.LevelInfo
	if inv.Outcome != OutcomeOK {
		level = slog.LevelWarn
	}
	a.log.LogAttrs(ctx, level, "tool invocation", toAttrs(attrs)...)
}

// Alert is a structured event for the deployment's central alerting route.
// Logging does not deliver a notification; operators must configure that route.
func (a *Auditor) Alert(ctx context.Context, owner, subject string) {
	a.log.ErrorContext(ctx, "MCP circuit breaker opened", "event", "mcp_circuit_open",
		"alert_owner", owner, "subject", subject)
}

func toAttrs(args []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(args))
	for _, a := range args {
		if attr, ok := a.(slog.Attr); ok {
			out = append(out, attr)
		}
	}
	return out
}

// Classify maps an error onto an audit outcome.
func Classify(err error, denied, invalid error) string {
	switch {
	case err == nil:
		return OutcomeOK
	case denied != nil && errors.Is(err, denied):
		return OutcomeDenied
	case invalid != nil && errors.Is(err, invalid):
		return OutcomeInvalid
	default:
		return OutcomeError
	}
}
