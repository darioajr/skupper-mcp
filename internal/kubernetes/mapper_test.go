package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/darioajr/skupper-scope/internal/domain"
)

func TestMapSite(t *testing.T) {
	objs := loadFixture(t, "sites.yaml")
	site, err := MapSite("west-cluster", fixtureByName(t, objs, "west"))
	if err != nil {
		t.Fatalf("MapSite: %v", err)
	}

	if got, want := site.ID, "11111111-1111-1111-1111-111111111111"; got != want {
		t.Errorf("ID = %q, want the resource UID %q", got, want)
	}
	if got, want := site.Context, "west-cluster"; got != want {
		t.Errorf("Context = %q, want %q", got, want)
	}
	if got, want := site.Kind, "Site"; got != want {
		t.Errorf("Kind = %q, want %q", got, want)
	}
	if got, want := site.APIVersion, "skupper.io/v2alpha1"; got != want {
		t.Errorf("APIVersion = %q, want %q", got, want)
	}
	if site.Status != domain.StatusReady {
		t.Errorf("Status = %q, want Ready", site.Status)
	}
	if got, want := site.Generation, int64(3); got != want {
		t.Errorf("Generation = %d, want %d", got, want)
	}
	if !site.GenerationObserved || site.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration = %d (observed=%t), want 3 taken from the Ready condition",
			site.ObservedGeneration, site.GenerationObserved)
	}
	if site.Stale() {
		t.Error("Stale() = true, want false when observedGeneration equals generation")
	}
	if got, want := site.SitesInNetwork, 2; got != want {
		t.Errorf("SitesInNetwork = %d, want %d", got, want)
	}
	if len(site.Endpoints) != 1 || site.Endpoints[0].Port != "55671" {
		t.Errorf("Endpoints = %+v, want the single inter-router endpoint on port 55671", site.Endpoints)
	}
	if len(site.Network) != 1 || len(site.Network[0].Links) != 1 {
		t.Fatalf("Network = %+v, want one site record carrying one link record", site.Network)
	}
	if got, want := site.Network[0].Links[0].RemoteSiteName, "east"; got != want {
		t.Errorf("network link remote site = %q, want %q", got, want)
	}
	if site.CreationTimestamp.IsZero() {
		t.Error("CreationTimestamp is zero, want the fixture's timestamp")
	}
	if got, want := len(site.Conditions), 3; got != want {
		t.Errorf("len(Conditions) = %d, want %d", got, want)
	}
}

func TestMapSiteStaleStatus(t *testing.T) {
	objs := loadFixture(t, "degraded.yaml")
	site, err := MapSite("north-cluster", fixtureByName(t, objs, "north"))
	if err != nil {
		t.Fatalf("MapSite: %v", err)
	}
	if !site.Stale() {
		t.Errorf("Stale() = false; generation %d with observedGeneration %d must be reported as stale",
			site.Generation, site.ObservedGeneration)
	}
	if site.Status != domain.StatusPending {
		t.Errorf("Status = %q, want Pending", site.Status)
	}
	if got, want := site.Message, "waiting for router"; got != want {
		t.Errorf("Message = %q, want %q", got, want)
	}
}

func TestMapLink(t *testing.T) {
	objs := loadFixture(t, "links.yaml")
	link, err := MapLink("west-cluster", fixtureByName(t, objs, "west-to-east"))
	if err != nil {
		t.Fatalf("MapLink: %v", err)
	}
	if got, want := link.RemoteSiteID, "22222222-2222-2222-2222-222222222222"; got != want {
		t.Errorf("RemoteSiteID = %q, want %q", got, want)
	}
	if got, want := link.RemoteSiteName, "east"; got != want {
		t.Errorf("RemoteSiteName = %q, want %q", got, want)
	}
	if got, want := link.Cost, 1; got != want {
		t.Errorf("Cost = %d, want %d", got, want)
	}
	if link.LocalSiteID != "" {
		t.Errorf("LocalSiteID = %q; the mapper must leave it empty, because only the "+
			"application layer sees sites and links together", link.LocalSiteID)
	}
	if len(link.Endpoints) != 1 || link.Endpoints[0].Host != "east-router.example.internal" {
		t.Errorf("Endpoints = %+v, want the fixture's single endpoint", link.Endpoints)
	}
}

func TestMapListener(t *testing.T) {
	objs := loadFixture(t, "listeners.yaml")
	l, err := MapListener("west-cluster", fixtureByName(t, objs, "backend"))
	if err != nil {
		t.Fatalf("MapListener: %v", err)
	}
	if got, want := l.RoutingKey, "backend"; got != want {
		t.Errorf("RoutingKey = %q, want %q", got, want)
	}
	if got, want := l.Port, 8080; got != want {
		t.Errorf("Port = %d, want %d", got, want)
	}
	if got, want := l.Protocol, "tcp"; got != want {
		t.Errorf("Protocol = %q, want the spec's type field %q", got, want)
	}
	if !l.HasMatchingConnector {
		t.Error("HasMatchingConnector = false, want true from the fixture status")
	}
}

func TestMapConnectorReducesPodsToACount(t *testing.T) {
	objs := loadFixture(t, "connectors.yaml")
	c, err := MapConnector("east-cluster", fixtureByName(t, objs, "backend"))
	if err != nil {
		t.Fatalf("MapConnector: %v", err)
	}
	if got, want := c.SelectedPodCount, 2; got != want {
		t.Errorf("SelectedPodCount = %d, want %d", got, want)
	}
	if got, want := c.Selector, "app=backend"; got != want {
		t.Errorf("Selector = %q, want %q", got, want)
	}

	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshalling connector: %v", err)
	}
	for _, leak := range []string{"backend-7d9f4c8b6-abcde", "10.128.2.41"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("connector JSON contains %q; pod names and IPs identify workloads and "+
				"must be reduced to a count", leak)
		}
	}
}

// TestMappersDropSecretNames pins the guarantee that no Secret name reaches the
// domain, for every kind that has a tlsCredentials field.
func TestMappersDropSecretNames(t *testing.T) {
	secretNames := []string{
		"link-west-to-east-secret",
		"listener-backend-secret",
		"connector-backend-secret",
		"north-link-secret",
	}

	var encoded []byte
	appendJSON := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshalling %T: %v", v, err)
		}
		encoded = append(encoded, b...)
	}

	for _, obj := range loadFixture(t, "links.yaml") {
		link, err := MapLink("c", obj)
		if err != nil {
			t.Fatal(err)
		}
		appendJSON(link)
	}
	for _, obj := range loadFixture(t, "listeners.yaml") {
		l, err := MapListener("c", obj)
		if err != nil {
			t.Fatal(err)
		}
		appendJSON(l)
	}
	for _, obj := range loadFixture(t, "connectors.yaml") {
		c, err := MapConnector("c", obj)
		if err != nil {
			t.Fatal(err)
		}
		appendJSON(c)
	}
	for _, obj := range loadFixture(t, "degraded.yaml") {
		if obj.GetKind() != "Link" {
			continue
		}
		link, err := MapLink("c", obj)
		if err != nil {
			t.Fatal(err)
		}
		appendJSON(link)
	}

	for _, name := range secretNames {
		if strings.Contains(string(encoded), name) {
			t.Errorf("mapped output contains the Secret name %q; tlsCredentials must never leave "+
				"the wire structs", name)
		}
	}
}

func TestMapRefFallsBackWhenUIDIsAbsent(t *testing.T) {
	objs := loadFixture(t, "sites.yaml")
	original := fixtureByName(t, objs, "west")
	obj := *original.DeepCopy()
	if meta, ok := obj.Object["metadata"].(map[string]any); ok {
		delete(meta, "uid")
	}

	site, err := MapSite("ctx", obj)
	if err != nil {
		t.Fatalf("MapSite: %v", err)
	}
	if got, want := site.ID, "ctx/west/west"; got != want {
		t.Errorf("ID = %q, want the context/namespace/name fallback %q", got, want)
	}
	if site.UID != "" {
		t.Errorf("UID = %q, want empty", site.UID)
	}
}
