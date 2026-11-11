package mcpserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/observability"
)

type auditStateKey struct{}
type auditState struct {
	recorded bool
	session  string
}

// guard runs before tool schema validation, so malformed and unknown calls are
// charged and audited too. The typed handlers retain authorization and read
// tracing, while this layer catches requests that never reach those handlers.
func (s *Server) guard(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		started := time.Now()
		var identityErr error
		if s.auth.Enabled {
			extra := req.GetExtra()
			if extra == nil {
				identityErr = fmt.Errorf("%w: verified transport metadata is required", auth.ErrForbidden)
			} else {
				ctx, identityErr = requestIdentity(ctx, s, extra)
			}
		}
		if identityErr != nil {
			s.auditor.Record(context.Background(), observability.Invocation{Tool: method,
				Outcome: observability.OutcomeDenied, Err: identityErr, Duration: time.Since(started)})
			return nil, identityErr
		}
		subject := auth.SubjectFromContext(ctx)
		user := subject.ID
		if user == "" {
			user = "anonymous"
		}
		session := "local"
		if req.GetSession() != nil && req.GetSession().ID() != "" {
			// A session identifier can act as a credential in some transports.
			// Correlate its hash, never the session header itself.
			session = fmt.Sprintf("%x", sha256.Sum256([]byte(req.GetSession().ID())))
		}
		state := &auditState{session: session}
		ctx = context.WithValue(ctx, auditStateKey{}, state)
		if method == "tools/call" || method == "resources/read" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.requestTimeout)
			defer cancel()
			opened, err := s.limiter.allow(time.Now(), user, session)
			if opened {
				s.auditor.Alert(ctx, s.limiter.cfg.AlertOwner, user)
			}
			if err != nil {
				s.auditor.Record(ctx, observability.Invocation{Tool: method, Subject: user, Session: session,
					Outcome: observability.OutcomeLimited, Err: err, Duration: time.Since(started)})
				return nil, err
			}
		}
		result, err := next(ctx, method, req)
		if !state.recorded {
			auditErr := err
			outcome := observability.OutcomeOK
			if err != nil {
				outcome = observability.OutcomeInvalid
			}
			if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult.IsError {
				outcome = observability.OutcomeInvalid
				auditErr = errors.New("tool request rejected before typed handler")
			}
			s.auditor.Record(ctx, observability.Invocation{Tool: method, Subject: user, Session: session,
				Outcome: outcome, Err: auditErr, Duration: time.Since(started)})
		}
		return result, err
	}
}

func requestIdentity(ctx context.Context, s *Server, extra *mcp.RequestExtra) (context.Context, error) {
	updated, err := auth.VerifiedRequestContext(ctx, s.auth, extra.TokenInfo, extra.Header)
	if err != nil {
		return ctx, err
	}
	return updated, nil
}
