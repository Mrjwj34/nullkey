//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAC_CRED_001_ApiKeyLifecycle(t *testing.T) {
	d := startDaemon(t)
	secret := "sk-test-lifecycle-0123456789abcdef"
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "primary", "auth_type": "api_key", "value": secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", resp.StatusCode, raw)
	}
	cred := decodeJSON[struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
		Status      string `json:"status"`
	}](t, raw)
	if !strings.HasPrefix(cred.ID, "cred_") || cred.Status != "connected" || cred.Fingerprint == "" {
		t.Fatalf("unexpected credential: %+v", cred)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("create response contains plaintext")
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/credentials/"+cred.ID+"/test", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test credential = %d; body=%s", resp.StatusCode, raw)
	}
	var testResult struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &testResult); err != nil || !testResult.OK {
		t.Fatalf("credential test failed: %v %s", err, raw)
	}

	resp, raw = d.doAuthed(t, http.MethodDelete, "/credentials/"+cred.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d; body=%s", resp.StatusCode, raw)
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/credentials", nil)
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("credential still listed: %s", raw)
	}
	resp, _ = d.doAuthed(t, http.MethodPost, "/credentials/"+cred.ID+"/test", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("test deleted credential = %d, want 404", resp.StatusCode)
	}
	if strings.Contains(d.logs.String(), secret) {
		t.Fatal("daemon log contains plaintext")
	}
}

func TestAC_CRED_002_RevokeAffectsGateway(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test-upstream-0123456789" {
			t.Errorf("upstream authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "upstream", "auth_type": "api_key",
		"value": "sk-test-upstream-0123456789", "base_url": upstream.URL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create credential = %d; body=%s", resp.StatusCode, raw)
	}
	cred := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw)
	ws := d.createWorkspace(t, "revoke-gateway")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{"model":"x"}`), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("gateway before revoke = %d; body=%s", resp.StatusCode, raw)
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	if before != 1 {
		t.Fatalf("upstream calls = %d, want 1", before)
	}
	if resp, _ := d.doAuthed(t, http.MethodDelete, "/credentials/"+cred.ID, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete credential = %d", resp.StatusCode)
	}
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{"model":"x"}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("gateway after revoke = %d; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "reauth_required" {
		t.Fatalf("code = %q, want reauth_required", env.Error.Code)
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	if after != before {
		t.Fatalf("revoked credential still reached upstream: %d -> %d", before, after)
	}
}

func TestAC_CRED_003_OAuthPKCE(t *testing.T) {
	stub := newOAuthStub(t, 3600)
	defer stub.Close()
	d := startDaemonWithEnv(t, stub.Env())

	resp, raw := d.doAuthed(t, http.MethodPost, "/oauth/start", map[string]any{"provider_id": "codex", "label": "Codex"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("oauth start = %d; body=%s", resp.StatusCode, raw)
	}
	start := decodeJSON[struct {
		FlowID    string    `json:"flow_id"`
		AuthURL   string    `json:"auth_url"`
		ExpiresAt time.Time `json:"expires_at"`
	}](t, raw)
	if start.FlowID == "" || start.AuthURL == "" {
		t.Fatalf("incomplete start: %+v", start)
	}
	auth, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := auth.Query()
	state := q.Get("state")
	challenge := q.Get("code_challenge")
	if state == "" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "test-client" {
		t.Fatalf("auth url missing PKCE fields: %s", start.AuthURL)
	}
	if q.Get("redirect_uri") != "http://"+d.controlAddr()+"/api/v1/oauth/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}

	resp, raw = d.do(t, http.MethodGet, "/oauth/callback?code=auth-code&state="+url.QueryEscape(state), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback = %d; body=%s", resp.StatusCode, raw)
	}
	form := stub.LastForm()
	verifier := form.Get("code_verifier")
	if verifier == "" {
		t.Fatal("token exchange did not include code_verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		t.Fatal("PKCE verifier does not match the challenge")
	}
	if form.Get("code") != "auth-code" {
		t.Fatalf("token exchange code = %q", form.Get("code"))
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/oauth/flows/"+start.FlowID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("flow = %d; body=%s", resp.StatusCode, raw)
	}
	flow := decodeJSON[struct {
		Status       string  `json:"status"`
		CredentialID *string `json:"credential_id"`
	}](t, raw)
	if flow.Status != "succeeded" || flow.CredentialID == nil {
		t.Fatalf("flow = %+v", flow)
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/credentials", nil)
	if strings.Contains(string(raw), stub.AccessToken()) {
		t.Fatal("credential list contains the OAuth access token")
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/connections/"+*flow.CredentialID+"/refresh", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d; body=%s", resp.StatusCode, raw)
	}
	if stub.RefreshCalls() == 0 {
		t.Fatal("refresh did not call the token endpoint")
	}
}

func TestAC_CRED_004_HostImport(t *testing.T) {
	d := startDaemon(t)
	hostDir := filepath.Join(d.home, "host-import")
	writeFile(t, filepath.Join(hostDir, ".codex", "auth.json"), `{"tokens":{"access_token":"sk-test-imported-0123456789abcdef"}}`)
	writeFile(t, filepath.Join(hostDir, ".config", "gh", "hosts.yml"), "github.com:\n    oauth_token: ghp_testimport0123456789abcdef\n    user: tester\n")

	resp, raw := d.doAuthed(t, http.MethodGet, "/oauth/importable", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("importable = %d; body=%s", resp.StatusCode, raw)
	}
	var importable struct {
		Items []struct {
			ProviderID string `json:"provider_id"`
			Source     string `json:"source"`
			Available  bool   `json:"available"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &importable); err != nil {
		t.Fatalf("decode importable: %v", err)
	}
	available := map[string]bool{}
	for _, item := range importable.Items {
		available[item.ProviderID] = item.Available
	}
	if !available["codex"] || !available["github"] {
		t.Fatalf("host logins not detected: %s", raw)
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/import", map[string]any{"provider_id": "codex", "source": ".codex/auth.json"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import codex = %d; body=%s", resp.StatusCode, raw)
	}
	result := decodeJSON[struct {
		CredentialID string `json:"credential_id"`
		Status       string `json:"status"`
	}](t, raw)
	if result.CredentialID == "" || result.Status == "connected" {
		if result.CredentialID == "" {
			t.Fatalf("missing credential id: %+v", result)
		}
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/credentials", nil)
	if strings.Contains(string(raw), "sk-test-imported") {
		t.Fatal("imported credential plaintext returned")
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/import", map[string]any{"provider_id": "github", "source": ".config/gh/hosts.yml"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import github = %d; body=%s", resp.StatusCode, raw)
	}
}

func TestAC_CRED_005_ProviderRegistry(t *testing.T) {
	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodGet, "/providers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("providers = %d; body=%s", resp.StatusCode, raw)
	}
	var list struct {
		Items []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			AuthTypes []string `json:"auth_types"`
			BaseURL   *string  `json:"base_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range list.Items {
		seen[item.ID] = true
		if len(item.AuthTypes) == 0 {
			t.Fatalf("provider %s has no auth_types", item.ID)
		}
		if item.ID == "openai" && (item.BaseURL == nil || *item.BaseURL == "") {
			t.Fatalf("openai base_url missing")
		}
	}
	for _, want := range []string{"codex", "openai", "anthropic", "github"} {
		if !seen[want] {
			t.Fatalf("provider %q missing from %s", want, want)
		}
	}
}

// oauthStub is a controlled token endpoint.
type oauthStub struct {
	server    *httptest.Server
	expiresIn int
	mu        sync.Mutex
	forms     []url.Values
	refreshes int
	access    string
}

func newOAuthStub(t *testing.T, expiresIn int) *oauthStub {
	t.Helper()
	s := &oauthStub{expiresIn: expiresIn, access: "sk-test-oauth-access-0123456789"}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			w.WriteHeader(http.StatusOK)
		case "/token":
			_ = r.ParseForm()
			s.mu.Lock()
			form := r.PostForm
			s.forms = append(s.forms, form)
			if form.Get("grant_type") == "refresh_token" {
				s.refreshes++
				s.access = "sk-test-oauth-refreshed-0123456789"
			}
			access := s.access
			expires := s.expiresIn
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": access, "refresh_token": "refresh-token-0123456789",
				"token_type": "Bearer", "expires_in": expires,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	return s
}

func (s *oauthStub) Close() { s.server.Close() }

func (s *oauthStub) Env() map[string]string {
	return map[string]string{
		"NULLKEY_OAUTH_CODEX_AUTH_URL":  s.server.URL + "/authorize",
		"NULLKEY_OAUTH_CODEX_TOKEN_URL": s.server.URL + "/token",
		"NULLKEY_OAUTH_CODEX_CLIENT_ID": "test-client",
		"NULLKEY_OAUTH_CODEX_SCOPES":    "openid profile",
	}
}

func (s *oauthStub) LastForm() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.forms) == 0 {
		return url.Values{}
	}
	return s.forms[len(s.forms)-1]
}

func (s *oauthStub) RefreshCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

func (s *oauthStub) AccessToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access
}

func (d *daemon) controlAddr() string {
	return strings.TrimPrefix(strings.TrimSuffix(d.baseURL, "/api/v1"), "http://")
}

var _ = os.Getenv
