// Package auth holds the authorization model and the HTTP middleware that
// verifies bearer tokens.
//
// No cryptography, JWT parsing, or OAuth protocol step is implemented here.
// Token verification is delegated to github.com/coreos/go-oidc/v3 for JWKS
// retrieval and signature validation, and the MCP authorization flow is
// delegated to the SDK's auth package.
package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Role is a domain permission level. The MVP has exactly two, and
// diagnostician is a strict superset of viewer.
type Role string

const (
	// RoleViewer may list resources and read the topology.
	RoleViewer Role = "viewer"
	// RoleDiagnostician may additionally run diagnose_connectivity.
	RoleDiagnostician Role = "diagnostician"
)

// ErrForbidden is returned when an authenticated caller lacks the role a tool
// requires.
var ErrForbidden = errors.New("forbidden")

// Subject is the authenticated caller. A zero Subject means the server is
// running without authentication, which configuration only permits on a
// loopback bind.
type Subject struct {
	ID            string
	Scopes        []string
	Roles         []Role
	Authenticated bool
}

// Allows reports whether the subject holds the required role.
//
// When authentication is disabled the subject holds every role: the trust
// boundary in that mode is the loopback interface, enforced by config
// validation, not this function.
func (s Subject) Allows(required Role) bool {
	if !s.Authenticated {
		return true
	}
	if slices.Contains(s.Roles, RoleDiagnostician) {
		return true
	}
	return slices.Contains(s.Roles, required)
}

// Require returns ErrForbidden unless the subject holds the role.
func (s Subject) Require(required Role) error {
	if s.Allows(required) {
		return nil
	}
	return fmt.Errorf("%w: this tool requires the %q role; the presented token granted %v",
		ErrForbidden, required, s.Roles)
}

// RolesFromScopes maps token scopes onto domain roles.
func RolesFromScopes(scopes []string, readScope, diagnoseScope string) []Role {
	var roles []Role
	if slices.Contains(scopes, readScope) {
		roles = append(roles, RoleViewer)
	}
	if slices.Contains(scopes, diagnoseScope) {
		roles = append(roles, RoleDiagnostician)
	}
	return roles
}

type subjectKey struct{}

// WithSubject attaches a subject to a context.
func WithSubject(ctx context.Context, s Subject) context.Context {
	return context.WithValue(ctx, subjectKey{}, s)
}

// SubjectFromContext returns the subject attached by the middleware. The zero
// value, which allows everything, is returned only when no middleware ran,
// which is the unauthenticated loopback mode.
func SubjectFromContext(ctx context.Context) Subject {
	if s, ok := ctx.Value(subjectKey{}).(Subject); ok {
		return s
	}
	return Subject{}
}
