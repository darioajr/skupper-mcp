package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
)

func TestNewServiceValidatesDependencies(t *testing.T) {
	cfg := config.Default()
	cfg.Kubernetes.AllowedNamespaces = []string{"west"}
	allowlist, err := cfg.Allowlist()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewService(nil, allowlist, 10); err == nil {
		t.Error("NewService accepted a nil repository")
	}
	if _, err := NewService(&memRepo{}, nil, 10); err == nil {
		t.Error("NewService accepted a nil allowlist")
	}
	if _, err := NewService(&memRepo{}, allowlist, 0); err == nil {
		t.Error("NewService accepted maxItems of 0")
	}
}

func TestListSitesAcrossEveryAllowedContext(t *testing.T) {
	svc := twoSiteService(t, twoSiteNetwork())
	res, err := svc.ListSites(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("len(Items) = %d, want both sites", len(res.Items))
	}
	if len(res.Scope) != 2 {
		t.Errorf("Scope = %v, want both context/namespace pairs so the caller can tell an "+
			"empty result from an unread one", res.Scope)
	}
	// Results are ordered so a client diffing two calls sees real changes only.
	if res.Items[0].Context != "east-cluster" {
		t.Errorf("Items[0].Context = %q, want results sorted by context/namespace/name",
			res.Items[0].Context)
	}
}

func TestListingRefusesAnUnlistedNamespace(t *testing.T) {
	svc := twoSiteService(t, twoSiteNetwork())
	_, err := svc.ListSites(context.Background(), "west-cluster", "kube-system")
	if !errors.Is(err, config.ErrNamespaceNotAllowed) {
		t.Fatalf("error = %v, want config.ErrNamespaceNotAllowed", err)
	}
}

func TestListingRefusesAnUnlistedContext(t *testing.T) {
	svc := twoSiteService(t, twoSiteNetwork())
	_, err := svc.ListSites(context.Background(), "prod-cluster", "")
	if !errors.Is(err, config.ErrContextNotAllowed) {
		t.Fatalf("error = %v, want config.ErrContextNotAllowed", err)
	}
}

func TestListingNeverReadsOutsideTheAllowlist(t *testing.T) {
	repo := twoSiteNetwork()
	svc := twoSiteService(t, repo)
	if _, err := svc.ListSites(context.Background(), "", ""); err != nil {
		t.Fatalf("ListSites: %v", err)
	}

	allowed := map[string]bool{"west-cluster/west": true, "east-cluster/east": true}
	for _, call := range repo.calls {
		key := call.Scope.Context + "/" + call.Scope.Namespace
		if !allowed[key] {
			t.Errorf("repository was queried for %q, which is outside the allowlist", key)
		}
		if call.Scope.Namespace == "" {
			t.Error("repository was queried with an empty namespace, which would read cluster-wide")
		}
	}
}

func TestMaxItemsCapsTheResultAndWarns(t *testing.T) {
	repo := &memRepo{sites: []domain.Site{
		site("west-cluster", "west", "a", "id-a"),
		site("west-cluster", "west", "b", "id-b"),
		site("west-cluster", "west", "c", "id-c"),
	}}
	svc := newService(t, repo, []string{"west-cluster"}, []string{"west"}, 2)

	res, err := svc.ListSites(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(res.Items) != 2 {
		t.Errorf("len(Items) = %d, want the cap of 2", len(res.Items))
	}
	if len(res.Warnings) == 0 {
		t.Fatal("no warning was returned; a truncated result must say so")
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "not shown") {
		t.Errorf("warnings = %v, want one that says resources were omitted", res.Warnings)
	}
}

func TestUnavailableScopeDegradesToAWarning(t *testing.T) {
	repo := twoSiteNetwork()
	repo.failScopes = map[string]error{
		"east-cluster/east": errors.New("dial tcp: connection refused"),
	}
	// Wrap the failure so it is recognisable as a scope problem rather than a
	// programming error.
	repo.failScopes["east-cluster/east"] = errors.Join(ErrScopeUnavailable, repo.failScopes["east-cluster/east"])

	svc := twoSiteService(t, repo)
	res, err := svc.ListSites(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListSites failed the whole call: %v; one unreachable cluster must degrade instead", err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "west" {
		t.Errorf("Items = %+v, want the reachable site only", res.Items)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "east-cluster/east") {
		t.Errorf("warnings = %v, want one naming the unreadable scope", res.Warnings)
	}
}

func TestUnexpectedRepositoryErrorFailsTheCall(t *testing.T) {
	repo := twoSiteNetwork()
	repo.failScopes = map[string]error{"west-cluster/west": errors.New("decode failure")}
	svc := twoSiteService(t, repo)

	if _, err := svc.ListSites(context.Background(), "", ""); err == nil {
		t.Fatal("ListSites hid an unexpected repository error behind a warning")
	}
}

func TestSiteFilterMatchesNameOrID(t *testing.T) {
	repo := twoSiteNetwork()
	svc := twoSiteService(t, repo)

	for _, filterValue := range []string{"east", "site-east"} {
		res, err := svc.ListLinks(context.Background(), "", "", filterValue)
		if err != nil {
			t.Fatalf("ListLinks(site=%q): %v", filterValue, err)
		}
		if len(res.Items) != 1 || res.Items[0].Name != "west-to-east" {
			t.Errorf("ListLinks(site=%q) returned %+v, want the west-to-east link", filterValue, res.Items)
		}
	}

	res, err := svc.ListLinks(context.Background(), "", "", "nonexistent")
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("ListLinks(site=nonexistent) returned %+v, want nothing", res.Items)
	}
}

func TestSnapshotResolvesSiteOwnershipByNamespace(t *testing.T) {
	svc := twoSiteService(t, twoSiteNetwork())
	snap, _, err := svc.Snapshot(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got, want := snap.Links[0].LocalSiteID, "site-west"; got != want {
		t.Errorf("link LocalSiteID = %q, want %q resolved from the link's own namespace", got, want)
	}
	if got, want := snap.Listeners[0].SiteID, "site-west"; got != want {
		t.Errorf("listener SiteID = %q, want %q", got, want)
	}
	if got, want := snap.Connectors[0].SiteID, "site-east"; got != want {
		t.Errorf("connector SiteID = %q, want %q", got, want)
	}
}

func TestSnapshotWarnsAboutTwoSitesInOneNamespace(t *testing.T) {
	repo := twoSiteNetwork()
	repo.sites = append(repo.sites, site("west-cluster", "west", "west-duplicate", "site-west-2"))
	svc := twoSiteService(t, repo)

	snap, _, err := svc.Snapshot(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	joined := strings.Join(snap.Warnings, "\n")
	if !strings.Contains(joined, "more than one Site") {
		t.Errorf("warnings = %v, want one about the ambiguous namespace", snap.Warnings)
	}
}

func TestEmptyListingsMarshalAsArraysNotNull(t *testing.T) {
	svc := newService(t, &memRepo{}, []string{"west-cluster"}, []string{"west"}, 10)
	res, err := svc.ListSites(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if res.Items == nil {
		t.Error("Items is nil; it must be an empty slice so the JSON is [] rather than null")
	}
}
