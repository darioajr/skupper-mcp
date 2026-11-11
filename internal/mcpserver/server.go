package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/observability"
)

// Version is reported to MCP clients during initialisation.
const Version = "0.1.0"

// ServerName is the MCP implementation name.
const ServerName = "skupper-mcp-server"

// Server wires the application service onto an MCP server.
type Server struct {
	mcp            *mcp.Server
	svc            *application.Service
	auditor        *observability.Auditor
	limiter        *invocationLimiter
	auth           config.Auth
	requestTimeout time.Duration
}

// New builds the MCP server and registers the six tools and five resources.
func New(svc *application.Service, auditor *observability.Auditor, configs ...config.Config) (*Server, error) {
	if svc == nil {
		return nil, errors.New("mcpserver: application service is required")
	}
	if auditor == nil {
		return nil, errors.New("mcpserver: auditor is required")
	}
	if len(configs) > 1 {
		return nil, errors.New("mcpserver: at most one configuration is accepted")
	}
	cfg := config.Default()
	if len(configs) == 1 {
		cfg = configs[0]
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("mcpserver: request timeout must be positive")
	}
	limiter, err := newInvocationLimiter(cfg.Limits)
	if err != nil {
		return nil, err
	}

	impl := &mcp.Implementation{
		Name:    ServerName,
		Version: Version,
		Title:   "Skupper MCP Server",
	}
	s := &Server{
		mcp: mcp.NewServer(impl, &mcp.ServerOptions{
			Instructions: instructions,
		}),
		svc:            svc,
		auditor:        auditor,
		limiter:        limiter,
		auth:           cfg.Auth,
		requestTimeout: cfg.RequestTimeout,
	}
	s.mcp.AddReceivingMiddleware(s.guard)
	s.registerTools()
	s.registerResources()
	return s, nil
}

// MCP returns the underlying SDK server, for transports to run.
func (s *Server) MCP() *mcp.Server { return s.mcp }

const instructions = `This server observes a Skupper v2 application network through the Kubernetes API.

Everything it returns is read-only and describes resource state: what is declared in a
resource spec, and what the Skupper controller reported in its status. The server never
sends traffic, measures latency, or validates TLS end to end. When relaying a diagnosis,
preserve the distinction its evidence entries draw between DECLARED, OBSERVED, INFERRED
and ABSENT, and do not restate an inference as a measurement.

Start with get_topology for an overview, then use the listing tools for detail, then
diagnose_connectivity for a specific routing key between two sites.`

// invocation carries the audit fields a tool handler fills in as it runs.
type invocation struct {
	tool       string
	contextArg string
	nsArg      string
	routingKey string
}

// run executes fn with authorization, timing and auditing applied uniformly, so
// no tool can be added that silently skips one of the three.
func run[Out any](
	ctx context.Context,
	s *Server,
	inv invocation,
	required auth.Role,
	fn func(context.Context) (Out, int, error),
) (*mcp.CallToolResult, Out, error) {
	var zero Out
	started := time.Now()
	ctx, trace := application.WithReadTrace(ctx)
	subject := auth.SubjectFromContext(ctx)

	record := func(out string, count int, err error) {
		session := ""
		if state, ok := ctx.Value(auditStateKey{}).(*auditState); ok {
			session = state.session
		}
		s.auditor.Record(ctx, observability.Invocation{
			Tool:        inv.tool,
			Subject:     subject.ID,
			Scopes:      subject.Scopes,
			Context:     inv.contextArg,
			Namespace:   inv.nsArg,
			RoutingKey:  inv.routingKey,
			ResultCount: count,
			Outcome:     out,
			Duration:    time.Since(started),
			Err:         err,
			ReadScopes:  trace.Scopes(),
			Session:     session,
		})
		if state, ok := ctx.Value(auditStateKey{}).(*auditState); ok {
			state.recorded = true
		}
	}

	if err := subject.Require(required); err != nil {
		record(observability.OutcomeDenied, 0, err)
		return nil, zero, err
	}

	result, count, err := fn(ctx)
	if err != nil {
		record(classify(err), 0, err)
		// Returning the error makes it a tool error rather than a protocol
		// error, so the client sees the explanation instead of a dead session.
		return nil, zero, err
	}
	record(observability.OutcomeOK, count, nil)
	return nil, result, nil
}

func classify(err error) string {
	switch {
	case errors.Is(err, config.ErrContextNotAllowed),
		errors.Is(err, config.ErrNamespaceNotAllowed),
		errors.Is(err, auth.ErrForbidden):
		return observability.OutcomeDenied
	case errors.Is(err, application.ErrInvalidRequest):
		return observability.OutcomeInvalid
	default:
		return observability.OutcomeError
	}
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "list_sites",
		Title: "List Skupper sites",
		Description: "List the Skupper v2 Site resources visible in the allowed contexts and namespaces, " +
			"with the status their controller reported. Use this first to learn which sites exist and " +
			"what their identifiers are.",
		Annotations: readOnlyAnnotations("List Skupper sites"),
	}, s.listSites)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "list_links",
		Title: "List Skupper links",
		Description: "List Skupper v2 Link resources and their observed state, including the remote site " +
			"each one reports, its cost, and its endpoints. Credentials and Secret names are never returned.",
		Annotations: readOnlyAnnotations("List Skupper links"),
	}, s.listLinks)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "list_listeners",
		Title: "List Skupper listeners",
		Description: "List Skupper v2 Listener resources: the routing key, host, port and protocol each " +
			"exposes locally, and whether its controller sees a matching connector.",
		Annotations: readOnlyAnnotations("List Skupper listeners"),
	}, s.listListeners)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "list_connectors",
		Title: "List Skupper connectors",
		Description: "List Skupper v2 Connector resources: the routing key, the workload selector or host, " +
			"the port and protocol, and whether the controller sees a matching listener.",
		Annotations: readOnlyAnnotations("List Skupper connectors"),
	}, s.listConnectors)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "get_topology",
		Title: "Get the application network topology",
		Description: "Return the application network as typed JSON: sites as nodes, links as edges, " +
			"listeners and connectors attached to their sites, and routing keys grouping the two sides " +
			"together. Warnings record anything incomplete or ambiguous. This returns data, not a diagram.",
		Annotations: readOnlyAnnotations("Get network topology"),
	}, s.getTopology)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:  "diagnose_connectivity",
		Title: "Diagnose connectivity for a routing key",
		Description: "Analyse whether a routing key can carry traffic from one site to another, by " +
			"examining site readiness, the link path between them, and the listener and connector on " +
			"each side. The analysis is static: it reads resource state and changes nothing. It does " +
			"not send traffic, measure latency, or validate TLS, and every conclusion is labelled as " +
			"declared state, observed state, or inference.",
		Annotations: readOnlyAnnotations("Diagnose connectivity"),
	}, s.diagnoseConnectivity)
}

func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	openWorld := true
	return &mcp.ToolAnnotations{
		Title:          title,
		ReadOnlyHint:   true,
		IdempotentHint: true,
		// The set of Skupper resources is not fixed and changes outside this
		// server's control, so the world a tool observes is open.
		OpenWorldHint: &openWorld,
	}
}

func (s *Server) listSites(ctx context.Context, _ *mcp.CallToolRequest, in ListSitesInput) (*mcp.CallToolResult, ListSitesOutput, error) {
	return run(ctx, s, invocation{tool: "list_sites", contextArg: in.Context, nsArg: in.Namespace}, auth.RoleViewer,
		func(ctx context.Context) (ListSitesOutput, int, error) {
			res, err := s.svc.ListSites(ctx, in.Context, in.Namespace)
			if err != nil {
				return ListSitesOutput{}, 0, err
			}
			return ListSitesOutput{Sites: res.Items, Scope: res.Scope, Warnings: res.Warnings}, len(res.Items), nil
		})
}

func (s *Server) listLinks(ctx context.Context, _ *mcp.CallToolRequest, in ListLinksInput) (*mcp.CallToolResult, ListLinksOutput, error) {
	return run(ctx, s, invocation{tool: "list_links", contextArg: in.Context, nsArg: in.Namespace}, auth.RoleViewer,
		func(ctx context.Context) (ListLinksOutput, int, error) {
			res, err := s.svc.ListLinks(ctx, in.Context, in.Namespace, in.Site)
			if err != nil {
				return ListLinksOutput{}, 0, err
			}
			return ListLinksOutput{Links: res.Items, Scope: res.Scope, Warnings: res.Warnings}, len(res.Items), nil
		})
}

func (s *Server) listListeners(ctx context.Context, _ *mcp.CallToolRequest, in ListListenersInput) (*mcp.CallToolResult, ListListenersOutput, error) {
	return run(ctx, s, invocation{tool: "list_listeners", contextArg: in.Context, nsArg: in.Namespace}, auth.RoleViewer,
		func(ctx context.Context) (ListListenersOutput, int, error) {
			res, err := s.svc.ListListeners(ctx, in.Context, in.Namespace, in.Site)
			if err != nil {
				return ListListenersOutput{}, 0, err
			}
			return ListListenersOutput{Listeners: res.Items, Scope: res.Scope, Warnings: res.Warnings}, len(res.Items), nil
		})
}

func (s *Server) listConnectors(ctx context.Context, _ *mcp.CallToolRequest, in ListConnectorsInput) (*mcp.CallToolResult, ListConnectorsOutput, error) {
	return run(ctx, s, invocation{tool: "list_connectors", contextArg: in.Context, nsArg: in.Namespace}, auth.RoleViewer,
		func(ctx context.Context) (ListConnectorsOutput, int, error) {
			res, err := s.svc.ListConnectors(ctx, in.Context, in.Namespace, in.Site)
			if err != nil {
				return ListConnectorsOutput{}, 0, err
			}
			return ListConnectorsOutput{Connectors: res.Items, Scope: res.Scope, Warnings: res.Warnings}, len(res.Items), nil
		})
}

func (s *Server) getTopology(ctx context.Context, _ *mcp.CallToolRequest, in GetTopologyInput) (*mcp.CallToolResult, GetTopologyOutput, error) {
	return run(ctx, s, invocation{tool: "get_topology", contextArg: in.Context, nsArg: in.Namespace}, auth.RoleViewer,
		func(ctx context.Context) (GetTopologyOutput, int, error) {
			topo, err := s.svc.GetTopology(ctx, in.Context, in.Namespace)
			if err != nil {
				return GetTopologyOutput{}, 0, err
			}
			return GetTopologyOutput{Topology: topo}, len(topo.Nodes), nil
		})
}

func (s *Server) diagnoseConnectivity(ctx context.Context, _ *mcp.CallToolRequest, in DiagnoseConnectivityInput) (*mcp.CallToolResult, DiagnoseConnectivityOutput, error) {
	req := application.DiagnoseRequest{
		SourceContext:   in.SourceContext,
		SourceNamespace: in.SourceNamespace,
		SourceSite:      in.SourceSite,
		TargetContext:   in.TargetContext,
		TargetNamespace: in.TargetNamespace,
		TargetSite:      in.TargetSite,
		RoutingKey:      in.RoutingKey,
	}
	inv := invocation{
		tool:       "diagnose_connectivity",
		contextArg: in.SourceContext,
		nsArg:      in.SourceNamespace,
		routingKey: in.RoutingKey,
	}
	return run(ctx, s, inv, auth.RoleDiagnostician,
		func(ctx context.Context) (DiagnoseConnectivityOutput, int, error) {
			d, err := s.svc.DiagnoseConnectivity(ctx, req)
			if err != nil {
				return DiagnoseConnectivityOutput{}, 0, err
			}
			return DiagnoseConnectivityOutput{Diagnosis: d}, len(d.Checks), nil
		})
}
