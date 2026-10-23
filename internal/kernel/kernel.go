// Package kernel is the composition root for nullkeyd: it loads configuration,
// provisions the daemon token, wires the control-plane API, and owns listener
// lifecycle. Feature adapters are added here as milestones land.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Mrjwj34/nullkey/internal/api"
	"github.com/Mrjwj34/nullkey/internal/version"
)

// Kernel is a configured nullkeyd process.
type Kernel struct {
	cfg            Config
	log            *slog.Logger
	api            *api.Server
	tokenGenerated bool
	tokenSource    string
	httpServer     *http.Server
}

// New validates configuration, creates the private home directory, provisions
// the daemon token, and builds the HTTP handler. It does not bind a listener.
func New(cfg Config, logger *slog.Logger) (*Kernel, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create home %s: %w", cfg.Home, err)
	}

	token, generated, err := cfg.LoadDaemonToken()
	if err != nil {
		return nil, fmt.Errorf("load daemon token: %w", err)
	}

	tokenSource := cfg.tokenPath()
	if cfg.DaemonToken != "" {
		tokenSource = "environment"
	}

	server := api.NewServer(api.Config{
		Token:        token,
		Version:      version.Version,
		Capabilities: api.DefaultCapabilities(),
	}, logger)

	return &Kernel{
		cfg:            cfg,
		log:            logger,
		api:            server,
		tokenGenerated: generated,
		tokenSource:    tokenSource,
	}, nil
}

// Handler exposes the control-plane handler (used by tests and embedders).
func (k *Kernel) Handler() http.Handler {
	return k.api.Handler()
}

// Config returns a copy of the validated configuration.
func (k *Kernel) Config() Config {
	return k.cfg
}

// Run binds the control-plane listener and serves until ctx is canceled or the
// server fails. It always binds the validated loopback address.
func (k *Kernel) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	listener, err := net.Listen("tcp", k.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", k.cfg.Listen, err)
	}

	server := &http.Server{
		Handler:           k.api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	k.httpServer = server

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	k.log.Info("nullkeyd_started",
		"addr", listener.Addr().String(),
		"home", k.cfg.Home,
		"token_source", k.tokenSource,
		"token_generated", k.tokenGenerated,
		"version", version.String(),
	)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		serveErr := <-errCh
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		k.log.Info("nullkeyd_stopped")
		return nil
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
