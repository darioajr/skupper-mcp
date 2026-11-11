package kubernetes

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/darioajr/skupper-scope/internal/domain"
	"github.com/darioajr/skupper-scope/internal/kubernetes/skupperapi"
)

// decode converts one unstructured object into a typed wire struct.
//
// Unknown fields are ignored rather than rejected, so a newer Skupper CRD adds
// data this server does not read without breaking it.
func decode[T any](obj unstructured.Unstructured) (T, error) {
	var out T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &out); err != nil {
		return out, fmt.Errorf("decoding %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
	}
	return out, nil
}

// mapRef builds the stable identity of a resource.
func mapRef(contextName string, meta metav1.ObjectMeta, typeMeta metav1.TypeMeta, conditions []domain.Condition) domain.ResourceRef {
	id := string(meta.UID)
	if id == "" {
		// A resource read from the API always has a UID; a hand-written fixture
		// may not. Falling back keeps identity stable within one cluster instead
		// of collapsing every such resource onto the empty string.
		id = contextName + "/" + meta.Namespace + "/" + meta.Name
	}
	observed, ok := domain.ObservedGeneration(conditions)
	ref := domain.ResourceRef{
		ID:                 id,
		Context:            contextName,
		Namespace:          meta.Namespace,
		Name:               meta.Name,
		UID:                string(meta.UID),
		APIVersion:         typeMeta.APIVersion,
		Kind:               typeMeta.Kind,
		ResourceVersion:    meta.ResourceVersion,
		Generation:         meta.Generation,
		ObservedGeneration: observed,
		GenerationObserved: ok,
	}
	if !meta.CreationTimestamp.IsZero() {
		ref.CreationTimestamp = meta.CreationTimestamp.Time
	}
	return ref
}

func mapConditions(in []metav1.Condition) []domain.Condition {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Condition, 0, len(in))
	for _, c := range in {
		mapped := domain.Condition{
			Type:               c.Type,
			Status:             string(c.Status),
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		}
		if !c.LastTransitionTime.IsZero() {
			mapped.LastTransitionTime = c.LastTransitionTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, mapped)
	}
	return out
}

func mapObservedState(s skupperapi.Status) domain.ObservedState {
	return domain.ObservedState{
		Status:     domain.ParseStatusType(s.StatusType),
		Message:    s.Message,
		Conditions: mapConditions(s.Conditions),
	}
}

func mapEndpoints(in []skupperapi.Endpoint) []domain.Endpoint {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Endpoint, 0, len(in))
	for _, e := range in {
		out = append(out, domain.Endpoint{Name: e.Name, Host: e.Host, Port: e.Port, Group: e.Group})
	}
	return out
}

// MapSite converts a Skupper Site into the domain.
func MapSite(contextName string, obj unstructured.Unstructured) (domain.Site, error) {
	src, err := decode[skupperapi.Site](obj)
	if err != nil {
		return domain.Site{}, err
	}
	state := mapObservedState(src.Status.Status)
	site := domain.Site{
		ResourceRef:    mapRef(contextName, src.ObjectMeta, src.TypeMeta, state.Conditions),
		ObservedState:  state,
		LinkAccess:     src.Spec.LinkAccess,
		Edge:           src.Spec.Edge,
		HA:             src.Spec.HA,
		Endpoints:      mapEndpoints(src.Status.Endpoints),
		SitesInNetwork: src.Status.SitesInNetwork,
	}
	site.Kind = orDefault(site.Kind, skupperapi.KindSite)
	for _, r := range src.Status.Network {
		record := domain.SiteRecord{
			ID: r.ID, Name: r.Name, Namespace: r.Namespace,
			Platform: r.Platform, Version: r.Version,
		}
		for _, l := range r.Links {
			record.Links = append(record.Links, domain.LinkRecord{
				Name: l.Name, RemoteSiteID: l.RemoteSiteID,
				RemoteSiteName: l.RemoteSiteName, Operational: l.Operational,
			})
		}
		for _, s := range r.Services {
			record.Services = append(record.Services, domain.ServiceRecord{
				RoutingKey: s.RoutingKey, Connectors: s.Connectors, Listeners: s.Listeners,
			})
		}
		site.Network = append(site.Network, record)
	}
	return site, nil
}

// MapLink converts a Skupper Link into the domain. The link's local site is
// filled in later by the application layer, which is the only layer that sees
// sites and links together.
func MapLink(contextName string, obj unstructured.Unstructured) (domain.Link, error) {
	src, err := decode[skupperapi.Link](obj)
	if err != nil {
		return domain.Link{}, err
	}
	state := mapObservedState(src.Status.Status)
	link := domain.Link{
		ResourceRef:    mapRef(contextName, src.ObjectMeta, src.TypeMeta, state.Conditions),
		ObservedState:  state,
		RemoteSiteID:   src.Status.RemoteSiteID,
		RemoteSiteName: src.Status.RemoteSiteName,
		Cost:           src.Spec.Cost,
		Endpoints:      mapEndpoints(src.Spec.Endpoints),
	}
	link.Kind = orDefault(link.Kind, skupperapi.KindLink)
	return link, nil
}

// MapListener converts a Skupper Listener into the domain.
func MapListener(contextName string, obj unstructured.Unstructured) (domain.Listener, error) {
	src, err := decode[skupperapi.Listener](obj)
	if err != nil {
		return domain.Listener{}, err
	}
	state := mapObservedState(src.Status.Status)
	l := domain.Listener{
		ResourceRef:          mapRef(contextName, src.ObjectMeta, src.TypeMeta, state.Conditions),
		ObservedState:        state,
		RoutingKey:           src.Spec.RoutingKey,
		Host:                 src.Spec.Host,
		Port:                 src.Spec.Port,
		Protocol:             src.Spec.Type,
		HasMatchingConnector: src.Status.HasMatchingConnector,
	}
	l.Kind = orDefault(l.Kind, skupperapi.KindListener)
	return l, nil
}

// MapConnector converts a Skupper Connector into the domain. Selected pod names
// and IP addresses are reduced to a count: they identify workloads and have no
// diagnostic value beyond "how many were matched".
func MapConnector(contextName string, obj unstructured.Unstructured) (domain.Connector, error) {
	src, err := decode[skupperapi.Connector](obj)
	if err != nil {
		return domain.Connector{}, err
	}
	state := mapObservedState(src.Status.Status)
	c := domain.Connector{
		ResourceRef:         mapRef(contextName, src.ObjectMeta, src.TypeMeta, state.Conditions),
		ObservedState:       state,
		RoutingKey:          src.Spec.RoutingKey,
		Host:                src.Spec.Host,
		Selector:            src.Spec.Selector,
		Port:                src.Spec.Port,
		Protocol:            src.Spec.Type,
		SelectedPodCount:    len(src.Status.SelectedPods),
		HasMatchingListener: src.Status.HasMatchingListener,
	}
	c.Kind = orDefault(c.Kind, skupperapi.KindConnector)
	return c, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
