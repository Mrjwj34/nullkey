//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type mcpStub struct {
	server    *httptest.Server
	mu        sync.Mutex
	auth      string
	toolCalls int
}

func newMCPStub(t *testing.T) *mcpStub {
	t.Helper()
	s := &mcpStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.auth = r.Header.Get("Authorization")
		if req.Method == "tools/call" {
			s.toolCalls++
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "stub", "version": "1"}},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{
					{"name": "allowed", "description": "allowed tool", "inputSchema": map[string]any{"type": "object"}},
					{"name": "other", "description": "other tool", "inputSchema": map[string]any{"type": "object"}},
				}},
			})
		case "tools/call":
			var params struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &params)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "tool:" + params.Name}}},
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}))
	return s
}

func (s *mcpStub) Close() { s.server.Close() }

func (s *mcpStub) ToolCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolCalls
}

func (s *mcpStub) Auth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

func TestAC_MCP_001_GitHubConnection(t *testing.T) {
	requireDarwin(t)
	stub := newMCPStub(t)
	defer stub.Close()
	d := startDaemonWithEnv(t, map[string]string{"NULLKEY_MCP_GITHUB_URL": stub.server.URL + "/mcp"})
	resp, raw := d.doAuthed(t, http.MethodGet, "/mcp/catalog", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "github") {
		t.Fatalf("catalog = %d %s", resp.StatusCode, raw)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "github", "label": "mcp", "auth_type": "pat", "value": "ghp_testmcp0123456789abcdef",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("credential = %d %s", resp.StatusCode, raw)
	}
	credID := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
	resp, raw = d.doAuthed(t, http.MethodPost, "/mcp/connections", map[string]any{
		"server_id": "github", "label": "github", "credential_id": credID, "tools": []string{"allowed"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create connection = %d %s", resp.StatusCode, raw)
	}
	conn := decodeJSON[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, raw)
	if conn.ID == "" || conn.Status != "connected" {
		t.Fatalf("unexpected connection: %+v", conn)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/mcp/connections/"+conn.ID+"/test", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test connection = %d %s", resp.StatusCode, raw)
	}
	result := decodeJSON[struct {
		OK    bool     `json:"ok"`
		Tools []string `json:"tools"`
	}](t, raw)
	if !result.OK || len(result.Tools) == 0 {
		t.Fatalf("test result = %+v", result)
	}
	if stub.Auth() != "Bearer ghp_testmcp0123456789abcdef" {
		t.Fatalf("upstream auth = %q", stub.Auth())
	}

	ws := d.createWorkspace(t, "mcp")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	var states struct {
		Items []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"items"`
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/states", nil)
	_ = json.Unmarshal(raw, &states)
	for _, st := range states.Items {
		if st.ID != sb.StateID {
			continue
		}
		cfg, err := os.ReadFile(filepath.Join(st.Path, "nullkey", "config.json"))
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if strings.Contains(string(cfg), "ghp_") || strings.Contains(string(cfg), "mcp_") {
			t.Fatalf("sandbox config contains an MCP credential: %s", cfg)
		}
		if !strings.Contains(string(cfg), "gateway") {
			t.Fatalf("sandbox config has no gateway URL: %s", cfg)
		}
	}
}

func TestAC_MCP_002_ToolCall(t *testing.T) {
	requireDarwin(t)
	stub := newMCPStub(t)
	defer stub.Close()
	d := startDaemonWithEnv(t, map[string]string{"NULLKEY_MCP_GITHUB_URL": stub.server.URL + "/mcp"})
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "github", "label": "mcp", "auth_type": "pat", "value": "ghp_testtool0123456789abcdef",
	})
	credID := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
	resp, raw = d.doAuthed(t, http.MethodPost, "/mcp/connections", map[string]any{
		"server_id": "github", "label": "github", "credential_id": credID,
	})
	connID := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
	ws := d.createWorkspace(t, "mcp-tool")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed","arguments":{}}}`)
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/mcp/"+connID, token, body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tool call = %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "tool:allowed") {
		t.Fatalf("unexpected tool result: %s", raw)
	}
	if stub.Auth() != "Bearer ghp_testtool0123456789abcdef" {
		t.Fatalf("upstream auth = %q", stub.Auth())
	}
	if strings.Contains(string(raw), "ghp_testtool") || strings.Contains(d.logs.String(), "ghp_testtool") {
		t.Fatal("MCP credential leaked")
	}
}

func TestAC_MCP_003_AllowlistAndRevoke(t *testing.T) {
	requireDarwin(t)
	stub := newMCPStub(t)
	defer stub.Close()
	d := startDaemonWithEnv(t, map[string]string{"NULLKEY_MCP_GITHUB_URL": stub.server.URL + "/mcp"})
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "github", "label": "mcp", "auth_type": "pat", "value": "ghp_testallow0123456789abcdef",
	})
	credID := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
	resp, raw = d.doAuthed(t, http.MethodPost, "/mcp/connections", map[string]any{
		"server_id": "github", "label": "github", "credential_id": credID, "tools": []string{"allowed"},
	})
	connID := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
	ws := d.createWorkspace(t, "mcp-allow")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	before := stub.ToolCalls()
	denied := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"other","arguments":{}}}`)
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/mcp/"+connID, token, denied, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied tool = %d %s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "tool_denied" {
		t.Fatalf("code = %q, want tool_denied", env.Error.Code)
	}
	if stub.ToolCalls() != before {
		t.Fatal("denied tool reached the upstream")
	}

	// A second sandbox cannot reuse the bound connection.
	ws2 := d.createWorkspace(t, "mcp-other")
	sb2 := d.createSandbox(t, ws2.ID, "shell", "")
	token2 := sandboxToken(t, d, sb2.ID)
	allowed := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed","arguments":{}}}`)
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/mcp/"+connID, token2, allowed, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-sandbox MCP = %d, want 404; body=%s", resp.StatusCode, raw)
	}
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/mcp/"+connID, token, allowed, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner tool call = %d %s", resp.StatusCode, raw)
	}

	resp, _ = d.doAuthed(t, http.MethodDelete, "/mcp/connections/"+connID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete connection = %d", resp.StatusCode)
	}
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/mcp/"+connID, token, allowed, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted connection = %d, want 404; body=%s", resp.StatusCode, raw)
	}
}
