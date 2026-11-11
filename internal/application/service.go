package application

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
)

// Service is the application entry point shared by MCP tools and MCP resources,
// so the two never diverge.
type Service struct {
	repo      SkupperResourceRepository
	allowlist *config.Allowlist
	maxItems  int
}

// NewService validates its dependencies and returns a ready service.
func NewService(repo SkupperResourceRepository, allowlist *config.Allowlist, maxItems int) (*Service, error) {
	if repo == nil {
		return nil, errors.New("application: repository is required")
	}
	if allowlist == nil {
		return nil, errors.New("application: allowlist is required")
	}
	if maxItems <= 0 {
		return nil, fmt.Errorf("application: maxItems must be positive, got %d", maxItems)
	}
	return &Service{repo: repo, allowlist: allowlist, maxItems: maxItems}, nil
}

// Selector is the validated form of the context/namespace arguments every tool
// accepts.
type Selector struct {
	Context    string
	Namespaces []string
}

// Resolve turns raw client arguments into an allowed selector, or returns
// config.ErrContextNotAllowed / config.ErrNamespaceNotAllowed.
func (s *Service) Resolve(contextName, namespace string) (Selector, error) {
	ctxName, namespaces, err := s.allowlist.ResolveScope(contextName, namespace)
	if err != nil {
		return Selector{}, err
	}
	return Selector{Context: ctxName, Namespaces: namespaces}, nil
}

// ResolveAll returns every allowed selector, for network-wide tools.
func (s *Service) ResolveAll(contextName, namespace string) ([]Selector, error) {
	if contextName != "" {
		sel, err := s.Resolve(contextName, namespace)
		if err != nil {
			return nil, err
		}
		return []Selector{sel}, nil
	}
	var out []Selector
	var errs []error
	for _, ctxName := range s.allowlist.Contexts() {
		sel, err := s.Resolve(ctxName, namespace)
		if err != nil {
			// A namespace filter that matches only some contexts is normal in a
			// multi-cluster setup; skip those rather than failing the call.
			if errors.Is(err, config.ErrNamespaceNotAllowed) {
				errs = append(errs, err)
				continue
			}
			return nil, err
		}
		out = append(out, sel)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no allowed scope matched: %w", errors.Join(errs...))
	}
	return out, nil
}

// listAcross runs one repository call per allowed namespace, accumulating
// warnings for scopes that could not be read and stopping at maxItems.
//
// Scopes are read sequentially. A cluster read is dominated by API server
// latency, but the item cap must be enforced across scopes, and fan-out here
// would trade a deterministic, ordered, capped result for concurrency this
// workload has not been shown to need.
func listAcross[T any](
	ctx context.Context,
	s *Service,
	selectors []Selector,
	list func(context.Context, Query) (Result[T], error),
) ([]T, []string, error) {
	var (
		items    []T
		warnings []string
	)
	for _, sel := range selectors {
		for _, ns := range sel.Namespaces {
			if err := ctx.Err(); err != nil {
				return items, warnings, err
			}
			remaining := s.maxItems - len(items)
			if remaining <= 0 {
				warnings = append(warnings, fmt.Sprintf(
					"result truncated at maxItems=%d; narrow the query with context or namespace", s.maxItems))
				return items, warnings, nil
			}
			traceRead(ctx, sel.Context, ns)
			res, err := list(ctx, Query{Scope: Scope{Context: sel.Context, Namespace: ns}, Limit: remaining})
			if err != nil {
				if errors.Is(err, ErrScopeUnavailable) {
					warnings = append(warnings, fmt.Sprintf("could not read %s/%s: %v", sel.Context, ns, err))
					continue
				}
				return nil, nil, fmt.Errorf("reading %s/%s: %w", sel.Context, ns, err)
			}
			items = append(items, res.Items...)
			if res.Truncated {
				warnings = append(warnings, fmt.Sprintf(
					"%s/%s returned more items than the remaining budget of %d; some resources are not shown",
					sel.Context, ns, remaining))
			}
		}
	}
	return items, warnings, nil
}

// Listing is the result shape shared by the four listing tools.
type Listing[T any] struct {
	Items    []T                 `json:"items"`
	Scope    []domain.ScopeEntry `json:"scope"`
	Warnings []string            `json:"warnings,omitempty"`
}

func scopeEntries(selectors []Selector) []domain.ScopeEntry {
	var out []domain.ScopeEntry
	for _, sel := range selectors {
		for _, ns := range sel.Namespaces {
			out = append(out, domain.ScopeEntry{Context: sel.Context, Namespace: ns})
		}
	}
	return out
}

// ListSites lists sites in the allowed scope.
func (s *Service) ListSites(ctx context.Context, contextName, namespace string) (Listing[domain.Site], error) {
	selectors, err := s.ResolveAll(contextName, namespace)
	if err != nil {
		return Listing[domain.Site]{}, err
	}
	items, warnings, err := listAcross(ctx, s, selectors, s.repo.ListSites)
	if err != nil {
		return Listing[domain.Site]{}, err
	}
	sort.Slice(items, func(i, j int) bool { return refLess(items[i].ResourceRef, items[j].ResourceRef) })
	sanitizeSites(items)
	return Listing[domain.Site]{Items: nonNil(items), Scope: scopeEntries(selectors), Warnings: sanitizeWarnings(warnings)}, nil
}

// ListLinks lists links, optionally filtered to those whose local or remote
// site matches siteFilter (a site name or site id).
func (s *Service) ListLinks(ctx context.Context, contextName, namespace, siteFilter string) (Listing[domain.Link], error) {
	selectors, err := s.ResolveAll(contextName, namespace)
	if err != nil {
		return Listing[domain.Link]{}, err
	}
	links, warnings, err := listAcross(ctx, s, selectors, s.repo.ListLinks)
	if err != nil {
		return Listing[domain.Link]{}, err
	}
	sites, siteWarnings, err := s.siteIndex(ctx, selectors)
	if err != nil {
		return Listing[domain.Link]{}, err
	}
	warnings = append(warnings, siteWarnings...)
	for i := range links {
		if site, ok := sites[links[i].Key()]; ok {
			links[i].LocalSiteID, links[i].LocalSiteName = site.ID, site.Name
		}
	}
	if siteFilter != "" {
		links = filter(links, func(l domain.Link) bool {
			return matchesSite(siteFilter, l.LocalSiteID, l.LocalSiteName) ||
				matchesSite(siteFilter, l.RemoteSiteID, l.RemoteSiteName)
		})
	}
	sort.Slice(links, func(i, j int) bool { return refLess(links[i].ResourceRef, links[j].ResourceRef) })
	sanitizeLinks(links)
	return Listing[domain.Link]{Items: nonNil(links), Scope: scopeEntries(selectors), Warnings: sanitizeWarnings(warnings)}, nil
}

// ListListeners lists listeners, optionally filtered by owning site.
func (s *Service) ListListeners(ctx context.Context, contextName, namespace, siteFilter string) (Listing[domain.Listener], error) {
	selectors, err := s.ResolveAll(contextName, namespace)
	if err != nil {
		return Listing[domain.Listener]{}, err
	}
	items, warnings, err := listAcross(ctx, s, selectors, s.repo.ListListeners)
	if err != nil {
		return Listing[domain.Listener]{}, err
	}
	sites, siteWarnings, err := s.siteIndex(ctx, selectors)
	if err != nil {
		return Listing[domain.Listener]{}, err
	}
	warnings = append(warnings, siteWarnings...)
	for i := range items {
		if site, ok := sites[items[i].Key()]; ok {
			items[i].SiteID, items[i].SiteName = site.ID, site.Name
		}
	}
	if siteFilter != "" {
		items = filter(items, func(l domain.Listener) bool { return matchesSite(siteFilter, l.SiteID, l.SiteName) })
	}
	sort.Slice(items, func(i, j int) bool { return refLess(items[i].ResourceRef, items[j].ResourceRef) })
	sanitizeListeners(items)
	return Listing[domain.Listener]{Items: nonNil(items), Scope: scopeEntries(selectors), Warnings: sanitizeWarnings(warnings)}, nil
}

// ListConnectors lists connectors, optionally filtered by owning site.
func (s *Service) ListConnectors(ctx context.Context, contextName, namespace, siteFilter string) (Listing[domain.Connector], error) {
	selectors, err := s.ResolveAll(contextName, namespace)
	if err != nil {
		return Listing[domain.Connector]{}, err
	}
	items, warnings, err := listAcross(ctx, s, selectors, s.repo.ListConnectors)
	if err != nil {
		return Listing[domain.Connector]{}, err
	}
	sites, siteWarnings, err := s.siteIndex(ctx, selectors)
	if err != nil {
		return Listing[domain.Connector]{}, err
	}
	warnings = append(warnings, siteWarnings...)
	for i := range items {
		if site, ok := sites[items[i].Key()]; ok {
			items[i].SiteID, items[i].SiteName = site.ID, site.Name
		}
	}
	if siteFilter != "" {
		items = filter(items, func(c domain.Connector) bool { return matchesSite(siteFilter, c.SiteID, c.SiteName) })
	}
	sort.Slice(items, func(i, j int) bool { return refLess(items[i].ResourceRef, items[j].ResourceRef) })
	sanitizeConnectors(items)
	return Listing[domain.Connector]{Items: nonNil(items), Scope: scopeEntries(selectors), Warnings: sanitizeWarnings(warnings)}, nil
}

// Snapshot reads every resource kind across the allowed scope. Topology and
// diagnosis both build on it, so they see one consistent set of inputs.
func (s *Service) Snapshot(ctx context.Context, contextName, namespace string) (domain.Snapshot, []Selector, error) {
	selectors, err := s.ResolveAll(contextName, namespace)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	var snap domain.Snapshot
	var warnings []string

	sites, w, err := listAcross(ctx, s, selectors, s.repo.ListSites)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	warnings = append(warnings, w...)

	links, w, err := listAcross(ctx, s, selectors, s.repo.ListLinks)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	warnings = append(warnings, w...)

	listeners, w, err := listAcross(ctx, s, selectors, s.repo.ListListeners)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	warnings = append(warnings, w...)

	connectors, w, err := listAcross(ctx, s, selectors, s.repo.ListConnectors)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	warnings = append(warnings, w...)

	snap.Sites, snap.Links, snap.Listeners, snap.Connectors = sites, links, listeners, connectors
	snap.Warnings = warnings
	resolveSiteOwnership(&snap)
	sanitizeSnapshot(&snap)
	return snap, selectors, nil
}

// siteIndex reads the sites in the given scope and keys them by namespace.
//
// Links, listeners and connectors carry no reference to their site: in Skupper
// v2 a namespace holds at most one Site, and that is the ownership relation. So
// a listing that reports site identity has to read sites as well, which is why
// this costs one extra call per scope rather than being derivable locally.
func (s *Service) siteIndex(ctx context.Context, selectors []Selector) (map[domain.SiteKey]domain.Site, []string, error) {
	sites, warnings, err := listAcross(ctx, s, selectors, s.repo.ListSites)
	if err != nil {
		return nil, nil, err
	}
	index := make(map[domain.SiteKey]domain.Site, len(sites))
	for _, site := range sites {
		if existing, dup := index[site.Key()]; dup {
			warnings = append(warnings, fmt.Sprintf(
				"namespace %s/%s holds more than one Site (%q and %q); Skupper v2 expects at most one, "+
					"so ownership there is ambiguous and the first was used",
				site.Context, site.Namespace, existing.Name, site.Name))
			continue
		}
		index[site.Key()] = site
	}
	return index, warnings, nil
}

// resolveSiteOwnership fills in the site identity of every namespaced resource.
// In Skupper v2 a namespace holds at most one site, so namespace membership is
// the ownership relation; there is no owner reference to follow.
func resolveSiteOwnership(snap *domain.Snapshot) {
	byKey := make(map[domain.SiteKey]domain.Site, len(snap.Sites))
	for _, site := range snap.Sites {
		// If a namespace somehow holds more than one site the first wins and the
		// ambiguity is reported, rather than silently picking at random.
		if existing, dup := byKey[site.Key()]; dup {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf(
				"namespace %s/%s holds more than one Site (%q and %q); Skupper v2 expects at most one, "+
					"so ownership of listeners, connectors and links there is ambiguous",
				site.Context, site.Namespace, existing.Name, site.Name))
			continue
		}
		byKey[site.Key()] = site
	}

	for i := range snap.Links {
		if site, ok := byKey[snap.Links[i].Key()]; ok {
			snap.Links[i].LocalSiteID = site.ID
			snap.Links[i].LocalSiteName = site.Name
		}
	}
	for i := range snap.Listeners {
		if site, ok := byKey[snap.Listeners[i].Key()]; ok {
			snap.Listeners[i].SiteID = site.ID
			snap.Listeners[i].SiteName = site.Name
		}
	}
	for i := range snap.Connectors {
		if site, ok := byKey[snap.Connectors[i].Key()]; ok {
			snap.Connectors[i].SiteID = site.ID
			snap.Connectors[i].SiteName = site.Name
		}
	}
}

func matchesSite(filter, id, name string) bool {
	return filter != "" && (filter == id || filter == name)
}

func filter[T any](items []T, keep func(T) bool) []T {
	out := items[:0]
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func refLess(a, b domain.ResourceRef) bool {
	if a.Context != b.Context {
		return a.Context < b.Context
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}
