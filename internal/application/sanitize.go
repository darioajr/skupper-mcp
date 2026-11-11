package application

import (
	"github.com/darioajr/skupper-scope/internal/domain"
	"github.com/darioajr/skupper-scope/internal/redact"
)

// sanitize scrubs the free-form text a Skupper controller writes into a
// resource, on its way out to an MCP client.
//
// It is applied here, at the application layer, rather than in the Kubernetes
// mapper, so the guarantee holds for every repository implementation rather
// than for one of them. Only controller-authored prose is touched: names,
// namespaces, routing keys, hosts and identifiers are structural, are what an
// operator needs to act on, and are left exactly as read.
func sanitizeState(s *domain.ObservedState) {
	s.Message = redact.String(s.Message)
	for i := range s.Conditions {
		s.Conditions[i].Message = redact.String(s.Conditions[i].Message)
		s.Conditions[i].Reason = redact.String(s.Conditions[i].Reason)
	}
}

func sanitizeSites(items []domain.Site) {
	for i := range items {
		sanitizeState(&items[i].ObservedState)
	}
}

func sanitizeLinks(items []domain.Link) {
	for i := range items {
		sanitizeState(&items[i].ObservedState)
	}
}

func sanitizeListeners(items []domain.Listener) {
	for i := range items {
		sanitizeState(&items[i].ObservedState)
	}
}

func sanitizeConnectors(items []domain.Connector) {
	for i := range items {
		sanitizeState(&items[i].ObservedState)
	}
}

// sanitizeWarnings scrubs warnings, which quote errors from client libraries
// and can therefore carry a URL with an embedded credential.
func sanitizeWarnings(warnings []string) []string {
	for i := range warnings {
		warnings[i] = redact.String(warnings[i])
	}
	return warnings
}

// sanitizeSnapshot scrubs an entire snapshot. Topology and diagnosis are both
// built from a snapshot, so scrubbing here covers both without either having
// to remember to do it.
func sanitizeSnapshot(snap *domain.Snapshot) {
	sanitizeSites(snap.Sites)
	sanitizeLinks(snap.Links)
	sanitizeListeners(snap.Listeners)
	sanitizeConnectors(snap.Connectors)
	snap.Warnings = sanitizeWarnings(snap.Warnings)
}
