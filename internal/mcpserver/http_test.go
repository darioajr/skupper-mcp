package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/observability"
)

func httpTestConfig() config.Config {
	cfg := config.Default()
	cfg.Addr = "127.0.0.1:0"
	cfg.Kubernetes.AllowedContexts = []string{"west-cluster", "east-cluster"}
	cfg.Kubernetes.AllowedNamespaces = []string{"west-cluster/west", "east-cluster/east"}
	cfg.ShutdownGrace = 2 * time.Second
	return cfg
}

func httpTestAuthConfig() config.Auth {
	cfg := config.Default().Auth
	cfg.Enabled, cfg.Issuer, cfg.Audience = true, "https://issuer.example/realms/skupper", "skupper-mcp"
	cfg.ResourceURL = "https://mcp.example/mcp"
	cfg.ResourceMetadataURL = "https://mcp.example/.well-known/oauth-protected-resource"
	cfg.ExchangeURL, cfg.KubernetesAudience = "https://issuer.example/token", "kubernetes"
	cfg.ExchangeClientID, cfg.ExchangeClientSecret = "exchange-client", "test-only"
	return cfg
}

func startHTTPServer(t *testing.T, cfg config.Config, mw auth.Middleware, ready Ready) *HTTPServer {
	t.Helper()
	srv, _ := newTestServer(t, demoNetwork())

	var logs bytes.Buffer
	log, err := observability.NewLogger(config.Log{Level: "debug", Format: "json"}, &logs)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	httpSrv, err := NewHTTPServer(cfg, srv, mw, ready, log)
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
			t.Error("Serve did not return within 10s of cancellation")
		}
	})
	return httpSrv
}

func anonymousMiddleware(t *testing.T) auth.Middleware {
	t.Helper()
	cfg := config.Default().Auth
	cfg.Enabled = false
	mw, err := auth.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	return mw
}

func TestHTTPServerServesMCPOverStreamableHTTP(t *testing.T) {
	cfg := httpTestConfig()
	srv := startHTTPServer(t, cfg, anonymousMiddleware(t), nil)

	transport := &mcp.StreamableClientTransport{Endpoint: "http://" + srv.Addr() + cfg.MCPPath}
	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "0.0.0"}, nil)

	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connecting over Streamable HTTP: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != len(expectedTools) {
		t.Errorf("got %d tools over HTTP, want %d", len(tools.Tools), len(expectedTools))
	}

	var out ListSitesOutput
	structuredOutput(t, callTool(t, session, "list_sites", nil), &out)
	if len(out.Sites) != 2 {
		t.Errorf("sites over HTTP = %+v, want 2", out.Sites)
	}
}

func TestProbesDoNotRequireACredential(t *testing.T) {
	cfg := httpTestConfig()
	cfg.Auth = httpTestAuthConfig()
	// A verifier that rejects everything, so a probe reaching the auth layer
	// would fail.
	rejectAll := auth.Verifier(func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		return nil, mcpauth.ErrInvalidToken
	})
	srv := startHTTPServer(t, cfg, auth.NewWithVerifier(cfg.Auth, rejectAll), nil)
	base := "http://" + srv.Addr()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200; a probe must never need a token", path, resp.StatusCode)
		}
	}

	// The MCP endpoint, by contrast, must be protected.
	resp, err := http.Post(base+cfg.MCPPath, "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("POST %s: %v", cfg.MCPPath, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST %s = %d, want 401", cfg.MCPPath, resp.StatusCode)
	}
}

func TestReadinessReportsFailureWithoutDetail(t *testing.T) {
	cfg := httpTestConfig()
	notReady := func() error { return errors.New("kubeconfig context west-cluster is unreachable") }
	srv := startHTTPServer(t, cfg, anonymousMiddleware(t), notReady)

	resp, err := http.Get("http://" + srv.Addr() + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz = %d, want 503", resp.StatusCode)
	}
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	if bytes.Contains(body[:n], []byte("kubeconfig")) {
		t.Error("the readiness response leaked the failure detail; /readyz is unauthenticated")
	}
}

func TestProtectedResourceMetadataIsPublishedWhenAuthIsOn(t *testing.T) {
	cfg := httpTestConfig()
	cfg.Auth = httpTestAuthConfig()
	rejectAll := auth.Verifier(func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		return nil, mcpauth.ErrInvalidToken
	})
	srv := startHTTPServer(t, cfg, auth.NewWithVerifier(cfg.Auth, rejectAll), nil)

	resp, err := http.Get("http://" + srv.Addr() + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var metadata struct {
		Resource string `json:"resource"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Resource != cfg.Auth.ResourceURL {
		t.Errorf("metadata resource = %q, want MCP endpoint %q rather than metadata URL", metadata.Resource, cfg.Auth.ResourceURL)
	}
}

func TestTransportRejectionsAreAuditedWithoutRequestContent(t *testing.T) {
	for _, tt := range []struct {
		name, token   string
		authenticated bool
		status        int
		subject       string
	}{
		{"missing token", "", true, http.StatusUnauthorized, "anonymous"},
		{"invalid token", "wrong", true, http.StatusUnauthorized, "anonymous"},
		{"authenticated malformed body", "good", true, http.StatusBadRequest, "alice"},
		{"local malformed body", "", false, http.StatusBadRequest, "anonymous"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, logs := newTestServer(t, demoNetwork())
			cfg := httpTestConfig()
			cfg.Auth = httpTestAuthConfig()
			cfg.Auth.Enabled = tt.authenticated
			var mw auth.Middleware
			if tt.authenticated {
				mw = auth.NewWithVerifier(cfg.Auth, func(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
					if token != "good" {
						return nil, mcpauth.ErrInvalidToken
					}
					return &mcpauth.TokenInfo{UserID: "alice", Scopes: []string{cfg.Auth.ScopeRead}, Expiration: time.Now().Add(time.Minute)}, nil
				})
			} else {
				mw = anonymousMiddleware(t)
			}
			log, err := observability.NewLogger(cfg.Log, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewHTTPServer(cfg, srv, mw, nil, log)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"credential":"body-must-never-be-logged"`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			h.http.Handler.ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("HTTP status = %d, want %d: %s", response.Code, tt.status, response.Body.String())
			}
			out := logs.String()
			if !strings.Contains(out, `"tool":"transport:mcp"`) || !strings.Contains(out, `"subject":"`+tt.subject+`"`) {
				t.Errorf("transport rejection missing identity/outcome: %s", out)
			}
			if strings.Contains(out, "body-must-never-be-logged") || strings.Contains(out, "Bearer ") {
				t.Error("transport audit leaked request body/header")
			}
		})
	}
}

func TestMetadataIsAbsentWhenAuthIsOff(t *testing.T) {
	cfg := httpTestConfig()
	srv := startHTTPServer(t, cfg, anonymousMiddleware(t), nil)

	resp, err := http.Get("http://" + srv.Addr() + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when there is no authorization server to advertise", resp.StatusCode)
	}
}

func TestServeShutsDownOnContextCancellation(t *testing.T) {
	cfg := httpTestConfig()
	srv, _ := newTestServer(t, demoNetwork())
	var logs bytes.Buffer
	log, err := observability.NewLogger(config.Log{Level: "debug", Format: "json"}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	httpSrv, err := NewHTTPServer(cfg, srv, anonymousMiddleware(t), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := httpSrv.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpSrv.Serve(ctx, cfg.ShutdownGrace) }()

	// Wait until the server is actually accepting connections.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + httpSrv.Addr() + "/healthz"); err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancellation; shutdown is not cooperative")
	}

	if resp, err := http.Get("http://" + httpSrv.Addr() + "/healthz"); err == nil {
		resp.Body.Close()
		t.Error("the listener is still accepting connections after shutdown")
	}
}

func TestNewHTTPServerValidatesDependencies(t *testing.T) {
	cfg := httpTestConfig()
	srv, _ := newTestServer(t, demoNetwork())
	log, err := observability.NewLogger(config.Log{Level: "info", Format: "json"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewHTTPServer(cfg, nil, anonymousMiddleware(t), nil, log); err == nil {
		t.Error("NewHTTPServer accepted a nil MCP server")
	}
	if _, err := NewHTTPServer(cfg, srv, nil, nil, log); err == nil {
		t.Error("NewHTTPServer accepted a nil auth middleware")
	}
	if _, err := NewHTTPServer(cfg, srv, anonymousMiddleware(t), nil, nil); err == nil {
		t.Error("NewHTTPServer accepted a nil logger")
	}
}
