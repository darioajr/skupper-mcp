package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
	"github.com/darioajr/skupper-scope/internal/observability"
)

// fakeRepo is the in-memory repository the MCP layer is tested against. The
// application layer already has its own; this one is deliberately independent
// so a change in one package's helper cannot mask a break in the other.
type fakeRepo struct {
	sites      []domain.Site
	links      []domain.Link
	listeners  []domain.Listener
	connectors []domain.Connector
	err        error
}

func scoped[T any](items []T, q application.Query, ref func(T) domain.ResourceRef) application.Result[T] {
	out := application.Result[T]{Items: []T{}}
	for _, item := range items {
		r := ref(item)
		if r.Context == q.Scope.Context && r.Namespace == q.Scope.Namespace {
			out.Items = append(out.Items, item)
		}
	}
	return out
}

func (f *fakeRepo) ListSites(_ context.Context, q application.Query) (application.Result[domain.Site], error) {
	if f.err != nil {
		return application.Result[domain.Site]{}, f.err
	}
	return scoped(f.sites, q, func(s domain.Site) domain.ResourceRef { return s.ResourceRef }), nil
}

func (f *fakeRepo) ListLinks(_ context.Context, q application.Query) (application.Result[domain.Link], error) {
	if f.err != nil {
		return application.Result[domain.Link]{}, f.err
	}
	return scoped(f.links, q, func(l domain.Link) domain.ResourceRef { return l.ResourceRef }), nil
}

func (f *fakeRepo) ListListeners(_ context.Context, q application.Query) (application.Result[domain.Listener], error) {
	if f.err != nil {
		return application.Result[domain.Listener]{}, f.err
	}
	return scoped(f.listeners, q, func(l domain.Listener) domain.ResourceRef { return l.ResourceRef }), nil
}

func (f *fakeRepo) ListConnectors(_ context.Context, q application.Query) (application.Result[domain.Connector], error) {
	if f.err != nil {
		return application.Result[domain.Connector]{}, f.err
	}
	return scoped(f.connectors, q, func(c domain.Connector) domain.ResourceRef { return c.ResourceRef }), nil
}

// demoNetwork is a west/east network whose resources carry planted credential
// material, so leak tests have something concrete to look for.
func demoNetwork() *fakeRepo {
	return &fakeRepo{
		sites: []domain.Site{
			{
				ResourceRef: domain.ResourceRef{
					ID: "site-west", Context: "west-cluster", Namespace: "west",
					Name: "west", UID: "site-west", Kind: "Site", APIVersion: "skupper.io/v2alpha1",
				},
				ObservedState: domain.ObservedState{Status: domain.StatusReady},
			},
			{
				ResourceRef: domain.ResourceRef{
					ID: "site-east", Context: "east-cluster", Namespace: "east",
					Name: "east", UID: "site-east", Kind: "Site", APIVersion: "skupper.io/v2alpha1",
				},
				ObservedState: domain.ObservedState{Status: domain.StatusReady},
			},
		},
		links: []domain.Link{{
			ResourceRef: domain.ResourceRef{
				ID: "link-1", Context: "west-cluster", Namespace: "west",
				Name: "west-to-east", UID: "link-1", Kind: "Link",
			},
			ObservedState:  domain.ObservedState{Status: domain.StatusReady},
			RemoteSiteID:   "site-east",
			RemoteSiteName: "east",
			Cost:           1,
		}},
		listeners: []domain.Listener{{
			ResourceRef: domain.ResourceRef{
				ID: "listener-1", Context: "west-cluster", Namespace: "west",
				Name: "backend", UID: "listener-1", Kind: "Listener",
			},
			ObservedState:        domain.ObservedState{Status: domain.StatusReady},
			RoutingKey:           "backend",
			Host:                 "backend",
			Port:                 8080,
			Protocol:             "tcp",
			HasMatchingConnector: true,
		}},
		connectors: []domain.Connector{{
			ResourceRef: domain.ResourceRef{
				ID: "connector-1", Context: "east-cluster", Namespace: "east",
				Name: "backend", UID: "connector-1", Kind: "Connector",
			},
			ObservedState:       domain.ObservedState{Status: domain.StatusReady},
			RoutingKey:          "backend",
			Selector:            "app=backend",
			Port:                8080,
			Protocol:            "tcp",
			SelectedPodCount:    2,
			HasMatchingListener: true,
		}},
	}
}

func newTestServer(t *testing.T, repo application.SkupperResourceRepository) (*Server, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	cfg.Kubernetes.AllowedContexts = []string{"west-cluster", "east-cluster"}
	cfg.Kubernetes.AllowedNamespaces = []string{"west-cluster/west", "east-cluster/east"}
	allowlist, err := cfg.Allowlist()
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	svc, err := application.NewService(repo, allowlist, cfg.MaxItems)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var logs bytes.Buffer
	log, err := observability.NewLogger(config.Log{Level: "debug", Format: "json"}, &logs)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	srv, err := New(svc, observability.NewAuditor(log))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, &logs
}

// connect drives the server through a real MCP client over the SDK's in-memory
// transport, so the tests exercise the protocol rather than the handlers.
func connect(t *testing.T, srv *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Wait() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

// structuredOutput decodes the tool's structured result into v, failing if the
// call returned a tool error.
func structuredOutput(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned an error: %s", resultText(res))
	}
	if res.StructuredContent == nil {
		t.Fatal("tool returned no structured content; every tool must return typed JSON")
	}
	encoded, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-encoding structured content: %v", err)
	}
	if err := json.Unmarshal(encoded, v); err != nil {
		t.Fatalf("decoding structured content into %T: %v\n%s", v, err, encoded)
	}
}

func resultText(res *mcp.CallToolResult) string {
	var b bytes.Buffer
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}
