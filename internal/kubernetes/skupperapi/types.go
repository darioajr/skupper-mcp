// Package skupperapi holds decode-only mirrors of the Skupper v2 custom
// resource contract.
//
// These are deliberately not imported from github.com/skupperproject/skupper.
// That module publishes no semver tag consumable by the Go module proxy, has no
// /v2 module path, pins an older client-go, and pulls the entire Skupper
// product dependency graph. See docs/adr/0001-skupper-api-access-strategy.md.
//
// Every field here mirrors pkg/apis/skupper/v2alpha1/types.go at commit
// 06223ca4030444a2c7b273fa19d8decb1c9e3f87. Fields this server does not read
// are omitted rather than carried, and tlsCredentials is omitted on purpose:
// it names a Secret, and no Secret name reaches a response.
package skupperapi

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GroupName is the Skupper v2 API group.
const GroupName = "skupper.io"

// Version is the Skupper v2 API version.
const Version = "v2alpha1"

// Resource names, as used by the dynamic client.
const (
	ResourceSites      = "sites"
	ResourceLinks      = "links"
	ResourceListeners  = "listeners"
	ResourceConnectors = "connectors"
)

// Kind names, as published in the CRDs.
const (
	KindSite      = "Site"
	KindLink      = "Link"
	KindListener  = "Listener"
	KindConnector = "Connector"
)

// Status is the block every Skupper v2 resource status embeds.
type Status struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	StatusType string             `json:"status,omitempty"`
	Message    string             `json:"message,omitempty"`
}

// Endpoint is a reachable address.
type Endpoint struct {
	Name  string `json:"name,omitempty"`
	Host  string `json:"host,omitempty"`
	Port  string `json:"port,omitempty"`
	Group string `json:"group,omitempty"`
}

// Site mirrors skupper.io/v2alpha1 Site.
type Site struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SiteSpec   `json:"spec,omitempty"`
	Status            SiteStatus `json:"status,omitempty"`
}

// SiteSpec mirrors SiteSpec. The serviceAccount and defaultIssuer fields are
// read but not exposed; settings is a free-form map and is not exposed at all.
type SiteSpec struct {
	LinkAccess string `json:"linkAccess,omitempty"`
	Edge       bool   `json:"edge,omitempty"`
	HA         bool   `json:"ha,omitempty"`
}

// SiteStatus mirrors SiteStatus.
type SiteStatus struct {
	Status         `json:",inline"`
	Endpoints      []Endpoint   `json:"endpoints,omitempty"`
	SitesInNetwork int          `json:"sitesInNetwork,omitempty"`
	Network        []SiteRecord `json:"network,omitempty"`
}

// SiteRecord mirrors SiteRecord.
type SiteRecord struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Platform  string          `json:"platform,omitempty"`
	Version   string          `json:"version,omitempty"`
	Links     []LinkRecord    `json:"links,omitempty"`
	Services  []ServiceRecord `json:"services,omitempty"`
}

// LinkRecord mirrors LinkRecord.
type LinkRecord struct {
	Name           string `json:"name,omitempty"`
	RemoteSiteID   string `json:"remoteSiteId,omitempty"`
	RemoteSiteName string `json:"remoteSiteName,omitempty"`
	Operational    bool   `json:"operational,omitempty"`
}

// ServiceRecord mirrors ServiceRecord.
type ServiceRecord struct {
	RoutingKey string   `json:"routingKey,omitempty"`
	Connectors []string `json:"connectors,omitempty"`
	Listeners  []string `json:"listeners,omitempty"`
}

// Link mirrors skupper.io/v2alpha1 Link.
type Link struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LinkSpec   `json:"spec,omitempty"`
	Status            LinkStatus `json:"status,omitempty"`
}

// LinkSpec mirrors LinkSpec, minus tlsCredentials.
type LinkSpec struct {
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	Cost      int        `json:"cost,omitempty"`
}

// LinkStatus mirrors LinkStatus.
type LinkStatus struct {
	Status         `json:",inline"`
	RemoteSiteID   string `json:"remoteSiteId,omitempty"`
	RemoteSiteName string `json:"remoteSiteName,omitempty"`
}

// Listener mirrors skupper.io/v2alpha1 Listener.
type Listener struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ListenerSpec   `json:"spec,omitempty"`
	Status            ListenerStatus `json:"status,omitempty"`
}

// ListenerSpec mirrors ListenerSpec, minus tlsCredentials.
type ListenerSpec struct {
	RoutingKey string `json:"routingKey,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	// Type is the Skupper protocol selector: tcp, http, http2 and so on.
	Type string `json:"type,omitempty"`
}

// ListenerStatus mirrors ListenerStatus.
type ListenerStatus struct {
	Status               `json:",inline"`
	HasMatchingConnector bool `json:"hasMatchingConnector,omitempty"`
}

// Connector mirrors skupper.io/v2alpha1 Connector.
type Connector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ConnectorSpec   `json:"spec,omitempty"`
	Status            ConnectorStatus `json:"status,omitempty"`
}

// ConnectorSpec mirrors ConnectorSpec, minus tlsCredentials.
type ConnectorSpec struct {
	RoutingKey string `json:"routingKey,omitempty"`
	Host       string `json:"host,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Port       int    `json:"port,omitempty"`
	Type       string `json:"type,omitempty"`
}

// ConnectorStatus mirrors ConnectorStatus. Selected pod names and IPs are
// counted but never carried into the domain.
type ConnectorStatus struct {
	Status              `json:",inline"`
	SelectedPods        []PodDetails `json:"selectedPods,omitempty"`
	HasMatchingListener bool         `json:"hasMatchingListener,omitempty"`
}

// PodDetails mirrors PodDetails.
type PodDetails struct {
	Name string `json:"name,omitempty"`
	IP   string `json:"ip,omitempty"`
}
