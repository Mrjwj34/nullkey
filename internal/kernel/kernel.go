package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mrjwj34/nullkey/internal/api"
	"github.com/Mrjwj34/nullkey/internal/artifacts"
	"github.com/Mrjwj34/nullkey/internal/audit"
	"github.com/Mrjwj34/nullkey/internal/core"
	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/egress"
	"github.com/Mrjwj34/nullkey/internal/events"
	"github.com/Mrjwj34/nullkey/internal/gateway"
	"github.com/Mrjwj34/nullkey/internal/gateway/mcp"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
	"github.com/Mrjwj34/nullkey/internal/secret"
	"github.com/Mrjwj34/nullkey/internal/store/sqlite"
	"github.com/Mrjwj34/nullkey/internal/terminal"
	"github.com/Mrjwj34/nullkey/internal/tlsutil"
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
	store          *sqlite.Store
	svc            *core.Service
	gatewayServer  *gateway.Server
	gatewayHTTP    *http.Server
	proxyHTTP      *http.Server
	tls            *tlsutil.Authority
}

// New validates configuration, opens the store and secrets, wires adapters and
// builds the three HTTP planes. It does not bind listeners.
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

	store, err := sqlite.Open(context.Background(), cfg.Home)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	secrets, err := secret.Open(cfg.Home, logger)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("open secret store: %w", err)
	}
	cachedSecrets := secret.NewCached(secrets, 5*time.Minute)
	authority, err := tlsutil.LoadOrCreate(filepath.Join(cfg.Home, "gateway-tls"))
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("load gateway TLS: %w", err)
	}
	runtimeCfg := native.Config{
		Home:          cfg.Home,
		HostHome:      cfg.HostHome,
		GatewayAddr:   cfg.GatewayListen,
		GatewayTLSURL: "https://" + cfg.GatewayListen,
		ProxyAddr:     cfg.ProxyListen,
		Logger:        logger,
	}
	nativeRuntime, err := native.New(runtimeCfg)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("native runtime: %w", err)
	}
	bus := events.New(256)
	auditSink := audit.New(store, logger)
	terminalHub := terminal.NewHub(logger)
	mcpProxy := mcp.New(&http.Client{Timeout: 30 * time.Second}, logger)

	svc, err := core.New(core.Options{
		Home:           cfg.Home,
		Store:          store,
		Secrets:        cachedSecrets,
		Runtime:        nativeRuntime,
		Terminal:       terminalHub,
		Events:         bus,
		Audit:          auditSink,
		Artifacts:      artifacts.New(),
		MCP:            &mcpAdapter{proxy: mcpProxy},
		Logger:         logger,
		GatewayAddr:    cfg.GatewayListen,
		GatewayTLSURL:  "https://" + cfg.GatewayListen,
		GatewayCAPath:  authority.CAPath,
		ProxyAddr:      cfg.ProxyListen,
		ControlAddr:    cfg.Listen,
		HostImportRoot: os.Getenv("NULLKEY_HOST_IMPORT_ROOT"),
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("core: %w", err)
	}
	if err := svc.Reconcile(context.Background()); err != nil {
		store.Close()
		return nil, fmt.Errorf("reconcile: %w", err)
	}

	egressProxy, err := egress.New(egress.Config{
		DefaultMode:   domainNetworkMode(os.Getenv("NULLKEY_DEFAULT_NETWORK_MODE")),
		Allowlist:     splitList(os.Getenv("NULLKEY_EGRESS_ALLOWLIST")),
		Resolver:      svc,
		Logger:        logger,
		AllowLoopback: os.Getenv("NULLKEY_EGRESS_ALLOW_LOOPBACK") == "1",
		OnDenied:      svc.RecordNetworkBlocked,
	})
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("egress proxy: %w", err)
	}

	server := api.NewServer(api.Config{
		Token:        token,
		Version:      version.Version,
		Capabilities: api.DefaultCapabilities(),
		Service:      svc,
	}, logger)
	gw := gateway.New(svc, logger)

	k := &Kernel{
		cfg:            cfg,
		log:            logger,
		api:            server,
		tokenGenerated: generated,
		tokenSource:    tokenSource,
		store:          store,
		svc:            svc,
		gatewayServer:  gw,
		tls:            authority,
		gatewayHTTP: &http.Server{
			Handler:           gw.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
		proxyHTTP: &http.Server{
			Handler:           egressProxy,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
	}
	return k, nil
}

// mcpAdapter maps the core MCP projection to the gateway/mcp package.
type mcpAdapter struct {
	proxy *mcp.Proxy
}

func (a *mcpAdapter) Test(ctx context.Context, conn core.MCPConn) ([]string, error) {
	return a.proxy.Test(ctx, mcp.Connection{
		ID: conn.ID, ServerID: conn.ServerID, BaseURL: conn.BaseURL, Credential: conn.Credential,
		CredentialHeader: conn.CredentialHeader, CredentialPrefix: conn.CredentialPrefix, AllowedTools: conn.AllowedTools,
	})
}

func (a *mcpAdapter) Proxy(ctx context.Context, conn core.MCPConn, w http.ResponseWriter, r *http.Request) error {
	err := a.proxy.Proxy(ctx, mcp.Connection{
		ID: conn.ID, ServerID: conn.ServerID, BaseURL: conn.BaseURL, Credential: conn.Credential,
		CredentialHeader: conn.CredentialHeader, CredentialPrefix: conn.CredentialPrefix, AllowedTools: conn.AllowedTools,
	}, w, r)
	if err == nil {
		return nil
	}
	// The MCP proxy writes its own error envelope before returning a typed
	// error. Do not write a second JSON body from the gateway layer.
	var mcpErr *mcp.Error
	if errors.As(err, &mcpErr) {
		return nil
	}
	return core.E(http.StatusBadGateway, "mcp_unavailable", "MCP server did not answer")
}

func domainNetworkMode(raw string) domain.NetworkMode {
	switch domain.NetworkMode(raw) {
	case domain.NetworkServicesOnly, domain.NetworkFiltered, domain.NetworkOpen:
		return domain.NetworkMode(raw)
	default:
		return domain.NetworkFiltered
	}
}

// Handler exposes the control-plane handler (used by tests and embedders).
func (k *Kernel) Handler() http.Handler { return k.api.Handler() }

// GatewayHandler exposes the data-plane handler.
func (k *Kernel) GatewayHandler() http.Handler { return k.gatewayServer.Handler() }

// Config returns a copy of the validated configuration.
func (k *Kernel) Config() Config { return k.cfg }

// Run binds all three loopback listeners and serves until ctx is canceled.
func (k *Kernel) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Warm the secret cache in the background. The first Security.framework
	// read can take several seconds; concurrent Codex bootstrap requests should
	// wait for one read, not each start their own.
	go func() {
		warmCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		k.svc.WarmCredentials(warmCtx)
	}()
	controlListener, err := net.Listen("tcp", k.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen control on %s: %w", k.cfg.Listen, err)
	}
	gatewayListener, err := net.Listen("tcp", k.cfg.GatewayListen)
	if err != nil {
		controlListener.Close()
		return fmt.Errorf("listen gateway on %s: %w", k.cfg.GatewayListen, err)
	}
	proxyListener, err := net.Listen("tcp", k.cfg.ProxyListen)
	if err != nil {
		controlListener.Close()
		gatewayListener.Close()
		return fmt.Errorf("listen proxy on %s: %w", k.cfg.ProxyListen, err)
	}

	server := &http.Server{
		Handler:           k.api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	k.httpServer = server

	errCh := make(chan error, 3)
	go func() { errCh <- server.Serve(controlListener) }()
	go func() { errCh <- k.gatewayHTTP.Serve(k.tls.MuxListener(gatewayListener)) }()
	go func() { errCh <- k.proxyHTTP.Serve(proxyListener) }()

	k.log.Info("nullkeyd_started",
		"addr", controlListener.Addr().String(),
		"gateway_addr", gatewayListener.Addr().String(),
		"proxy_addr", proxyListener.Addr().String(),
		"home", k.cfg.Home,
		"token_source", k.tokenSource,
		"token_generated", k.tokenGenerated,
		"version", version.String(),
	)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		k.svc.Shutdown(shutdownCtx)
		_ = server.Shutdown(shutdownCtx)
		_ = k.gatewayHTTP.Shutdown(shutdownCtx)
		_ = k.proxyHTTP.Shutdown(shutdownCtx)
		for i := 0; i < 3; i++ {
			select {
			case err := <-errCh:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					k.log.Warn("server_stopped", "error", err)
				}
			case <-time.After(time.Second):
			}
		}
		_ = k.store.Close()
		k.log.Info("nullkeyd_stopped")
		return nil
	case serveErr := <-errCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		k.svc.Shutdown(shutdownCtx)
		_ = server.Close()
		_ = k.gatewayHTTP.Close()
		_ = k.proxyHTTP.Close()
		_ = k.store.Close()
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
