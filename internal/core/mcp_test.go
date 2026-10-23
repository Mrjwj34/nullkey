package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

type fakeMCPProxy struct {
	testTools  []string
	testErr    error
	proxyErr   error
	gotTest    MCPConn
	gotProxy   MCPConn
	testCalls  int
	proxyCalls int
}

func (f *fakeMCPProxy) Test(_ context.Context, conn MCPConn) ([]string, error) {
	f.testCalls++
	f.gotTest = conn
	return f.testTools, f.testErr
}

func (f *fakeMCPProxy) Proxy(_ context.Context, conn MCPConn, w http.ResponseWriter, _ *http.Request) error {
	f.proxyCalls++
	f.gotProxy = conn
	if f.proxyErr == nil {
		w.WriteHeader(http.StatusNoContent)
	}
	return f.proxyErr
}

func TestCreateMCPConnectionValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "nope", Label: "x"})
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "  "})
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{
		ServerID: "github", Label: "x", CredentialID: "cred_missing",
	})
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestCreateMCPConnection(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	conn, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{
		ServerID: "github", Label: "  github tools  ", Tools: []string{"search"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conn.ServerID != "github" || conn.Label != "github tools" || conn.Status != "connected" || conn.CredentialID != nil {
		t.Fatalf("connection = %+v", conn)
	}
	var cfg struct {
		BaseURL string   `json:"base_url"`
		Tools   []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(conn.ConfigJSON), &cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.BaseURL != "https://api.githubcopilot.com/mcp/" || len(cfg.Tools) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	if len(conn.Tools) != 1 {
		// Create does not hydrate tools from the JSON, but the store does.
		stored, err := svc.Store.GetMCPConnection(ctx, conn.ID)
		if err != nil || len(stored.Tools) != 1 {
			t.Fatalf("stored tools = %+v, %v", stored, err)
		}
	}

	t.Setenv("NULLKEY_MCP_GITHUB_URL", "http://127.0.0.1:9/mcp")
	overridden, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "env"})
	if err != nil {
		t.Fatalf("create env: %v", err)
	}
	var envCfg struct {
		BaseURL string `json:"base_url"`
	}
	_ = json.Unmarshal([]byte(overridden.ConfigJSON), &envCfg)
	if envCfg.BaseURL != "http://127.0.0.1:9/mcp" {
		t.Fatalf("env base url = %q", envCfg.BaseURL)
	}

	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "github", Label: "pat", AuthType: "pat", Value: "ghp_test_token",
	})
	withCred, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{
		ServerID: "github", Label: "cred", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("create with credential: %v", err)
	}
	if withCred.CredentialID == nil || *withCred.CredentialID != cred.ID {
		t.Fatalf("credential id = %+v", withCred.CredentialID)
	}

	items, next, err := svc.ListMCPConnections(ctx, 10, "")
	if err != nil || next != "" || len(items) != 3 {
		t.Fatalf("list = %d, %q, %v", len(items), next, err)
	}

	if err := svc.DeleteMCPConnection(ctx, conn.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err = svc.DeleteMCPConnection(ctx, conn.ID)
	wantError(t, err, http.StatusNotFound, "not_found")
	evs, _, err := svc.Audit.List(ctx, 50, "", "mcp.deleted")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestMCPStoreErrors(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()
	_, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x"})
	wantError(t, err, http.StatusInternalServerError, "internal")
	_, err = svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x", CredentialID: "cred_x"})
	wantError(t, err, http.StatusInternalServerError, "internal")
	_, _, err = svc.ListMCPConnections(ctx, 10, "")
	wantError(t, err, http.StatusInternalServerError, "internal")
	err = svc.DeleteMCPConnection(ctx, "mcp_x")
	wantError(t, err, http.StatusInternalServerError, "internal")
	_, err = svc.TestMCPConnection(ctx, "mcp_x")
	wantError(t, err, http.StatusInternalServerError, "internal")
	_, _, err = svc.StartMCPOAuth(ctx, "mcp_x")
	wantError(t, err, http.StatusInternalServerError, "internal")
	err = svc.MCPGateway(ctx, "sbx_x", "mcp_x", httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestTestMCPConnection(t *testing.T) {
	fake := &fakeMCPProxy{testTools: []string{"search", "read"}}
	svc, _ := newTestService(t, func(o *Options) { o.MCP = fake })
	ctx := context.Background()
	conn, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tools, err := svc.TestMCPConnection(ctx, conn.ID)
	if err != nil || len(tools) != 2 {
		t.Fatalf("test = %v, %v", tools, err)
	}
	if fake.testCalls != 1 || fake.gotTest.ID != conn.ID || fake.gotTest.ServerID != "github" {
		t.Fatalf("proxy not called correctly: %+v", fake)
	}
	if fake.gotTest.CredentialHeader != "Authorization" || fake.gotTest.CredentialPrefix != "Bearer " {
		t.Fatalf("credential shape = %+v", fake.gotTest)
	}
	stored, err := svc.Store.GetMCPConnection(ctx, conn.ID)
	if err != nil || stored.Status != "connected" || len(stored.Tools) != 2 {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	// Nil tool lists normalize to an empty slice.
	fake.testTools = nil
	tools, err = svc.TestMCPConnection(ctx, conn.ID)
	if err != nil || tools == nil || len(tools) != 0 {
		t.Fatalf("nil tools = %#v, %v", tools, err)
	}

	// Proxy failure marks the cached status as error.
	fake.testErr = errors.New("upstream down")
	_, err = svc.TestMCPConnection(ctx, conn.ID)
	wantError(t, err, http.StatusBadGateway, "mcp_unavailable")
	stored, err = svc.Store.GetMCPConnection(ctx, conn.ID)
	if err != nil || stored.Status != "error" {
		t.Fatalf("error status = %+v, %v", stored, err)
	}
	fake.testErr = nil

	// No proxy configured is a distinct not-implemented error.
	noProxy, _ := newTestService(t)
	conn2, err := noProxy.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x"})
	if err != nil {
		t.Fatalf("create no proxy: %v", err)
	}
	_, err = noProxy.TestMCPConnection(ctx, conn2.ID)
	wantError(t, err, http.StatusNotImplemented, "not_implemented")

	_, err = svc.TestMCPConnection(ctx, "mcp_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	// A connection without a resolvable endpoint fails before the proxy call.
	fs, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "filesystem", Label: "fs"})
	if err != nil {
		t.Fatalf("create fs: %v", err)
	}
	_, err = svc.TestMCPConnection(ctx, fs.ID)
	wantError(t, err, http.StatusBadGateway, "mcp_unavailable")
}

func TestMCPConn(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	filesystem, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "filesystem", Label: "fs"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored, err := svc.Store.GetMCPConnection(ctx, filesystem.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, err = svc.mcpConn(ctx, stored)
	wantError(t, err, http.StatusBadGateway, "mcp_unavailable")

	t.Setenv("NULLKEY_MCP_FILESYSTEM_URL", "http://127.0.0.1:9/mcp")
	cfg, err := svc.mcpConn(ctx, stored)
	if err != nil || cfg.BaseURL != "http://127.0.0.1:9/mcp" || cfg.ID != filesystem.ID {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}

	// A stored credential is resolved into the connection projection.
	credOK := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "github", Label: "pat", AuthType: "pat", Value: "ghp_resolved",
	})
	connOK, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{
		ServerID: "github", Label: "ok", CredentialID: credOK.ID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	storedOK, err := svc.Store.GetMCPConnection(ctx, connOK.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	cfg, err = svc.mcpConn(ctx, storedOK)
	if err != nil || cfg.Credential != "ghp_resolved" || cfg.CredentialHeader != "Authorization" {
		t.Fatalf("credential projection = %+v, %v", cfg, err)
	}

	// Missing credential metadata.
	missing := domain.MCPConnection{
		ID: newID("mcp_"), ServerID: "github", Label: "x", Status: "connected",
		ConfigJSON:   `{"base_url":"http://127.0.0.1:9/mcp"}`,
		CredentialID: func() *string { s := "cred_missing"; return &s }(),
		CreatedAt:    svc.now(),
	}
	if err := svc.Store.CreateMCPConnection(ctx, missing); err != nil {
		t.Fatalf("insert: %v", err)
	}
	stored, err = svc.Store.GetMCPConnection(ctx, missing.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, err = svc.mcpConn(ctx, stored)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	// Credential exists but its secret is gone.
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "github", Label: "pat", AuthType: "pat", Value: "v",
	})
	if err := svc.Secrets.Delete(ctx, cred.KeychainAccount); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	withCred, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{
		ServerID: "github", Label: "c", CredentialID: cred.ID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored, err = svc.Store.GetMCPConnection(ctx, withCred.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, err = svc.mcpConn(ctx, stored)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")
}

func TestMCPGateway(t *testing.T) {
	fake := &fakeMCPProxy{}
	svc, _ := newTestService(t, func(o *Options) { o.MCP = fake })
	ctx := context.Background()
	conn, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if err := svc.MCPGateway(ctx, "sbx_first", conn.ID, rec, req); err != nil {
		t.Fatalf("gateway: %v", err)
	}
	if rec.Code != http.StatusNoContent || fake.proxyCalls != 1 || fake.gotProxy.ID != conn.ID {
		t.Fatalf("proxy call = %d, code = %d", fake.proxyCalls, rec.Code)
	}
	bound, err := svc.Store.GetMCPConnection(ctx, conn.ID)
	if err != nil || bound.SandboxID != "sbx_first" {
		t.Fatalf("binding = %+v, %v", bound, err)
	}

	// Cross-sandbox reuse is rejected and the proxy is not called.
	rec = httptest.NewRecorder()
	if err := svc.MCPGateway(ctx, "sbx_other", conn.ID, rec, req); err == nil {
		t.Fatalf("cross-sandbox reuse accepted")
	} else {
		wantError(t, err, http.StatusNotFound, "not_found")
	}
	if fake.proxyCalls != 1 {
		t.Fatalf("proxy called for cross-sandbox request")
	}

	// Missing connection.
	err = svc.MCPGateway(ctx, "sbx_first", "mcp_missing", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusNotFound, "not_found")

	// Proxy errors: typed errors pass through, generic errors become 502.
	fake.proxyErr = E(http.StatusForbidden, "network_denied", "denied")
	err = svc.MCPGateway(ctx, "sbx_first", conn.ID, httptest.NewRecorder(), req)
	wantError(t, err, http.StatusForbidden, "network_denied")
	fake.proxyErr = errors.New("boom")
	err = svc.MCPGateway(ctx, "sbx_first", conn.ID, httptest.NewRecorder(), req)
	wantError(t, err, http.StatusBadGateway, "mcp_unavailable")
	fake.proxyErr = nil

	// Deleted connections are gone.
	if err := svc.DeleteMCPConnection(ctx, conn.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err = svc.MCPGateway(ctx, "sbx_first", conn.ID, httptest.NewRecorder(), req)
	wantError(t, err, http.StatusNotFound, "not_found")

	// No proxy configured.
	noProxy, _ := newTestService(t)
	conn2, err := noProxy.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = noProxy.MCPGateway(ctx, "sbx_x", conn2.ID, httptest.NewRecorder(), req)
	wantError(t, err, http.StatusNotImplemented, "not_implemented")

	// Endpoint cannot be resolved.
	fs, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "filesystem", Label: "fs"})
	if err != nil {
		t.Fatalf("create fs: %v", err)
	}
	err = svc.MCPGateway(ctx, "sbx_fs", fs.ID, httptest.NewRecorder(), req)
	wantError(t, err, http.StatusBadGateway, "mcp_unavailable")
}

func TestStartMCPOAuth(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, _, err := svc.StartMCPOAuth(ctx, "mcp_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	conn, err := svc.CreateMCPConnection(ctx, CreateMCPConnectionRequest{ServerID: "github", Label: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err = svc.StartMCPOAuth(ctx, conn.ID)
	wantError(t, err, http.StatusBadRequest, "oauth_unavailable")
}

func TestMCPBaseURL(t *testing.T) {
	svc, _ := newTestService(t)
	t.Setenv("NULLKEY_MCP_GITHUB_URL", "")
	if got := svc.mcpBaseURL("github"); got != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("github base = %q", got)
	}
	if got := svc.mcpBaseURL("filesystem"); got != "" {
		t.Fatalf("filesystem base = %q", got)
	}
	t.Setenv("NULLKEY_MCP_FILESYSTEM_URL", "http://127.0.0.1:9")
	if got := svc.mcpBaseURL("filesystem"); got != "http://127.0.0.1:9" {
		t.Fatalf("env base = %q", got)
	}
}
