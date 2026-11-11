package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/domain"
)

// expectedTools is the complete tool surface. The test that uses it fails both
// when a tool disappears and when one is added, so a generic escape hatch
// cannot be introduced quietly.
var expectedTools = []string{
	"diagnose_connectivity",
	"get_topology",
	"list_connectors",
	"list_links",
	"list_listeners",
	"list_sites",
}

func TestClientSeesExactlyTheSixTools(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}

	if len(got) != len(expectedTools) {
		t.Fatalf("tools = %v, want exactly %v", got, expectedTools)
	}
	for i, want := range expectedTools {
		if got[i] != want {
			t.Errorf("tools = %v, want %v", got, expectedTools)
			break
		}
	}
}

func TestNoToolOffersAGenericEscapeHatch(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	forbiddenNames := []string{"execute", "command", "kubectl", "shell", "exec", "secret", "raw", "apply", "delete", "patch"}
	forbiddenParams := []string{"kubeconfig", "url", "path", "file", "command", "args", "resource", "kind", "apiversion", "labelselector"}

	for _, tool := range res.Tools {
		lowerName := strings.ToLower(tool.Name)
		for _, bad := range forbiddenNames {
			if strings.Contains(lowerName, bad) {
				t.Errorf("tool %q looks like a generic escape hatch", tool.Name)
			}
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not annotated read-only", tool.Name)
		}

		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshalling the schema of %s: %v", tool.Name, err)
		}
		var parsed struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(schema, &parsed); err != nil {
			t.Fatalf("parsing the schema of %s: %v", tool.Name, err)
		}
		for param := range parsed.Properties {
			for _, bad := range forbiddenParams {
				if strings.ToLower(param) == bad {
					t.Errorf("tool %q accepts parameter %q; a client must not be able to name "+
						"a connection, a file, or an arbitrary resource kind", tool.Name, param)
				}
			}
		}
	}
}

func TestListingToolsReturnStructuredOutput(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	t.Run("list_sites", func(t *testing.T) {
		var out ListSitesOutput
		structuredOutput(t, callTool(t, session, "list_sites", nil), &out)
		if len(out.Sites) != 2 {
			t.Errorf("sites = %+v, want both", out.Sites)
		}
		if len(out.Scope) != 2 {
			t.Errorf("scope = %+v, want both context/namespace pairs", out.Scope)
		}
	})

	t.Run("list_links", func(t *testing.T) {
		var out ListLinksOutput
		structuredOutput(t, callTool(t, session, "list_links", nil), &out)
		if len(out.Links) != 1 {
			t.Fatalf("links = %+v, want 1", out.Links)
		}
		if out.Links[0].LocalSiteID != "site-west" || out.Links[0].RemoteSiteID != "site-east" {
			t.Errorf("link = %+v, want both ends resolved", out.Links[0])
		}
	})

	t.Run("list_listeners", func(t *testing.T) {
		var out ListListenersOutput
		structuredOutput(t, callTool(t, session, "list_listeners", nil), &out)
		if len(out.Listeners) != 1 || out.Listeners[0].RoutingKey != "backend" {
			t.Fatalf("listeners = %+v, want the backend listener", out.Listeners)
		}
		if out.Listeners[0].SiteID != "site-west" {
			t.Errorf("listener SiteID = %q, want site-west resolved from the namespace",
				out.Listeners[0].SiteID)
		}
	})

	t.Run("list_connectors", func(t *testing.T) {
		var out ListConnectorsOutput
		structuredOutput(t, callTool(t, session, "list_connectors", nil), &out)
		if len(out.Connectors) != 1 || out.Connectors[0].SelectedPodCount != 2 {
			t.Fatalf("connectors = %+v, want the backend connector with 2 selected pods", out.Connectors)
		}
		if out.Connectors[0].SiteID != "site-east" {
			t.Errorf("connector SiteID = %q, want site-east resolved from the namespace",
				out.Connectors[0].SiteID)
		}
	})
}

func TestGetTopologyReturnsTypedJSONNotADiagram(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	res := callTool(t, session, "get_topology", nil)
	var out GetTopologyOutput
	structuredOutput(t, res, &out)

	if len(out.Nodes) != 2 || len(out.Edges) != 1 {
		t.Fatalf("topology = %d nodes, %d edges; want 2 and 1", len(out.Nodes), len(out.Edges))
	}
	if len(out.RoutingKeys) != 1 || out.RoutingKeys[0].RoutingKey != "backend" {
		t.Errorf("routing keys = %+v, want the backend group", out.RoutingKeys)
	}
	if len(out.ObservedScope) != 2 {
		t.Errorf("observedScope = %+v, want both pairs so an empty result is distinguishable "+
			"from an unread one", out.ObservedScope)
	}

	// The text content is the JSON encoding of the structured output, not a
	// rendered diagram; a client decides how to draw the network.
	text := resultText(res)
	for _, forbidden := range []string{"graph TD", "graph LR", "```mermaid", "flowchart"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("topology output contains %q; the primary representation must be typed JSON", forbidden)
		}
	}
}

func TestDiagnoseConnectivityHealthyPath(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	var out DiagnoseConnectivityOutput
	structuredOutput(t, callTool(t, session, "diagnose_connectivity", map[string]any{
		"sourceContext":   "west-cluster",
		"sourceNamespace": "west",
		"sourceSite":      "west",
		"targetContext":   "east-cluster",
		"targetNamespace": "east",
		"targetSite":      "east",
		"routingKey":      "backend",
	}), &out)

	if out.Status != domain.DiagnosisHealthy {
		t.Errorf("status = %q, want HEALTHY\nsummary: %s", out.Status, out.Summary)
	}
	if len(out.Checks) == 0 {
		t.Fatal("diagnosis returned no checks")
	}
	if out.Method.Technique == "" || len(out.Method.NotVerified) == 0 {
		t.Error("the diagnosis carries no in-band disclaimer; a model relaying it could overclaim")
	}
	if len(out.Path) != 1 {
		t.Errorf("path = %+v, want one hop", out.Path)
	}
}

func TestToolArgumentValidation(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		wantText string
	}{
		{
			name:     "a denied context is refused by name",
			tool:     "list_sites",
			args:     map[string]any{"context": "prod-cluster"},
			wantText: "not allowed",
		},
		{
			name:     "a denied namespace is refused by name",
			tool:     "list_sites",
			args:     map[string]any{"context": "west-cluster", "namespace": "kube-system"},
			wantText: "not allowed",
		},
		{
			name:     "diagnose without a routing key is refused",
			tool:     "diagnose_connectivity",
			args:     map[string]any{"sourceNamespace": "west", "targetNamespace": "east"},
			wantText: "routingKey",
		},
		{
			name:     "diagnose without an identified source is refused",
			tool:     "diagnose_connectivity",
			args:     map[string]any{"routingKey": "backend", "targetNamespace": "east"},
			wantText: "sourceSite or sourceNamespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callTool(t, session, tt.tool, tt.args)
			if !res.IsError {
				t.Fatalf("%s accepted an invalid request", tt.tool)
			}
			if !strings.Contains(resultText(res), tt.wantText) {
				t.Errorf("error = %q, want it to mention %q", resultText(res), tt.wantText)
			}
		})
	}
}

func TestSchemaRejectsAnUnknownArgumentType(t *testing.T) {
	srv, logs := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_sites",
		Arguments: map[string]any{"namespace": 42},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a namespace of the wrong type was accepted; the input schema must reject it")
	}
	if !strings.Contains(resultText(res), `want "string"`) {
		t.Errorf("error = %q, want a schema type complaint", resultText(res))
	}
	// Schema rejections are audited before the typed handler; no resource read
	// or unvalidated argument content should enter the log.
	if strings.Contains(logs.String(), `"tool":"list_sites"`) {
		t.Error("a schema-invalid call reached the handler")
	}
	if !strings.Contains(logs.String(), `"tool":"tools/call","outcome":"invalid"`) {
		t.Errorf("schema rejection was not audited: %s", logs.String())
	}
}

func TestResourcesMirrorTheTools(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	res, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	want := map[string]bool{
		URISites: false, URILinks: false, URIListeners: false,
		URIConnectors: false, URITopology: false,
	}
	for _, r := range res.Resources {
		if _, ok := want[r.URI]; !ok {
			t.Errorf("unexpected resource %q", r.URI)
			continue
		}
		want[r.URI] = true
		if r.MIMEType != "application/json" {
			t.Errorf("resource %q has MIME type %q, want application/json", r.URI, r.MIMEType)
		}
	}
	for uri, seen := range want {
		if !seen {
			t.Errorf("resource %q was not published", uri)
		}
	}

	// The topology resource and the topology tool must agree, because they call
	// the same application service.
	read, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: URITopology})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(read.Contents) != 1 {
		t.Fatalf("len(Contents) = %d, want 1", len(read.Contents))
	}
	var fromResource domain.Topology
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &fromResource); err != nil {
		t.Fatalf("decoding the topology resource: %v", err)
	}

	var fromTool GetTopologyOutput
	structuredOutput(t, callTool(t, session, "get_topology", nil), &fromTool)

	a, _ := json.Marshal(fromResource)
	b, _ := json.Marshal(fromTool.Topology)
	if string(a) != string(b) {
		t.Errorf("the topology resource and the topology tool disagree:\nresource: %s\ntool:     %s", a, b)
	}
}

func TestDiagnoseRequiresTheDiagnosticianRole(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())

	viewer := auth.Subject{ID: "alice", Authenticated: true, Roles: []auth.Role{auth.RoleViewer}}
	ctx := auth.WithSubject(context.Background(), viewer)

	_, out, err := run(ctx, srv, invocation{tool: "diagnose_connectivity"}, auth.RoleDiagnostician,
		func(context.Context) (string, int, error) { return "reached", 0, nil })
	if err == nil {
		t.Fatal("a viewer was allowed to run a diagnostician-only tool")
	}
	if out != "" {
		t.Error("the handler body ran despite the authorization failure")
	}
}

func TestViewerRoleMayList(t *testing.T) {
	srv, _ := newTestServer(t, demoNetwork())
	viewer := auth.Subject{ID: "alice", Authenticated: true, Roles: []auth.Role{auth.RoleViewer}}
	ctx := auth.WithSubject(context.Background(), viewer)

	_, out, err := run(ctx, srv, invocation{tool: "list_sites"}, auth.RoleViewer,
		func(context.Context) (string, int, error) { return "reached", 1, nil })
	if err != nil {
		t.Fatalf("a viewer was denied a viewer tool: %v", err)
	}
	if out != "reached" {
		t.Errorf("output = %q, want the handler to have run", out)
	}
}

func TestEveryToolCallIsAudited(t *testing.T) {
	srv, logs := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	callTool(t, session, "list_sites", map[string]any{"context": "west-cluster", "namespace": "west"})

	out := logs.String()
	for _, want := range []string{`"event":"tool_invocation"`, `"tool":"list_sites"`,
		`"outcome":"ok"`, `"read_scopes":[{"context":"west-cluster","namespace":"west"}]`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log is missing %s:\n%s", want, out)
		}
	}
}

func TestDeniedCallsAreAuditedAsDenied(t *testing.T) {
	srv, logs := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	callTool(t, session, "list_sites", map[string]any{"context": "prod-cluster"})

	if !strings.Contains(logs.String(), `"outcome":"denied"`) {
		t.Errorf("a denied call was not audited as denied:\n%s", logs.String())
	}
}

func TestAuditLogNeverContainsResourceBodies(t *testing.T) {
	srv, logs := newTestServer(t, demoNetwork())
	session := connect(t, srv)

	callTool(t, session, "list_sites", nil)
	callTool(t, session, "get_topology", nil)

	out := logs.String()
	// Resource identity may be logged as scope, but resource content must not
	// be: seeing a site's name in the log would mean the whole object was.
	for _, forbidden := range []string{"resourceVersion", "conditions", "site-west", "routingKey"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("audit log contains %q, which means a Kubernetes object reached it:\n%s", forbidden, out)
		}
	}
}
