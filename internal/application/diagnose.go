package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/darioajr/skupper-scope/internal/domain"
)

// DiagnoseRequest is the validated input of diagnose_connectivity.
type DiagnoseRequest struct {
	SourceContext   string
	SourceNamespace string
	SourceSite      string
	TargetContext   string
	TargetNamespace string
	TargetSite      string
	RoutingKey      string
}

// ErrInvalidRequest marks a request this server will not act on.
var ErrInvalidRequest = errors.New("invalid request")

// Validate checks the request before any cluster is read.
func (r DiagnoseRequest) Validate() error {
	var missing []string
	if strings.TrimSpace(r.RoutingKey) == "" {
		missing = append(missing, "routingKey")
	}
	if strings.TrimSpace(r.SourceSite) == "" && strings.TrimSpace(r.SourceNamespace) == "" {
		missing = append(missing, "sourceSite or sourceNamespace")
	}
	if strings.TrimSpace(r.TargetSite) == "" && strings.TrimSpace(r.TargetNamespace) == "" {
		missing = append(missing, "targetSite or targetNamespace")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s must be provided", ErrInvalidRequest, strings.Join(missing, ", "))
	}
	return nil
}

// DiagnoseConnectivity performs a deterministic, read-only analysis of whether
// the source site can reach the target site on a routing key.
//
// It changes nothing, sends no traffic, and every conclusion it draws is
// labelled as declared state, observed state, or inference.
func (s *Service) DiagnoseConnectivity(ctx context.Context, req DiagnoseRequest) (domain.Diagnosis, error) {
	if err := req.Validate(); err != nil {
		return domain.Diagnosis{}, err
	}
	// Both endpoints are validated against the allowlist before anything is
	// read, so a denied scope is reported as an authorization error rather than
	// as an inconclusive diagnosis.
	if _, err := s.Resolve(req.SourceContext, req.SourceNamespace); err != nil {
		return domain.Diagnosis{}, fmt.Errorf("source scope: %w", err)
	}
	if _, err := s.Resolve(req.TargetContext, req.TargetNamespace); err != nil {
		return domain.Diagnosis{}, fmt.Errorf("target scope: %w", err)
	}

	// The whole allowed scope is read, not only the two endpoints: a link path
	// can transit sites the caller did not name.
	snap, _, err := s.Snapshot(ctx, "", "")
	if err != nil {
		return domain.Diagnosis{}, err
	}
	return Diagnose(snap, req), nil
}

// Diagnose is the pure analysis, separated from I/O so every branch is testable
// against an in-memory snapshot.
func Diagnose(snap domain.Snapshot, req DiagnoseRequest) domain.Diagnosis {
	d := domain.Diagnosis{
		Method:   domain.DefaultMethod(),
		Warnings: append([]string(nil), snap.Warnings...),
		Path:     []domain.PathHop{},
	}

	source, sourceFound := findSite(snap.Sites, req.SourceContext, req.SourceNamespace, req.SourceSite)
	target, targetFound := findSite(snap.Sites, req.TargetContext, req.TargetNamespace, req.TargetSite)

	readiness := checkSiteReadiness(source, sourceFound, target, targetFound, req)
	d.Checks = append(d.Checks, readiness)

	var path []domain.PathHop
	var pathFound bool
	if sourceFound && targetFound {
		path, pathFound = findLinkPath(snap.Links, source.ID, target.ID)
		d.Path = path
	}
	d.Checks = append(d.Checks, checkLinkPath(source, sourceFound, target, targetFound, path, pathFound))
	d.Checks = append(d.Checks, checkLinkHealth(path, pathFound))

	listeners := matchEndpoints(snap.Listeners, req.RoutingKey, source, sourceFound,
		req.SourceContext, req.SourceNamespace,
		func(l domain.Listener) domain.ResourceRef { return l.ResourceRef },
		func(l domain.Listener) string { return l.RoutingKey })
	connectors := matchEndpoints(snap.Connectors, req.RoutingKey, target, targetFound,
		req.TargetContext, req.TargetNamespace,
		func(c domain.Connector) domain.ResourceRef { return c.ResourceRef },
		func(c domain.Connector) string { return c.RoutingKey })

	d.Checks = append(d.Checks, checkSourceListener(listeners, req))
	d.Checks = append(d.Checks, checkTargetConnector(connectors, req))
	d.Checks = append(d.Checks, checkEndpointMatch(listeners, connectors, req))
	d.Checks = append(d.Checks, checkWorkloadBinding(connectors))

	d.Status, d.Confidence, d.Summary = summarise(d.Checks, req)
	return d
}

func checkSiteReadiness(source domain.Site, sourceFound bool, target domain.Site, targetFound bool, req DiagnoseRequest) domain.Check {
	c := domain.Check{Name: domain.CheckSiteReadiness}
	describe := func(site domain.Site, found bool, role, ctxName, ns, name string) domain.CheckStatus {
		if !found {
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:      domain.EvidenceAbsent,
				Source:    role,
				Statement: fmt.Sprintf("no Site matched %s in the observed scope", describeSelector(ctxName, ns, name)),
			})
			return domain.CheckFail
		}
		c.Evidence = append(c.Evidence, domain.Evidence{
			Kind:      domain.EvidenceObserved,
			Source:    resourceSource(site.ResourceRef),
			Statement: fmt.Sprintf("Site %q reports status %s%s", site.Name, site.Status, suffixMessage(site.Message)),
		})
		if site.Stale() {
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:   domain.EvidenceObserved,
				Source: resourceSource(site.ResourceRef),
				Statement: fmt.Sprintf("status was computed from generation %d but the spec is at generation %d, "+
					"so this status describes an earlier configuration", site.ObservedGeneration, site.Generation),
			})
			return domain.CheckWarn
		}
		switch site.Status {
		case domain.StatusReady:
			return domain.CheckPass
		case domain.StatusError:
			return domain.CheckFail
		case domain.StatusPending:
			return domain.CheckWarn
		default:
			return domain.CheckUnknown
		}
	}

	status := describe(source, sourceFound, "source", req.SourceContext, req.SourceNamespace, req.SourceSite)
	status = domain.Worst(status, describe(target, targetFound, "target", req.TargetContext, req.TargetNamespace, req.TargetSite))
	c.Status = status

	switch {
	case !sourceFound || !targetFound:
		c.Explanation = "At least one endpoint site was not found in the namespaces this server is allowed to read."
		c.Recommendation = "Confirm the site name and namespace, and that the namespace is in " +
			"SKUPPER_MCP_ALLOWED_NAMESPACES for the right context."
	case status == domain.CheckPass:
		c.Explanation = "Both sites exist and their controllers report them as Ready."
	case status == domain.CheckFail:
		c.Explanation = "A site reports an Error status, so nothing downstream of it can be trusted to work."
		c.Recommendation = "Inspect the failing Site resource's conditions and the Skupper controller logs in that namespace."
	default:
		c.Explanation = "A site is not yet Ready, or its reported status is older than its spec."
		c.Recommendation = "Wait for reconciliation to settle, then re-run this diagnosis."
	}
	return c
}

func checkLinkPath(source domain.Site, sourceFound bool, target domain.Site, targetFound bool, path []domain.PathHop, found bool) domain.Check {
	c := domain.Check{Name: domain.CheckLinkPath}
	if !sourceFound || !targetFound {
		c.Status = domain.CheckUnknown
		c.Explanation = "The link path could not be searched because an endpoint site was not resolved."
		c.Evidence = []domain.Evidence{{
			Kind:      domain.EvidenceAbsent,
			Statement: "path search skipped: one or both endpoint sites are unknown",
		}}
		return c
	}
	if source.ID == target.ID {
		c.Status = domain.CheckPass
		c.Explanation = "Source and target are the same site, so no inter-site link is needed."
		c.Evidence = []domain.Evidence{{
			Kind:      domain.EvidenceInferred,
			Statement: fmt.Sprintf("source and target both resolve to site %q", source.Name),
		}}
		return c
	}
	if !found {
		c.Status = domain.CheckFail
		c.Explanation = "No chain of Link resources connects the two sites within the observed scope."
		c.Recommendation = "Create a link between the sites, or widen the allowed namespaces if an " +
			"intermediate site is outside this server's view."
		c.Evidence = []domain.Evidence{{
			Kind: domain.EvidenceAbsent,
			Statement: fmt.Sprintf("no Link path from site %q to site %q was found among the observed links",
				source.Name, target.Name),
		}}
		return c
	}
	c.Status = domain.CheckPass
	c.Explanation = fmt.Sprintf("A path of %d link(s) connects the two sites.", len(path))
	for _, hop := range path {
		c.Evidence = append(c.Evidence, domain.Evidence{
			Kind:      domain.EvidenceObserved,
			Source:    "Link " + hop.LinkName,
			Statement: fmt.Sprintf("link %q joins %s to %s", hop.LinkName, displayName(hop.FromSiteName, hop.FromSiteID), displayName(hop.ToSiteName, hop.ToSiteID)),
		})
	}
	c.Evidence = append(c.Evidence, domain.Evidence{
		Kind: domain.EvidenceInferred,
		Statement: "path traversal treats a Link as usable in both directions, because a Skupper link " +
			"carries traffic both ways once established even though its configuration names one initiator",
	})
	return c
}

func checkLinkHealth(path []domain.PathHop, pathFound bool) domain.Check {
	c := domain.Check{Name: domain.CheckLinkHealth}
	if !pathFound {
		c.Status = domain.CheckUnknown
		c.Explanation = "There is no path whose health could be assessed."
		c.Evidence = []domain.Evidence{{Kind: domain.EvidenceAbsent, Statement: "no link path to assess"}}
		return c
	}
	if len(path) == 0 {
		c.Status = domain.CheckPass
		c.Explanation = "Source and target are the same site, so there is no link to assess."
		c.Evidence = []domain.Evidence{{Kind: domain.EvidenceInferred, Statement: "no inter-site link involved"}}
		return c
	}
	status := domain.CheckPass
	for _, hop := range path {
		c.Evidence = append(c.Evidence, domain.Evidence{
			Kind:      domain.EvidenceObserved,
			Source:    "Link " + hop.LinkName,
			Statement: fmt.Sprintf("link %q reports status %s", hop.LinkName, hop.LinkStatus),
		})
		switch hop.LinkStatus {
		case domain.StatusReady:
		case domain.StatusError:
			status = domain.Worst(status, domain.CheckFail)
		case domain.StatusPending:
			status = domain.Worst(status, domain.CheckWarn)
		default:
			status = domain.Worst(status, domain.CheckUnknown)
		}
	}
	c.Status = status
	switch status {
	case domain.CheckPass:
		c.Explanation = "Every link on the path reports Ready."
	case domain.CheckFail:
		c.Explanation = "A link on the path reports an Error, so the path is not usable."
		c.Recommendation = "Inspect that Link's conditions and the router logs at both ends."
	case domain.CheckWarn:
		c.Explanation = "A link on the path is still Pending, so the path may not carry traffic yet."
		c.Recommendation = "Wait for the link to become Ready; if it stays Pending, check that the " +
			"remote endpoint is reachable and that the link's credentials were issued."
	default:
		c.Explanation = "A link on the path reports a status this server does not recognise."
	}
	return c
}

func checkSourceListener(listeners []endpointMatch, req DiagnoseRequest) domain.Check {
	c := domain.Check{Name: domain.CheckSourceListener}
	if len(listeners) == 0 {
		c.Status = domain.CheckFail
		c.Explanation = fmt.Sprintf("No Listener for routing key %q exists at the source.", req.RoutingKey)
		c.Recommendation = "Create a Listener with this routing key in the source namespace; " +
			"without it there is no local address for clients to connect to."
		c.Evidence = []domain.Evidence{{
			Kind: domain.EvidenceAbsent,
			Statement: fmt.Sprintf("no Listener with routingKey %q found at %s",
				req.RoutingKey, describeSelector(req.SourceContext, req.SourceNamespace, req.SourceSite)),
		}}
		return c
	}
	status := domain.CheckPass
	for _, m := range listeners {
		c.Evidence = append(c.Evidence,
			domain.Evidence{
				Kind:   domain.EvidenceDeclared,
				Source: resourceSource(m.ref),
				Statement: fmt.Sprintf("Listener %q declares routingKey %q on %s:%d (%s)",
					m.ref.Name, req.RoutingKey, displayHost(m.host), m.port, displayProtocol(m.protocol)),
			},
			domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(m.ref),
				Statement: fmt.Sprintf("Listener %q reports status %s%s", m.ref.Name, m.status, suffixMessage(m.message)),
			})
		status = domain.Worst(status, statusToCheck(m.status))
	}
	c.Status = status
	if status == domain.CheckPass {
		c.Explanation = "The source has a Ready Listener for this routing key."
	} else {
		c.Explanation = "A Listener exists but is not Ready."
		c.Recommendation = "Inspect the Listener's conditions in the source namespace."
	}
	return c
}

func checkTargetConnector(connectors []endpointMatch, req DiagnoseRequest) domain.Check {
	c := domain.Check{Name: domain.CheckTargetConnector}
	if len(connectors) == 0 {
		c.Status = domain.CheckFail
		c.Explanation = fmt.Sprintf("No Connector for routing key %q exists at the target.", req.RoutingKey)
		c.Recommendation = "Create a Connector with this routing key in the target namespace, " +
			"selecting the workload that should serve the traffic."
		c.Evidence = []domain.Evidence{{
			Kind: domain.EvidenceAbsent,
			Statement: fmt.Sprintf("no Connector with routingKey %q found at %s",
				req.RoutingKey, describeSelector(req.TargetContext, req.TargetNamespace, req.TargetSite)),
		}}
		return c
	}
	status := domain.CheckPass
	for _, m := range connectors {
		target := m.selector
		if target == "" {
			target = "host " + displayHost(m.host)
		} else {
			target = "selector " + target
		}
		c.Evidence = append(c.Evidence,
			domain.Evidence{
				Kind:   domain.EvidenceDeclared,
				Source: resourceSource(m.ref),
				Statement: fmt.Sprintf("Connector %q declares routingKey %q to %s on port %d (%s)",
					m.ref.Name, req.RoutingKey, target, m.port, displayProtocol(m.protocol)),
			},
			domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(m.ref),
				Statement: fmt.Sprintf("Connector %q reports status %s%s", m.ref.Name, m.status, suffixMessage(m.message)),
			})
		status = domain.Worst(status, statusToCheck(m.status))
	}
	c.Status = status
	if status == domain.CheckPass {
		c.Explanation = "The target has a Ready Connector for this routing key."
	} else {
		c.Explanation = "A Connector exists but is not Ready."
		c.Recommendation = "Inspect the Connector's conditions in the target namespace."
	}
	return c
}

func checkEndpointMatch(listeners, connectors []endpointMatch, req DiagnoseRequest) domain.Check {
	c := domain.Check{Name: domain.CheckEndpointMatch}
	if len(listeners) == 0 || len(connectors) == 0 {
		c.Status = domain.CheckUnknown
		c.Explanation = "Both a Listener and a Connector are needed before their settings can be compared."
		c.Evidence = []domain.Evidence{{
			Kind:      domain.EvidenceAbsent,
			Statement: "one side of the routing key is missing, so no comparison was made",
		}}
		return c
	}

	c.Evidence = append(c.Evidence, domain.Evidence{
		Kind:      domain.EvidenceInferred,
		Statement: fmt.Sprintf("both sides declare the same routing key %q, which is the association Skupper uses", req.RoutingKey),
	})

	status := domain.CheckPass
	var notes []string

	// The controller's own matched flags are stronger evidence than anything
	// this server can infer, because they come from the router's view of the
	// whole network rather than from the resources visible here.
	for _, l := range listeners {
		if l.matched {
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(l.ref),
				Statement: fmt.Sprintf("Listener %q reports hasMatchingConnector=true", l.ref.Name),
			})
		} else {
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(l.ref),
				Statement: fmt.Sprintf("Listener %q reports hasMatchingConnector=false", l.ref.Name),
			})
			status = domain.Worst(status, domain.CheckFail)
			notes = append(notes, "the source router has not seen a connector for this routing key anywhere in the network")
		}
	}
	for _, c2 := range connectors {
		if c2.matched {
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(c2.ref),
				Statement: fmt.Sprintf("Connector %q reports hasMatchingListener=true", c2.ref.Name),
			})
		} else {
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(c2.ref),
				Statement: fmt.Sprintf("Connector %q reports hasMatchingListener=false", c2.ref.Name),
			})
			status = domain.Worst(status, domain.CheckFail)
			notes = append(notes, "the target router has not seen a listener for this routing key anywhere in the network")
		}
	}

	for _, l := range listeners {
		for _, k := range connectors {
			if !protocolsCompatible(l.protocol, k.protocol) {
				status = domain.Worst(status, domain.CheckWarn)
				c.Evidence = append(c.Evidence, domain.Evidence{
					Kind: domain.EvidenceDeclared,
					Statement: fmt.Sprintf("Listener %q declares protocol %s while Connector %q declares %s",
						l.ref.Name, displayProtocol(l.protocol), k.ref.Name, displayProtocol(k.protocol)),
				})
				notes = append(notes, "the declared protocols differ, which usually breaks the application even when the routing key matches")
			}
			if l.port != 0 && k.port != 0 && l.port != k.port {
				c.Evidence = append(c.Evidence, domain.Evidence{
					Kind: domain.EvidenceDeclared,
					Statement: fmt.Sprintf("Listener %q listens on port %d while Connector %q targets port %d",
						l.ref.Name, l.port, k.ref.Name, k.port),
				})
				c.Evidence = append(c.Evidence, domain.Evidence{
					Kind: domain.EvidenceInferred,
					Statement: "differing ports are legal, because Skupper routes on the routing key and the " +
						"connector port is the backend port; this is reported for review, not as a fault",
				})
			}
		}
	}

	c.Status = status
	switch status {
	case domain.CheckPass:
		c.Explanation = "The routing key matches on both sides and both controllers confirm they see a peer."
	case domain.CheckFail:
		c.Explanation = "At least one side's controller reports that it does not see a peer for this routing key. " +
			strings.Join(dedupe(notes), "; ") + "."
		c.Recommendation = "Check that the sites are linked and that the routing keys are spelled identically, " +
			"including case."
	default:
		c.Explanation = "Both sides see each other, but their declared settings differ. " + strings.Join(dedupe(notes), "; ") + "."
		c.Recommendation = "Align the declared protocol on the Listener and Connector."
	}
	return c
}

func checkWorkloadBinding(connectors []endpointMatch) domain.Check {
	c := domain.Check{Name: domain.CheckWorkloadBinding}
	if len(connectors) == 0 {
		c.Status = domain.CheckUnknown
		c.Explanation = "There is no Connector whose workload binding could be checked."
		c.Evidence = []domain.Evidence{{Kind: domain.EvidenceAbsent, Statement: "no connector to assess"}}
		return c
	}
	status := domain.CheckPass
	for _, k := range connectors {
		switch {
		case k.selector == "":
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:   domain.EvidenceDeclared,
				Source: resourceSource(k.ref),
				Statement: fmt.Sprintf("Connector %q targets host %q directly rather than selecting pods, "+
					"so this server cannot tell whether a backend is listening there",
					k.ref.Name, displayHost(k.host)),
			})
			status = domain.Worst(status, domain.CheckUnknown)
		case k.selectedPods > 0:
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:   domain.EvidenceObserved,
				Source: resourceSource(k.ref),
				Statement: fmt.Sprintf("Connector %q selected %d pod(s) with selector %q",
					k.ref.Name, k.selectedPods, k.selector),
			})
		default:
			c.Evidence = append(c.Evidence, domain.Evidence{
				Kind:      domain.EvidenceObserved,
				Source:    resourceSource(k.ref),
				Statement: fmt.Sprintf("Connector %q selected no pods with selector %q", k.ref.Name, k.selector),
			})
			status = domain.Worst(status, domain.CheckFail)
		}
	}
	c.Status = status
	switch status {
	case domain.CheckPass:
		c.Explanation = "Every connector has selected at least one backend pod."
	case domain.CheckFail:
		c.Explanation = "A connector's selector matches no pods, so traffic that reaches the target has no backend."
		c.Recommendation = "Check that the workload is running in the target namespace and that its labels " +
			"match the connector's selector."
	default:
		c.Explanation = "The connector targets a host rather than a pod selector, so its backend cannot be observed here."
		c.Recommendation = "Verify the backend at that host by other means; this server does not probe it."
	}
	return c
}

func summarise(checks []domain.Check, req DiagnoseRequest) (domain.DiagnosisStatus, domain.Confidence, string) {
	var fails, warns, unknowns int
	for _, c := range checks {
		switch c.Status {
		case domain.CheckFail:
			fails++
		case domain.CheckWarn:
			warns++
		case domain.CheckUnknown:
			unknowns++
		}
	}

	var status domain.DiagnosisStatus
	switch {
	case fails > 0:
		status = domain.DiagnosisBroken
	case warns > 0:
		status = domain.DiagnosisDegraded
	case unknowns > 0 && unknowns >= len(checks)/2:
		status = domain.DiagnosisUnknown
	case unknowns > 0:
		status = domain.DiagnosisDegraded
	default:
		status = domain.DiagnosisHealthy
	}

	// Confidence tracks how much of the verdict rests on unobserved data, not
	// how bad the verdict is.
	var confidence domain.Confidence
	switch {
	case unknowns == 0:
		confidence = domain.ConfidenceHigh
	case unknowns <= 2:
		confidence = domain.ConfidenceMedium
	default:
		confidence = domain.ConfidenceLow
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Routing key %q from %s to %s: ", req.RoutingKey,
		describeSelector(req.SourceContext, req.SourceNamespace, req.SourceSite),
		describeSelector(req.TargetContext, req.TargetNamespace, req.TargetSite))
	switch status {
	case domain.DiagnosisHealthy:
		b.WriteString("every observed check passed. This means the Skupper resources are configured and " +
			"reconciled consistently; no traffic was sent to confirm it.")
	case domain.DiagnosisDegraded:
		fmt.Fprintf(&b, "%d check(s) raised a warning and %d could not be determined. "+
			"The configuration is present but something is unsettled or unverifiable.", warns, unknowns)
	case domain.DiagnosisBroken:
		fmt.Fprintf(&b, "%d check(s) failed. At least one required resource is missing or reports an error, "+
			"so this path cannot be working.", fails)
	default:
		b.WriteString("too little was observable to reach a conclusion.")
	}
	return status, confidence, b.String()
}

// endpointMatch flattens a Listener or a Connector into the fields the
// diagnosis compares, so both sides run through the same code.
type endpointMatch struct {
	ref          domain.ResourceRef
	status       domain.StatusType
	message      string
	host         string
	port         int
	protocol     string
	selector     string
	selectedPods int
	matched      bool
}

func matchEndpoints[T any](
	items []T,
	routingKey string,
	site domain.Site,
	siteFound bool,
	ctxName, namespace string,
	refOf func(T) domain.ResourceRef,
	keyOf func(T) string,
) []endpointMatch {
	var out []endpointMatch
	for _, item := range items {
		if !strings.EqualFold(keyOf(item), routingKey) {
			continue
		}
		ref := refOf(item)
		if !endpointInScope(ref, site, siteFound, ctxName, namespace) {
			continue
		}
		out = append(out, toEndpointMatch(any(item)))
	}
	sort.Slice(out, func(i, j int) bool { return refLess(out[i].ref, out[j].ref) })
	return out
}

func endpointInScope(ref domain.ResourceRef, site domain.Site, siteFound bool, ctxName, namespace string) bool {
	if siteFound {
		return ref.Context == site.Context && ref.Namespace == site.Namespace
	}
	if namespace == "" {
		return false
	}
	if ctxName != "" && ref.Context != ctxName {
		return false
	}
	return ref.Namespace == namespace
}

func toEndpointMatch(item any) endpointMatch {
	switch v := item.(type) {
	case domain.Listener:
		return endpointMatch{
			ref: v.ResourceRef, status: v.Status, message: v.Message,
			host: v.Host, port: v.Port, protocol: v.Protocol, matched: v.HasMatchingConnector,
		}
	case domain.Connector:
		return endpointMatch{
			ref: v.ResourceRef, status: v.Status, message: v.Message,
			host: v.Host, port: v.Port, protocol: v.Protocol, selector: v.Selector,
			selectedPods: v.SelectedPodCount, matched: v.HasMatchingListener,
		}
	default:
		return endpointMatch{}
	}
}

func findSite(sites []domain.Site, ctxName, namespace, name string) (domain.Site, bool) {
	var candidates []domain.Site
	for _, s := range sites {
		if ctxName != "" && s.Context != ctxName {
			continue
		}
		if namespace != "" && s.Namespace != namespace {
			continue
		}
		if name != "" && s.Name != name && s.ID != name {
			continue
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 {
		return domain.Site{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return refLess(candidates[i].ResourceRef, candidates[j].ResourceRef) })
	return candidates[0], true
}

// findLinkPath returns the lowest-cost path of links between two sites.
//
// The graph is undirected: a Skupper Link names one initiating side, but once
// established it carries traffic in both directions, so reachability must not
// depend on which side happens to hold the Link resource.
func findLinkPath(links []domain.Link, sourceID, targetID string) ([]domain.PathHop, bool) {
	if sourceID == "" || targetID == "" {
		return nil, false
	}
	if sourceID == targetID {
		return []domain.PathHop{}, true
	}

	type edge struct {
		to  string
		hop domain.PathHop
	}
	adjacency := map[string][]edge{}
	for _, l := range links {
		if l.LocalSiteID == "" || l.RemoteSiteID == "" {
			continue
		}
		fwd := domain.PathHop{
			FromSiteID: l.LocalSiteID, FromSiteName: l.LocalSiteName,
			ToSiteID: l.RemoteSiteID, ToSiteName: l.RemoteSiteName,
			LinkID: l.ID, LinkName: l.Name, LinkStatus: l.Status, Cost: l.Cost,
		}
		rev := domain.PathHop{
			FromSiteID: l.RemoteSiteID, FromSiteName: l.RemoteSiteName,
			ToSiteID: l.LocalSiteID, ToSiteName: l.LocalSiteName,
			LinkID: l.ID, LinkName: l.Name, LinkStatus: l.Status, Cost: l.Cost,
		}
		adjacency[l.LocalSiteID] = append(adjacency[l.LocalSiteID], edge{to: l.RemoteSiteID, hop: fwd})
		adjacency[l.RemoteSiteID] = append(adjacency[l.RemoteSiteID], edge{to: l.LocalSiteID, hop: rev})
	}
	for id := range adjacency {
		sort.SliceStable(adjacency[id], func(i, j int) bool { return adjacency[id][i].hop.LinkName < adjacency[id][j].hop.LinkName })
	}

	// Breadth-first search yields the fewest hops, which is the property an
	// operator reads a path for. Link cost is reported per hop but is not used
	// as the search metric, because a read-only observer cannot know how the
	// router weighted it.
	visited := map[string]bool{sourceID: true}
	prev := map[string]domain.PathHop{}
	queue := []string{sourceID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, e := range adjacency[current] {
			if visited[e.to] {
				continue
			}
			visited[e.to] = true
			prev[e.to] = e.hop
			if e.to == targetID {
				return reconstruct(prev, sourceID, targetID), true
			}
			queue = append(queue, e.to)
		}
	}
	return nil, false
}

func reconstruct(prev map[string]domain.PathHop, sourceID, targetID string) []domain.PathHop {
	var reversed []domain.PathHop
	for at := targetID; at != sourceID; {
		hop, ok := prev[at]
		if !ok {
			break
		}
		reversed = append(reversed, hop)
		at = hop.FromSiteID
	}
	path := make([]domain.PathHop, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		path = append(path, reversed[i])
	}
	return path
}

func statusToCheck(s domain.StatusType) domain.CheckStatus {
	switch s {
	case domain.StatusReady:
		return domain.CheckPass
	case domain.StatusError:
		return domain.CheckFail
	case domain.StatusPending:
		return domain.CheckWarn
	default:
		return domain.CheckUnknown
	}
}

func describeSelector(ctxName, namespace, name string) string {
	parts := make([]string, 0, 3)
	if ctxName != "" {
		parts = append(parts, "context "+ctxName)
	}
	if namespace != "" {
		parts = append(parts, "namespace "+namespace)
	}
	if name != "" {
		parts = append(parts, "site "+name)
	}
	if len(parts) == 0 {
		return "the observed scope"
	}
	return strings.Join(parts, "/")
}

func resourceSource(ref domain.ResourceRef) string {
	return fmt.Sprintf("%s %s/%s/%s", ref.Kind, ref.Context, ref.Namespace, ref.Name)
}

func displayName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

func displayHost(h string) string {
	if h == "" {
		return "(unset)"
	}
	return h
}

func suffixMessage(msg string) string {
	if msg == "" {
		return ""
	}
	return fmt.Sprintf(" with message %q", msg)
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := items[:0]
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
