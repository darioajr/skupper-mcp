package domain

import "time"

// ResourceRef is the stable identity of an observed Kubernetes resource.
//
// ID is the resource UID when the cluster reported one. Skupper itself derives
// a site's identity from the UID (Site.GetSiteId), so the UID is the only
// cross-cluster-stable handle available to a read-only observer. When the UID
// is absent, ID falls back to "context/namespace/name", which is stable within
// a single cluster.
type ResourceRef struct {
	ID              string `json:"id"`
	Context         string `json:"context"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	APIVersion      string `json:"apiVersion,omitempty"`
	Kind            string `json:"kind,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`

	Generation         int64 `json:"generation,omitempty"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// GenerationObserved is false when the controller has not yet reported an
	// observed generation, which is different from having observed generation 0.
	GenerationObserved bool `json:"generationObserved"`

	CreationTimestamp time.Time `json:"creationTimestamp,omitzero"`
}

// Stale reports whether the controller's observed generation lags the spec
// generation, meaning the reported status describes an older spec.
func (r ResourceRef) Stale() bool {
	return r.GenerationObserved && r.Generation > r.ObservedGeneration
}

// ObservedState is the status block every Skupper v2 resource embeds.
type ObservedState struct {
	Status     StatusType  `json:"status"`
	Message    string      `json:"message,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// Site is a Skupper site. In Skupper v2 a namespace holds at most one site, so
// (Context, Namespace) identifies the site that owns every listener, connector
// and link in that namespace.
type Site struct {
	ResourceRef   `json:",inline"`
	ObservedState `json:",inline"`

	LinkAccess     string     `json:"linkAccess,omitempty"`
	Edge           bool       `json:"edge,omitempty"`
	HA             bool       `json:"ha,omitempty"`
	Endpoints      []Endpoint `json:"endpoints,omitempty"`
	SitesInNetwork int        `json:"sitesInNetwork,omitempty"`
	// Network is the site's own view of the application network, as published
	// by its controller. It is declared state from a peer's perspective and is
	// treated as corroborating evidence, never as ground truth.
	Network []SiteRecord `json:"network,omitempty"`
}

// Endpoint is a reachable address published in a site or link.
type Endpoint struct {
	Name  string `json:"name,omitempty"`
	Host  string `json:"host,omitempty"`
	Port  string `json:"port,omitempty"`
	Group string `json:"group,omitempty"`
}

// SiteRecord is one entry of a site's published network view.
type SiteRecord struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Platform  string          `json:"platform,omitempty"`
	Version   string          `json:"version,omitempty"`
	Links     []LinkRecord    `json:"links,omitempty"`
	Services  []ServiceRecord `json:"services,omitempty"`
}

// LinkRecord is a link as reported inside a site's network view.
type LinkRecord struct {
	Name           string `json:"name,omitempty"`
	RemoteSiteID   string `json:"remoteSiteId,omitempty"`
	RemoteSiteName string `json:"remoteSiteName,omitempty"`
	Operational    bool   `json:"operational"`
}

// ServiceRecord is a routing key as reported inside a site's network view.
type ServiceRecord struct {
	RoutingKey string   `json:"routingKey,omitempty"`
	Connectors []string `json:"connectors,omitempty"`
	Listeners  []string `json:"listeners,omitempty"`
}

// Link is an outbound connection from the site in this namespace to a remote
// site. Skupper links are directional in configuration but carry traffic in
// both directions once established.
type Link struct {
	ResourceRef   `json:",inline"`
	ObservedState `json:",inline"`

	// LocalSiteID is the site in the link's own namespace, resolved by the
	// mapper. It is empty when no site was visible there.
	LocalSiteID   string `json:"localSiteId,omitempty"`
	LocalSiteName string `json:"localSiteName,omitempty"`

	RemoteSiteID   string `json:"remoteSiteId,omitempty"`
	RemoteSiteName string `json:"remoteSiteName,omitempty"`

	Cost      int        `json:"cost,omitempty"`
	Endpoints []Endpoint `json:"endpoints,omitempty"`
}

// Listener exposes a routing key locally so traffic can be sent into the
// network.
type Listener struct {
	ResourceRef   `json:",inline"`
	ObservedState `json:",inline"`

	SiteID     string `json:"siteId,omitempty"`
	SiteName   string `json:"siteName,omitempty"`
	RoutingKey string `json:"routingKey,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	// Protocol is the Skupper spec "type" field (tcp, http, http2, ...).
	Protocol string `json:"protocol,omitempty"`
	// HasMatchingConnector is reported by the controller, not inferred here.
	HasMatchingConnector bool `json:"hasMatchingConnector"`
}

// Connector attaches a local workload to a routing key so it can serve traffic
// from the network.
type Connector struct {
	ResourceRef   `json:",inline"`
	ObservedState `json:",inline"`

	SiteID     string `json:"siteId,omitempty"`
	SiteName   string `json:"siteName,omitempty"`
	RoutingKey string `json:"routingKey,omitempty"`
	Host       string `json:"host,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Port       int    `json:"port,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	// SelectedPodCount is the number of pods the controller matched. Pod names
	// and IPs are deliberately not carried into the domain.
	SelectedPodCount int `json:"selectedPodCount"`
	// HasMatchingListener is reported by the controller, not inferred here.
	HasMatchingListener bool `json:"hasMatchingListener"`
}

// Snapshot is a consistent-enough view of one query across the allowed scope.
// It is assembled from independent list calls, so Warnings records anything
// that could not be read.
type Snapshot struct {
	Sites      []Site      `json:"sites"`
	Links      []Link      `json:"links"`
	Listeners  []Listener  `json:"listeners"`
	Connectors []Connector `json:"connectors"`
	Warnings   []string    `json:"warnings,omitempty"`
}

// SiteKey identifies the site that owns a namespace.
type SiteKey struct {
	Context   string
	Namespace string
}

// Key returns the namespace-scoped site key for a resource.
func (r ResourceRef) Key() SiteKey {
	return SiteKey{Context: r.Context, Namespace: r.Namespace}
}
