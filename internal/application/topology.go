package application

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/darioajr/skupper-scope/internal/domain"
)

// GetTopology reads the allowed scope and builds the network view.
func (s *Service) GetTopology(ctx context.Context, contextName, namespace string) (domain.Topology, error) {
	snap, selectors, err := s.Snapshot(ctx, contextName, namespace)
	if err != nil {
		return domain.Topology{}, err
	}
	topo := BuildTopology(snap)
	topo.ObservedScope = scopeEntries(selectors)
	return topo, nil
}

// BuildTopology converts a snapshot into a typed topology.
//
// Two association rules are applied, and neither is a claim about traffic:
//   - a link is an edge from the site in the link's namespace to the site whose
//     id the link's status reports as remote;
//   - a listener and a connector are associated when their routing keys are
//     equal. Skupper exposes no stronger binding to a read-only observer.
func BuildTopology(snap domain.Snapshot) domain.Topology {
	topo := domain.Topology{Warnings: append([]string(nil), snap.Warnings...)}

	nodes := make(map[string]*domain.TopologyNode, len(snap.Sites))
	order := make([]string, 0, len(snap.Sites))

	for _, site := range snap.Sites {
		if _, exists := nodes[site.ID]; exists {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf(
				"two sites share the identity %q (%s/%s and an earlier one); the topology keeps the first",
				site.ID, site.Context, site.Namespace))
			continue
		}
		nodes[site.ID] = &domain.TopologyNode{
			ID:        site.ID,
			Context:   site.Context,
			Namespace: site.Namespace,
			Name:      site.Name,
			Status:    site.Status,
			Message:   site.Message,
			Edge:      site.Edge,
			HA:        site.HA,
			Observed:  true,
		}
		order = append(order, site.ID)
	}

	// A link may name a remote site that lives outside the allowed scope. Such
	// a site becomes an unobserved placeholder node so the edge is not silently
	// dropped and the caller can see where the view stops.
	placeholder := func(id, name string) {
		if id == "" {
			return
		}
		if _, ok := nodes[id]; ok {
			return
		}
		nodes[id] = &domain.TopologyNode{
			ID:       id,
			Name:     name,
			Status:   domain.StatusUnknown,
			Observed: false,
		}
		order = append(order, id)
	}

	for _, link := range snap.Links {
		source := link.LocalSiteID
		if source == "" {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf(
				"link %s/%s/%s has no Site in its namespace, so its local end could not be resolved",
				link.Context, link.Namespace, link.Name))
			source = unresolvedSiteID(link.Context, link.Namespace)
			placeholder(source, link.Namespace)
		}
		target := link.RemoteSiteID
		resolved := false
		if target != "" {
			_, resolved = nodes[target]
			if !resolved {
				placeholder(target, link.RemoteSiteName)
			}
		} else {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf(
				"link %s/%s/%s reports no remoteSiteId, so its far end is unknown; "+
					"this is expected while the link is still being established",
				link.Context, link.Namespace, link.Name))
		}
		topo.Edges = append(topo.Edges, domain.TopologyEdge{
			ID:             link.ID,
			Name:           link.Name,
			Source:         source,
			Target:         target,
			Status:         link.Status,
			Message:        link.Message,
			Cost:           link.Cost,
			TargetResolved: resolved,
		})
	}

	attach := func(siteID string, ref domain.TopologyEndpointRef, isListener bool) {
		node, ok := nodes[siteID]
		if !ok {
			return
		}
		if isListener {
			node.Listeners = append(node.Listeners, ref)
		} else {
			node.Connectors = append(node.Connectors, ref)
		}
	}

	for _, l := range snap.Listeners {
		if l.SiteID == "" {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf(
				"listener %s/%s/%s has no Site in its namespace and is not attached to any node",
				l.Context, l.Namespace, l.Name))
			continue
		}
		attach(l.SiteID, domain.TopologyEndpointRef{
			ID: l.ID, Name: l.Name, RoutingKey: l.RoutingKey,
			Host: l.Host, Port: l.Port, Protocol: l.Protocol, Status: l.Status,
		}, true)
	}
	for _, c := range snap.Connectors {
		if c.SiteID == "" {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf(
				"connector %s/%s/%s has no Site in its namespace and is not attached to any node",
				c.Context, c.Namespace, c.Name))
			continue
		}
		attach(c.SiteID, domain.TopologyEndpointRef{
			ID: c.ID, Name: c.Name, RoutingKey: c.RoutingKey,
			Host: c.Host, Port: c.Port, Protocol: c.Protocol, Selector: c.Selector, Status: c.Status,
		}, false)
	}

	for _, id := range order {
		topo.Nodes = append(topo.Nodes, *nodes[id])
	}
	topo.RoutingKeys = buildRoutingKeyGroups(snap)
	if topo.Nodes == nil {
		topo.Nodes = []domain.TopologyNode{}
	}
	if topo.Edges == nil {
		topo.Edges = []domain.TopologyEdge{}
	}
	return topo
}

func unresolvedSiteID(contextName, namespace string) string {
	return "unresolved:" + contextName + "/" + namespace
}

func buildRoutingKeyGroups(snap domain.Snapshot) []domain.RoutingKeyGroup {
	groups := map[string]*domain.RoutingKeyGroup{}
	get := func(key string) *domain.RoutingKeyGroup {
		g, ok := groups[key]
		if !ok {
			g = &domain.RoutingKeyGroup{RoutingKey: key}
			groups[key] = g
		}
		return g
	}

	for _, l := range snap.Listeners {
		if l.RoutingKey == "" {
			continue
		}
		get(l.RoutingKey).Listeners = append(get(l.RoutingKey).Listeners, domain.RoutingKeyMember{
			ID: l.ID, SiteID: l.SiteID, Context: l.Context, Namespace: l.Namespace,
			Name: l.Name, Port: l.Port, Protocol: l.Protocol,
		})
	}
	for _, c := range snap.Connectors {
		if c.RoutingKey == "" {
			continue
		}
		get(c.RoutingKey).Connectors = append(get(c.RoutingKey).Connectors, domain.RoutingKeyMember{
			ID: c.ID, SiteID: c.SiteID, Context: c.Context, Namespace: c.Namespace,
			Name: c.Name, Port: c.Port, Protocol: c.Protocol,
		})
	}

	out := make([]domain.RoutingKeyGroup, 0, len(groups))
	for _, g := range groups {
		g.Mismatches = detectMismatches(*g)
		if g.Listeners == nil {
			g.Listeners = []domain.RoutingKeyMember{}
		}
		if g.Connectors == nil {
			g.Connectors = []domain.RoutingKeyMember{}
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RoutingKey < out[j].RoutingKey })
	return out
}

func detectMismatches(g domain.RoutingKeyGroup) []domain.RoutingKeyMismatch {
	var out []domain.RoutingKeyMismatch
	switch {
	case len(g.Connectors) == 0 && len(g.Listeners) > 0:
		for _, l := range g.Listeners {
			out = append(out, domain.RoutingKeyMismatch{
				Kind:       domain.MismatchListenerOrphan,
				ListenerID: l.ID,
				Detail: fmt.Sprintf("routing key %q has a listener in %s/%s but no connector "+
					"in the observed scope; traffic sent to it has nowhere to go unless a connector "+
					"exists in a namespace this server cannot read",
					g.RoutingKey, l.Context, l.Namespace),
			})
		}
		return out
	case len(g.Listeners) == 0 && len(g.Connectors) > 0:
		for _, c := range g.Connectors {
			out = append(out, domain.RoutingKeyMismatch{
				Kind:        domain.MismatchConnectorOrphan,
				ConnectorID: c.ID,
				Detail: fmt.Sprintf("routing key %q has a connector in %s/%s but no listener "+
					"in the observed scope; the backend is exposed but nothing sends to it",
					g.RoutingKey, c.Context, c.Namespace),
			})
		}
		return out
	}

	for _, l := range g.Listeners {
		for _, c := range g.Connectors {
			if l.Port != 0 && c.Port != 0 && l.Port != c.Port {
				out = append(out, domain.RoutingKeyMismatch{
					Kind: domain.MismatchPort, ListenerID: l.ID, ConnectorID: c.ID,
					Detail: fmt.Sprintf("listener %s/%s listens on port %d while connector %s/%s targets port %d; "+
						"Skupper maps the routing key rather than the port, so this is a smell rather than a fault",
						l.Namespace, l.Name, l.Port, c.Namespace, c.Name, c.Port),
				})
			}
			if !protocolsCompatible(l.Protocol, c.Protocol) {
				out = append(out, domain.RoutingKeyMismatch{
					Kind: domain.MismatchProtocol, ListenerID: l.ID, ConnectorID: c.ID,
					Detail: fmt.Sprintf("listener %s/%s declares protocol %q while connector %s/%s declares %q",
						l.Namespace, l.Name, displayProtocol(l.Protocol), c.Namespace, c.Name, displayProtocol(c.Protocol)),
				})
			}
		}
	}
	return out
}

// protocolsCompatible compares declared protocols. An unset protocol is the
// Skupper default and matches anything, so it is never reported as a mismatch.
func protocolsCompatible(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return true
	}
	return a == b
}

func displayProtocol(p string) string {
	if p == "" {
		return "tcp (default)"
	}
	return p
}
