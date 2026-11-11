package application

import (
	"context"
	"testing"

	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
)

// memRepo is an in-memory SkupperResourceRepository. Every use case is testable
// through it with no cluster, which is the point of the interface living in
// this package rather than in internal/kubernetes.
type memRepo struct {
	sites      []domain.Site
	links      []domain.Link
	listeners  []domain.Listener
	connectors []domain.Connector

	// failScopes maps "context/namespace" onto an error to return, so partial
	// failure of a multi-cluster read can be exercised.
	failScopes map[string]error
	// calls records the queries made, so limit propagation can be asserted.
	calls []Query
}

func (m *memRepo) record(q Query) error {
	m.calls = append(m.calls, q)
	if err, ok := m.failScopes[q.Scope.Context+"/"+q.Scope.Namespace]; ok {
		return err
	}
	return nil
}

func inScope[T any](items []T, q Query, ref func(T) domain.ResourceRef) Result[T] {
	out := Result[T]{Items: []T{}}
	for _, item := range items {
		r := ref(item)
		if r.Context != q.Scope.Context || r.Namespace != q.Scope.Namespace {
			continue
		}
		if len(out.Items) >= q.Limit {
			out.Truncated = true
			break
		}
		out.Items = append(out.Items, item)
	}
	return out
}

func (m *memRepo) ListSites(_ context.Context, q Query) (Result[domain.Site], error) {
	if err := m.record(q); err != nil {
		return Result[domain.Site]{}, err
	}
	return inScope(m.sites, q, func(s domain.Site) domain.ResourceRef { return s.ResourceRef }), nil
}

func (m *memRepo) ListLinks(_ context.Context, q Query) (Result[domain.Link], error) {
	if err := m.record(q); err != nil {
		return Result[domain.Link]{}, err
	}
	return inScope(m.links, q, func(l domain.Link) domain.ResourceRef { return l.ResourceRef }), nil
}

func (m *memRepo) ListListeners(_ context.Context, q Query) (Result[domain.Listener], error) {
	if err := m.record(q); err != nil {
		return Result[domain.Listener]{}, err
	}
	return inScope(m.listeners, q, func(l domain.Listener) domain.ResourceRef { return l.ResourceRef }), nil
}

func (m *memRepo) ListConnectors(_ context.Context, q Query) (Result[domain.Connector], error) {
	if err := m.record(q); err != nil {
		return Result[domain.Connector]{}, err
	}
	return inScope(m.connectors, q, func(c domain.Connector) domain.ResourceRef { return c.ResourceRef }), nil
}

var _ SkupperResourceRepository = (*memRepo)(nil)

func newService(t *testing.T, repo SkupperResourceRepository, contexts, namespaces []string, maxItems int) *Service {
	t.Helper()
	cfg := config.Default()
	cfg.Kubernetes.AllowedContexts = contexts
	cfg.Kubernetes.AllowedNamespaces = namespaces
	allowlist, err := cfg.Allowlist()
	if err != nil {
		t.Fatalf("building allowlist: %v", err)
	}
	svc, err := NewService(repo, allowlist, maxItems)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// Builders keep the tests readable: each one produces a resource that is Ready
// unless the test says otherwise.

func site(ctxName, ns, name, id string, opts ...func(*domain.Site)) domain.Site {
	s := domain.Site{
		ResourceRef: domain.ResourceRef{
			ID: id, Context: ctxName, Namespace: ns, Name: name, UID: id, Kind: "Site",
		},
		ObservedState: domain.ObservedState{Status: domain.StatusReady},
	}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func link(ctxName, ns, name, id, remoteID, remoteName string, opts ...func(*domain.Link)) domain.Link {
	l := domain.Link{
		ResourceRef: domain.ResourceRef{
			ID: id, Context: ctxName, Namespace: ns, Name: name, UID: id, Kind: "Link",
		},
		ObservedState:  domain.ObservedState{Status: domain.StatusReady},
		RemoteSiteID:   remoteID,
		RemoteSiteName: remoteName,
	}
	for _, o := range opts {
		o(&l)
	}
	return l
}

func listener(ctxName, ns, name, id, routingKey string, port int, opts ...func(*domain.Listener)) domain.Listener {
	l := domain.Listener{
		ResourceRef: domain.ResourceRef{
			ID: id, Context: ctxName, Namespace: ns, Name: name, UID: id, Kind: "Listener",
		},
		ObservedState:        domain.ObservedState{Status: domain.StatusReady},
		RoutingKey:           routingKey,
		Host:                 name,
		Port:                 port,
		Protocol:             "tcp",
		HasMatchingConnector: true,
	}
	for _, o := range opts {
		o(&l)
	}
	return l
}

func connector(ctxName, ns, name, id, routingKey string, port int, opts ...func(*domain.Connector)) domain.Connector {
	c := domain.Connector{
		ResourceRef: domain.ResourceRef{
			ID: id, Context: ctxName, Namespace: ns, Name: name, UID: id, Kind: "Connector",
		},
		ObservedState:       domain.ObservedState{Status: domain.StatusReady},
		RoutingKey:          routingKey,
		Selector:            "app=" + name,
		Port:                port,
		Protocol:            "tcp",
		SelectedPodCount:    1,
		HasMatchingListener: true,
	}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// twoSiteNetwork is the healthy west/east topology most tests start from.
func twoSiteNetwork() *memRepo {
	return &memRepo{
		sites: []domain.Site{
			site("west-cluster", "west", "west", "site-west"),
			site("east-cluster", "east", "east", "site-east"),
		},
		links: []domain.Link{
			link("west-cluster", "west", "west-to-east", "link-1", "site-east", "east"),
		},
		listeners: []domain.Listener{
			listener("west-cluster", "west", "backend", "listener-1", "backend", 8080),
		},
		connectors: []domain.Connector{
			connector("east-cluster", "east", "backend", "connector-1", "backend", 8080),
		},
	}
}

func twoSiteService(t *testing.T, repo *memRepo) *Service {
	t.Helper()
	return newService(t, repo,
		[]string{"west-cluster", "east-cluster"},
		[]string{"west-cluster/west", "east-cluster/east"},
		100)
}
