// Package kubernetes implements the application's repository against the
// Kubernetes API, using a dynamic client over the Skupper v2 CRDs.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
)

// ClientFactory produces one dynamic client per allowed Kubernetes context.
//
// Contexts are resolved from the ambient kubeconfig, or from the pod's service
// account when running in cluster. No caller, and in particular no MCP client,
// can supply a kubeconfig path, a server URL, or a credential: the only thing a
// tool argument can do is name a context that the operator already allowed.
type ClientFactory struct {
	cfg config.Kubernetes

	mu      sync.Mutex
	clients map[string]dynamic.Interface
	// newClient is swappable so tests can inject a fake dynamic client without
	// a cluster or a kubeconfig.
	newClient func(contextName string) (dynamic.Interface, error)
}

// UserClient keeps the operator-selected API address and CA, but removes all
// ambient authentication, exec plugins and impersonation before using the
// exchanged user's credential. Kubernetes itself enforces native RBAC.
func (f *ClientFactory) UserClient(ctx context.Context, contextName string) (dynamic.Interface, error) {
	token, err := auth.NativeToken(ctx)
	if err != nil {
		return nil, err
	}
	base, err := f.restConfig(contextName)
	if err != nil {
		return nil, fmt.Errorf("resolving user cluster configuration: %w", err)
	}
	if !strings.HasPrefix(base.Host, "https://") || base.Insecure {
		return nil, fmt.Errorf("%w: native-user Kubernetes connections require verified HTTPS", auth.ErrForbidden)
	}
	cfg := rest.AnonymousClientConfig(base)
	cfg.BearerToken = token
	cfg.QPS, cfg.Burst = f.cfg.QPS, f.cfg.Burst
	cfg.UserAgent = "skupper-mcp-server"
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("building native-user HTTP transport: %w", err)
	}
	// A source must not redirect a delegated bearer credential to an endpoint
	// outside the operator-approved API address, including a plaintext URL.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err := dynamic.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("building native-user client: %w", err)
	}
	return client, nil
}

// NewClientFactory validates the configuration and returns a factory.
func NewClientFactory(cfg config.Kubernetes) (*ClientFactory, error) {
	if cfg.QPS <= 0 || cfg.Burst <= 0 {
		return nil, errors.New("kubernetes: qps and burst must be positive")
	}
	f := &ClientFactory{cfg: cfg, clients: map[string]dynamic.Interface{}}
	f.newClient = f.build
	return f, nil
}

// NewClientFactoryWithClients returns a factory backed by pre-built clients.
// It exists for tests and for any future embedding that supplies its own
// transport; production code uses NewClientFactory.
func NewClientFactoryWithClients(cfg config.Kubernetes, clients map[string]dynamic.Interface) *ClientFactory {
	copied := make(map[string]dynamic.Interface, len(clients))
	for k, v := range clients {
		copied[k] = v
	}
	return &ClientFactory{
		cfg:     cfg,
		clients: copied,
		newClient: func(contextName string) (dynamic.Interface, error) {
			return nil, fmt.Errorf("kubernetes: no client configured for context %q", contextName)
		},
	}
}

// Client returns the dynamic client for a context, building it on first use.
func (f *ClientFactory) Client(contextName string) (dynamic.Interface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[contextName]; ok {
		return c, nil
	}
	c, err := f.newClient(contextName)
	if err != nil {
		return nil, err
	}
	f.clients[contextName] = c
	return c, nil
}

// Preflight builds a client for every allowed context at startup, so a broken
// kubeconfig fails the process rather than the first tool call.
func (f *ClientFactory) Preflight(contexts []string) error {
	var errs []error
	for _, ctxName := range contexts {
		if _, err := f.Client(ctxName); err != nil {
			errs = append(errs, fmt.Errorf("context %q: %w", ctxName, err))
		}
	}
	return errors.Join(errs...)
}

// PreflightUser validates trusted connection settings without constructing an
// ambient authenticated transport (which could invoke a kubeconfig exec plugin).
// It is configuration validation, not a live reachability probe.
func (f *ClientFactory) PreflightUser(contexts []string) error {
	for _, name := range contexts {
		cfg, err := f.restConfig(name)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(cfg.Host, "https://") || cfg.Insecure {
			return fmt.Errorf("kubernetes: native-user context %q requires verified HTTPS", name)
		}
	}
	return nil
}

func (f *ClientFactory) build(contextName string) (dynamic.Interface, error) {
	restCfg, err := f.restConfig(contextName)
	if err != nil {
		return nil, err
	}
	restCfg.QPS = f.cfg.QPS
	restCfg.Burst = f.cfg.Burst
	restCfg.UserAgent = "skupper-mcp-server"
	client, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client for context %q: %w", contextName, err)
	}
	return client, nil
}

func (f *ClientFactory) restConfig(contextName string) (*rest.Config, error) {
	if f.cfg.InCluster {
		// In cluster there is exactly one API server, reached through the pod's
		// service account. Every allowed context name maps onto it, which is why
		// currentContextAlias exists.
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster configuration: %w", err)
		}
		return cfg, nil
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" && contextName != f.cfg.CurrentContextAlias {
		overrides.CurrentContext = contextName
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving kubeconfig context %q: %w", contextName, err)
	}
	return cfg, nil
}
