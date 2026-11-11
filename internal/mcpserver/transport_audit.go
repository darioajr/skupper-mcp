package mcpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/observability"
)

type transportIdentityKey struct{}
type transportIdentity struct{ subject string }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func captureTransportIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := r.Context().Value(transportIdentityKey{}).(*transportIdentity); ok {
			identity.subject = auth.SubjectFromContext(r.Context()).ID
		}
		next.ServeHTTP(w, r)
	})
}

// auditTransport catches failures before JSON-RPC dispatch, including missing
// tokens and malformed bodies. Headers, request bodies and URL queries are
// deliberately excluded from this unauthenticated logging surface.
func (s *Server) auditTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		identity := &transportIdentity{}
		r = r.WithContext(context.WithValue(r.Context(), transportIdentityKey{}, identity))
		status := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(status, r)
		if status.status >= http.StatusBadRequest {
			outcome := observability.OutcomeInvalid
			if status.status == http.StatusUnauthorized || status.status == http.StatusForbidden {
				outcome = observability.OutcomeDenied
			}
			s.auditor.Record(r.Context(), observability.Invocation{Tool: "transport:mcp",
				Subject: identity.subject, Outcome: outcome, HTTPStatus: status.status, Duration: time.Since(started)})
		}
	})
}
