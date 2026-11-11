package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// fileConfig mirrors Config but takes durations as strings and every scalar as
// a pointer, so "absent in the file" is distinguishable from "set to zero" and
// a file may override only the keys it names.
type fileConfig struct {
	Transport *string `yaml:"transport"`
	Addr      *string `yaml:"addr"`
	MCPPath   *string `yaml:"mcpPath"`

	ReadOnly                   *bool   `yaml:"readOnly"`
	AllowUnauthenticatedRemote *bool   `yaml:"allowUnauthenticatedRemote"`
	MaxItems                   *int    `yaml:"maxItems"`
	RequestTimeout             *string `yaml:"requestTimeout"`
	SessionTimeout             *string `yaml:"sessionTimeout"`
	ShutdownGrace              *string `yaml:"shutdownGrace"`
	Limits                     *struct {
		PerUser          *int    `yaml:"perUser"`
		PerSession       *int    `yaml:"perSession"`
		Window           *string `yaml:"window"`
		BreakerThreshold *int    `yaml:"breakerThreshold"`
		Cooldown         *string `yaml:"cooldown"`
		MaxEntries       *int    `yaml:"maxEntries"`
		AlertOwner       *string `yaml:"alertOwner"`
	} `yaml:"limits"`

	Kubernetes *struct {
		InCluster           *bool    `yaml:"inCluster"`
		AllowedContexts     []string `yaml:"allowedContexts"`
		CurrentContextAlias *string  `yaml:"currentContextAlias"`
		AllowedNamespaces   []string `yaml:"allowedNamespaces"`
		QPS                 *float32 `yaml:"qps"`
		Burst               *int     `yaml:"burst"`
		PageSize            *int64   `yaml:"pageSize"`
	} `yaml:"kubernetes"`

	Auth *struct {
		Enabled             *bool   `yaml:"enabled"`
		Issuer              *string `yaml:"issuer"`
		Audience            *string `yaml:"audience"`
		ResourceMetadataURL *string `yaml:"resourceMetadataUrl"`
		ResourceURL         *string `yaml:"resourceUrl"`
		ScopeRead           *string `yaml:"scopeRead"`
		ScopeDiagnose       *string `yaml:"scopeDiagnose"`
		ClockSkew           *string `yaml:"clockSkew"`
		ExchangeURL         *string `yaml:"exchangeUrl"`
		KubernetesAudience  *string `yaml:"kubernetesAudience"`
		ExchangeClientID    *string `yaml:"exchangeClientId"`
	} `yaml:"auth"`

	Log *struct {
		Level  *string `yaml:"level"`
		Format *string `yaml:"format"`
	} `yaml:"log"`

	// Secret-shaped keys are rejected rather than ignored, so a user who puts a
	// credential in this file is told rather than silently having it dropped.
	Token        *string `yaml:"token"`
	Password     *string `yaml:"password"`
	ClientSecret *string `yaml:"clientSecret"`
	Kubeconfig   *string `yaml:"kubeconfig"`
	BearerToken  *string `yaml:"bearerToken"`
}

func applyFile(cfg *Config, path string) error {
	// The path comes from SKUPPER_MCP_CONFIG_FILE, which only the operator who
	// starts the process can set. No MCP client, and no value inside the file
	// itself, can redirect this read.
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, never client-supplied
	if err != nil {
		return fmt.Errorf("%w: reading %s: %w", ErrInvalidConfig, path, err)
	}
	var fc fileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return fmt.Errorf("%w: parsing %s: %w", ErrInvalidConfig, path, err)
	}

	for name, present := range map[string]bool{
		"token":        fc.Token != nil,
		"password":     fc.Password != nil,
		"clientSecret": fc.ClientSecret != nil,
		"kubeconfig":   fc.Kubeconfig != nil,
		"bearerToken":  fc.BearerToken != nil,
	} {
		if present {
			return fmt.Errorf("%w: %s sets %q; this file must not carry credentials or "+
				"kubeconfig paths, and no client-supplied value may select a cluster connection",
				ErrInvalidConfig, path, name)
		}
	}

	var errs []error
	dur := func(key string, src *string, dst *time.Duration) {
		if src == nil {
			return
		}
		d, err := time.ParseDuration(*src)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w (want a Go duration such as 10s)", path, key, err))
			return
		}
		*dst = d
	}

	if fc.Transport != nil {
		cfg.Transport = Transport(strings.ToLower(*fc.Transport))
	}
	assign(fc.Addr, &cfg.Addr)
	assign(fc.MCPPath, &cfg.MCPPath)
	assign(fc.ReadOnly, &cfg.ReadOnly)
	assign(fc.AllowUnauthenticatedRemote, &cfg.AllowUnauthenticatedRemote)
	assign(fc.MaxItems, &cfg.MaxItems)
	dur("requestTimeout", fc.RequestTimeout, &cfg.RequestTimeout)
	dur("sessionTimeout", fc.SessionTimeout, &cfg.SessionTimeout)
	dur("shutdownGrace", fc.ShutdownGrace, &cfg.ShutdownGrace)
	if l := fc.Limits; l != nil {
		assign(l.PerUser, &cfg.Limits.PerUser)
		assign(l.PerSession, &cfg.Limits.PerSession)
		dur("limits.window", l.Window, &cfg.Limits.Window)
		assign(l.BreakerThreshold, &cfg.Limits.BreakerThreshold)
		dur("limits.cooldown", l.Cooldown, &cfg.Limits.Cooldown)
		assign(l.MaxEntries, &cfg.Limits.MaxEntries)
		assign(l.AlertOwner, &cfg.Limits.AlertOwner)
	}

	if k := fc.Kubernetes; k != nil {
		assign(k.InCluster, &cfg.Kubernetes.InCluster)
		assign(k.CurrentContextAlias, &cfg.Kubernetes.CurrentContextAlias)
		assign(k.QPS, &cfg.Kubernetes.QPS)
		assign(k.Burst, &cfg.Kubernetes.Burst)
		assign(k.PageSize, &cfg.Kubernetes.PageSize)
		if k.AllowedContexts != nil {
			cfg.Kubernetes.AllowedContexts = k.AllowedContexts
		}
		if k.AllowedNamespaces != nil {
			cfg.Kubernetes.AllowedNamespaces = k.AllowedNamespaces
		}
	}
	if a := fc.Auth; a != nil {
		assign(a.Enabled, &cfg.Auth.Enabled)
		assign(a.Issuer, &cfg.Auth.Issuer)
		assign(a.Audience, &cfg.Auth.Audience)
		assign(a.ResourceMetadataURL, &cfg.Auth.ResourceMetadataURL)
		assign(a.ResourceURL, &cfg.Auth.ResourceURL)
		assign(a.ScopeRead, &cfg.Auth.ScopeRead)
		assign(a.ScopeDiagnose, &cfg.Auth.ScopeDiagnose)
		assign(a.ExchangeURL, &cfg.Auth.ExchangeURL)
		assign(a.KubernetesAudience, &cfg.Auth.KubernetesAudience)
		assign(a.ExchangeClientID, &cfg.Auth.ExchangeClientID)
		dur("auth.clockSkew", a.ClockSkew, &cfg.Auth.ClockSkew)
	}
	if l := fc.Log; l != nil {
		assign(l.Level, &cfg.Log.Level)
		assign(l.Format, &cfg.Log.Format)
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, errs[0])
	}
	return nil
}

func assign[T any](src *T, dst *T) {
	if src != nil {
		*dst = *src
	}
}
