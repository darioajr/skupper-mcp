package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/kubernetes"
	"github.com/darioajr/skupper-scope/internal/mcpserver"
	"github.com/darioajr/skupper-scope/internal/observability"
)

type userTransport struct{ token *atomic.Value }

func (u userTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+u.token.Load().(string))
	return http.DefaultTransport.RoundTrip(r)
}

// This stack uses real HTTP, TLS, token exchange, MCP and dynamic Kubernetes
// clients. The stub enforces different native rights for two user credentials;
// a shared ambient credential would make the negative cases incorrectly pass.
func TestNativeUserPermissionsAndRefreshedRequestCredentials(t *testing.T) {
	var exchangeCalls, apiCalls atomic.Int32
	var redirectCalls atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectCalls.Add(1) }))
	defer redirectTarget.Close()
	stub := &stubAPIServer{t: t}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		token := r.Header.Get("Authorization")
		if token == "Bearer ambient-admin" || strings.HasPrefix(token, "Bearer mcp-") || r.Method != http.MethodGet {
			t.Errorf("Kubernetes received an ambient/MCP credential or non-read method")
		}
		if strings.Contains(r.URL.Path, "/namespaces/redirect/") {
			http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
			return
		}
		allowed := token == "Bearer native-alice-v2" ||
			(token == "Bearer native-alice-v1" && strings.Contains(r.URL.Path, "/namespaces/west/"))
		if !allowed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"native RBAC denial","code":403}`))
			return
		}
		stub.ServeHTTP(w, r)
	}))
	defer api.Close()
	exchange := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchangeCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		subject := r.Form.Get("subject_token")
		if subject == "mcp-exchange-denied" {
			http.Error(w, "must-not-leak-provider-secret", http.StatusForbidden)
			return
		}
		if r.Form.Get("audience") != "kubernetes" {
			t.Error("wrong downstream audience")
		}
		_ = json.NewEncoder(w).Encode(struct {
			AccessToken     string `json:"access_token"`
			TokenType       string `json:"token_type"`
			IssuedTokenType string `json:"issued_token_type"`
			ExpiresIn       int    `json:"expires_in"`
		}{strings.Replace(subject, "mcp-", "native-", 1), "Bearer", "urn:ietf:params:oauth:token-type:access_token", 60})
	}))
	defer exchange.Close()

	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
	kubeconfig := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: cluster
clusters:
- name: cluster
  cluster:
    server: %s
    certificate-authority-data: %s
users:
- name: ambient
  user:
    token: ambient-admin
contexts:
- name: cluster
  context:
    cluster: cluster
    user: ambient
`, api.URL, base64.StdEncoding.EncodeToString(ca))
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	cfg := config.Default()
	cfg.Addr = "127.0.0.1:0"
	cfg.Kubernetes.AllowedContexts = []string{"cluster"}
	cfg.Kubernetes.AllowedNamespaces = []string{"west", "east", "redirect"}
	cfg.Auth.Enabled, cfg.Auth.Issuer, cfg.Auth.Audience = true, "https://issuer.example", "mcp"
	cfg.Auth.ExchangeURL, cfg.Auth.KubernetesAudience = exchange.URL, "kubernetes"
	cfg.Auth.ExchangeClientID, cfg.Auth.ExchangeClientSecret = "exchange-client", "test-only-secret"
	cfg.Auth.ResourceURL = "https://mcp.example/mcp"
	cfg.Auth.ResourceMetadataURL = "https://mcp.example/.well-known/oauth-protected-resource"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	factory, err := kubernetes.NewClientFactory(cfg.Kubernetes)
	if err != nil {
		t.Fatal(err)
	}
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
	log, err := observability.NewLogger(cfg.Log, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.New(svc, observability.NewAuditor(log), cfg)
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.Verifier(func(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		user := "alice"
		if token == "mcp-bob" {
			user = "bob"
		} else if token != "mcp-alice-v1" && token != "mcp-alice-v2" && token != "mcp-exchange-denied" {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: user, Scopes: []string{cfg.Auth.ScopeRead, cfg.Auth.ScopeDiagnose}, Expiration: time.Now().Add(time.Hour)}, nil
	})
	mw := auth.NewWithVerifier(cfg.Auth, verifier)
	withTrustedIDP := auth.Middleware(func(next http.Handler) http.Handler {
		handler := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(oidc.ClientContext(r.Context(), exchange.Client())))
		})
	})
	httpServer, err := mcpserver.NewHTTPServer(cfg, srv, withTrustedIDP, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := httpServer.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(ctx, time.Second) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var token atomic.Value
	token.Store("mcp-alice-v1")
	client := mcp.NewClient(&mcp.Implementation{Name: "native-rbac-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://" + httpServer.Addr() + "/mcp", HTTPClient: &http.Client{Transport: userTransport{&token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	request := func(ns string, wantError bool) {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_sites", Arguments: map[string]any{"context": "cluster", "namespace": ns}})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError {
			t.Fatalf("namespace %s: error=%t, want %t; native RBAC must bound MCP results", ns, result.IsError, wantError)
		}
	}
	request("west", false)
	request("east", true)
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: mcpserver.URISites}); err == nil {
		t.Fatal("broad resource read elevated alice beyond her native west-only rights")
	}
	before := exchangeCalls.Load()
	request("unapproved", true)
	if exchangeCalls.Load() != before {
		t.Fatal("allowlist denial contacted the token endpoint")
	}
	token.Store("mcp-alice-v2")
	request("east", false)
	request("redirect", true)
	if redirectCalls.Load() != 0 {
		t.Fatal("Kubernetes redirected a delegated credential to an unapproved endpoint")
	}
	token.Store("mcp-exchange-denied")
	reads := apiCalls.Load()
	request("west", true)
	if apiCalls.Load() != reads {
		t.Fatal("exchange refusal fell back to ambient Kubernetes credentials")
	}
	// Legacy HTTP sessions retain their initialization context in the SDK.
	// Refreshing a token must still change the native credential for each call.
	legacyID := ""
	postLegacy := func(body, credential string) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "http://"+httpServer.Addr()+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if legacyID != "" {
			req.Header.Set("Mcp-Session-Id", legacyID)
		}
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if id := response.Header.Get("Mcp-Session-Id"); id != "" {
			legacyID = id
		}
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
			t.Fatalf("legacy request status %d: %s", response.StatusCode, payload)
		}
		return payload
	}
	postLegacy(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-native-test","version":"1"}}}`, "mcp-alice-v1")
	if legacyID == "" {
		t.Fatal("legacy session did not receive an identifier; token-refresh regression would not be exercised")
	}
	postLegacy(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "mcp-alice-v1")
	for i, tt := range []struct {
		credential string
		denied     bool
	}{{"mcp-alice-v1", true}, {"mcp-alice-v2", false}, {"mcp-alice-v1", true}} {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"list_sites","arguments":{"context":"cluster","namespace":"east"}}}`, i+2)
		payload := postLegacy(body, tt.credential)
		var result struct {
			Result struct {
				IsError bool              `json:"isError"`
				Content []json.RawMessage `json:"content"`
			} `json:"result"`
		}
		data := string(payload)
		for _, line := range strings.Split(data, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
				break
			}
		}
		if err := json.Unmarshal([]byte(data), &result); err != nil {
			t.Fatalf("legacy result: %v: %s", err, payload)
		}
		if result.Result.IsError != tt.denied || len(result.Result.Content) == 0 {
			t.Fatalf("legacy call reused stale connection credentials: %s", payload)
		}
	}
	// A separate identity has the MCP read scope but no native Kubernetes grant.
	token.Store("mcp-bob")
	bob, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://" + httpServer.Addr() + "/mcp", HTTPClient: &http.Client{Transport: userTransport{&token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	result, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_sites", Arguments: map[string]any{"namespace": "west"}})
	if err != nil || !result.IsError {
		t.Fatalf("MCP scope elevated bob beyond native rights: result=%v error=%v", result, err)
	}
}
