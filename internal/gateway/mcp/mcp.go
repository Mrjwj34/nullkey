// Package mcp implements the host-side MCP gateway.
//
// Credentialed MCP servers run on the host. Sandboxes talk to this gateway,
// which injects the real credential and enforces a per-connection tool
// allowlist. Request and response bodies, headers and credentials must never be
// written to logs and must never be copied into error values.
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// gatewayTestTimeout bounds the complete initialize + tools/list exchange.
	gatewayTestTimeout = 10 * time.Second

	defaultCredentialHeader = "Authorization"
	defaultCredentialPrefix = "Bearer "

	errCodeInvalidRequest    = "invalid_request"
	errCodeInvalidConnection = "invalid_connection"
	errCodeMCPUnavailable    = "mcp_unavailable"
	errCodeMCPUpstream       = "mcp_upstream_error"
	errCodeMCPProtocol       = "mcp_protocol_error"
	errCodeMCPError          = "mcp_error"
	errCodeToolDenied        = "tool_denied"
)

// Connection describes one host-side MCP server and how the gateway
// authenticates to it.
type Connection struct {
	ID               string
	ServerID         string
	BaseURL          string
	Credential       string
	CredentialHeader string
	CredentialPrefix string
	AllowedTools     []string
}

// Error is a typed MCP gateway error. Status is the HTTP status that should be
// returned to the sandbox. Error messages are safe to surface: they never
// contain credentials, upstream bodies, or headers.
type Error struct {
	Status  int
	Code    string
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "mcp: <nil>"
	}
	switch {
	case e.Code == "":
		return e.Message
	case e.Message == "":
		return e.Code
	default:
		return e.Code + ": " + e.Message
	}
}

// Proxy forwards JSON-RPC requests to host-side MCP servers.
type Proxy struct {
	Client *http.Client
	Logger *slog.Logger
}

// New builds a Proxy. Nil dependencies fall back to the standard library
// defaults.
func New(client *http.Client, logger *slog.Logger) *Proxy {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Proxy{Client: client, Logger: logger}
}

func (p *Proxy) httpClient() *http.Client {
	if p != nil && p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

func (p *Proxy) logger() *slog.Logger {
	if p != nil && p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// do sends a request without following redirects. An injected credential must
// never be forwarded to a destination other than the configured MCP server.
func (p *Proxy) do(req *http.Request) (*http.Response, error) {
	base := p.httpClient()
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client.Do(req)
}

func newError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func writeProxyError(w http.ResponseWriter, e *Error) {
	if w == nil || e == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	type errorBody struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.NewEncoder(w).Encode(struct {
		Error errorBody `json:"error"`
	}{Error: errorBody{Code: e.Code, Message: e.Message}})
}

// credentialHeader returns the header name, value and whether a credential is
// configured. The default is Authorization: Bearer <credential>. A custom
// CredentialHeader without a CredentialPrefix is sent with the raw credential.
func credentialHeader(conn Connection) (name, value string, ok bool) {
	if conn.Credential == "" {
		return "", "", false
	}
	name = strings.TrimSpace(conn.CredentialHeader)
	if name == "" {
		name = defaultCredentialHeader
	}
	prefix := conn.CredentialPrefix
	if prefix == "" && strings.EqualFold(name, defaultCredentialHeader) {
		prefix = defaultCredentialPrefix
	}
	return name, prefix + conn.Credential, true
}

func applyCredential(header http.Header, conn Connection) {
	name, value, ok := credentialHeader(conn)
	if !ok {
		return
	}
	header.Set(name, value)
}

func (p *Proxy) newRequest(ctx context.Context, method, baseURL string, body io.Reader) (*http.Request, error) {
	if method == "" {
		return nil, newError(http.StatusBadRequest, errCodeInvalidRequest, "mcp request method is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, newError(http.StatusBadRequest, errCodeInvalidConnection, "mcp connection has no base URL")
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL, body)
	if err != nil {
		return nil, newError(http.StatusBadRequest, errCodeInvalidConnection, "mcp connection has an invalid base URL")
	}
	return req, nil
}
