package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// CreateMCPConnectionRequest is the core request.
type CreateMCPConnectionRequest struct {
	ServerID     string
	Label        string
	CredentialID string
	AuthType     string
	Tools        []string
}

// ListMCPConnections returns bindings.
func (s *Service) ListMCPConnections(ctx context.Context, limit int, cursor string) ([]domain.MCPConnection, string, error) {
	items, next, err := s.Store.ListMCPConnections(ctx, limit, cursor)
	if err != nil {
		return nil, "", Wrap(http.StatusInternalServerError, "internal", "could not list MCP connections", err)
	}
	return items, next, nil
}

// CreateMCPConnection creates a host-side MCP binding.
func (s *Service) CreateMCPConnection(ctx context.Context, req CreateMCPConnectionRequest) (domain.MCPConnection, error) {
	server, ok := mcpServerByID(req.ServerID)
	if !ok {
		return domain.MCPConnection{}, Invalid("unknown server_id")
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		return domain.MCPConnection{}, Invalid("label is required")
	}
	if req.CredentialID != "" {
		if _, err := s.Store.GetCredential(ctx, req.CredentialID); err != nil {
			if isNotFound(err) {
				return domain.MCPConnection{}, NotFound("credential not found")
			}
			return domain.MCPConnection{}, Wrap(http.StatusInternalServerError, "internal", "could not read credential", err)
		}
	}
	baseURL := s.mcpBaseURL(server.ID)
	cfg, _ := json.Marshal(map[string]any{"base_url": baseURL, "tools": req.Tools})
	conn := domain.MCPConnection{
		ID:           newID("mcp_"),
		ServerID:     server.ID,
		Label:        req.Label,
		Status:       "connected",
		CredentialID: nil,
		ConfigJSON:   string(cfg),
		CreatedAt:    s.now(),
	}
	if req.CredentialID != "" {
		conn.CredentialID = &req.CredentialID
	}
	if err := s.Store.CreateMCPConnection(ctx, conn); err != nil {
		return domain.MCPConnection{}, Wrap(http.StatusInternalServerError, "internal", "could not create MCP connection", err)
	}
	s.Audit.Record(ctx, "mcp.created", nil, map[string]any{"connection_id": conn.ID, "server_id": conn.ServerID})
	s.PublishEvent("mcp.created", map[string]any{"connection_id": conn.ID, "status": "connected"})
	return conn, nil
}

func (s *Service) mcpBaseURL(serverID string) string {
	key := "NULLKEY_MCP_" + strings.ToUpper(strings.ReplaceAll(serverID, "-", "_")) + "_URL"
	if v := os.Getenv(key); v != "" {
		return v
	}
	switch serverID {
	case "github":
		return "https://api.githubcopilot.com/mcp/"
	default:
		return ""
	}
}

// DeleteMCPConnection removes a binding.
func (s *Service) DeleteMCPConnection(ctx context.Context, id string) error {
	if _, err := s.Store.GetMCPConnection(ctx, id); err != nil {
		if isNotFound(err) {
			return NotFound("MCP connection not found")
		}
		return Wrap(http.StatusInternalServerError, "internal", "could not read MCP connection", err)
	}
	if err := s.Store.DeleteMCPConnection(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not delete MCP connection", err)
	}
	s.Audit.Record(ctx, "mcp.deleted", nil, map[string]any{"connection_id": id})
	s.PublishEvent("mcp.deleted", map[string]any{"connection_id": id, "status": "deleted"})
	return nil
}

// TestMCPConnection lists tools and updates the cached status.
func (s *Service) TestMCPConnection(ctx context.Context, id string) ([]string, error) {
	conn, err := s.Store.GetMCPConnection(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, NotFound("MCP connection not found")
		}
		return nil, Wrap(http.StatusInternalServerError, "internal", "could not read MCP connection", err)
	}
	cfg, err := s.mcpConn(ctx, conn)
	if err != nil {
		return nil, err
	}
	if s.MCP == nil {
		return nil, E(http.StatusNotImplemented, "not_implemented", "MCP gateway is not available")
	}
	tools, err := s.MCP.Test(ctx, cfg)
	if err != nil {
		_ = s.Store.SetMCPConnectionStatus(ctx, id, "error", nil)
		return nil, E(http.StatusBadGateway, "mcp_unavailable", "MCP server did not answer")
	}
	if tools == nil {
		tools = []string{}
	}
	_ = s.Store.SetMCPConnectionStatus(ctx, id, "connected", tools)
	return tools, nil
}

func (s *Service) mcpConn(ctx context.Context, conn domain.MCPConnection) (MCPConn, error) {
	var cfg struct {
		BaseURL string   `json:"base_url"`
		Tools   []string `json:"tools"`
	}
	_ = json.Unmarshal([]byte(conn.ConfigJSON), &cfg)
	if cfg.BaseURL == "" {
		cfg.BaseURL = s.mcpBaseURL(conn.ServerID)
	}
	if cfg.BaseURL == "" {
		return MCPConn{}, E(http.StatusBadGateway, "mcp_unavailable", "MCP server has no HTTP endpoint configured")
	}
	out := MCPConn{
		ID:               conn.ID,
		ServerID:         conn.ServerID,
		BaseURL:          cfg.BaseURL,
		AllowedTools:     cfg.Tools,
		CredentialHeader: "Authorization",
		CredentialPrefix: "Bearer ",
	}
	if conn.CredentialID != nil && *conn.CredentialID != "" {
		cred, err := s.Store.GetCredential(ctx, *conn.CredentialID)
		if err != nil {
			return MCPConn{}, E(http.StatusUnauthorized, "reauth_required", "MCP credential is unavailable")
		}
		value, err := s.CredentialSecret(ctx, cred)
		if err != nil {
			return MCPConn{}, err
		}
		out.Credential = value
	}
	return out, nil
}

// MCPGateway proxies a sandbox MCP request, binding the connection to the first
// sandbox that uses it. Cross-sandbox reuse is rejected.
func (s *Service) MCPGateway(ctx context.Context, sandboxID, connectionID string, w http.ResponseWriter, r *http.Request) error {
	conn, err := s.Store.GetMCPConnection(ctx, connectionID)
	if err != nil {
		if isNotFound(err) {
			return NotFound("MCP connection not found")
		}
		return Wrap(http.StatusInternalServerError, "internal", "could not read MCP connection", err)
	}
	if conn.SandboxID == "" {
		claimed, err := s.Store.ClaimMCPConnection(ctx, connectionID, sandboxID)
		if err != nil {
			return Wrap(http.StatusInternalServerError, "internal", "could not bind MCP connection", err)
		}
		// Re-read after the atomic claim: another sandbox may have won.
		conn, err = s.Store.GetMCPConnection(ctx, connectionID)
		if err != nil {
			return Wrap(http.StatusInternalServerError, "internal", "could not read MCP connection", err)
		}
		if !claimed || conn.SandboxID != sandboxID {
			return NotFound("MCP connection not found")
		}
	}
	if conn.SandboxID != sandboxID {
		return NotFound("MCP connection not found")
	}
	cfg, err := s.mcpConn(ctx, conn)
	if err != nil {
		return err
	}
	if s.MCP == nil {
		return E(http.StatusNotImplemented, "not_implemented", "MCP gateway is not available")
	}
	if err := s.MCP.Proxy(ctx, cfg, w, r); err != nil {
		if ce, ok := AsError(err); ok {
			return ce
		}
		return E(http.StatusBadGateway, "mcp_unavailable", "MCP server did not answer")
	}
	return nil
}

// StartMCPOAuth is reserved for MCP servers that require dynamic client
// registration. v1 supports pre-registered PAT/OAuth credentials instead.
func (s *Service) StartMCPOAuth(ctx context.Context, id string) (string, string, error) {
	if _, err := s.Store.GetMCPConnection(ctx, id); err != nil {
		if isNotFound(err) {
			return "", "", NotFound("MCP connection not found")
		}
		return "", "", Wrap(http.StatusInternalServerError, "internal", "could not read MCP connection", err)
	}
	return "", "", E(http.StatusBadRequest, "oauth_unavailable", "this MCP server does not support dynamic OAuth registration; use a PAT or OAuth credential")
}
