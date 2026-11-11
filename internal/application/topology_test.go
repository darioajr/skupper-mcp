package application

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/darioajr/skupper-scope/internal/domain"
)

func snapshotOf(repo *memRepo) domain.Snapshot {
	snap := domain.Snapshot{
		Sites:      repo.sites,
		Links:      repo.links,
		Listeners:  repo.listeners,
		Connectors: repo.connectors,
	}
	resolveSiteOwnership(&snap)
	return snap
}

func nodeByID(t *testing.T, topo domain.Topology, id string) domain.TopologyNode {
	t.Helper()
	for _, n := range topo.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("topology has no node %q; nodes are %+v", id, topo.Nodes)
	return domain.TopologyNode{}
}

func groupFor(t *testing.T, topo domain.Topology, key string) domain.RoutingKeyGroup {
	t.Helper()
	for _, g := range topo.RoutingKeys {
		if g.RoutingKey == key {
			return g
		}
	}
	t.Fatalf("topology has no routing key group %q", key)
	return domain.RoutingKeyGroup{}
}

func TestBuildTopology(t *testing.T) {
	tests := []struct {
		name   string
		snap   domain.Snapshot
		assert func(*testing.T, domain.Topology)
	}{
		{
			name: "healthy two-site network",
			snap: snapshotOf(twoSiteNetwork()),
			assert: func(t *testing.T, topo domain.Topology) {
				if len(topo.Nodes) != 2 {
					t.Fatalf("len(Nodes) = %d, want 2", len(topo.Nodes))
				}
				if len(topo.Edges) != 1 {
					t.Fatalf("len(Edges) = %d, want 1", len(topo.Edges))
				}
				edge := topo.Edges[0]
				if edge.Source != "site-west" || edge.Target != "site-east" {
					t.Errorf("edge = %s -> %s, want site-west -> site-east", edge.Source, edge.Target)
				}
				if !edge.TargetResolved {
					t.Error("TargetResolved = false, want true when the remote site was observed")
				}
				west := nodeByID(t, topo, "site-west")
				if len(west.Listeners) != 1 || west.Listeners[0].RoutingKey != "backend" {
					t.Errorf("west listeners = %+v, want the backend listener attached", west.Listeners)
				}
				east := nodeByID(t, topo, "site-east")
				if len(east.Connectors) != 1 {
					t.Errorf("east connectors = %+v, want the backend connector attached", east.Connectors)
				}
				if len(topo.Warnings) != 0 {
					t.Errorf("warnings = %v, want none for a healthy network", topo.Warnings)
				}
			},
		},
		{
			name: "link to a site outside the observed scope becomes an unobserved node",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.sites = repo.sites[:1] // drop east
				repo.connectors = nil
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				east := nodeByID(t, topo, "site-east")
				if east.Observed {
					t.Error("Observed = true for a site that was never read; the caller must be able " +
						"to tell where the view stops")
				}
				if east.Status != domain.StatusUnknown {
					t.Errorf("status = %q, want Unknown for an unobserved node", east.Status)
				}
				if topo.Edges[0].TargetResolved {
					t.Error("TargetResolved = true, want false for a dangling edge")
				}
			},
		},
		{
			name: "link with no remote site id is kept and warned about",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.links = []domain.Link{link("west-cluster", "west", "pending-link", "link-2", "", "")}
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				if len(topo.Edges) != 1 {
					t.Fatalf("len(Edges) = %d, want the incomplete link to still appear", len(topo.Edges))
				}
				if topo.Edges[0].Target != "" || topo.Edges[0].TargetResolved {
					t.Errorf("edge = %+v, want an unresolved target", topo.Edges[0])
				}
				if !strings.Contains(strings.Join(topo.Warnings, "\n"), "reports no remoteSiteId") {
					t.Errorf("warnings = %v, want one explaining the missing remote site", topo.Warnings)
				}
			},
		},
		{
			name: "link in a namespace with no site is reported rather than dropped",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.sites = []domain.Site{site("east-cluster", "east", "east", "site-east")}
				repo.listeners = nil
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				if len(topo.Edges) != 1 {
					t.Fatalf("len(Edges) = %d, want the orphaned link to still appear", len(topo.Edges))
				}
				if !strings.HasPrefix(topo.Edges[0].Source, "unresolved:") {
					t.Errorf("edge source = %q, want an explicit unresolved marker", topo.Edges[0].Source)
				}
				if !strings.Contains(strings.Join(topo.Warnings, "\n"), "has no Site in its namespace") {
					t.Errorf("warnings = %v, want one about the missing local site", topo.Warnings)
				}
			},
		},
		{
			name: "routing key with both sides has no mismatch",
			snap: snapshotOf(twoSiteNetwork()),
			assert: func(t *testing.T, topo domain.Topology) {
				g := groupFor(t, topo, "backend")
				if len(g.Listeners) != 1 || len(g.Connectors) != 1 {
					t.Fatalf("group = %+v, want one listener and one connector", g)
				}
				if len(g.Mismatches) != 0 {
					t.Errorf("mismatches = %+v, want none", g.Mismatches)
				}
			},
		},
		{
			name: "listener with no connector is reported as an orphan",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors = nil
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				g := groupFor(t, topo, "backend")
				if len(g.Mismatches) != 1 || g.Mismatches[0].Kind != domain.MismatchListenerOrphan {
					t.Fatalf("mismatches = %+v, want one listener-without-connector", g.Mismatches)
				}
				if !strings.Contains(g.Mismatches[0].Detail, "cannot read") {
					t.Errorf("detail = %q, want it to admit the connector may exist outside the "+
						"observed scope", g.Mismatches[0].Detail)
				}
			},
		},
		{
			name: "connector with no listener is reported as an orphan",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.listeners = nil
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				g := groupFor(t, topo, "backend")
				if len(g.Mismatches) != 1 || g.Mismatches[0].Kind != domain.MismatchConnectorOrphan {
					t.Fatalf("mismatches = %+v, want one connector-without-listener", g.Mismatches)
				}
			},
		},
		{
			name: "differing ports are reported as a smell",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors = []domain.Connector{
					connector("east-cluster", "east", "backend", "connector-1", "backend", 9090),
				}
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				g := groupFor(t, topo, "backend")
				if !hasMismatch(g.Mismatches, domain.MismatchPort) {
					t.Errorf("mismatches = %+v, want a port mismatch", g.Mismatches)
				}
			},
		},
		{
			name: "differing protocols are reported",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors = []domain.Connector{
					connector("east-cluster", "east", "backend", "connector-1", "backend", 8080,
						func(c *domain.Connector) { c.Protocol = "http2" }),
				}
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				g := groupFor(t, topo, "backend")
				if !hasMismatch(g.Mismatches, domain.MismatchProtocol) {
					t.Errorf("mismatches = %+v, want a protocol mismatch", g.Mismatches)
				}
			},
		},
		{
			name: "an unset protocol is the default and never mismatches",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors = []domain.Connector{
					connector("east-cluster", "east", "backend", "connector-1", "backend", 8080,
						func(c *domain.Connector) { c.Protocol = "" }),
				}
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				g := groupFor(t, topo, "backend")
				if hasMismatch(g.Mismatches, domain.MismatchProtocol) {
					t.Errorf("mismatches = %+v, want no protocol mismatch when one side is unset", g.Mismatches)
				}
			},
		},
		{
			name: "an unknown status value is preserved rather than rejected",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.sites[0].Status = domain.StatusType("Reconfiguring")
				return snapshotOf(repo)
			}(),
			assert: func(t *testing.T, topo domain.Topology) {
				if got := nodeByID(t, topo, "site-west").Status; got != domain.StatusType("Reconfiguring") {
					t.Errorf("status = %q, want the unrecognised value carried through unchanged", got)
				}
			},
		},
		{
			name: "an empty snapshot produces empty arrays, not nulls",
			snap: domain.Snapshot{},
			assert: func(t *testing.T, topo domain.Topology) {
				encoded, err := json.Marshal(topo)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{`"nodes":[]`, `"edges":[]`, `"routingKeys":[]`} {
					if !strings.Contains(string(encoded), want) {
						t.Errorf("topology JSON is missing %s: %s", want, encoded)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, BuildTopology(tt.snap))
		})
	}
}

func TestBuildTopologyIsDeterministic(t *testing.T) {
	snap := snapshotOf(twoSiteNetwork())
	first, err := json.Marshal(BuildTopology(snap))
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		next, err := json.Marshal(BuildTopology(snap))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(next) {
			t.Fatal("BuildTopology produced different output for the same snapshot; " +
				"a client diffing two calls would see phantom changes")
		}
	}
}

func TestTopologyRoutingKeysAreSorted(t *testing.T) {
	repo := twoSiteNetwork()
	repo.listeners = append(repo.listeners,
		listener("west-cluster", "west", "alpha", "listener-2", "alpha-key", 1000),
		listener("west-cluster", "west", "zeta", "listener-3", "zeta-key", 1001))
	topo := BuildTopology(snapshotOf(repo))

	keys := make([]string, 0, len(topo.RoutingKeys))
	for _, g := range topo.RoutingKeys {
		keys = append(keys, g.RoutingKey)
	}
	if !slices.IsSorted(keys) {
		t.Errorf("routing keys = %v, want them sorted", keys)
	}
}

func hasMismatch(mismatches []domain.RoutingKeyMismatch, kind string) bool {
	for _, m := range mismatches {
		if m.Kind == kind {
			return true
		}
	}
	return false
}
