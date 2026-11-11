// Package domain holds the vocabulary of a Skupper application network as this
// server understands it: sites, links, listeners, connectors, the topology they
// form, and the diagnosis that can be drawn from their observed state.
//
// Nothing here imports Kubernetes or the Model Context Protocol. A domain type
// describes what was observed, not how it was fetched or how it will be
// serialised, which is what makes the topology builder and the diagnosis engine
// testable against an in-memory snapshot with no cluster.
package domain
