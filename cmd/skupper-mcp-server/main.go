// Command skupper-mcp-server exposes a read-only view of a Skupper v2
// application network to MCP clients.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/application"
	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
	"github.com/darioajr/skupper-scope/internal/kubernetes"
	"github.com/darioajr/skupper-scope/internal/mcpserver"
	"github.com/darioajr/skupper-scope/internal/observability"
)

func main() {
	if err := run(); err != nil {
		// Configuration errors are multi-line and meant to be read, so they go
		// to stderr plainly rather than as a log record.
		fmt.Fprintf(os.Stderr, "skupper-mcp-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// SIGTERM is what Kubernetes sends; SIGINT is what a terminal sends. The
	// context they cancel is threaded through every transport, so shutdown is
	// cooperative rather than a process kill.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadFromOS()
	if err != nil {
		return err
	}

	// Logs go to stderr so stdout stays clean for the stdio transport, where it
	// carries the JSON-RPC stream.
	log, err := observability.NewLogger(cfg.Log, os.Stderr)
	if err != nil {
		return err
	}

	allowlist, err := cfg.Allowlist()
	if err != nil {
		return err
	}

	factory, err := kubernetes.NewClientFactory(cfg.Kubernetes)
	if err != nil {
		return err
	}
	// Fail at startup rather than on the first tool call.
	preflight := factory.Preflight
	if cfg.Auth.Enabled {
		preflight = factory.PreflightUser
	}
	if err := preflight(allowlist.Contexts()); err != nil {
		return fmt.Errorf("kubernetes preflight: %w", err)
	}

	repo, err := kubernetes.NewRepository(factory, cfg)
	if err != nil {
		return err
	}
	svc, err := application.NewService(repo, allowlist, cfg.MaxItems)
	if err != nil {
		return err
	}
	server, err := mcpserver.New(svc, observability.NewAuditor(log), cfg)
	if err != nil {
		return err
	}

	if cfg.Transport == config.TransportHTTP && !cfg.Auth.Enabled && cfg.AllowUnauthenticatedRemote {
		log.Warn("serving MCP with no authentication on a non-loopback address; "+
			"anything that can reach this port can read every allowed Skupper namespace. "+
			"Confirm a NetworkPolicy or equivalent bounds who can reach it",
			"addr", cfg.Addr)
	}

	log.Info("starting",
		"version", mcpserver.Version,
		"transport", string(cfg.Transport),
		"read_only", cfg.ReadOnly,
		"auth_enabled", cfg.Auth.Enabled,
		"contexts", allowlist.Contexts(),
		"max_items", cfg.MaxItems,
	)

	switch cfg.Transport {
	case config.TransportStdio:
		if err := server.MCP().Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil

	case config.TransportHTTP:
		discoveryCtx, cancel := context.WithTimeout(ctx, auth.DiscoveryTimeout)
		defer cancel()
		middleware, err := auth.New(discoveryCtx, cfg.Auth)
		if err != nil {
			return err
		}
		ready := func() error {
			// Readiness checks connection configuration, not cluster reachability:
			// user-specific credentials exist only during authenticated calls.
			return preflight(allowlist.Contexts())
		}
		httpSrv, err := mcpserver.NewHTTPServer(cfg, server, middleware, ready, log)
		if err != nil {
			return err
		}
		if err := httpSrv.Start(); err != nil {
			return err
		}
		return httpSrv.Serve(ctx, cfg.ShutdownGrace)

	default:
		return fmt.Errorf("unsupported transport %q", cfg.Transport)
	}
}
