package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

// minimalEnv is a configuration that validates, so each test can change exactly
// the one thing it is about.
func minimalEnv(extra map[string]string) map[string]string {
	base := map[string]string{
		EnvPrefix + "ALLOWED_NAMESPACES": "west,east",
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(env(minimalEnv(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Transport != TransportHTTP {
		t.Errorf("Transport = %q, want %q", cfg.Transport, TransportHTTP)
	}
	if !cfg.ReadOnly {
		t.Error("ReadOnly = false, want true by default")
	}
	if cfg.MaxItems != DefaultMaxItems {
		t.Errorf("MaxItems = %d, want %d", cfg.MaxItems, DefaultMaxItems)
	}
	if cfg.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("RequestTimeout = %s, want %s", cfg.RequestTimeout, DefaultRequestTimeout)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want the loopback default %q", cfg.Addr, DefaultAddr)
	}
}

func TestLoadEnvironmentParsing(t *testing.T) {
	cfg, err := Load(env(minimalEnv(map[string]string{
		EnvPrefix + "MAX_ITEMS":        "42",
		EnvPrefix + "REQUEST_TIMEOUT":  "3s",
		EnvPrefix + "SESSION_TIMEOUT":  "5m",
		EnvPrefix + "ALLOWED_CONTEXTS": " west-cluster , east-cluster ",
		EnvPrefix + "K8S_QPS":          "12.5",
		EnvPrefix + "K8S_PAGE_SIZE":    "50",
		EnvPrefix + "LOG_FORMAT":       "text",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxItems != 42 {
		t.Errorf("MaxItems = %d, want 42", cfg.MaxItems)
	}
	if cfg.RequestTimeout != 3*time.Second {
		t.Errorf("RequestTimeout = %s, want 3s", cfg.RequestTimeout)
	}
	if cfg.SessionTimeout != 5*time.Minute {
		t.Errorf("SessionTimeout = %s, want 5m", cfg.SessionTimeout)
	}
	if got, want := len(cfg.Kubernetes.AllowedContexts), 2; got != want {
		t.Fatalf("len(AllowedContexts) = %d, want %d", got, want)
	}
	if cfg.Kubernetes.AllowedContexts[0] != "west-cluster" {
		t.Errorf("AllowedContexts[0] = %q, want whitespace to be trimmed",
			cfg.Kubernetes.AllowedContexts[0])
	}
	if cfg.Kubernetes.QPS != 12.5 {
		t.Errorf("QPS = %v, want 12.5", cfg.Kubernetes.QPS)
	}
	if cfg.Kubernetes.PageSize != 50 {
		t.Errorf("PageSize = %d, want 50", cfg.Kubernetes.PageSize)
	}
}

func TestLoadReportsUnparsableValues(t *testing.T) {
	_, err := Load(env(minimalEnv(map[string]string{EnvPrefix + "REQUEST_TIMEOUT": "ten seconds"})))
	if err == nil {
		t.Fatal("Load accepted an unparsable duration")
	}
	if !strings.Contains(err.Error(), "Go duration") {
		t.Errorf("error = %q, want it to say what a valid value looks like", err)
	}
}

func TestValidateRejections(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantText string
	}{
		{
			name:     "no allowed namespaces",
			env:      map[string]string{},
			wantText: "never lists across all namespaces",
		},
		{
			name:     "read only turned off",
			env:      minimalEnv(map[string]string{EnvPrefix + "READ_ONLY": "false"}),
			wantText: "implements no write path",
		},
		{
			name: "non-loopback bind without auth",
			env: minimalEnv(map[string]string{
				EnvPrefix + "ADDR": "0.0.0.0:8080",
			}),
			wantText: "must not be reachable off-host",
		},
		{
			name:     "max items above the ceiling",
			env:      minimalEnv(map[string]string{EnvPrefix + "MAX_ITEMS": "999999"}),
			wantText: "above the ceiling",
		},
		{
			name: "auth without an issuer",
			env: minimalEnv(map[string]string{
				EnvPrefix + "AUTH_ENABLED":  "true",
				EnvPrefix + "AUTH_AUDIENCE": "skupper-mcp",
			}),
			wantText: "AUTH_ISSUER is required",
		},
		{
			name: "auth with a plaintext issuer",
			env: minimalEnv(map[string]string{
				EnvPrefix + "AUTH_ENABLED":  "true",
				EnvPrefix + "AUTH_ISSUER":   "http://keycloak.example/realms/skupper",
				EnvPrefix + "AUTH_AUDIENCE": "skupper-mcp",
			}),
			wantText: "must be an https URL",
		},
		{
			name: "auth without an audience",
			env: minimalEnv(map[string]string{
				EnvPrefix + "AUTH_ENABLED": "true",
				EnvPrefix + "AUTH_ISSUER":  "https://keycloak.example/realms/skupper",
			}),
			wantText: "AUTH_AUDIENCE is required",
		},
		{
			name:     "unknown transport",
			env:      minimalEnv(map[string]string{EnvPrefix + "TRANSPORT": "grpc"}),
			wantText: `set SKUPPER_MCP_TRANSPORT to "http" or "stdio"`,
		},
		{
			name:     "MCP limits cannot be disabled",
			env:      minimalEnv(map[string]string{EnvPrefix + "LIMIT_PER_USER": "0"}),
			wantText: "MCP limits must be positive",
		},
		{
			name:     "limiter state must hold a user and session",
			env:      minimalEnv(map[string]string{EnvPrefix + "LIMIT_MAX_ENTRIES": "1"}),
			wantText: "maxEntries between 2 and 100000",
		},
		{
			name: "namespace qualified with an unlisted context",
			env: minimalEnv(map[string]string{
				EnvPrefix + "ALLOWED_CONTEXTS":   "west-cluster",
				EnvPrefix + "ALLOWED_NAMESPACES": "east-cluster/east",
			}),
			wantText: "which is not in allowedContexts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if err == nil {
				t.Fatalf("Load accepted a configuration that should be rejected")
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error does not wrap ErrInvalidConfig: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantText)
			}
		})
	}
}

func TestNonLoopbackBindIsAllowedWithAnExplicitAcknowledgement(t *testing.T) {
	// A Pod has to bind 0.0.0.0 to receive Service traffic. The flag makes that
	// a deliberate choice rather than a default.
	_, err := Load(env(minimalEnv(map[string]string{
		EnvPrefix + "ADDR":                         "0.0.0.0:8080",
		EnvPrefix + "ALLOW_UNAUTHENTICATED_REMOTE": "true",
	})))
	if err != nil {
		t.Fatalf("Load rejected an acknowledged non-loopback bind: %v", err)
	}
}

func TestUnauthenticatedRemoteBindFailsClosedByDefault(t *testing.T) {
	_, err := Load(env(minimalEnv(map[string]string{EnvPrefix + "ADDR": "0.0.0.0:8080"})))
	if err == nil {
		t.Fatal("Load accepted an unauthenticated non-loopback bind with no acknowledgement")
	}
	if !strings.Contains(err.Error(), "ALLOW_UNAUTHENTICATED_REMOTE") {
		t.Errorf("error = %q, want it to name the flag that would permit this", err)
	}
}

func TestNonLoopbackBindIsAllowedWithAuth(t *testing.T) {
	_, err := Load(env(minimalEnv(map[string]string{
		EnvPrefix + "ADDR":                        "0.0.0.0:8080",
		EnvPrefix + "AUTH_ENABLED":                "true",
		EnvPrefix + "AUTH_ISSUER":                 "https://keycloak.example/realms/skupper",
		EnvPrefix + "AUTH_AUDIENCE":               "skupper-mcp",
		EnvPrefix + "AUTH_EXCHANGE_URL":           "https://keycloak.example/token",
		EnvPrefix + "AUTH_KUBERNETES_AUDIENCE":    "kubernetes",
		EnvPrefix + "AUTH_EXCHANGE_CLIENT_ID":     "skupper-exchange",
		EnvPrefix + "AUTH_EXCHANGE_CLIENT_SECRET": "test-only",
		EnvPrefix + "AUTH_RESOURCE_URL":           "https://skupper.example/mcp",
		EnvPrefix + "AUTH_RESOURCE_METADATA_URL":  "https://skupper.example/.well-known/oauth-protected-resource",
	})))
	if err != nil {
		t.Fatalf("Load rejected an authenticated non-loopback bind: %v", err)
	}
}

func TestNativeUserConfigurationFailsClosed(t *testing.T) {
	for _, tt := range []struct{ name, key, value, want string }{
		{"stdio cannot silently bypass auth", "TRANSPORT", "stdio", "requires the http transport"},
		{"exchange cannot use plaintext", "AUTH_EXCHANGE_URL", "http://issuer.example/token", "AUTH_EXCHANGE_URL"},
		{"exchange cannot reuse MCP audience", "AUTH_KUBERNETES_AUDIENCE", "mcp", "separate Kubernetes audience"},
		{"exchange requires runtime credential", "AUTH_EXCHANGE_CLIENT_SECRET", "", "env-only AUTH_EXCHANGE_CLIENT_SECRET"},
		{"roles cannot collapse", "AUTH_SCOPE_DIAGNOSE", "skupper:read", "scopes must differ"},
		{"public resource URL requires HTTPS", "AUTH_RESOURCE_URL", "http://mcp.example/mcp", "AUTH_RESOURCE_URL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values := minimalEnv(map[string]string{
				EnvPrefix + "AUTH_ENABLED": "true", EnvPrefix + "AUTH_ISSUER": "https://issuer.example", EnvPrefix + "AUTH_AUDIENCE": "mcp",
				EnvPrefix + "AUTH_RESOURCE_URL": "https://mcp.example/mcp", EnvPrefix + "AUTH_RESOURCE_METADATA_URL": "https://mcp.example/.well-known/oauth-protected-resource",
				EnvPrefix + "AUTH_EXCHANGE_URL": "https://issuer.example/token", EnvPrefix + "AUTH_KUBERNETES_AUDIENCE": "kubernetes",
				EnvPrefix + "AUTH_EXCHANGE_CLIENT_ID": "exchange-client", EnvPrefix + "AUTH_EXCHANGE_CLIENT_SECRET": "test-only",
			})
			if _, err := Load(env(values)); err != nil {
				t.Fatalf("valid native-user configuration rejected: %v", err)
			}
			values[EnvPrefix+tt.key] = tt.value
			if _, err := Load(env(values)); !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("unsafe configuration = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		EnvPrefix + "MAX_ITEMS": "-1",
		EnvPrefix + "LOG_LEVEL": "verbose",
	}))
	if err == nil {
		t.Fatal("Load accepted a configuration with several problems")
	}
	for _, want := range []string{"maxItems", "log.level", "ALLOWED_NAMESPACES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing the problem %q; validation must not stop at the first one:\n%v", want, err)
		}
	}
}

func TestFileIsOverriddenByEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "maxItems: 10\nrequestTimeout: 1s\nkubernetes:\n  allowedNamespaces: [west]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(env(map[string]string{
		EnvPrefix + "CONFIG_FILE": path,
		EnvPrefix + "MAX_ITEMS":   "77",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxItems != 77 {
		t.Errorf("MaxItems = %d, want the environment value 77 to beat the file's 10", cfg.MaxItems)
	}
	if cfg.RequestTimeout != time.Second {
		t.Errorf("RequestTimeout = %s, want the file value 1s to beat the built-in default", cfg.RequestTimeout)
	}
	if got := cfg.Kubernetes.AllowedNamespaces; len(got) != 1 || got[0] != "west" {
		t.Errorf("AllowedNamespaces = %v, want the file's [west]", got)
	}
}

func TestFileRejectsCredentials(t *testing.T) {
	for _, key := range []string{"token", "password", "clientSecret", "kubeconfig", "bearerToken"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(key+": something\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(env(map[string]string{EnvPrefix + "CONFIG_FILE": path}))
			if err == nil {
				t.Fatalf("Load accepted a config file carrying %q", key)
			}
			if !strings.Contains(err.Error(), "must not carry credentials") {
				t.Errorf("error = %q, want it to explain why the key is refused", err)
			}
		})
	}
}

func TestFileRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("maxItem: 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(env(map[string]string{EnvPrefix + "CONFIG_FILE": path}))
	if err == nil {
		t.Fatal("Load accepted a config file with a misspelled key; a typo must not be silently ignored")
	}
}
