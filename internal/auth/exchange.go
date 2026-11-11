package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/darioajr/skupper-scope/internal/config"
)

type nativeCredentialKey struct{}
type nativeCredential func(context.Context) (string, error)

// VerifiedRequestContext uses transport-verified metadata from this request,
// rather than the identity or credential captured when a session was opened.
func VerifiedRequestContext(ctx context.Context, cfg config.Auth, info *mcpauth.TokenInfo, header http.Header) (context.Context, error) {
	if info == nil || info.UserID == "" || info.Expiration.IsZero() || time.Now().After(info.Expiration.Add(cfg.ClockSkew)) {
		return nil, fmt.Errorf("%w: authenticated request identity is required", ErrForbidden)
	}
	_, token, ok := strings.Cut(header.Get("Authorization"), " ")
	if !ok || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: request credential is missing", ErrForbidden)
	}
	subject := Subject{ID: info.UserID, Authenticated: true, Scopes: info.Scopes,
		Roles: RolesFromScopes(info.Scopes, cfg.ScopeRead, cfg.ScopeDiagnose)}
	return withNativeCredential(WithSubject(ctx, subject), cfg, strings.TrimSpace(token)), nil
}

// NativeToken resolves a request-local, audience-specific user credential.
// Neither the MCP token nor a service credential is a fallback on failure.
func NativeToken(ctx context.Context) (string, error) {
	resolve, ok := ctx.Value(nativeCredentialKey{}).(nativeCredential)
	if !ok || !SubjectFromContext(ctx).Authenticated {
		return "", fmt.Errorf("%w: native user credential is missing", ErrForbidden)
	}
	return resolve(ctx)
}

func withNativeCredential(ctx context.Context, cfg config.Auth, token string) context.Context {
	// Cache only within this HTTP request, not across users, sessions or calls.
	var once sync.Once
	var credential string
	var err error
	resolve := nativeCredential(func(callCtx context.Context) (string, error) {
		once.Do(func() { credential, err = exchangeToken(callCtx, cfg, token) })
		return credential, err
	})
	return context.WithValue(ctx, nativeCredentialKey{}, resolve)
}

func exchangeToken(ctx context.Context, cfg config.Auth, token string) (string, error) {
	u, parseErr := url.Parse(cfg.ExchangeURL)
	if parseErr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: native token exchange requires a trusted HTTPS endpoint", ErrForbidden)
	}
	if cfg.ExchangeURL == "" || cfg.KubernetesAudience == "" || cfg.ExchangeClientID == "" || cfg.ExchangeClientSecret == "" {
		return "", fmt.Errorf("%w: native token exchange is not configured", ErrForbidden)
	}
	form := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":        {token},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"audience":             {cfg.KubernetesAudience},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ExchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: invalid token exchange endpoint", ErrForbidden)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(cfg.ExchangeClientID, cfg.ExchangeClientSecret)
	client := &http.Client{
		Timeout: 10 * time.Second,
		// An IdP redirect must never forward credentials to another endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// Reuse the trusted transport installed for OIDC (e.g. an enterprise CA).
	// Timeout and redirect policy remain fixed even with a custom transport.
	if configured, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && configured != nil {
		client.Transport = configured.Transport
	}
	response, err := client.Do(req)
	if err != nil {
		// Do not echo transport errors or IdP bodies: either can contain tokens.
		return "", fmt.Errorf("%w: native token exchange unavailable", ErrForbidden)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: native token exchange rejected (HTTP %d)", ErrForbidden, response.StatusCode)
	}
	var result struct {
		AccessToken     string `json:"access_token"`
		TokenType       string `json:"token_type"`
		IssuedTokenType string `json:"issued_token_type"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil ||
		result.AccessToken == "" || result.AccessToken == token ||
		!strings.EqualFold(result.TokenType, "Bearer") ||
		result.IssuedTokenType != "urn:ietf:params:oauth:token-type:access_token" || result.ExpiresIn <= 0 {
		return "", fmt.Errorf("%w: invalid native token exchange response", ErrForbidden)
	}
	return result.AccessToken, nil
}
