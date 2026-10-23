package api

import (
	"log/slog"
	"net/http"
	"time"
)

// Capabilities is the JSON body of GET /api/v1/capabilities. It must reflect
// what the kernel can actually do; advertise a feature only after its acceptance
// assertions pass.
type Capabilities struct {
	Runtimes         []string `json:"runtimes"`
	DesktopAvailable bool     `json:"desktop_available"`
	NetworkModes     []string `json:"network_modes"`
	Tunnel           bool     `json:"tunnel"`
	Artifacts        bool     `json:"artifacts"`
	OAuthProviders   []string `json:"oauth_providers"`
	MCPServers       []string `json:"mcp_servers"`
}

// DefaultCapabilities describes the P0 skeleton. Do not flip a boolean or add
// an item until the corresponding feature is implemented and covered by tests.
func DefaultCapabilities() Capabilities {
	return Capabilities{
		Runtimes:         []string{"native"},
		DesktopAvailable: false,
		NetworkModes:     []string{"services_only", "filtered", "open"},
		Tunnel:           false,
		Artifacts:        false,
		OAuthProviders:   []string{},
		MCPServers:       []string{},
	}
}

// Config configures the control-plane HTTP server.
type Config struct {
	// Token is the daemon bearer token. It is required for all non-public
	// endpoints and must never be logged.
	Token string
	// Version is reported by /health.
	Version string
	// StartedAt is the uptime origin. Defaults to now.
	StartedAt time.Time
	// Capabilities is reported by /capabilities.
	Capabilities Capabilities
}

// Server is the control-plane HTTP transport.
type Server struct {
	cfg     Config
	log     *slog.Logger
	started time.Time
	handler http.Handler
}

// NewServer builds the control-plane handler with middleware and all routes
// documented in api/openapi.yaml.
func NewServer(cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now()
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0"
	}
	cfg.Capabilities = normalizeCapabilities(cfg.Capabilities)

	s := &Server{
		cfg:     cfg,
		log:     logger,
		started: cfg.StartedAt,
	}
	s.handler = s.buildHandler()
	return s
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range routeTable {
		route := route
		mux.HandleFunc(route.Method+" "+route.Path, func(w http.ResponseWriter, r *http.Request) {
			route.Handler(s, w, r)
		})
	}
	// Any path under /api/v1 that is not in the OpenAPI route table gets a JSON
	// 404 instead of ServeMux's plain-text default.
	mux.HandleFunc("/api/v1/", s.handleNotFound)

	var h http.Handler = mux
	h = s.authenticate(h)
	h = s.cors(h)
	h = s.recoverPanic(h)
	h = s.accessLog(h)
	h = securityHeaders(h)
	h = requestID(h)
	return h
}

func normalizeCapabilities(c Capabilities) Capabilities {
	if c.Runtimes == nil {
		c.Runtimes = []string{}
	}
	if c.NetworkModes == nil {
		c.NetworkModes = []string{}
	}
	if c.OAuthProviders == nil {
		c.OAuthProviders = []string{}
	}
	if c.MCPServers == nil {
		c.MCPServers = []string{}
	}
	return c
}

type healthResponse struct {
	OK       bool   `json:"ok"`
	Version  string `json:"version"`
	UptimeMS int64  `json:"uptime_ms"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, healthResponse{
		OK:       true,
		Version:  s.cfg.Version,
		UptimeMS: time.Since(s.started).Milliseconds(),
	})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, s.cfg.Capabilities)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotFound, CodeNotFound, "route not found", map[string]any{
		"method": r.Method,
		"path":   r.URL.Path,
	})
}

func (s *Server) handleNotImplemented(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotImplemented, CodeNotImplemented, "endpoint is not implemented yet", map[string]any{
		"method": r.Method,
		"path":   r.URL.Path,
	})
}
