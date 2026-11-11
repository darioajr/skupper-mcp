package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
)

func backendRequest() DiagnoseRequest {
	return DiagnoseRequest{
		SourceContext:   "west-cluster",
		SourceNamespace: "west",
		SourceSite:      "west",
		TargetContext:   "east-cluster",
		TargetNamespace: "east",
		TargetSite:      "east",
		RoutingKey:      "backend",
	}
}

func checkByName(t *testing.T, d domain.Diagnosis, name string) domain.Check {
	t.Helper()
	for _, c := range d.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("diagnosis has no check named %q; checks are %+v", name, d.Checks)
	return domain.Check{}
}

func TestDiagnoseRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*DiagnoseRequest)
		wantErr bool
		wantMsg string
	}{
		{name: "complete request", mutate: func(*DiagnoseRequest) {}},
		{
			name:    "missing routing key",
			mutate:  func(r *DiagnoseRequest) { r.RoutingKey = "" },
			wantErr: true, wantMsg: "routingKey",
		},
		{
			name:   "source identified by namespace alone",
			mutate: func(r *DiagnoseRequest) { r.SourceSite = "" },
		},
		{
			name:   "source identified by site alone",
			mutate: func(r *DiagnoseRequest) { r.SourceNamespace = "" },
		},
		{
			name:    "source not identified at all",
			mutate:  func(r *DiagnoseRequest) { r.SourceSite, r.SourceNamespace = "", "" },
			wantErr: true, wantMsg: "sourceSite or sourceNamespace",
		},
		{
			name:    "target not identified at all",
			mutate:  func(r *DiagnoseRequest) { r.TargetSite, r.TargetNamespace = "", "" },
			wantErr: true, wantMsg: "targetSite or targetNamespace",
		},
		{
			name:    "whitespace is not an identifier",
			mutate:  func(r *DiagnoseRequest) { r.RoutingKey = "   " },
			wantErr: true, wantMsg: "routingKey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := backendRequest()
			tt.mutate(&req)
			err := req.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Validate accepted an incomplete request")
				}
				if !errors.Is(err, ErrInvalidRequest) {
					t.Errorf("error = %v, want it to wrap ErrInvalidRequest", err)
				}
				if !strings.Contains(err.Error(), tt.wantMsg) {
					t.Errorf("error = %q, want it to name %q", err, tt.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate rejected a valid request: %v", err)
			}
		})
	}
}

func TestDiagnoseBranches(t *testing.T) {
	tests := []struct {
		name           string
		snap           domain.Snapshot
		req            DiagnoseRequest
		wantStatus     domain.DiagnosisStatus
		wantConfidence domain.Confidence
		wantChecks     map[string]domain.CheckStatus
		checkPath      bool
		wantPathLen    int
		assert         func(*testing.T, domain.Diagnosis)
	}{
		{
			name:           "healthy path",
			snap:           snapshotOf(twoSiteNetwork()),
			req:            backendRequest(),
			wantStatus:     domain.DiagnosisHealthy,
			wantConfidence: domain.ConfidenceHigh,
			checkPath:      true,
			wantPathLen:    1,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckSiteReadiness:   domain.CheckPass,
				domain.CheckLinkPath:        domain.CheckPass,
				domain.CheckLinkHealth:      domain.CheckPass,
				domain.CheckSourceListener:  domain.CheckPass,
				domain.CheckTargetConnector: domain.CheckPass,
				domain.CheckEndpointMatch:   domain.CheckPass,
				domain.CheckWorkloadBinding: domain.CheckPass,
			},
			assert: func(t *testing.T, d domain.Diagnosis) {
				if !strings.Contains(d.Summary, "no traffic was sent") {
					t.Errorf("summary = %q, want it to state that nothing was actually sent", d.Summary)
				}
			},
		},
		{
			name: "source site missing",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.sites = repo.sites[1:] // east only
				repo.listeners = nil
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckSiteReadiness:  domain.CheckFail,
				domain.CheckLinkPath:       domain.CheckUnknown,
				domain.CheckLinkHealth:     domain.CheckUnknown,
				domain.CheckSourceListener: domain.CheckFail,
			},
			assert: func(t *testing.T, d domain.Diagnosis) {
				c := checkByName(t, d, domain.CheckSiteReadiness)
				if !hasEvidenceKind(c.Evidence, domain.EvidenceAbsent) {
					t.Errorf("evidence = %+v, want an ABSENT entry for the missing site", c.Evidence)
				}
				if c.Recommendation == "" {
					t.Error("no recommendation for a missing site")
				}
			},
		},
		{
			name: "no link between the sites",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.links = nil
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckSiteReadiness: domain.CheckPass,
				domain.CheckLinkPath:      domain.CheckFail,
				domain.CheckLinkHealth:    domain.CheckUnknown,
			},
		},
		{
			name: "link on the path is pending",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.links[0].Status = domain.StatusPending
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisDegraded,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckLinkPath:   domain.CheckPass,
				domain.CheckLinkHealth: domain.CheckWarn,
			},
		},
		{
			name: "link on the path reports an error",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.links[0].Status = domain.StatusError
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{domain.CheckLinkHealth: domain.CheckFail},
		},
		{
			name: "listener missing at the source",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.listeners = nil
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckSourceListener:  domain.CheckFail,
				domain.CheckTargetConnector: domain.CheckPass,
				domain.CheckEndpointMatch:   domain.CheckUnknown,
			},
		},
		{
			name: "connector missing at the target",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors = nil
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckTargetConnector: domain.CheckFail,
				domain.CheckWorkloadBinding: domain.CheckUnknown,
			},
		},
		{
			name: "controllers do not see each other",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.listeners[0].HasMatchingConnector = false
				repo.connectors[0].HasMatchingListener = false
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{domain.CheckEndpointMatch: domain.CheckFail},
			assert: func(t *testing.T, d domain.Diagnosis) {
				c := checkByName(t, d, domain.CheckEndpointMatch)
				if !strings.Contains(c.Explanation, "does not see a peer") {
					t.Errorf("explanation = %q, want it to say the controllers report no peer", c.Explanation)
				}
			},
		},
		{
			name: "protocols differ",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors[0].Protocol = "http2"
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisDegraded,
			wantChecks: map[string]domain.CheckStatus{domain.CheckEndpointMatch: domain.CheckWarn},
		},
		{
			name: "ports differ but that is legal",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors[0].Port = 9090
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisHealthy,
			wantChecks: map[string]domain.CheckStatus{domain.CheckEndpointMatch: domain.CheckPass},
			assert: func(t *testing.T, d domain.Diagnosis) {
				c := checkByName(t, d, domain.CheckEndpointMatch)
				if !strings.Contains(evidenceText(c.Evidence), "reported for review, not as a fault") {
					t.Errorf("evidence = %+v, want it to explain why differing ports are not a fault", c.Evidence)
				}
			},
		},
		{
			name: "connector selector matches no pods",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors[0].SelectedPodCount = 0
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{domain.CheckWorkloadBinding: domain.CheckFail},
		},
		{
			name: "connector targets a host so the backend cannot be observed",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.connectors[0].Selector = ""
				repo.connectors[0].Host = "backend.svc.internal"
				repo.connectors[0].SelectedPodCount = 0
				return snapshotOf(repo)
			}(),
			req:            backendRequest(),
			wantStatus:     domain.DiagnosisDegraded,
			wantConfidence: domain.ConfidenceMedium,
			wantChecks:     map[string]domain.CheckStatus{domain.CheckWorkloadBinding: domain.CheckUnknown},
			assert: func(t *testing.T, d domain.Diagnosis) {
				c := checkByName(t, d, domain.CheckWorkloadBinding)
				if !strings.Contains(c.Recommendation, "does not probe it") {
					t.Errorf("recommendation = %q, want it to admit the server cannot check the host",
						c.Recommendation)
				}
			},
		},
		{
			name: "source and target are the same site",
			snap: snapshotOf(func() *memRepo {
				repo := twoSiteNetwork()
				repo.connectors = []domain.Connector{
					connector("west-cluster", "west", "backend", "connector-1", "backend", 8080),
				}
				return repo
			}()),
			req: DiagnoseRequest{
				SourceContext: "west-cluster", SourceNamespace: "west", SourceSite: "west",
				TargetContext: "west-cluster", TargetNamespace: "west", TargetSite: "west",
				RoutingKey: "backend",
			},
			wantStatus:  domain.DiagnosisHealthy,
			checkPath:   true,
			wantPathLen: 0,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckLinkPath:   domain.CheckPass,
				domain.CheckLinkHealth: domain.CheckPass,
			},
		},
		{
			name: "site status is stale",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.sites[0].Generation = 5
				repo.sites[0].ObservedGeneration = 2
				repo.sites[0].GenerationObserved = true
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisDegraded,
			wantChecks: map[string]domain.CheckStatus{domain.CheckSiteReadiness: domain.CheckWarn},
			assert: func(t *testing.T, d domain.Diagnosis) {
				if !strings.Contains(evidenceText(checkByName(t, d, domain.CheckSiteReadiness).Evidence),
					"describes an earlier configuration") {
					t.Error("evidence does not explain that the status predates the spec")
				}
			},
		},
		{
			name: "an unknown site status yields an unknown check, not a failure",
			snap: func() domain.Snapshot {
				repo := twoSiteNetwork()
				repo.sites[0].Status = domain.StatusType("Reconfiguring")
				return snapshotOf(repo)
			}(),
			req:        backendRequest(),
			wantStatus: domain.DiagnosisDegraded,
			wantChecks: map[string]domain.CheckStatus{domain.CheckSiteReadiness: domain.CheckUnknown},
		},
		{
			name: "routing key matching ignores case",
			snap: snapshotOf(twoSiteNetwork()),
			req: func() DiagnoseRequest {
				r := backendRequest()
				r.RoutingKey = "BACKEND"
				return r
			}(),
			wantStatus: domain.DiagnosisHealthy,
			wantChecks: map[string]domain.CheckStatus{domain.CheckSourceListener: domain.CheckPass},
		},
		{
			name:       "nothing observable at all",
			snap:       domain.Snapshot{},
			req:        backendRequest(),
			wantStatus: domain.DiagnosisBroken,
			wantChecks: map[string]domain.CheckStatus{
				domain.CheckSiteReadiness: domain.CheckFail,
				domain.CheckLinkPath:      domain.CheckUnknown,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Diagnose(tt.snap, tt.req)

			if d.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q\nsummary: %s", d.Status, tt.wantStatus, d.Summary)
			}
			if tt.wantConfidence != "" && d.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %q, want %q", d.Confidence, tt.wantConfidence)
			}
			for name, want := range tt.wantChecks {
				if got := checkByName(t, d, name).Status; got != want {
					t.Errorf("check %q = %q, want %q", name, got, want)
				}
			}
			if tt.checkPath && len(d.Path) != tt.wantPathLen {
				t.Errorf("len(Path) = %d, want %d", len(d.Path), tt.wantPathLen)
			}
			for _, c := range d.Checks {
				if len(c.Evidence) == 0 {
					t.Errorf("check %q has no evidence; every check must show what it rests on", c.Name)
				}
				if c.Explanation == "" {
					t.Errorf("check %q has no explanation", c.Name)
				}
				// A WARN or FAIL is actionable, so it must say what to do. An
				// UNKNOWN is usually unknown because a prerequisite failed, and
				// that prerequisite's check carries the recommendation.
				if (c.Status == domain.CheckWarn || c.Status == domain.CheckFail) && c.Recommendation == "" {
					t.Errorf("check %q is %q but offers no recommendation", c.Name, c.Status)
				}
			}
			if tt.assert != nil {
				tt.assert(t, d)
			}
		})
	}
}

// TestDiagnosisAlwaysCarriesItsDisclaimer pins the guarantee that a model
// relaying a result cannot honestly claim a measurement was taken.
func TestDiagnosisAlwaysCarriesItsDisclaimer(t *testing.T) {
	d := Diagnose(snapshotOf(twoSiteNetwork()), backendRequest())

	if !strings.Contains(d.Method.Technique, "No traffic was sent") {
		t.Errorf("Method.Technique = %q, want it to state that no traffic was sent", d.Method.Technique)
	}
	wantAbsent := []string{"latency", "TLS", "reachability"}
	joined := strings.Join(d.Method.NotVerified, " ")
	for _, w := range wantAbsent {
		if !strings.Contains(joined, w) {
			t.Errorf("Method.NotVerified = %v, want it to disclaim %q", d.Method.NotVerified, w)
		}
	}
}

func TestEveryEvidenceEntryIsLabelled(t *testing.T) {
	valid := map[domain.EvidenceKind]bool{
		domain.EvidenceDeclared: true,
		domain.EvidenceObserved: true,
		domain.EvidenceInferred: true,
		domain.EvidenceAbsent:   true,
	}
	for _, snap := range []domain.Snapshot{snapshotOf(twoSiteNetwork()), {}} {
		d := Diagnose(snap, backendRequest())
		for _, c := range d.Checks {
			for _, e := range c.Evidence {
				if !valid[e.Kind] {
					t.Errorf("check %q has evidence with kind %q; every entry must be labelled "+
						"DECLARED, OBSERVED, INFERRED or ABSENT", c.Name, e.Kind)
				}
				if e.Statement == "" {
					t.Errorf("check %q has an evidence entry with no statement", c.Name)
				}
			}
		}
	}
}

func TestFindLinkPathTraversesMultipleHops(t *testing.T) {
	links := []domain.Link{
		link("c", "west", "west-mid", "l1", "site-mid", "mid",
			func(l *domain.Link) { l.LocalSiteID = "site-west" }),
		link("c", "mid", "mid-east", "l2", "site-east", "east",
			func(l *domain.Link) { l.LocalSiteID = "site-mid" }),
	}
	path, found := findLinkPath(links, "site-west", "site-east")
	if !found {
		t.Fatal("findLinkPath did not find a two-hop path")
	}
	if len(path) != 2 {
		t.Fatalf("len(path) = %d, want 2", len(path))
	}
	if path[0].LinkName != "west-mid" || path[1].LinkName != "mid-east" {
		t.Errorf("path = %+v, want it ordered from source to target", path)
	}
}

// TestFindLinkPathIsUndirected pins the decision that a link established from
// one side is usable from the other.
func TestFindLinkPathIsUndirected(t *testing.T) {
	links := []domain.Link{
		link("c", "east", "east-to-west", "l1", "site-west", "west",
			func(l *domain.Link) { l.LocalSiteID = "site-east" }),
	}
	if _, found := findLinkPath(links, "site-west", "site-east"); !found {
		t.Error("no path found from west to east; a Skupper link carries traffic both ways " +
			"once established, so reachability must not depend on which side holds the resource")
	}
}

func TestFindLinkPathIgnoresUnresolvedLinks(t *testing.T) {
	links := []domain.Link{
		link("c", "west", "half-built", "l1", "", "",
			func(l *domain.Link) { l.LocalSiteID = "site-west" }),
	}
	if _, found := findLinkPath(links, "site-west", "site-east"); found {
		t.Error("findLinkPath used a link with no remote site id")
	}
}

func TestFindLinkPathTerminatesOnACycle(t *testing.T) {
	links := []domain.Link{
		link("c", "a", "a-b", "l1", "site-b", "b", func(l *domain.Link) { l.LocalSiteID = "site-a" }),
		link("c", "b", "b-c", "l2", "site-c", "c", func(l *domain.Link) { l.LocalSiteID = "site-b" }),
		link("c", "c", "c-a", "l3", "site-a", "a", func(l *domain.Link) { l.LocalSiteID = "site-c" }),
	}
	path, found := findLinkPath(links, "site-a", "site-c")
	if !found {
		t.Fatal("no path found in a ring topology")
	}
	if len(path) != 1 {
		t.Errorf("len(path) = %d, want the single-hop route rather than the long way round", len(path))
	}
}

func TestDiagnoseConnectivityChecksTheAllowlistBeforeReading(t *testing.T) {
	repo := twoSiteNetwork()
	svc := twoSiteService(t, repo)

	req := backendRequest()
	req.TargetNamespace = "kube-system"
	_, err := svc.DiagnoseConnectivity(context.Background(), req)
	if !errors.Is(err, config.ErrNamespaceNotAllowed) {
		t.Fatalf("error = %v, want config.ErrNamespaceNotAllowed", err)
	}
	if len(repo.calls) != 0 {
		t.Errorf("the repository was queried %d time(s) for a denied request; the allowlist must "+
			"be checked before any I/O", len(repo.calls))
	}
}

func TestDiagnoseConnectivityRejectsAnInvalidRequestWithoutReading(t *testing.T) {
	repo := twoSiteNetwork()
	svc := twoSiteService(t, repo)

	_, err := svc.DiagnoseConnectivity(context.Background(), DiagnoseRequest{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if len(repo.calls) != 0 {
		t.Errorf("the repository was queried for an invalid request")
	}
}

func TestDiagnoseConnectivityReadsTheWholeAllowedScope(t *testing.T) {
	// A path can transit a site the caller did not name, so the diagnosis must
	// read every allowed namespace rather than only the two endpoints.
	repo := twoSiteNetwork()
	svc := twoSiteService(t, repo)

	if _, err := svc.DiagnoseConnectivity(context.Background(), backendRequest()); err != nil {
		t.Fatalf("DiagnoseConnectivity: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range repo.calls {
		seen[c.Scope.Context+"/"+c.Scope.Namespace] = true
	}
	for _, want := range []string{"west-cluster/west", "east-cluster/east"} {
		if !seen[want] {
			t.Errorf("scope %q was never read; a link path may transit sites the caller did not name", want)
		}
	}
}

func TestDiagnosisIsDeterministic(t *testing.T) {
	snap := snapshotOf(twoSiteNetwork())
	first, err := json.Marshal(Diagnose(snap, backendRequest()))
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		next, err := json.Marshal(Diagnose(snap, backendRequest()))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(next) {
			t.Fatal("Diagnose is not deterministic for a fixed snapshot")
		}
	}
}

func hasEvidenceKind(evidence []domain.Evidence, kind domain.EvidenceKind) bool {
	for _, e := range evidence {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func evidenceText(evidence []domain.Evidence) string {
	var b strings.Builder
	for _, e := range evidence {
		b.WriteString(e.Statement)
		b.WriteString("\n")
	}
	return b.String()
}
