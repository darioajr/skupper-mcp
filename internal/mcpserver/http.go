package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/darioajr/skupper-scope/internal/auth"
	"github.com/darioajr/skupper-scope/internal/config"
)

// HTTPServer serves MCP over Streamable HTTP alongside health probes.
type HTTPServer struct {
	http *http.Server
	log  *slog.Logger
	addr string
	// ready is closed once the listener is bound, so tests and readiness
	// probes agree on when the server is actually accepting connections.
	listener net.Listener
}

// Ready is the readiness state a probe reads.
type Ready func() error

// NewHTTPServer builds the HTTP server. The MCP handler sits behind the auth
// middleware; the probe endpoints deliberately do not, so a liveness probe
// never needs a credential.
func NewHTTPServer(cfg config.Config, srv *Server, authMW auth.Middleware, ready Ready, log *slog.Logger) (*HTTPServer, error) {
	if srv == nil {
		return nil, errors.New("mcpserver: server is required")
	}
	if authMW == nil {
		return nil, errors.New("mcpserver: auth middleware is required")
	}
	if log == nil {
		return nil, errors.New("mcpserver: logger is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Transport != config.TransportHTTP {
		return nil, errors.New("mcpserver: HTTP configuration is required")
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("mcpserver: request timeout must be positive")
	}
	limiter, err := newInvocationLimiter(cfg.Limits)
	if err != nil {
		return nil, err
	}
	// HTTP configuration is applied before serving; SDK request metadata must
	// be revalidated on every call, including after a token refresh.
	srv.auth, srv.limiter = cfg.Auth, limiter
	srv.requestTimeout = cfg.RequestTimeout

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv.MCP() },
		&mcp.StreamableHTTPOptions{
			SessionTimeout: cfg.SessionTimeout,
			Logger:         log,
		},
	)

	// Cross-origin protection is applied by wrapping the handler, as the SDK
	// now recommends, rather than through the deprecated option field. It stops
	// a browser page on another origin from driving this server through the
	// user's session.
	protection := http.NewCrossOriginProtection()
	mcpHandler := protection.Handler(handler)
	mcpHandler = srv.auditTransport(authMW(captureTransportIdentity(mcpHandler)))

	mux := http.NewServeMux()
	mux.Handle(cfg.MCPPath, mcpHandler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready != nil {
			if err := ready(); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				// The reason is logged, not returned: a probe response is an
				// unauthenticated surface.
				log.Warn("readiness check failed", slog.String("error", err.Error()))
				_, _ = w.Write([]byte("not ready\n"))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})

	if cfg.Auth.Enabled {
		mux.Handle("GET /.well-known/oauth-protected-resource",
			auth.ProtectedResourceMetadata(cfg.Auth, resourceURL(cfg)))
	}

	return &HTTPServer{
		log:  log,
		addr: cfg.Addr,
		http: &http.Server{
			Addr:    cfg.Addr,
			Handler: mux,
			// ReadHeaderTimeout bounds slow-header attacks. There is no write or
			// idle timeout because a Streamable HTTP session holds a long-lived
			// SSE stream that either would sever.
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
	}, nil
}

func resourceURL(cfg config.Config) string {
	if cfg.Auth.ResourceURL != "" {
		return cfg.Auth.ResourceURL
	}
	return "http://" + cfg.Addr + cfg.MCPPath
}

// Addr returns the bound address, which differs from the configured one when
// port 0 was requested.
func (h *HTTPServer) Addr() string {
	if h.listener != nil {
		return h.listener.Addr().String()
	}
	return h.addr
}

// Start binds the listener without serving, so a bind failure is reported
// before the process claims to be running.
func (h *HTTPServer) Start() error {
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		return fmt.Errorf("binding %s: %w", h.addr, err)
	}
	h.listener = ln
	return nil
}

// Serve blocks until the context is cancelled or the server fails, then shuts
// down gracefully within grace.
func (h *HTTPServer) Serve(ctx context.Context, grace time.Duration) error {
	if h.listener == nil {
		if err := h.Start(); err != nil {
			return err
		}
	}
	errCh := make(chan error, 1)
	go func() {
		h.log.Info("serving MCP over Streamable HTTP", slog.String("addr", h.Addr()))
		err := h.http.Serve(h.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	h.log.Info("shutting down", slog.Duration("grace", grace))
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := h.http.Shutdown(shutdownCtx); err != nil {
		// Shutdown returning an error means in-flight requests outlived the
		// grace period; closing forces them down so the process can exit.
		h.log.Warn("graceful shutdown did not complete; closing remaining connections",
			slog.String("error", err.Error()))
		_ = h.http.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-errCh
}
