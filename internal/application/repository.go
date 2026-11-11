// Package application holds the use cases: listing, topology construction and
// connectivity diagnosis. It depends on the domain and on a small repository
// interface it declares itself, never on client-go or on the MCP SDK, so every
// use case is testable against an in-memory repository with no cluster.
package application

import (
	"context"
	"errors"

	"github.com/darioajr/skupper-scope/internal/domain"
)

// Scope is a single resolved (context, namespace) pair to read. It is produced
// by the allowlist, never taken directly from a client argument.
type Scope struct {
	Context   string
	Namespace string
}

// Query is one bounded read against a repository.
type Query struct {
	Scope Scope
	// Limit caps the number of items returned. A repository must stop reading
	// once it has Limit items and report truncation rather than continuing.
	Limit int
}

// Result carries items plus whether the read was cut short.
type Result[T any] struct {
	Items     []T
	Truncated bool
}

// SkupperResourceRepository reads Skupper v2 resources. It is read-only by
// construction: there is no verb here that could mutate a cluster.
type SkupperResourceRepository interface {
	ListSites(ctx context.Context, q Query) (Result[domain.Site], error)
	ListLinks(ctx context.Context, q Query) (Result[domain.Link], error)
	ListListeners(ctx context.Context, q Query) (Result[domain.Listener], error)
	ListConnectors(ctx context.Context, q Query) (Result[domain.Connector], error)
}

// ErrScopeUnavailable marks a scope that could not be read for a reason that is
// not an authorization failure, for example an unreachable API server. Callers
// degrade to a warning instead of failing the whole tool call, because a
// partial view of a multi-cluster network is still useful as long as the gap is
// reported.
var ErrScopeUnavailable = errors.New("scope unavailable")
