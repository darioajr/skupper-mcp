// Package e2e drives the whole server end to end: a real MCP client speaking
// Streamable HTTP to a real HTTP server, whose repository is a real dynamic
// Kubernetes client pointed at a stub API server that serves the Skupper v2
// CRD endpoints.
//
// Every layer is the production one except the API server itself. That is the
// part a unit test cannot cover: the group/version/resource paths, the list
// query parameters, the unstructured decoding, and the mapping into the domain.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/domain"
	"github.com/darioajr/skupper-scope/internal/kubernetes"
	"github.com/darioajr/skupper-scope/internal/mcpserver"
	"github.com/darioajr/skupper-scope/internal/observability"
)

// fixtureDir points at the same sanitized fixtures the mapper tests use, so the
// two cannot drift.
const fixtureDir = "../../internal/kubernetes/testdata"

// stubAPIServer serves Skupper v2 list endpoints from the repository fixtures.
type stubAPIServer struct {
	t *testing.T
	// requests records every path served, so the test can assert that the
	// server never issued a cluster-wide or unexpected read.
	requests []string
}

func (s *stubAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests = append(s.requests, r.URL.Path)

	// The dynamic client lists at
	// /apis/{group}/{version}/namespaces/{ns}/{resource}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 6 || parts[0] != "apis" || parts[1] != "skupper.io" || parts[2] != "v2alpha1" {
		http.NotFound(w, r)
		return
	}
	namespace, resource := parts[4], parts[5]

	items := s.itemsFor(resource, namespace)
	body := map[string]any{
		"apiVersion": "skupper.io/v2alpha1",
		"kind":       kindFor(resource) + "List",
		"metadata":   map[string]any{"resourceVersion": "1"},
		"items":      items,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.t.Errorf("encoding stub response: %v", err)
	}
}

func kindFor(resource string) string {
	switch resource {
	case "sites":
		return "Site"
	case "links":
		return "Link"
	case "listeners":
		return "Listener"
	case "connectors":
		return "Connector"
	default:
		return "Unknown"
	}
}

// itemsFor returns the fixture objects of the given kind that live in the
// given namespace.
func (s *stubAPIServer) itemsFor(resource, namespace string) []any {
	kind := kindFor(resource)
	var out []any
	for _, file := range []string{"sites.yaml", "links.yaml", "listeners.yaml", "connectors.yaml"} {
		for _, item := range loadList(s.t, file) {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if obj["kind"] != kind {
				continue
			}
			meta, _ := obj["metadata"].(map[string]any)
			if meta == nil || meta["namespace"] != namespace {
				continue
			}
			out = append(out, obj)
		}
	}
	return out
}

func loadList(t *testing.T, name string) []any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	asJSON, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("converting %s: %v", name, err)
	}
	var list struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(asJSON, &list); err != nil {
		t.Fatalf("decoding %s: %v", name, err)
	}
	return list.Items
}

// stack is the fully wired server under test.
type stack struct {
	addr    string
	mcpPath string
	apiStub *stubAPIServer
	logs    *bytes.Buffer
}

func startStack(t *testing.T) *stack {
	t.Helper()

	stub := &stubAPIServer{t: t}
	apiServer := httptest.NewServer(stub)
	t.Cleanup(apiServer.Close)

	cfg := config.Default()
	cfg.Addr = "127.0.0.1:0"
	cfg.Kubernetes.AllowedContexts = []string{"west-cluster", "east-cluster"}
	cfg.Kubernetes.AllowedNamespaces = []string{"west-cluster/west", "east-cluster/east"}
	cfg.ShutdownGrace = 2 * time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test configuration is invalid: %v", err)
	}

	// A real dynamic client, over real HTTP, against the stub.
	newClient := func() dynamic.Interface {
		client, err := dynamic.NewForConfig(&rest.Config{Host: apiServer.URL})
		if err != nil {
			t.Fatalf("building the dynamic client: %v", err)
		}
		return client
	}
	factory := kubernetes.NewClientFactoryWithClients(cfg.Kubernetes, map[string]dynamic.Interface{
		"west-cluster": newClient(),
		"east-cluster": newClient(),
	})

	repo, err := kubernetes.NewRepository(factory, cfg)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	allowlist, err := cfg.Allowlist()
	if err != nil {
		t.Fatalf("Allowlist: %v", err)
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
	srv, err := mcpserver.New(svc, observability.NewAuditor(log))
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	middleware, err := auth.New(context.Background(), cfg.Auth)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	httpSrv, err := mcpserver.NewHTTPServer(cfg, srv, middleware, nil, log)
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	if err := httpSrv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpSrv.Serve(ctx, cfg.ShutdownGrace) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the server did not shut down within 10s")
		}
	})

	return &stack{addr: httpSrv.Addr(), mcpPath: cfg.MCPPath, apiStub: stub, logs: &logs}
}

func (s *stack) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{Endpoint: "http://" + s.addr + s.mcpPath}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connecting to the server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		var text strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		t.Fatalf("%s returned an error: %s", name, text.String())
	}
	if out == nil {
		return
	}
	encoded, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling the result of %s: %v", name, err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		t.Fatalf("decoding the result of %s: %v\n%s", name, err, encoded)
	}
}

// TestAllSixToolsAgainstAStubAPIServer is the end-to-end acceptance test: a
// real MCP client, a real HTTP transport, a real dynamic Kubernetes client, and
// the project's own sanitized Skupper v2 fixtures.
func TestAllSixToolsAgainstAStubAPIServer(t *testing.T) {
	stack := startStack(t)
	session := stack.connect(t)

	t.Run("the client sees six tools", func(t *testing.T) {
		tools, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		if len(tools.Tools) != 6 {
			names := make([]string, 0, len(tools.Tools))
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
			}
			t.Fatalf("got %d tools (%v), want 6", len(tools.Tools), names)
		}
	})

	t.Run("list_sites reads both clusters", func(t *testing.T) {
		var out mcpserver.ListSitesOutput
		call(t, session, "list_sites", nil, &out)
		if len(out.Sites) != 2 {
			t.Fatalf("sites = %+v, want west and east", out.Sites)
		}
		byName := map[string]domain.Site{}
		for _, s := range out.Sites {
			byName[s.Name] = s
		}
		west, ok := byName["west"]
		if !ok {
			t.Fatal("the west site is missing")
		}
		if west.ID != "11111111-1111-1111-1111-111111111111" {
			t.Errorf("west ID = %q, want the fixture UID", west.ID)
		}
		if west.Status != domain.StatusReady {
			t.Errorf("west status = %q, want Ready", west.Status)
		}
		if west.SitesInNetwork != 2 {
			t.Errorf("west sitesInNetwork = %d, want 2", west.SitesInNetwork)
		}
	})

	t.Run("list_links resolves both ends", func(t *testing.T) {
		var out mcpserver.ListLinksOutput
		call(t, session, "list_links", nil, &out)
		if len(out.Links) != 1 {
			t.Fatalf("links = %+v, want 1", out.Links)
		}
		link := out.Links[0]
		if link.LocalSiteName != "west" {
			t.Errorf("local site = %q, want west", link.LocalSiteName)
		}
		if link.RemoteSiteName != "east" {
			t.Errorf("remote site = %q, want east", link.RemoteSiteName)
		}
		if link.Cost != 1 {
			t.Errorf("cost = %d, want 1", link.Cost)
		}
	})

	t.Run("list_listeners", func(t *testing.T) {
		var out mcpserver.ListListenersOutput
		call(t, session, "list_listeners", nil, &out)
		if len(out.Listeners) != 1 {
			t.Fatalf("listeners = %+v, want 1", out.Listeners)
		}
		l := out.Listeners[0]
		if l.RoutingKey != "backend" || l.Port != 8080 || l.Protocol != "tcp" {
			t.Errorf("listener = %+v, want routingKey backend on tcp/8080", l)
		}
		if l.SiteName != "west" {
			t.Errorf("listener site = %q, want west", l.SiteName)
		}
	})

	t.Run("list_connectors", func(t *testing.T) {
		var out mcpserver.ListConnectorsOutput
		call(t, session, "list_connectors", nil, &out)
		if len(out.Connectors) != 1 {
			t.Fatalf("connectors = %+v, want 1", out.Connectors)
		}
		c := out.Connectors[0]
		if c.Selector != "app=backend" {
			t.Errorf("selector = %q, want app=backend", c.Selector)
		}
		if c.SelectedPodCount != 2 {
			t.Errorf("selectedPodCount = %d, want 2", c.SelectedPodCount)
		}
		if c.SiteName != "east" {
			t.Errorf("connector site = %q, want east", c.SiteName)
		}
	})

	t.Run("get_topology", func(t *testing.T) {
		var out mcpserver.GetTopologyOutput
		call(t, session, "get_topology", nil, &out)
		if len(out.Nodes) != 2 {
			t.Fatalf("nodes = %+v, want 2", out.Nodes)
		}
		if len(out.Edges) != 1 {
			t.Fatalf("edges = %+v, want 1", out.Edges)
		}
		if !out.Edges[0].TargetResolved {
			t.Error("the edge target was not resolved across clusters")
		}
		if len(out.RoutingKeys) != 1 || out.RoutingKeys[0].RoutingKey != "backend" {
			t.Fatalf("routing keys = %+v, want the backend group", out.RoutingKeys)
		}
		group := out.RoutingKeys[0]
		if len(group.Listeners) != 1 || len(group.Connectors) != 1 {
			t.Errorf("group = %+v, want one of each side", group)
		}
		if len(group.Mismatches) != 0 {
			t.Errorf("mismatches = %+v, want none for a healthy network", group.Mismatches)
		}
	})

	t.Run("diagnose_connectivity", func(t *testing.T) {
		var out mcpserver.DiagnoseConnectivityOutput
		call(t, session, "diagnose_connectivity", map[string]any{
			"sourceContext":   "west-cluster",
			"sourceNamespace": "west",
			"targetContext":   "east-cluster",
			"targetNamespace": "east",
			"routingKey":      "backend",
		}, &out)

		if out.Status != domain.DiagnosisHealthy {
			t.Errorf("status = %q, want HEALTHY\nsummary: %s", out.Status, out.Summary)
		}
		if out.Confidence != domain.ConfidenceHigh {
			t.Errorf("confidence = %q, want HIGH", out.Confidence)
		}
		if len(out.Path) != 1 || out.Path[0].LinkName != "west-to-east" {
			t.Errorf("path = %+v, want the single west-to-east hop", out.Path)
		}
		if len(out.Method.NotVerified) == 0 {
			t.Error("the diagnosis has no in-band disclaimer")
		}
	})

	t.Run("resources mirror the tools", func(t *testing.T) {
		for _, uri := range []string{
			mcpserver.URISites, mcpserver.URILinks, mcpserver.URIListeners,
			mcpserver.URIConnectors, mcpserver.URITopology,
		} {
			read, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
			if err != nil {
				t.Fatalf("ReadResource(%s): %v", uri, err)
			}
			if len(read.Contents) != 1 || read.Contents[0].Text == "" {
				t.Fatalf("resource %s returned no content", uri)
			}
			var parsed any
			if err := json.Unmarshal([]byte(read.Contents[0].Text), &parsed); err != nil {
				t.Errorf("resource %s is not valid JSON: %v", uri, err)
			}
		}
	})

	t.Run("the server never issued a cluster-wide read", func(t *testing.T) {
		for _, path := range stack.apiStub.requests {
			if !strings.Contains(path, "/namespaces/") {
				t.Errorf("the server requested %q, which is not namespace-scoped", path)
			}
			if !strings.Contains(path, "/namespaces/west/") && !strings.Contains(path, "/namespaces/east/") {
				t.Errorf("the server requested %q, outside the allowlist", path)
			}
		}
		if len(stack.apiStub.requests) == 0 {
			t.Error("the stub API server was never called")
		}
	})

	t.Run("no Secret name reached any response", func(t *testing.T) {
		// The fixtures set tlsCredentials on the link, the listener and the
		// connector. None of those names may appear in anything the server
		// returned or logged.
		var everything strings.Builder
		for _, uri := range []string{mcpserver.URILinks, mcpserver.URIListeners, mcpserver.URIConnectors} {
			read, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
			if err != nil {
				t.Fatal(err)
			}
			everything.WriteString(read.Contents[0].Text)
		}
		everything.WriteString(stack.logs.String())

		for _, secret := range []string{
			"link-west-to-east-secret",
			"listener-backend-secret",
			"connector-backend-secret",
			"backend-7d9f4c8b6-abcde",
			"10.128.2.41",
		} {
			if strings.Contains(everything.String(), secret) {
				t.Errorf("%q leaked into a response or the log", secret)
			}
		}
	})
}

func TestDeniedScopeNeverReachesTheAPIServer(t *testing.T) {
	stack := startStack(t)
	session := stack.connect(t)

	before := len(stack.apiStub.requests)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_sites",
		Arguments: map[string]any{"context": "west-cluster", "namespace": "kube-system"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a denied namespace was accepted")
	}
	if len(stack.apiStub.requests) != before {
		t.Errorf("the API server was contacted for a denied namespace: %v",
			stack.apiStub.requests[before:])
	}
}

func TestUnknownCRDDegradesRatherThanFailing(t *testing.T) {
	// A cluster where the Skupper CRDs are not installed answers 404. The
	// server must report the gap and still return whatever it could read.
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`,
			http.StatusNotFound)
	}))
	t.Cleanup(notFound.Close)

	cfg := config.Default()
	cfg.Kubernetes.AllowedContexts = []string{"bare-cluster"}
	cfg.Kubernetes.AllowedNamespaces = []string{"west"}

	client, err := dynamic.NewForConfig(&rest.Config{Host: notFound.URL})
	if err != nil {
		t.Fatal(err)
	}
	factory := kubernetes.NewClientFactoryWithClients(cfg.Kubernetes,
		map[string]dynamic.Interface{"bare-cluster": client})
	repo, err := kubernetes.NewRepository(factory, cfg)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := cfg.Allowlist()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.NewService(repo, allowlist, cfg.MaxItems)
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.ListSites(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListSites failed outright: %v; a missing CRD must degrade to a warning", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("items = %+v, want none", res.Items)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("no warning was returned for a missing CRD")
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "CRD is not installed") {
		t.Errorf("warnings = %v, want one naming the missing CRD", res.Warnings)
	}
}
