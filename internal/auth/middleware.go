package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/darioajr/skupper-scope/internal/config"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Verifier turns a bearer token into a TokenInfo. It is the seam that lets
// tests exercise the authorization path without an identity provider.
type Verifier = mcpauth.TokenVerifier

// New builds the authentication middleware for the configured mode.
//
// When auth is disabled it returns a middleware that attaches an
// unauthenticated Subject, so downstream code always finds a Subject in the
// context and never has to special-case its absence.
func New(ctx context.Context, cfg config.Auth) (Middleware, error) {
	if !cfg.Enabled {
		return anonymous, nil
	}
	verifier, err := NewOIDCVerifier(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewWithVerifier(cfg, verifier), nil
}

// NewWithVerifier builds the middleware around an explicit verifier.
func NewWithVerifier(cfg config.Auth, verifier Verifier) Middleware {
	// RequireBearerToken performs the RFC 6750 / RFC 9728 handling: it rejects
	// a missing or malformed Authorization header, enforces expiry with the
	// configured skew, checks the required scopes, and populates the
	// WWW-Authenticate header so a client can discover the authorization server.
	require := mcpauth.RequireBearerToken(verifier, &mcpauth.RequireBearerTokenOptions{
		ResourceMetadataURL: cfg.ResourceMetadataURL,
		// Only the read scope is required at the transport layer. The diagnose
		// scope is enforced per tool, so a viewer receives a clear "forbidden"
		// on one tool rather than a 401 on the whole session.
		Scopes:    []string{cfg.ScopeRead},
		ClockSkew: cfg.ClockSkew,
	})
	return func(next http.Handler) http.Handler {
		return require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := mcpauth.TokenInfoFromContext(r.Context())
			subject := Subject{Authenticated: true}
			if info != nil {
				subject.ID = info.UserID
				subject.Scopes = info.Scopes
				subject.Roles = RolesFromScopes(info.Scopes, cfg.ScopeRead, cfg.ScopeDiagnose)
			}
			if subject.ID == "" {
				http.Error(w, "authenticated user identity is required", http.StatusUnauthorized)
				return
			}
			ctx := WithSubject(r.Context(), subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		}))
	}
}

func anonymous(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithSubject(r.Context(), Subject{Authenticated: false})))
	})
}

// NewOIDCVerifier builds a token verifier backed by the issuer's published
// JWKS. Discovery happens once at startup so a misconfigured issuer fails the
// process instead of every request; go-oidc refreshes the key set on its own
// as keys rotate.
func NewOIDCVerifier(ctx context.Context, cfg config.Auth) (Verifier, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery against %q failed: %w", cfg.Issuer, err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.Audience})

	return func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		idToken, err := verifier.Verify(ctx, token)
		if err != nil {
			// The token itself is never echoed back or logged; only the reason.
			return nil, fmt.Errorf("%w: %w", mcpauth.ErrInvalidToken, err)
		}
		var claims struct {
			Scope    string   `json:"scope"`
			Scopes   []string `json:"scopes"`
			Subject  string   `json:"sub"`
			Username string   `json:"preferred_username"`
		}
		if err := idToken.Claims(&claims); err != nil {
			return nil, fmt.Errorf("%w: reading claims: %w", mcpauth.ErrInvalidToken, err)
		}
		// sub is stable within the validated issuer; display names can change
		// or collide and must not key permissions or user rate limits.
		userID := claims.Subject
		return &mcpauth.TokenInfo{
			Scopes:     mergeScopes(claims.Scope, claims.Scopes),
			Expiration: idToken.Expiry,
			UserID:     userID,
		}, nil
	}, nil
}

// mergeScopes accepts both the RFC 6749 space-delimited "scope" string and the
// array form some providers emit, because Keycloak and others differ here.
func mergeScopes(spaceDelimited string, list []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range strings.Fields(spaceDelimited) {
		add(s)
	}
	for _, s := range list {
		add(s)
	}
	return out
}

// ProtectedResourceMetadata serves RFC 9728 metadata so an MCP client can
// discover which authorization server to use.
func ProtectedResourceMetadata(cfg config.Auth, resourceURL string) http.Handler {
	return mcpauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               resourceURL,
		AuthorizationServers:   []string{cfg.Issuer},
		ScopesSupported:        []string{cfg.ScopeRead, cfg.ScopeDiagnose},
		BearerMethodsSupported: []string{"header"},
	})
}

// DiscoveryTimeout bounds the startup OIDC discovery call.
const DiscoveryTimeout = 15 * time.Second
