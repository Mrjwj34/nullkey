package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/core"
	"github.com/Mrjwj34/nullkey/internal/domain"
)

type credentialPage struct {
	Items      []domain.Credential `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func TestAPICredentials(t *testing.T) {
	api := newTestAPI(t)
	const secret = "sk-test-openai-1234abcd"

	rec := api.postJSON("/api/v1/credentials", map[string]any{
		"provider_id": "openai",
		"label":       "work",
		"auth_type":   "api_key",
		"value":       secret,
	})
	wantStatus(t, rec, http.StatusCreated)
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("response leaked the credential value: %s", rec.Body.String())
	}
	cred := mustDecode[domain.Credential](t, rec)
	if cred.ProviderID != "openai" || cred.Fingerprint == "" || strings.Contains(cred.Fingerprint, secret) {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.AuthType != domain.AuthAPIKey || cred.Status != domain.CredentialConnected {
		t.Fatalf("credential = %+v", cred)
	}

	// Header auth requires and records header_name.
	rec = api.postJSON("/api/v1/credentials", map[string]any{
		"provider_id": "anthropic",
		"label":       "header",
		"auth_type":   "header",
		"value":       "sk-test-anthropic-abcd",
		"header_name": "x-api-key",
		"base_url":    "https://api.example.test",
	})
	wantStatus(t, rec, http.StatusCreated)
	headerCred := mustDecode[domain.Credential](t, rec)

	// Invalid payloads.
	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown provider", map[string]any{"provider_id": "nope", "label": "x", "auth_type": "api_key", "value": "sk-test"}},
		{"missing label", map[string]any{"provider_id": "openai", "auth_type": "api_key", "value": "sk-test"}},
		{"missing auth type", map[string]any{"provider_id": "openai", "label": "x", "value": "sk-test"}},
		{"unsupported auth type", map[string]any{"provider_id": "openai", "label": "x", "auth_type": "oauth", "value": "sk-test"}},
		{"missing value", map[string]any{"provider_id": "openai", "label": "x", "auth_type": "api_key"}},
		{"header without name", map[string]any{"provider_id": "anthropic", "label": "x", "auth_type": "header", "value": "sk-test"}},
		{"bad base url", map[string]any{"provider_id": "openai", "label": "x", "auth_type": "api_key", "value": "sk-test", "base_url": "ftp://example.test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErrorCode(t, api.postJSON("/api/v1/credentials", tc.body), http.StatusBadRequest, CodeInvalidRequest)
		})
	}
	rec = api.auth(http.MethodPost, "/api/v1/credentials", `{"provider_id":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	// List with pagination.
	rec = api.auth(http.MethodGet, "/api/v1/credentials", "")
	wantStatus(t, rec, http.StatusOK)
	if page := mustDecode[credentialPage](t, rec); len(page.Items) < 2 {
		t.Fatalf("credentials = %+v", page.Items)
	}
	rec = api.auth(http.MethodGet, "/api/v1/credentials?limit=1", "")
	wantStatus(t, rec, http.StatusOK)
	first := mustDecode[credentialPage](t, rec)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first credential page = %+v", first)
	}
	rec = api.auth(http.MethodGet, "/api/v1/credentials?limit=1&cursor="+*first.NextCursor, "")
	wantStatus(t, rec, http.StatusOK)
	if second := mustDecode[credentialPage](t, rec); len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second credential page = %+v", second)
	}
	rec = api.auth(http.MethodGet, "/api/v1/credentials?cursor=%40%40%40", "")
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)

	// Test success.
	rec = api.auth(http.MethodPost, "/api/v1/credentials/"+cred.ID+"/test", "")
	wantStatus(t, rec, http.StatusOK)
	tested := mustDecode[struct {
		OK        bool    `json:"ok"`
		LatencyMS *int64  `json:"latency_ms"`
		Error     *string `json:"error"`
	}](t, rec)
	if !tested.OK || tested.Error != nil {
		t.Fatalf("test credential = %+v", tested)
	}

	// Test expired.
	if err := api.store.SetCredentialStatus(context.Background(), cred.ID, domain.CredentialExpired, nil); err != nil {
		t.Fatalf("set status: %v", err)
	}
	rec = api.auth(http.MethodPost, "/api/v1/credentials/"+cred.ID+"/test", "")
	wantStatus(t, rec, http.StatusOK)
	expired := mustDecode[struct {
		OK    bool    `json:"ok"`
		Error *string `json:"error"`
	}](t, rec)
	if expired.OK || expired.Error == nil || *expired.Error != "credential expired" {
		t.Fatalf("expired test = %+v", expired)
	}

	// Test with a missing secret value.
	unavailable := api.createCredential(t, map[string]any{
		"provider_id": "openai",
		"label":       "unavailable",
		"auth_type":   "api_key",
		"value":       "sk-test-unavailable-0000",
	})
	stored, err := api.store.GetCredential(context.Background(), unavailable.ID)
	if err != nil {
		t.Fatalf("get credential: %v", err)
	}
	if err := api.svc.Secrets.Delete(context.Background(), stored.KeychainAccount); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	rec = api.auth(http.MethodPost, "/api/v1/credentials/"+unavailable.ID+"/test", "")
	wantStatus(t, rec, http.StatusOK)
	missing := mustDecode[struct {
		OK    bool    `json:"ok"`
		Error *string `json:"error"`
	}](t, rec)
	if missing.OK || missing.Error == nil || *missing.Error != "credential value is unavailable" {
		t.Fatalf("missing secret test = %+v", missing)
	}

	rec = api.auth(http.MethodPost, "/api/v1/credentials/cred_missing/test", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Delete is idempotent only in the sense that a second delete is 404.
	rec = api.auth(http.MethodDelete, "/api/v1/credentials/cred_missing", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodDelete, "/api/v1/credentials/"+headerCred.ID, "")
	wantStatus(t, rec, http.StatusNoContent)
	rec = api.auth(http.MethodDelete, "/api/v1/credentials/"+unavailable.ID, "")
	wantStatus(t, rec, http.StatusNoContent)
	rec = api.auth(http.MethodPost, "/api/v1/credentials/"+unavailable.ID+"/test", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}

// oauthTokenServer is a loopback stand-in for an OAuth token endpoint.
func oauthTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			if r.Form.Get("refresh_token") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"sk-test-access-refreshed","refresh_token":"sk-test-refresh-2","expires_in":1200}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"sk-test-access-1","refresh_token":"sk-test-refresh-1","expires_in":3600}`)
	}))
}

func configureCodexOAuth(t *testing.T, tokenURL string) {
	t.Helper()
	t.Setenv("NULLKEY_OAUTH_CODEX_AUTH_URL", "https://auth.example.test/authorize")
	t.Setenv("NULLKEY_OAUTH_CODEX_TOKEN_URL", tokenURL)
	t.Setenv("NULLKEY_OAUTH_CODEX_CLIENT_ID", "fake-client-id")
	t.Setenv("NULLKEY_OAUTH_CODEX_CLIENT_SECRET", "sk-test-client-secret")
	t.Setenv("NULLKEY_OAUTH_CODEX_SCOPES", "openid profile")
}

func startOAuthFlow(t *testing.T, api *testAPI) (flowID, state string) {
	t.Helper()
	rec := api.postJSON("/api/v1/oauth/start", map[string]any{"provider_id": "codex", "label": "test"})
	wantStatus(t, rec, http.StatusOK)
	started := mustDecode[struct {
		FlowID    string    `json:"flow_id"`
		AuthURL   string    `json:"auth_url"`
		ExpiresAt time.Time `json:"expires_at"`
	}](t, rec)
	if started.FlowID == "" || started.AuthURL == "" || started.ExpiresAt.IsZero() {
		t.Fatalf("start oauth = %+v", started)
	}
	parsed, err := url.Parse(started.AuthURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	query := parsed.Query()
	state = query.Get("state")
	if state == "" || query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("auth url is missing PKCE parameters: %s", started.AuthURL)
	}
	if query.Get("client_id") != "fake-client-id" {
		t.Fatalf("client_id = %q", query.Get("client_id"))
	}
	return started.FlowID, state
}

func TestAPIOAuthFlow(t *testing.T) {
	tokenServer := oauthTokenServer(t)
	defer tokenServer.Close()
	configureCodexOAuth(t, tokenServer.URL)

	api := newTestAPI(t)

	rec := api.auth(http.MethodGet, "/api/v1/oauth/providers", "")
	wantStatus(t, rec, http.StatusOK)
	providers := mustDecode[struct {
		Items []core.OAuthProvider `json:"items"`
	}](t, rec).Items
	codexAvailable := false
	for _, provider := range providers {
		if provider.ID == "codex" && provider.Status == "available" {
			codexAvailable = true
		}
	}
	if !codexAvailable {
		t.Fatalf("codex provider not available: %+v", providers)
	}

	flowID, state := startOAuthFlow(t, api)

	rec = api.postJSON("/api/v1/oauth/start", map[string]any{"provider_id": "unknown"})
	wantErrorCode(t, rec, http.StatusBadRequest, ErrorCode("oauth_unavailable"))
	rec = api.auth(http.MethodPost, "/api/v1/oauth/start", `{"provider_id":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	// Browser callback is public and one-time.
	rec = api.request(http.MethodGet, "/api/v1/oauth/callback?code=test-code&state="+url.QueryEscape(state), "", nil)
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Nullkey connected") {
		t.Fatalf("callback body = %s", rec.Body.String())
	}
	rec = api.request(http.MethodGet, "/api/v1/oauth/callback?code=test-code&state="+url.QueryEscape(state), "", nil)
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "Nullkey OAuth failed") {
		t.Fatalf("replay callback body = %s", rec.Body.String())
	}
	rec = api.request(http.MethodGet, "/api/v1/oauth/callback?code=test-code", "", nil)
	wantStatus(t, rec, http.StatusBadRequest)

	rec = api.auth(http.MethodGet, "/api/v1/oauth/flows/"+flowID, "")
	wantStatus(t, rec, http.StatusOK)
	flow := mustDecode[domain.OAuthFlow](t, rec)
	if flow.Status != domain.OAuthSucceeded || flow.CredentialID == nil {
		t.Fatalf("flow = %+v", flow)
	}
	credentialID := *flow.CredentialID

	rec = api.auth(http.MethodPost, "/api/v1/oauth/flows/"+flowID+"/cancel", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeConflict)
	rec = api.auth(http.MethodGet, "/api/v1/oauth/flows/flow_missing", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/oauth/flows/flow_missing/cancel", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Refresh the stored OAuth bundle against the stub token endpoint.
	rec = api.auth(http.MethodPost, "/api/v1/oauth/connections/"+credentialID+"/refresh", "")
	wantStatus(t, rec, http.StatusOK)
	refreshed := mustDecode[struct {
		OK        bool   `json:"ok"`
		ExpiresAt string `json:"expires_at"`
	}](t, rec)
	if !refreshed.OK || refreshed.ExpiresAt == "" {
		t.Fatalf("refresh = %+v", refreshed)
	}
	rec = api.auth(http.MethodPost, "/api/v1/oauth/connections/cred_missing/refresh", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	plain := api.createCredential(t, map[string]any{
		"provider_id": "openai",
		"label":       "plain",
		"auth_type":   "api_key",
		"value":       "sk-test-plain-1234",
	})
	rec = api.auth(http.MethodPost, "/api/v1/oauth/connections/"+plain.ID+"/refresh", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	// A cancelled flow cannot be completed by a late callback.
	cancelID, cancelState := startOAuthFlow(t, api)
	rec = api.auth(http.MethodPost, "/api/v1/oauth/flows/"+cancelID+"/cancel", "")
	wantStatus(t, rec, http.StatusOK)
	cancelled := mustDecode[domain.OAuthFlow](t, rec)
	if cancelled.Status != domain.OAuthCancelled {
		t.Fatalf("cancelled flow = %+v", cancelled)
	}
	rec = api.auth(http.MethodPost, "/api/v1/oauth/flows/"+cancelID+"/cancel", "")
	wantStatus(t, rec, http.StatusOK)
	rec = api.request(http.MethodGet, "/api/v1/oauth/callback?code=late&state="+url.QueryEscape(cancelState), "", nil)
	wantStatus(t, rec, http.StatusBadRequest)

	// Deleting the connection revokes it.
	rec = api.auth(http.MethodDelete, "/api/v1/oauth/connections/"+credentialID, "")
	wantStatus(t, rec, http.StatusNoContent)
	rec = api.auth(http.MethodDelete, "/api/v1/oauth/connections/"+credentialID, "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}

func TestAPIOAuthImport(t *testing.T) {
	hostRoot := t.TempDir()
	api := newTestAPIWith(t, func(opts *core.Options) {
		opts.HostImportRoot = hostRoot
	})

	rec := api.auth(http.MethodGet, "/api/v1/oauth/importable", "")
	wantStatus(t, rec, http.StatusOK)
	importable := mustDecode[struct {
		Items []struct {
			ProviderID string `json:"provider_id"`
			Source     string `json:"source"`
			Available  bool   `json:"available"`
		} `json:"items"`
	}](t, rec)
	if len(importable.Items) != 3 {
		t.Fatalf("importable = %+v", importable.Items)
	}
	for _, item := range importable.Items {
		if item.Available {
			t.Fatalf("unexpectedly available host login: %+v", item)
		}
	}

	codexPath := filepath.Join(hostRoot, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte(`{"tokens":{"access_token":"sk-test-host-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rec = api.auth(http.MethodGet, "/api/v1/oauth/importable", "")
	wantStatus(t, rec, http.StatusOK)
	afterFile := mustDecode[struct {
		Items []struct {
			ProviderID string `json:"provider_id"`
			Available  bool   `json:"available"`
		} `json:"items"`
	}](t, rec)
	found := false
	for _, item := range afterFile.Items {
		if item.ProviderID == "codex" && item.Available {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex host login not reported: %+v", afterFile.Items)
	}

	rec = api.postJSON("/api/v1/oauth/import", map[string]any{"provider_id": "codex"})
	wantStatus(t, rec, http.StatusCreated)
	imported := mustDecode[domain.ImportResult](t, rec)
	if imported.CredentialID == "" || imported.Status != "connected" {
		t.Fatalf("import = %+v", imported)
	}
	rec = api.postJSON("/api/v1/oauth/import", map[string]any{"provider_id": "codex", "source": "host", "label": "host"})
	wantStatus(t, rec, http.StatusCreated)
	rec = api.postJSON("/api/v1/oauth/import", map[string]any{"provider_id": "openai"})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/oauth/import", map[string]any{"provider_id": "codex", "source": "elsewhere"})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodPost, "/api/v1/oauth/import", `{"provider_id":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	// Invalid host file content is a 400, a removed file is a 404.
	if err := os.WriteFile(codexPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = api.postJSON("/api/v1/oauth/import", map[string]any{"provider_id": "codex"})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	if err := os.Remove(codexPath); err != nil {
		t.Fatal(err)
	}
	rec = api.postJSON("/api/v1/oauth/import", map[string]any{"provider_id": "codex"})
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}

func TestAPIMCPConnections(t *testing.T) {
	fake := &fakeMCP{testTools: []string{"search", "read"}}
	api := newTestAPIWith(t, func(opts *core.Options) {
		opts.MCP = fake
	})

	rec := api.auth(http.MethodGet, "/api/v1/mcp/catalog", "")
	wantStatus(t, rec, http.StatusOK)
	catalog := mustDecode[struct {
		Items []core.MCPServerCatalogItem `json:"items"`
	}](t, rec).Items
	if len(catalog) < 2 {
		t.Fatalf("catalog = %+v", catalog)
	}

	rec = api.auth(http.MethodGet, "/api/v1/mcp/connections", "")
	wantStatus(t, rec, http.StatusOK)
	if page := mustDecode[struct {
		Items []domain.MCPConnection `json:"items"`
	}](t, rec); page.Items == nil {
		t.Fatal("connections items must be an array, not null")
	}

	rec = api.postJSON("/api/v1/mcp/connections", map[string]any{"server_id": "github", "label": "gh"})
	wantStatus(t, rec, http.StatusCreated)
	conn := mustDecode[domain.MCPConnection](t, rec)
	if conn.ServerID != "github" || conn.Status != "connected" {
		t.Fatalf("connection = %+v", conn)
	}

	rec = api.auth(http.MethodPost, "/api/v1/mcp/connections", `{"server_id":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/mcp/connections", map[string]any{"server_id": "nope", "label": "x"})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/mcp/connections", map[string]any{"server_id": "github"})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/mcp/connections", map[string]any{"server_id": "github", "label": "x", "credential_id": "cred_missing"})
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	cred := api.createCredential(t, map[string]any{
		"provider_id": "github",
		"label":       "mcp",
		"auth_type":   "pat",
		"value":       "sk-test-github-pat",
	})
	rec = api.postJSON("/api/v1/mcp/connections", map[string]any{
		"server_id":     "github",
		"label":         "with credential",
		"credential_id": cred.ID,
		"tools":         []string{"search"},
	})
	wantStatus(t, rec, http.StatusCreated)
	withCred := mustDecode[domain.MCPConnection](t, rec)
	if withCred.CredentialID == nil || *withCred.CredentialID != cred.ID {
		t.Fatalf("connection = %+v", withCred)
	}

	rec = api.auth(http.MethodGet, "/api/v1/mcp/connections?limit=1", "")
	wantStatus(t, rec, http.StatusOK)
	first := mustDecode[struct {
		Items      []domain.MCPConnection `json:"items"`
		NextCursor *string                `json:"next_cursor"`
	}](t, rec)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	rec = api.auth(http.MethodGet, "/api/v1/mcp/connections?limit=1&cursor="+*first.NextCursor, "")
	wantStatus(t, rec, http.StatusOK)
	rec = api.auth(http.MethodGet, "/api/v1/mcp/connections?cursor=%40%40%40", "")
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)

	rec = api.auth(http.MethodPost, "/api/v1/mcp/connections/"+conn.ID+"/test", "")
	wantStatus(t, rec, http.StatusOK)
	tested := mustDecode[struct {
		OK    bool     `json:"ok"`
		Tools []string `json:"tools"`
	}](t, rec)
	if !tested.OK || len(tested.Tools) != 2 {
		t.Fatalf("test connection = %+v", tested)
	}
	rec = api.auth(http.MethodPost, "/api/v1/mcp/connections/mcp_missing/test", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	fake.testErr = errors.New("upstream down")
	rec = api.auth(http.MethodPost, "/api/v1/mcp/connections/"+conn.ID+"/test", "")
	wantStatus(t, rec, http.StatusBadGateway)
	if env := decodeError(t, rec); string(env.Error.Code) != "mcp_unavailable" {
		t.Fatalf("code = %q, want mcp_unavailable", env.Error.Code)
	}
	fake.testErr = nil

	rec = api.auth(http.MethodPost, "/api/v1/mcp/connections/"+conn.ID+"/oauth/start", "")
	wantStatus(t, rec, http.StatusBadRequest)
	if env := decodeError(t, rec); string(env.Error.Code) != "oauth_unavailable" {
		t.Fatalf("code = %q, want oauth_unavailable", env.Error.Code)
	}
	rec = api.auth(http.MethodPost, "/api/v1/mcp/connections/mcp_missing/oauth/start", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	rec = api.auth(http.MethodDelete, "/api/v1/mcp/connections/"+conn.ID, "")
	wantStatus(t, rec, http.StatusNoContent)
	rec = api.auth(http.MethodDelete, "/api/v1/mcp/connections/"+conn.ID, "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}
