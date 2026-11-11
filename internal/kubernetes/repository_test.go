package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
)

func listKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		sitesGVR:      "SiteList",
		linksGVR:      "LinkList",
		listenersGVR:  "ListenerList",
		connectorsGVR: "ConnectorList",
	}
}

func newFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
}

func testRepository(t *testing.T, clients map[string]dynamic.Interface, mutate func(*config.Config)) *Repository {
	t.Helper()
	cfg := config.Default()
	cfg.Kubernetes.AllowedContexts = []string{"west-cluster"}
	cfg.Kubernetes.AllowedNamespaces = []string{"west"}
	if mutate != nil {
		mutate(&cfg)
	}
	repo, err := NewRepository(NewClientFactoryWithClients(cfg.Kubernetes, clients), cfg)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	return repo
}

func asObjects(items []unstructured.Unstructured) []runtime.Object {
	out := make([]runtime.Object, 0, len(items))
	for i := range items {
		out = append(out, &items[i])
	}
	return out
}

func TestRepositoryListsEachKind(t *testing.T) {
	var objs []runtime.Object
	objs = append(objs, asObjects(loadFixture(t, "sites.yaml"))...)
	objs = append(objs, asObjects(loadFixture(t, "links.yaml"))...)
	objs = append(objs, asObjects(loadFixture(t, "listeners.yaml"))...)
	objs = append(objs, asObjects(loadFixture(t, "connectors.yaml"))...)

	client := newFakeClient(objs...)
	repo := testRepository(t, map[string]dynamic.Interface{"west-cluster": client}, nil)
	ctx := context.Background()
	q := application.Query{Scope: application.Scope{Context: "west-cluster", Namespace: "west"}, Limit: 100}

	sites, err := repo.ListSites(ctx, q)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(sites.Items) != 1 || sites.Items[0].Name != "west" {
		t.Errorf("ListSites returned %d item(s) %+v, want only the site in namespace west",
			len(sites.Items), sites.Items)
	}

	links, err := repo.ListLinks(ctx, q)
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(links.Items) != 1 {
		t.Errorf("ListLinks returned %d items, want 1", len(links.Items))
	}

	listeners, err := repo.ListListeners(ctx, q)
	if err != nil {
		t.Fatalf("ListListeners: %v", err)
	}
	if len(listeners.Items) != 1 {
		t.Errorf("ListListeners returned %d items, want 1", len(listeners.Items))
	}

	connectors, err := repo.ListConnectors(ctx, q)
	if err != nil {
		t.Fatalf("ListConnectors: %v", err)
	}
	if len(connectors.Items) != 0 {
		t.Errorf("ListConnectors in namespace west returned %d items, want 0; "+
			"the connector fixture lives in namespace east", len(connectors.Items))
	}
}

func TestRepositoryRefusesClusterWideList(t *testing.T) {
	repo := testRepository(t, map[string]dynamic.Interface{"west-cluster": newFakeClient()}, nil)
	_, err := repo.ListSites(context.Background(),
		application.Query{Scope: application.Scope{Context: "west-cluster"}, Limit: 10})
	if err == nil {
		t.Fatal("ListSites with an empty namespace returned no error; a cluster-wide read must be refused")
	}
	if got := err.Error(); !strings.Contains(got, "cluster-wide") {
		t.Errorf("error = %q, want it to explain that a namespace is required", got)
	}
}

func TestRepositoryRejectsNonPositiveLimit(t *testing.T) {
	repo := testRepository(t, map[string]dynamic.Interface{"west-cluster": newFakeClient()}, nil)
	_, err := repo.ListSites(context.Background(),
		application.Query{Scope: application.Scope{Context: "west-cluster", Namespace: "west"}, Limit: 0})
	if err == nil {
		t.Fatal("ListSites with limit 0 returned no error, want a rejection")
	}
}

func TestRepositoryTruncatesAtLimit(t *testing.T) {
	var objs []runtime.Object
	for i := range 5 {
		objs = append(objs, newSiteObject(fmt.Sprintf("site-%d", i), "west", fmt.Sprintf("uid-%d", i)))
	}
	repo := testRepository(t, map[string]dynamic.Interface{"west-cluster": newFakeClient(objs...)},
		func(c *config.Config) { c.Kubernetes.PageSize = 2 })

	res, err := repo.ListSites(context.Background(),
		application.Query{Scope: application.Scope{Context: "west-cluster", Namespace: "west"}, Limit: 3})
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(res.Items) != 3 {
		t.Errorf("len(Items) = %d, want exactly the limit of 3", len(res.Items))
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true when more items exist than the limit allows")
	}
}

func TestRepositoryUnknownContextIsScopeUnavailable(t *testing.T) {
	repo := testRepository(t, map[string]dynamic.Interface{"west-cluster": newFakeClient()}, nil)
	_, err := repo.ListSites(context.Background(),
		application.Query{Scope: application.Scope{Context: "nowhere", Namespace: "west"}, Limit: 10})
	if !errors.Is(err, application.ErrScopeUnavailable) {
		t.Errorf("error = %v, want it to wrap application.ErrScopeUnavailable so the caller degrades "+
			"to a warning rather than failing the whole tool call", err)
	}
}

func TestClassifyListErrorDegradesRecoverableFailures(t *testing.T) {
	scope := application.Scope{Context: "west-cluster", Namespace: "west"}
	gr := schema.GroupResource{Group: "skupper.io", Resource: "sites"}

	tests := []struct {
		name        string
		err         error
		degradable  bool
		wantMessage string
	}{
		{
			name:        "CRD not installed",
			err:         apierrors.NewNotFound(gr, "sites"),
			degradable:  true,
			wantMessage: "CRD is not installed",
		},
		{
			name:        "forbidden",
			err:         apierrors.NewForbidden(gr, "sites", errors.New("no")),
			degradable:  true,
			wantMessage: "grant get and list",
		},
		{
			name:       "server timeout",
			err:        apierrors.NewServerTimeout(gr, "list", 1),
			degradable: true,
		},
		{
			name:       "too many requests",
			err:        apierrors.NewTooManyRequests("slow down", 1),
			degradable: true,
		},
		{
			name:       "unexpected failure is a real error",
			err:        errors.New("connection reset by peer"),
			degradable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyListError(sitesGVR, scope, tt.err)
			if errors.Is(got, application.ErrScopeUnavailable) != tt.degradable {
				t.Errorf("errors.Is(err, ErrScopeUnavailable) = %t, want %t (err = %v)",
					!tt.degradable, tt.degradable, got)
			}
			if tt.wantMessage != "" && !strings.Contains(got.Error(), tt.wantMessage) {
				t.Errorf("error = %q, want it to contain %q", got, tt.wantMessage)
			}
		})
	}
}

func TestRepositoryPropagatesForbiddenAsScopeUnavailable(t *testing.T) {
	client := newFakeClient()
	client.PrependReactor("list", "sites", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "skupper.io", Resource: "sites"}, "sites", errors.New("RBAC denied"))
	})
	repo := testRepository(t, map[string]dynamic.Interface{"west-cluster": client}, nil)

	_, err := repo.ListSites(context.Background(),
		application.Query{Scope: application.Scope{Context: "west-cluster", Namespace: "west"}, Limit: 10})
	if !errors.Is(err, application.ErrScopeUnavailable) {
		t.Errorf("error = %v, want ErrScopeUnavailable so a single denied namespace does not "+
			"fail a multi-cluster query", err)
	}
}

func newSiteObject(name, namespace, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "skupper.io/v2alpha1",
		"kind":       "Site",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       uid,
		},
		"status": map[string]any{"status": string(domain.StatusReady)},
	}}
}
