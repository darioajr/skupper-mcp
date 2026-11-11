package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/darioajr/skupper-scope/internal/config"
)

func testAuthConfig() config.Auth {
	c := config.Default().Auth
	c.Enabled = true
	c.Issuer = "https://issuer.example/realms/skupper"
	c.Audience = "skupper-mcp"
	return c
}

func TestRolesFromScopes(t *testing.T) {
	cfg := testAuthConfig()
	tests := []struct {
		name   string
		scopes []string
		want   []Role
	}{
		{name: "no scopes grants nothing", scopes: nil},
		{name: "read scope grants viewer", scopes: []string{cfg.ScopeRead}, want: []Role{RoleViewer}},
		{
			name:   "diagnose scope grants diagnostician",
			scopes: []string{cfg.ScopeDiagnose},
			want:   []Role{RoleDiagnostician},
		},
		{
			name:   "both scopes grant both roles",
			scopes: []string{cfg.ScopeRead, cfg.ScopeDiagnose},
			want:   []Role{RoleViewer, RoleDiagnostician},
		},
		{name: "an unrelated scope grants nothing", scopes: []string{"openid", "profile"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RolesFromScopes(tt.scopes, cfg.ScopeRead, cfg.ScopeDiagnose)
			if len(got) != len(tt.want) {
				t.Fatalf("roles = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("roles = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSubjectAuthorization(t *testing.T) {
	tests := []struct {
		name     string
		subject  Subject
		required Role
		want     bool
	}{
		{
			name:     "an unauthenticated subject is allowed, because that mode is loopback-only",
			subject:  Subject{Authenticated: false},
			required: RoleDiagnostician,
			want:     true,
		},
		{
			name:     "a viewer may list",
			subject:  Subject{Authenticated: true, Roles: []Role{RoleViewer}},
			required: RoleViewer,
			want:     true,
		},
		{
			name:     "a viewer may not diagnose",
			subject:  Subject{Authenticated: true, Roles: []Role{RoleViewer}},
			required: RoleDiagnostician,
			want:     false,
		},
		{
			name:     "a diagnostician may list, because the role is a superset",
			subject:  Subject{Authenticated: true, Roles: []Role{RoleDiagnostician}},
			required: RoleViewer,
			want:     true,
		},
		{
			name:     "a token with no roles may do nothing",
			subject:  Subject{Authenticated: true},
			required: RoleViewer,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.subject.Allows(tt.required); got != tt.want {
				t.Errorf("Allows(%q) = %t, want %t", tt.required, got, tt.want)
			}
			err := tt.subject.Require(tt.required)
			if tt.want && err != nil {
				t.Errorf("Require(%q) = %v, want nil", tt.required, err)
			}
			if !tt.want && !errors.Is(err, ErrForbidden) {
				t.Errorf("Require(%q) = %v, want ErrForbidden", tt.required, err)
			}
		})
	}
}

func TestSubjectRoundTripsThroughContext(t *testing.T) {
	want := Subject{ID: "alice", Authenticated: true, Roles: []Role{RoleViewer}}
	got := SubjectFromContext(WithSubject(context.Background(), want))
	if got.ID != want.ID || !got.Authenticated || len(got.Roles) != 1 {
		t.Errorf("SubjectFromContext() = %+v, want %+v", got, want)
	}
}

func TestSubjectFromEmptyContextAllowsEverything(t *testing.T) {
	// The zero subject is the unauthenticated local mode, which config
	// validation restricts to a loopback bind.
	s := SubjectFromContext(context.Background())
	if s.Authenticated {
		t.Error("a context with no subject reported an authenticated caller")
	}
	if !s.Allows(RoleDiagnostician) {
		t.Error("the zero subject denied a role; unauthenticated local use must not be blocked")
	}
}

// staticVerifier accepts one token and returns fixed claims, so the middleware
// can be exercised without an identity provider.
func staticVerifier(token string, info *mcpauth.TokenInfo) Verifier {
	return func(_ context.Context, presented string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		if presented != token {
			return nil, mcpauth.ErrInvalidToken
		}
		return info, nil
	}
}

func TestMiddlewareRejectsAndAcceptsTokens(t *testing.T) {
	cfg := testAuthConfig()
	valid := &mcpauth.TokenInfo{
		Scopes:     []string{cfg.ScopeRead, cfg.ScopeDiagnose},
		Expiration: time.Now().Add(time.Hour),
		UserID:     "alice",
	}

	tests := []struct {
		name       string
		header     string
		wantStatus int
		wantRoles  int
	}{
		{name: "no authorization header", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic abc", wantStatus: http.StatusUnauthorized},
		{name: "unknown token", header: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "valid token", header: "Bearer good", wantStatus: http.StatusOK, wantRoles: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen Subject
			handler := NewWithVerifier(cfg, staticVerifier("good", valid))(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seen = SubjectFromContext(r.Context())
					w.WriteHeader(http.StatusOK)
				}))

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusUnauthorized {
				if rec.Header().Get("WWW-Authenticate") == "" {
					t.Error("no WWW-Authenticate header on a 401; a client cannot discover " +
						"the authorization server without it")
				}
				return
			}
			if !seen.Authenticated || seen.ID != "alice" {
				t.Errorf("subject = %+v, want an authenticated alice", seen)
			}
			if len(seen.Roles) != tt.wantRoles {
				t.Errorf("roles = %v, want %d", seen.Roles, tt.wantRoles)
			}
		})
	}
}

func TestMiddlewareRejectsAnExpiredToken(t *testing.T) {
	cfg := testAuthConfig()
	expired := &mcpauth.TokenInfo{
		Scopes:     []string{cfg.ScopeRead},
		Expiration: time.Now().Add(-time.Hour),
		UserID:     "alice",
	}
	handler := NewWithVerifier(cfg, staticVerifier("good", expired))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an expired token", rec.Code)
	}
}

func TestMiddlewareRequiresTheReadScope(t *testing.T) {
	cfg := testAuthConfig()
	noScopes := &mcpauth.TokenInfo{Expiration: time.Now().Add(time.Hour), UserID: "mallory"}
	handler := NewWithVerifier(cfg, staticVerifier("good", noScopes))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("a token with no scopes reached the handler; the read scope is required at the transport layer")
	}
}

func TestDisabledAuthAttachesAnUnauthenticatedSubject(t *testing.T) {
	cfg := config.Default().Auth
	cfg.Enabled = false

	middleware, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var seen Subject
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = SubjectFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when auth is disabled", rec.Code)
	}
	if seen.Authenticated {
		t.Error("the subject claims to be authenticated with auth disabled")
	}
}

func TestMergeScopesAcceptsBothClaimShapes(t *testing.T) {
	got := mergeScopes("skupper:read openid", []string{"skupper:diagnose", "skupper:read"})
	want := []string{"skupper:read", "openid", "skupper:diagnose"}
	if len(got) != len(want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("scopes = %v, want %v (order is first-seen, duplicates removed)", got, want)
		}
	}
}

func TestProtectedResourceMetadataAdvertisesBothScopes(t *testing.T) {
	cfg := testAuthConfig()
	rec := httptest.NewRecorder()
	ProtectedResourceMetadata(cfg, "https://skupper-mcp.example/mcp").
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{cfg.Issuer, cfg.ScopeRead, cfg.ScopeDiagnose} {
		if !strings.Contains(body, want) {
			t.Errorf("metadata is missing %q: %s", want, body)
		}
	}
}
