package domain

// Topology is a structured view of an application network. It is returned as
// typed JSON so the MCP client decides how to render it; this package never
// produces Mermaid or any other presentation format.
type Topology struct {
	Nodes         []TopologyNode    `json:"nodes"`
	Edges         []TopologyEdge    `json:"edges"`
	RoutingKeys   []RoutingKeyGroup `json:"routingKeys"`
	Warnings      []string          `json:"warnings,omitempty"`
	ObservedScope []ScopeEntry      `json:"observedScope"`
}

// ScopeEntry records exactly which context/namespace pairs were read, so a
// caller can tell an empty result from an unread one.
type ScopeEntry struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
}

// TopologyNode is a site.
type TopologyNode struct {
	ID        string     `json:"id"`
	Context   string     `json:"context"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Status    StatusType `json:"status"`
	Message   string     `json:"message,omitempty"`
	Edge      bool       `json:"edge,omitempty"`
	HA        bool       `json:"ha,omitempty"`
	// Observed is false for a site that is referenced by a link but was not
	// itself read, because it lives outside the allowed scope.
	Observed bool `json:"observed"`

	Listeners  []TopologyEndpointRef `json:"listeners,omitempty"`
	Connectors []TopologyEndpointRef `json:"connectors,omitempty"`
}

// TopologyEndpointRef is a listener or connector attached to a site.
type TopologyEndpointRef struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	RoutingKey string     `json:"routingKey,omitempty"`
	Host       string     `json:"host,omitempty"`
	Port       int        `json:"port,omitempty"`
	Protocol   string     `json:"protocol,omitempty"`
	Selector   string     `json:"selector,omitempty"`
	Status     StatusType `json:"status"`
}

// TopologyEdge is a link between two sites.
type TopologyEdge struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Source string     `json:"source"`
	Target string     `json:"target,omitempty"`
	Status StatusType `json:"status"`

	Message string `json:"message,omitempty"`
	Cost    int    `json:"cost,omitempty"`
	// TargetResolved is false when the link names a remote site that was not
	// observed, so the edge dangles.
	TargetResolved bool `json:"targetResolved"`
}

// RoutingKeyGroup associates listeners and connectors that share a routing key.
// A routing key is the only association Skupper exposes between the two sides;
// it is a name match, not a proof that traffic flows.
type RoutingKeyGroup struct {
	RoutingKey string               `json:"routingKey"`
	Listeners  []RoutingKeyMember   `json:"listeners"`
	Connectors []RoutingKeyMember   `json:"connectors"`
	Mismatches []RoutingKeyMismatch `json:"mismatches,omitempty"`
}

// RoutingKeyMember is one side of a routing key association.
type RoutingKeyMember struct {
	ID        string `json:"id"`
	SiteID    string `json:"siteId,omitempty"`
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int    `json:"port,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
}

// RoutingKeyMismatch records a difference between the two sides of a routing
// key that is likely, but not certainly, a misconfiguration.
type RoutingKeyMismatch struct {
	Kind        string `json:"kind"`
	Detail      string `json:"detail"`
	ListenerID  string `json:"listenerId,omitempty"`
	ConnectorID string `json:"connectorId,omitempty"`
}

// Mismatch kinds.
const (
	MismatchPort            = "port"
	MismatchProtocol        = "protocol"
	MismatchListenerOrphan  = "listener-without-connector"
	MismatchConnectorOrphan = "connector-without-listener"
)
