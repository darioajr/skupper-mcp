package application

import (
	"context"
	"sync"

	"github.com/darioajr/skupper-scope/internal/domain"
)

type readTraceKey struct{}

// ReadTrace records attempted scopes, including failed reads, without bodies.
// Collection belongs at the I/O boundary: requested scope can be much smaller
// than a diagnosis's transit search, or larger than a truncated listing.
type ReadTrace struct {
	mu     sync.Mutex
	scopes []domain.ScopeEntry
}

func WithReadTrace(ctx context.Context) (context.Context, *ReadTrace) {
	t := &ReadTrace{scopes: []domain.ScopeEntry{}}
	return context.WithValue(ctx, readTraceKey{}, t), t
}

func (t *ReadTrace) Scopes() []domain.ScopeEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]domain.ScopeEntry{}, t.scopes...)
}

func traceRead(ctx context.Context, contextName, namespace string) {
	t, ok := ctx.Value(readTraceKey{}).(*ReadTrace)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := domain.ScopeEntry{Context: contextName, Namespace: namespace}
	for _, previous := range t.scopes {
		if previous == entry {
			return
		}
	}
	t.scopes = append(t.scopes, entry)
}
