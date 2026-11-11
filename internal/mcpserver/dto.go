// Package mcpserver exposes the application through MCP tools and resources.
//
// Tools and resources share one application.Service, so the two surfaces cannot
// drift apart. This package owns the MCP-facing DTOs; the domain types stay
// free of any MCP concern.
package mcpserver

import (
	"github.com/darioajr/skupper-scope/internal/domain"
)

// ScopeArgs are the scope arguments common to the listing tools.
//
// There is deliberately no kubeconfig path, no API server URL, and no label
// selector here. A client may only name a context and a namespace that the
// operator already allowed.
type ScopeArgs struct {
	Context   string `json:"context,omitempty" jsonschema:"logical Kubernetes context name to read; omit to read every allowed context"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace to read; omit to read every namespace allowed in that context"`
}

// ListSitesInput is the input of list_sites.
type ListSitesInput struct {
	ScopeArgs
}

// ListSitesOutput is the output of list_sites.
type ListSitesOutput struct {
	Sites    []domain.Site       `json:"sites"`
	Scope    []domain.ScopeEntry `json:"scope"`
	Warnings []string            `json:"warnings,omitempty"`
}

// ListLinksInput is the input of list_links.
type ListLinksInput struct {
	ScopeArgs
	Site string `json:"site,omitempty" jsonschema:"site name or site id; when set, only links whose local or remote end is that site are returned"`
}

// ListLinksOutput is the output of list_links.
type ListLinksOutput struct {
	Links    []domain.Link       `json:"links"`
	Scope    []domain.ScopeEntry `json:"scope"`
	Warnings []string            `json:"warnings,omitempty"`
}

// ListListenersInput is the input of list_listeners.
type ListListenersInput struct {
	ScopeArgs
	Site string `json:"site,omitempty" jsonschema:"site name or site id owning the listeners"`
}

// ListListenersOutput is the output of list_listeners.
type ListListenersOutput struct {
	Listeners []domain.Listener   `json:"listeners"`
	Scope     []domain.ScopeEntry `json:"scope"`
	Warnings  []string            `json:"warnings,omitempty"`
}

// ListConnectorsInput is the input of list_connectors.
type ListConnectorsInput struct {
	ScopeArgs
	Site string `json:"site,omitempty" jsonschema:"site name or site id owning the connectors"`
}

// ListConnectorsOutput is the output of list_connectors.
type ListConnectorsOutput struct {
	Connectors []domain.Connector  `json:"connectors"`
	Scope      []domain.ScopeEntry `json:"scope"`
	Warnings   []string            `json:"warnings,omitempty"`
}

// GetTopologyInput is the input of get_topology.
type GetTopologyInput struct {
	ScopeArgs
}

// GetTopologyOutput is the output of get_topology. It is typed JSON, never a
// rendered diagram: how to draw the network is the client's decision.
type GetTopologyOutput struct {
	domain.Topology
}

// DiagnoseConnectivityInput is the input of diagnose_connectivity.
type DiagnoseConnectivityInput struct {
	SourceContext   string `json:"sourceContext,omitempty" jsonschema:"logical Kubernetes context of the source site"`
	SourceNamespace string `json:"sourceNamespace,omitempty" jsonschema:"namespace of the source site; required unless sourceSite is given"`
	SourceSite      string `json:"sourceSite,omitempty" jsonschema:"name or id of the source site; required unless sourceNamespace is given"`
	TargetContext   string `json:"targetContext,omitempty" jsonschema:"logical Kubernetes context of the target site"`
	TargetNamespace string `json:"targetNamespace,omitempty" jsonschema:"namespace of the target site; required unless targetSite is given"`
	TargetSite      string `json:"targetSite,omitempty" jsonschema:"name or id of the target site; required unless targetNamespace is given"`
	RoutingKey      string `json:"routingKey" jsonschema:"the Skupper routing key whose path should be analysed"`
}

// DiagnoseConnectivityOutput is the output of diagnose_connectivity.
type DiagnoseConnectivityOutput struct {
	domain.Diagnosis
}
