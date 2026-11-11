package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/domain"
)

// plantedSecrets are strings that must never appear in any MCP response. They
// are written into the fields an attacker would hope leak through: a resource
// name, a status message, a selector, an endpoint host.
var plantedSecrets = []string{
	"eyJhbGciOiJSUzI1NiJ9.SUPER-SECRET-JWT-BODY.signature",
	"-----BEGIN PRIVATE KEY-----MIIEvQIBADANBgkqh",
	"skupper-site-ca-tls-secret-material",
	"AKIAIOSFODNN7EXAMPLE",
}

// poisonedNetwork plants credential material in every free-form string field
// the domain carries, so the leak test exercises real paths rather than a
// hypothetical one.
func poisonedNetwork() *fakeRepo {
	repo := demoNetwork()

	repo.sites[0].Message = "failed to mount " + plantedSecrets[2]
	repo.sites[0].Endpoints = []domain.Endpoint{{
		Name: "inter-router",
		Host: "router.internal",
		Port: "55671",
	}}
	repo.sites[0].Conditions = []domain.Condition{{
		Type:    domain.ConditionReady,
		Status:  "False",
		Reason:  "Error",
		Message: "token rejected: Bearer " + plantedSecrets[0],
	}}

	repo.links[0].Message = "handshake failed with " + plantedSecrets[1]
	repo.listeners[0].Message = "credential " + plantedSecrets[3]
	repo.connectors[0].Message = "pulling " + plantedSecrets[2]

	return repo
}

// TestNoToolOrResourceLeaksCredentialMaterial is the end-to-end version of the
// mapper-level guarantee: whatever a controller writes into a status message,
// none of it reaches a client.
func TestNoToolOrResourceLeaksCredentialMaterial(t *testing.T) {
	srv, logs := newTestServer(t, poisonedNetwork())
	session := connect(t, srv)
	ctx := context.Background()

	var everything strings.Builder

	toolCalls := []struct {
		name string
		args map[string]any
	}{
		{name: "list_sites"},
		{name: "list_links"},
		{name: "list_listeners"},
		{name: "list_connectors"},
		{name: "get_topology"},
		{name: "diagnose_connectivity", args: map[string]any{
			"sourceContext": "west-cluster", "sourceNamespace": "west", "sourceSite": "west",
			"targetContext": "east-cluster", "targetNamespace": "east", "targetSite": "east",
			"routingKey": "backend",
		}},
	}
	for _, call := range toolCalls {
		res := callTool(t, session, call.name, call.args)
		everything.WriteString(resultText(res))
		if res.StructuredContent != nil {
			encoded, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatalf("marshalling the result of %s: %v", call.name, err)
			}
			everything.Write(encoded)
		}
	}

	for _, uri := range []string{URISites, URILinks, URIListeners, URIConnectors, URITopology} {
		read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("ReadResource(%s): %v", uri, err)
		}
		for _, c := range read.Contents {
			everything.WriteString(c.Text)
		}
	}

	responses := everything.String()
	for _, secret := range plantedSecrets {
		if strings.Contains(responses, secret) {
			t.Errorf("an MCP response contains the planted credential %q", secret)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the audit log contains the planted credential %q", secret)
		}
	}
}

// TestStatusMessagesAreRedactedNotDropped checks that redaction preserves the
// useful part of a message. A message replaced wholesale would make the
// diagnosis useless; one passed through verbatim would leak.
func TestStatusMessagesAreRedactedNotDropped(t *testing.T) {
	srv, _ := newTestServer(t, poisonedNetwork())
	session := connect(t, srv)

	var out ListSitesOutput
	structuredOutput(t, callTool(t, session, "list_sites", nil), &out)

	var west domain.Site
	for _, s := range out.Sites {
		if s.Name == "west" {
			west = s
		}
	}
	if west.Message == "" {
		t.Fatal("the site message was dropped entirely; the operator loses the reason for the failure")
	}
	if !strings.Contains(west.Message, "failed to mount") {
		t.Errorf("message = %q, want the non-secret part preserved", west.Message)
	}
	if strings.Contains(west.Message, plantedSecrets[2]) {
		t.Errorf("message = %q, want the credential removed", west.Message)
	}
}
