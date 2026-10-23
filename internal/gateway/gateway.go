// Package gateway serves the sandbox-facing data plane on a separate loopback
// listener. Sandbox tokens are the only accepted credential here; the daemon
// token never works on this plane.
package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Mrjwj34/nullkey/internal/core"
	"github.com/Mrjwj34/nullkey/internal/domain"
	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

// Server is the data-plane HTTP handler.
type Server struct {
	svc *core.Service
	log *slog.Logger
}

// New builds the gateway.
func New(svc *core.Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{svc: svc, log: logger}
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/terminal", s.svc.TerminalHandler())
	mux.Handle("/tunnel/", s.svc.TunnelHandler())
	mux.HandleFunc("/openai/", s.model("openai"))
	mux.HandleFunc("/anthropic/", s.model("anthropic"))
	mux.HandleFunc("/mcp/", s.mcp)
	mux.HandleFunc("/git/github/", s.git)
	mux.HandleFunc("/", s.notFound)
	return s.accessLog(mux)
}

func (s *Server) model(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sb, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		if err := s.svc.ModelGateway(r.Context(), sb, provider, w, r); err != nil {
			s.writeError(w, err)
		}
	}
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/mcp/")
	connectionID := strings.SplitN(rest, "/", 2)[0]
	if connectionID == "" {
		s.writeError(w, core.E(http.StatusNotFound, "not_found", "MCP connection not found"))
		return
	}
	if err := s.svc.MCPGateway(r.Context(), sb.ID, connectionID, w, r); err != nil {
		s.writeError(w, err)
	}
}

func (s *Server) git(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/git/github/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		s.writeError(w, core.E(http.StatusNotFound, "not_found", "repository not found"))
		return
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if err := s.svc.GitGateway(r.Context(), sb, parts[0], repo, w, r); err != nil {
		s.writeError(w, err)
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (domain.Sandbox, bool) {
	token, ok := bearer(r.Header.Get("Authorization"))
	if !ok {
		s.writeJSONError(w, http.StatusUnauthorized, "token_invalid", "missing or invalid sandbox token", "")
		return domain.Sandbox{}, false
	}
	sb, ok := s.svc.SandboxByToken(r.Context(), token)
	if !ok {
		s.writeJSONError(w, http.StatusUnauthorized, "token_invalid", "missing or invalid sandbox token", "")
		return domain.Sandbox{}, false
	}
	return sb, true
}

func bearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeJSONError(w, http.StatusNotFound, "not_found", "data-plane route not found", "")
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	requestID := nkid.RequestID()
	if ce, ok := core.AsError(err); ok {
		s.writeJSONError(w, ce.Status, ce.Code, ce.Message, requestID)
		return
	}
	s.log.Error("gateway_error", "error", err, "request_id", requestID)
	s.writeJSONError(w, http.StatusInternalServerError, "internal", "internal server error", requestID)
}

func (s *Server) writeJSONError(w http.ResponseWriter, status int, code, message, requestID string) {
	if requestID == "" {
		requestID = nkid.RequestID()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-Id", requestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message, "request_id": requestID},
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		// Never log query strings, headers, or bodies: terminal/tunnel tickets
		// and model payloads may contain sensitive data.
		s.log.Info("gateway_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", sw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
