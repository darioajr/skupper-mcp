// Package config loads and validates the server configuration.
//
// Precedence, highest first:
//
//  1. environment variables prefixed with SKUPPER_MCP_
//  2. an optional YAML file named by SKUPPER_MCP_CONFIG_FILE
//  3. built-in defaults
//
// The file may not contain credentials of any kind. Kubernetes credentials come
// from the ambient kubeconfig or the in-cluster service account, never from
// this configuration and never from an MCP client.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is the single prefix for every environment variable read here.
const EnvPrefix = "SKUPPER_MCP_"

// Transport selects the MCP transport.
type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportStdio Transport = "stdio"
)

// Defaults.
const (
	DefaultAddr           = "127.0.0.1:8080"
	DefaultMaxItems       = 500
	DefaultRequestTimeout = 10 * time.Second
	DefaultSessionTimeout = 30 * time.Minute
	DefaultMCPPath        = "/mcp"
	// MaxItemsCeiling bounds MaxItems so a misconfiguration cannot turn a
	// listing into an unbounded cluster scan.
	MaxItemsCeiling = 5000
)

// Config is the validated server configuration.
type Config struct {
	Transport Transport `yaml:"transport"`
	Addr      string    `yaml:"addr"`
	MCPPath   string    `yaml:"mcpPath"`

	ReadOnly bool `yaml:"readOnly"`
	// AllowUnauthenticatedRemote acknowledges binding a server with no
	// authentication to a non-loopback address. It exists because a Pod must
	// bind 0.0.0.0 to receive Service traffic, and in that setting the
	// NetworkPolicy and Service are the trust boundary rather than the loopback
	// interface. It defaults to false so the unsafe case fails closed and has to
	// be chosen deliberately.
	AllowUnauthenticatedRemote bool          `yaml:"allowUnauthenticatedRemote"`
	MaxItems                   int           `yaml:"maxItems"`
	RequestTimeout             time.Duration `yaml:"requestTimeout"`
	SessionTimeout             time.Duration `yaml:"sessionTimeout"`
	ShutdownGrace              time.Duration `yaml:"shutdownGrace"`

	Kubernetes Kubernetes `yaml:"kubernetes"`
	Auth       Auth       `yaml:"auth"`
	Log        Log        `yaml:"log"`
	Limits     Limits     `yaml:"limits"`
}

// Limits bound MCP invocations independently of Kubernetes client throttling.
type Limits struct {
	PerUser          int           `yaml:"perUser"`
	PerSession       int           `yaml:"perSession"`
	Window           time.Duration `yaml:"window"`
	BreakerThreshold int           `yaml:"breakerThreshold"`
	Cooldown         time.Duration `yaml:"cooldown"`
	MaxEntries       int           `yaml:"maxEntries"`
	AlertOwner       string        `yaml:"alertOwner"`
}

// Kubernetes holds the allowlist and client tuning. There is no kubeconfig
// path here on purpose: the path is resolved from the ambient environment so
// no configuration value, and therefore no MCP client, can redirect the server
// at an arbitrary file.
type Kubernetes struct {
	// InCluster forces use of the pod service account.
	InCluster bool `yaml:"inCluster"`
	// AllowedContexts lists the kubeconfig context names this server may use.
	// Empty means "the current context only", named by CurrentContextAlias.
	AllowedContexts []string `yaml:"allowedContexts"`
	// CurrentContextAlias is the logical name exposed to MCP clients for the
	// ambient context, used in-cluster where there is no kubeconfig context.
	CurrentContextAlias string `yaml:"currentContextAlias"`
	// AllowedNamespaces lists the namespaces readable in every context.
	// Entries may be qualified as "context/namespace" to scope them.
	// Empty is a configuration error: this server never reads cluster-wide.
	AllowedNamespaces []string `yaml:"allowedNamespaces"`
	// QPS and Burst bound the load placed on each API server.
	QPS   float32 `yaml:"qps"`
	Burst int     `yaml:"burst"`
	// PageSize is the chunk size for list calls.
	PageSize int64 `yaml:"pageSize"`
}

// Auth configures the remote authorization layer.
type Auth struct {
	// Enabled turns on OAuth 2.0 bearer token verification.
	Enabled bool `yaml:"enabled"`
	// Issuer is the OIDC issuer URL, for example a Keycloak realm.
	Issuer string `yaml:"issuer"`
	// Audience is the expected token audience, that is, this resource server.
	Audience string `yaml:"audience"`
	// ResourceMetadataURL is advertised in WWW-Authenticate per RFC 9728.
	ResourceMetadataURL string `yaml:"resourceMetadataUrl"`
	ResourceURL         string `yaml:"resourceUrl"`
	// ScopeRead and ScopeDiagnose map token scopes onto the two domain roles.
	ScopeRead     string `yaml:"scopeRead"`
	ScopeDiagnose string `yaml:"scopeDiagnose"`
	// ClockSkew tolerates drift between this server and the issuer.
	ClockSkew time.Duration `yaml:"clockSkew"`
	// Token exchange preserves native Kubernetes RBAC without forwarding the
	// MCP access token to a different resource server. The secret is env-only.
	ExchangeURL          string `yaml:"exchangeUrl"`
	KubernetesAudience   string `yaml:"kubernetesAudience"`
	ExchangeClientID     string `yaml:"exchangeClientId"`
	ExchangeClientSecret string `yaml:"-" json:"-"`
}

// Log configures structured logging.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns the built-in defaults, before file and environment overrides.
func Default() Config {
	return Config{
		Transport:      TransportHTTP,
		Addr:           DefaultAddr,
		MCPPath:        DefaultMCPPath,
		ReadOnly:       true,
		MaxItems:       DefaultMaxItems,
		RequestTimeout: DefaultRequestTimeout,
		SessionTimeout: DefaultSessionTimeout,
		ShutdownGrace:  15 * time.Second,
		Kubernetes: Kubernetes{
			CurrentContextAlias: "default",
			QPS:                 20,
			Burst:               30,
			PageSize:            200,
		},
		Auth: Auth{
			ScopeRead:     "skupper:read",
			ScopeDiagnose: "skupper:diagnose",
			ClockSkew:     30 * time.Second,
		},
		Log: Log{Level: "info", Format: "json"},
		Limits: Limits{PerUser: 60, PerSession: 30, Window: time.Minute,
			BreakerThreshold: 5, Cooldown: time.Minute, MaxEntries: 10000, AlertOwner: "local-operator"},
	}
}

// ErrInvalidConfig is the sentinel wrapped by every validation failure.
var ErrInvalidConfig = errors.New("invalid configuration")

// Load builds a Config from defaults, then the optional YAML file, then the
// environment, and validates the result.
func Load(getenv func(string) string) (Config, error) {
	cfg := Default()

	if path := getenv(EnvPrefix + "CONFIG_FILE"); path != "" {
		if err := applyFile(&cfg, path); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadFromOS is Load reading the process environment.
func LoadFromOS() (Config, error) { return Load(os.Getenv) }

func applyEnv(cfg *Config, getenv func(string) string) error {
	str := func(key string, dst *string) {
		if v := getenv(EnvPrefix + key); v != "" {
			*dst = v
		}
	}
	list := func(key string, dst *[]string) {
		if v, ok := lookup(getenv, key); ok {
			*dst = splitList(v)
		}
	}
	var errs []error
	boolean := func(key string, dst *bool) {
		v, ok := lookup(getenv, key)
		if !ok {
			return
		}
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s: %w", EnvPrefix, key, err))
			return
		}
		*dst = parsed
	}
	integer := func(key string, dst *int) {
		v, ok := lookup(getenv, key)
		if !ok {
			return
		}
		parsed, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s: %w", EnvPrefix, key, err))
			return
		}
		*dst = parsed
	}
	duration := func(key string, dst *time.Duration) {
		v, ok := lookup(getenv, key)
		if !ok {
			return
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s%s: %w (want a Go duration such as 10s)", EnvPrefix, key, err))
			return
		}
		*dst = parsed
	}

	if v := getenv(EnvPrefix + "TRANSPORT"); v != "" {
		cfg.Transport = Transport(strings.ToLower(v))
	}
	str("ADDR", &cfg.Addr)
	str("MCP_PATH", &cfg.MCPPath)
	boolean("READ_ONLY", &cfg.ReadOnly)
	boolean("ALLOW_UNAUTHENTICATED_REMOTE", &cfg.AllowUnauthenticatedRemote)
	integer("MAX_ITEMS", &cfg.MaxItems)
	duration("REQUEST_TIMEOUT", &cfg.RequestTimeout)
	duration("SESSION_TIMEOUT", &cfg.SessionTimeout)
	duration("SHUTDOWN_GRACE", &cfg.ShutdownGrace)

	boolean("IN_CLUSTER", &cfg.Kubernetes.InCluster)
	list("ALLOWED_CONTEXTS", &cfg.Kubernetes.AllowedContexts)
	list("ALLOWED_NAMESPACES", &cfg.Kubernetes.AllowedNamespaces)
	str("CURRENT_CONTEXT_ALIAS", &cfg.Kubernetes.CurrentContextAlias)
	integer("K8S_BURST", &cfg.Kubernetes.Burst)
	if v, ok := lookup(getenv, "K8S_QPS"); ok {
		parsed, err := strconv.ParseFloat(v, 32)
		if err != nil {
			errs = append(errs, fmt.Errorf("%sK8S_QPS: %w", EnvPrefix, err))
		} else {
			cfg.Kubernetes.QPS = float32(parsed)
		}
	}
	if v, ok := lookup(getenv, "K8S_PAGE_SIZE"); ok {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("%sK8S_PAGE_SIZE: %w", EnvPrefix, err))
		} else {
			cfg.Kubernetes.PageSize = parsed
		}
	}

	boolean("AUTH_ENABLED", &cfg.Auth.Enabled)
	str("AUTH_ISSUER", &cfg.Auth.Issuer)
	str("AUTH_AUDIENCE", &cfg.Auth.Audience)
	str("AUTH_RESOURCE_METADATA_URL", &cfg.Auth.ResourceMetadataURL)
	str("AUTH_RESOURCE_URL", &cfg.Auth.ResourceURL)
	str("AUTH_SCOPE_READ", &cfg.Auth.ScopeRead)
	str("AUTH_SCOPE_DIAGNOSE", &cfg.Auth.ScopeDiagnose)
	duration("AUTH_CLOCK_SKEW", &cfg.Auth.ClockSkew)
	str("AUTH_EXCHANGE_URL", &cfg.Auth.ExchangeURL)
	str("AUTH_KUBERNETES_AUDIENCE", &cfg.Auth.KubernetesAudience)
	str("AUTH_EXCHANGE_CLIENT_ID", &cfg.Auth.ExchangeClientID)
	str("AUTH_EXCHANGE_CLIENT_SECRET", &cfg.Auth.ExchangeClientSecret)
	integer("LIMIT_PER_USER", &cfg.Limits.PerUser)
	integer("LIMIT_PER_SESSION", &cfg.Limits.PerSession)
	duration("LIMIT_WINDOW", &cfg.Limits.Window)
	integer("LIMIT_BREAKER_THRESHOLD", &cfg.Limits.BreakerThreshold)
	duration("LIMIT_COOLDOWN", &cfg.Limits.Cooldown)
	integer("LIMIT_MAX_ENTRIES", &cfg.Limits.MaxEntries)
	str("LIMIT_ALERT_OWNER", &cfg.Limits.AlertOwner)

	str("LOG_LEVEL", &cfg.Log.Level)
	str("LOG_FORMAT", &cfg.Log.Format)

	return errors.Join(errs...)
}

func lookup(getenv func(string) string, key string) (string, bool) {
	v := getenv(EnvPrefix + key)
	return v, v != ""
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
