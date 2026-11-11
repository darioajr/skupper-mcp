package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Validate reports every problem it finds, not only the first, and phrases each
// one as the action the operator should take.
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch c.Transport {
	case TransportHTTP, TransportStdio:
	default:
		add("transport is %q; set %sTRANSPORT to \"http\" or \"stdio\"", c.Transport, EnvPrefix)
	}

	if !c.ReadOnly {
		add("%sREAD_ONLY is false; this server implements no write path, so a false value "+
			"would be a promise it cannot keep. Remove the override or set it to true", EnvPrefix)
	}

	if c.MaxItems <= 0 {
		add("maxItems is %d; set %sMAX_ITEMS to a positive number (default %d)", c.MaxItems, EnvPrefix, DefaultMaxItems)
	} else if c.MaxItems > MaxItemsCeiling {
		add("maxItems is %d, above the ceiling of %d; a higher value would let one tool call "+
			"scan an unbounded number of resources", c.MaxItems, MaxItemsCeiling)
	}

	if c.RequestTimeout <= 0 {
		add("requestTimeout is %s; set %sREQUEST_TIMEOUT to a positive Go duration such as 10s", c.RequestTimeout, EnvPrefix)
	}
	if c.SessionTimeout < 0 {
		add("sessionTimeout is negative; use 0 to disable idle session expiry")
	}
	if c.ShutdownGrace <= 0 {
		add("shutdownGrace is %s; set %sSHUTDOWN_GRACE to a positive Go duration", c.ShutdownGrace, EnvPrefix)
	}

	if c.Transport == TransportHTTP {
		if !strings.HasPrefix(c.MCPPath, "/") {
			add("mcpPath is %q; it must begin with \"/\"", c.MCPPath)
		}
		host, port, err := net.SplitHostPort(c.Addr)
		switch {
		case err != nil:
			add("addr is %q; use host:port, for example %s", c.Addr, DefaultAddr)
		case port == "":
			add("addr %q has no port", c.Addr)
		case !c.Auth.Enabled && !isLoopbackHost(host) && !c.AllowUnauthenticatedRemote:
			add("addr %q is not loopback and %sAUTH_ENABLED is false; an unauthenticated "+
				"Skupper reader must not be reachable off-host. Enable auth, bind to 127.0.0.1, or — "+
				"if something else bounds who can reach this port, such as a NetworkPolicy in front of "+
				"a Pod that has to bind 0.0.0.0 — set %sALLOW_UNAUTHENTICATED_REMOTE=true to say so "+
				"deliberately", c.Addr, EnvPrefix, EnvPrefix)
		}
	}

	if c.Auth.Enabled {
		for name, value := range map[string]string{"AUTH_RESOURCE_URL": c.Auth.ResourceURL, "AUTH_RESOURCE_METADATA_URL": c.Auth.ResourceMetadataURL} {
			u, err := url.Parse(value)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				add("%s%s must be the public https URL without credentials, query or fragment", EnvPrefix, name)
			}
		}
		if c.Transport != TransportHTTP {
			add("authenticated native-user access requires the http transport; stdio is local ambient-credential mode")
		}
		u, err := url.Parse(c.Auth.ExchangeURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			add("%sAUTH_EXCHANGE_URL must be an https token endpoint without credentials, query or fragment", EnvPrefix)
		}
		if c.Auth.KubernetesAudience == "" || c.Auth.KubernetesAudience == c.Auth.Audience {
			add("%sAUTH_KUBERNETES_AUDIENCE must name a separate Kubernetes audience", EnvPrefix)
		}
		if c.Auth.ExchangeClientID == "" || c.Auth.ExchangeClientSecret == "" {
			add("AUTH_EXCHANGE_CLIENT_ID and env-only AUTH_EXCHANGE_CLIENT_SECRET are required for native-user token exchange")
		}
		if c.Auth.Issuer == "" {
			add("%sAUTH_ISSUER is required when auth is enabled", EnvPrefix)
		} else if u, err := url.Parse(c.Auth.Issuer); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			add("%sAUTH_ISSUER must be an https URL without credentials, query or fragment", EnvPrefix)
		}
		if c.Auth.Audience == "" {
			add("%sAUTH_AUDIENCE is required when auth is enabled; it must name this "+
				"resource server so tokens minted for another audience are rejected", EnvPrefix)
		}
		if c.Auth.ScopeRead == "" {
			add("%sAUTH_SCOPE_READ must not be empty", EnvPrefix)
		}
		if c.Auth.ScopeDiagnose == "" {
			add("%sAUTH_SCOPE_DIAGNOSE must not be empty", EnvPrefix)
		}
		if c.Auth.ScopeRead == c.Auth.ScopeDiagnose {
			add("read and diagnose scopes must differ to preserve the role boundary")
		}
		if c.Auth.ClockSkew < 0 {
			add("auth.clockSkew must not be negative")
		}
	}
	if c.Limits.PerUser <= 0 || c.Limits.PerSession <= 0 || c.Limits.Window <= 0 ||
		c.Limits.BreakerThreshold <= 0 || c.Limits.Cooldown <= 0 || c.Limits.MaxEntries < 2 ||
		c.Limits.MaxEntries > 100000 || strings.TrimSpace(c.Limits.AlertOwner) == "" {
		add("MCP limits must be positive, maxEntries between 2 and 100000, and alertOwner must identify the alert recipient")
	}

	if len(c.Kubernetes.AllowedNamespaces) == 0 {
		add("%sALLOWED_NAMESPACES is empty; this server never lists across all namespaces, "+
			"so at least one namespace must be allowed (for example \"west,east\" or \"prod/payments\")", EnvPrefix)
	}
	if c.Kubernetes.CurrentContextAlias == "" {
		add("kubernetes.currentContextAlias must not be empty")
	}
	if c.Kubernetes.QPS <= 0 {
		add("kubernetes.qps must be positive")
	}
	if c.Kubernetes.Burst <= 0 {
		add("kubernetes.burst must be positive")
	}
	if c.Kubernetes.PageSize <= 0 {
		add("kubernetes.pageSize must be positive")
	}

	if _, err := c.Allowlist(); err != nil {
		problems = append(problems, strings.TrimPrefix(err.Error(), ErrInvalidConfig.Error()+": "))
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level is %q; use debug, info, warn or error", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format is %q; use json or text", c.Log.Format)
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  - %s", ErrInvalidConfig, strings.Join(problems, "\n  - "))
}

func isLoopbackHost(host string) bool {
	if host == "" {
		// An empty host in host:port means every interface.
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
