package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func TestNativeTokenExchange(t *testing.T) {
	for _, tt := range []struct {
		name, response string
		status         int
		want           bool
	}{
		{"delegated access token", `{"access_token":"native-user","token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","expires_in":30}`, 200, true},
		{"refusal", `upstream-secret`, 403, false},
		{"MCP token must not pass through", `{"access_token":"mcp-user","token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","expires_in":30}`, 200, false},
		{"missing expiry", `{"access_token":"native-user","token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:access_token"}`, 200, false},
		{"wrong token type", `{"access_token":"native-user","token_type":"MAC","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","expires_in":30}`, 200, false},
		{"invalid JSON", `upstream-secret`, 200, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost {
					t.Errorf("exchange method = %s, want POST to the IdP only", r.Method)
				}
				id, secret, ok := r.BasicAuth()
				if !ok || id != "exchange-client" || secret != "test-only-secret" {
					t.Error("exchange client authentication is missing")
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.PostForm.Get("subject_token") != "mcp-user" || r.PostForm.Get("audience") != "kubernetes" || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
					t.Errorf("wrong delegated exchange request")
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			cfg := testAuthConfig()
			cfg.ExchangeURL, cfg.KubernetesAudience = server.URL, "kubernetes"
			cfg.ExchangeClientID, cfg.ExchangeClientSecret = "exchange-client", "test-only-secret"
			ctx := oidc.ClientContext(context.Background(), server.Client())
			ctx, err := VerifiedRequestContext(ctx, cfg, &mcpauth.TokenInfo{UserID: "alice", Scopes: []string{cfg.ScopeRead}, Expiration: time.Now().Add(time.Minute)}, http.Header{"Authorization": {"Bearer mcp-user"}})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				token, err := NativeToken(ctx)
				if tt.want && (err != nil || token != "native-user") {
					t.Fatalf("NativeToken = %q, %v, want delegated user", token, err)
				}
				if !tt.want && !errors.Is(err, ErrForbidden) {
					t.Fatalf("NativeToken = %q, %v, want explicit denial", token, err)
				}
				if err != nil && (strings.Contains(err.Error(), "upstream-secret") || strings.Contains(err.Error(), "test-only-secret")) {
					t.Error("exchange error leaked credentials or upstream body")
				}
			}
			if calls != 1 {
				t.Errorf("exchange requests = %d, want one request-local exchange", calls)
			}
		})
	}
}

func TestNativeExchangeRejectsRedirects(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls++ }))
	defer destination.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	cfg := testAuthConfig()
	cfg.ExchangeURL, cfg.KubernetesAudience = server.URL, "kubernetes"
	cfg.ExchangeClientID, cfg.ExchangeClientSecret = "client", "test-only"
	_, err := exchangeToken(oidc.ClientContext(context.Background(), server.Client()), cfg, "mcp-user")
	if !errors.Is(err, ErrForbidden) || destinationCalls != 0 {
		t.Fatalf("redirect = %v, destination requests = %d; credentials must not follow redirects", err, destinationCalls)
	}
}

func TestNativeCredentialRequiresVerifiedIdentity(t *testing.T) {
	if _, err := NativeToken(context.Background()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing identity = %v, want forbidden", err)
	}
	for _, info := range []*mcpauth.TokenInfo{nil, {Expiration: time.Now().Add(time.Minute)}, {UserID: "alice", Expiration: time.Now().Add(-time.Hour)}} {
		if _, err := VerifiedRequestContext(context.Background(), testAuthConfig(), info, http.Header{"Authorization": {"Bearer test"}}); !errors.Is(err, ErrForbidden) {
			t.Errorf("unattributable/expired request accepted: %v", err)
		}
	}
}
