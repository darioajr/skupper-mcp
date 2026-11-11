package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/auth"
)

// Resource URIs published by this server. Each one is the whole allowed scope,
// which is why they take no arguments: a client that needs to narrow the view
// calls the equivalent tool instead.
const (
	URISites      = "skupper://sites"
	URILinks      = "skupper://links"
	URIListeners  = "skupper://listeners"
	URIConnectors = "skupper://connectors"
	URITopology   = "skupper://topology"
)

// registerResources publishes the read-only resources. Each handler calls the
// same application service the corresponding tool uses, so the two surfaces
// cannot report different things.
func (s *Server) registerResources() {
	add := func(uri, name, title, description string, load func(context.Context) (any, int, error)) {
		s.mcp.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        name,
			Title:       title,
			Description: description,
			MIMEType:    "application/json",
		}, s.resourceHandler(name, load))
	}

	add(URISites, "sites", "Skupper sites",
		"Every Skupper Site in the allowed scope, as JSON.",
		func(ctx context.Context) (any, int, error) {
			res, err := s.svc.ListSites(ctx, "", "")
			return res, len(res.Items), err
		})

	add(URILinks, "links", "Skupper links",
		"Every Skupper Link in the allowed scope, as JSON. Credentials are never included.",
		func(ctx context.Context) (any, int, error) {
			res, err := s.svc.ListLinks(ctx, "", "", "")
			return res, len(res.Items), err
		})

	add(URIListeners, "listeners", "Skupper listeners",
		"Every Skupper Listener in the allowed scope, as JSON.",
		func(ctx context.Context) (any, int, error) {
			res, err := s.svc.ListListeners(ctx, "", "", "")
			return res, len(res.Items), err
		})

	add(URIConnectors, "connectors", "Skupper connectors",
		"Every Skupper Connector in the allowed scope, as JSON.",
		func(ctx context.Context) (any, int, error) {
			res, err := s.svc.ListConnectors(ctx, "", "", "")
			return res, len(res.Items), err
		})

	add(URITopology, "topology", "Application network topology",
		"The application network as typed JSON: sites, links, and routing key associations.",
		func(ctx context.Context) (any, int, error) {
			topo, err := s.svc.GetTopology(ctx, "", "")
			return topo, len(topo.Nodes), err
		})
}

func (s *Server) resourceHandler(name string, load func(context.Context) (any, int, error)) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		_, result, err := run(ctx, s, invocation{tool: "resource:" + name}, auth.RoleViewer,
			func(ctx context.Context) (*mcp.ReadResourceResult, int, error) {
				payload, count, err := load(ctx)
				if err != nil {
					return nil, 0, err
				}
				encoded, err := json.MarshalIndent(payload, "", "  ")
				if err != nil {
					return nil, 0, fmt.Errorf("encoding resource %s: %w", name, err)
				}
				return &mcp.ReadResourceResult{
					Contents: []*mcp.ResourceContents{{
						URI:      req.Params.URI,
						MIMEType: "application/json",
						Text:     string(encoded),
					}},
				}, count, nil
			})
		if err != nil {
			return nil, err
		}
		return result, nil
	}
}
