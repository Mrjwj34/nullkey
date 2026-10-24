package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func testJWT(t *testing.T, exp int64, account string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := map[string]any{"exp": exp}
	if account != "" {
		claims[codexAccountClaim] = map[string]any{"chatgpt_account_id": account}
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestParseCodexHostLoginComplete(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour).Unix()
	access := testJWT(t, exp, "acct-from-jwt")
	idToken := testJWT(t, exp, "acct-from-id")
	raw := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"rt.1.test","account_id":"acct-explicit"}}`, access, idToken)
	login, ok := parseCodexHostLogin([]byte(raw))
	if !ok {
		t.Fatal("parse failed")
	}
	if login.AccessToken != access || login.RefreshToken != "rt.1.test" || login.IDToken != idToken {
		t.Fatalf("login = %+v", login)
	}
	if login.AccountID != "acct-explicit" {
		t.Fatalf("account = %q", login.AccountID)
	}
	if login.ExpiresAt == nil || login.ExpiresAt.Unix() != exp {
		t.Fatalf("expiry = %v", login.ExpiresAt)
	}

	// Without tokens.account_id, the id_token claim is authoritative.
	raw = fmt.Sprintf(`{"tokens":{"access_token":%q,"id_token":%q,"refresh_token":"r"}}`, access, idToken)
	login, _ = parseCodexHostLogin([]byte(raw))
	if login.AccountID != "acct-from-id" {
		t.Fatalf("claim account = %q", login.AccountID)
	}
}

func TestImportHostLoginPreservesCodexBundle(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, func(o *Options) { o.HostImportRoot = root })
	exp := time.Now().Add(90 * time.Minute).Unix()
	access := testJWT(t, exp, "acct-1")
	raw := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"rt.1.secret","account_id":"acct-1"}}`,
		access, testJWT(t, exp, "acct-1"))
	writeHostFile(t, root, ".codex/auth.json", raw)

	result, err := svc.ImportHostLogin(context.Background(), "codex", "", "chatgpt")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	cred, err := svc.Store.GetCredential(context.Background(), result.CredentialID)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.ExpiresAt == nil || cred.ExpiresAt.Unix() != exp {
		t.Fatalf("expiry = %v", cred.ExpiresAt)
	}
	stored, err := svc.Secrets.Get(context.Background(), cred.KeychainAccount)
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	bundle, ok := decodeOAuthSecret(stored)
	if !ok {
		t.Fatalf("bundle not encoded: %s", stored)
	}
	if bundle.AccessToken != access || bundle.RefreshToken != "rt.1.secret" || bundle.AccountID != "acct-1" || bundle.IDToken == "" {
		t.Fatalf("bundle = %+v", bundle)
	}
	if bundle.ExpiresAt.Unix() != exp {
		t.Fatalf("bundle expiry = %v", bundle.ExpiresAt)
	}
	// The importable view must expose the JWT expiry too.
	for _, item := range svc.ImportableLogins() {
		if item.ProviderID == "codex" && (item.ExpiresAt == nil || item.ExpiresAt.Unix() != exp) {
			t.Fatalf("importable expiry = %+v", item)
		}
	}
}

func TestCredentialBundleRefreshesCodexAndPreservesIdentity(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "codex", Label: "oauth", AuthType: "oauth", Value: "old-access", BaseURL: "https://chatgpt.com/backend-api/codex",
	})
	old := oauthSecret{
		AccessToken: "old-access", RefreshToken: "old-refresh", IDToken: "old-id", AccountID: "acct-old",
		ExpiresAt: svc.now().Add(-time.Minute),
	}
	if err := svc.Secrets.Put(ctx, cred.KeychainAccount, encodeOAuthBundle(old)); err != nil {
		t.Fatalf("put: %v", err)
	}
	var clientID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		clientID = r.Form.Get("client_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  testJWT(t, svc.now().Add(time.Hour).Unix(), "acct-new"),
			"refresh_token": "new-refresh",
			"id_token":      testJWT(t, svc.now().Add(time.Hour).Unix(), "acct-new"),
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	t.Setenv("NULLKEY_OAUTH_CODEX_TOKEN_URL", server.URL)

	bundle, err := svc.credentialBundleFor(ctx, cred)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if bundle.AccessToken == "old-access" || bundle.RefreshToken != "new-refresh" || bundle.AccountID != "acct-new" {
		t.Fatalf("bundle = %+v", bundle)
	}
	if clientID != codexOAuthClientID {
		t.Fatalf("client_id = %q", clientID)
	}
	if bundle.ExpiresAt.Before(svc.now().Add(50 * time.Minute)) {
		t.Fatalf("expiry = %v", bundle.ExpiresAt)
	}
	reread, err := svc.Store.GetCredential(ctx, cred.ID)
	if err != nil || reread.Status != domain.CredentialConnected || reread.ExpiresAt == nil {
		t.Fatalf("credential = %+v, %v", reread, err)
	}
	evs, _, err := svc.Audit.List(ctx, 20, "", "oauth.refreshed")
	if err != nil || len(evs) == 0 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestModelGatewayCodexInjectsAccountAndStripsSandboxHeader(t *testing.T) {
	var gotAuth, gotAccount, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "codex", Label: "oauth", AuthType: "oauth", Value: "placeholder", BaseURL: upstream.URL,
	})
	bundle := oauthSecret{AccessToken: "real-access", RefreshToken: "refresh", AccountID: "acct-real", ExpiresAt: svc.now().Add(time.Hour)}
	if err := svc.Secrets.Put(ctx, cred.KeychainAccount, encodeOAuthBundle(bundle)); err != nil {
		t.Fatalf("put: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://gateway/openai/responses", strings.NewReader(`{"model":"gpt-6-luna"}`))
	req.Header.Set("Authorization", "Bearer sandbox-token")
	req.Header.Set("ChatGPT-Account-Id", "attacker-chosen")
	rec := httptest.NewRecorder()
	if err := svc.ModelGateway(ctx, domain.Sandbox{ID: "sbx"}, "openai", rec, req); err != nil {
		t.Fatalf("ModelGateway: %v", err)
	}
	if gotAuth != "Bearer real-access" || gotAccount != "acct-real" || gotPath != "/responses" {
		t.Fatalf("upstream = %q %q %q", gotAuth, gotAccount, gotPath)
	}
}

func TestCodexBackendGatewayAllowlistAndIdentity(t *testing.T) {
	var gotPath, gotAuth, gotAccount string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "codex", Label: "oauth", AuthType: "oauth", Value: "placeholder", BaseURL: upstream.URL + "/backend-api/codex",
	})
	bundle := oauthSecret{AccessToken: "real-access", RefreshToken: "refresh", AccountID: "acct-real", ExpiresAt: svc.now().Add(time.Hour)}
	if err := svc.Secrets.Put(ctx, cred.KeychainAccount, encodeOAuthBundle(bundle)); err != nil {
		t.Fatalf("put: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://gateway/backend-api/wham/accounts/check", nil)
	req.Header.Set("ChatGPT-Account-Id", "attacker-chosen")
	rec := httptest.NewRecorder()
	if err := svc.CodexBackendGateway(ctx, domain.Sandbox{ID: "sbx"}, rec, req); err != nil {
		t.Fatalf("backend: %v", err)
	}
	if gotPath != "/backend-api/wham/accounts/check" || gotAuth != "Bearer real-access" || gotAccount != "acct-real" {
		t.Fatalf("upstream = %q %q %q", gotPath, gotAuth, gotAccount)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	bad := httptest.NewRequest(http.MethodGet, "http://gateway/backend-api/admin/delete", nil)
	if err := svc.CodexBackendGateway(ctx, domain.Sandbox{ID: "sbx"}, httptest.NewRecorder(), bad); err == nil {
		t.Fatal("disallowed path was proxied")
	}
	post := httptest.NewRequest(http.MethodPost, "http://gateway/backend-api/wham/settings/user", nil)
	if err := svc.CodexBackendGateway(ctx, domain.Sandbox{ID: "sbx"}, httptest.NewRecorder(), post); err == nil {
		t.Fatal("disallowed method was proxied")
	}
}

func TestWriteGeneratedCodexConfig(t *testing.T) {
	svc, _ := newTestService(t)
	caDir := t.TempDir()
	caPath := filepath.Join(caDir, "ca.pem")
	if err := os.WriteFile(caPath, []byte("TEST CA"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.GatewayAddr = "127.0.0.1:49153"
	svc.GatewayTLSURL = "https://127.0.0.1:49153"
	svc.GatewayCAPath = caPath
	state := t.TempDir()
	env, err := svc.writeGeneratedConfig(state, "sbx_test", domain.NetworkFiltered, "codex", "/private/tmp/nk-ws")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	configPath := filepath.Join(state, "home", ".codex", "config.toml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(config)
	for _, want := range []string{
		`model_provider = "nullkey"`,
		`base_url = "https://127.0.0.1:49153/openai"`,
		`chatgpt_base_url = "https://127.0.0.1:49153/backend-api"`,
		`requires_openai_auth = false`,
		`daemon_auto_start = false`,
		`plugins = false`,
		// Codex cannot nest its own Seatbelt sandbox inside the sandbox profile;
		// without these it stops on folder trust and per-command approval.
		`approval_policy = "never"`,
		`sandbox_mode = "danger-full-access"`,
		"[projects.\"/private/tmp/nk-ws\"]\ntrust_level = \"trusted\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sk-") {
		t.Fatalf("config contains a credential-looking value:\n%s", text)
	}
	if strings.Contains(text, "model_catalog_url") {
		t.Fatal("without ChatGPT OAuth, the model catalog must not require a ChatGPT login")
	}
	info, err := os.Stat(configPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v", info.Mode().Perm(), err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CODEX_CA_CERTIFICATE="+filepath.Join(state, "nullkey", "gateway-ca.pem")) {
		t.Fatalf("env = %v", env)
	}
	if !strings.Contains(joined, "CODEX_HOME="+filepath.Join(state, "home", ".codex")) {
		t.Fatalf("env = %v", env)
	}
	copied, err := os.ReadFile(filepath.Join(state, "nullkey", "gateway-ca.pem"))
	if err != nil || string(copied) != "TEST CA" {
		t.Fatalf("copied CA = %q, %v", copied, err)
	}
	// A non-codex harness gets the CA but no Codex config.
	other := t.TempDir()
	if _, err := svc.writeGeneratedConfig(other, "sbx_other", domain.NetworkFiltered, "shell", ""); err != nil {
		t.Fatalf("shell config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "home", ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("codex config created for shell: %v", err)
	}
}

func TestCodexCatalogMatchesSelectedCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := domain.Credential{
		ID: "cred_chatgpt", ProviderID: "codex", Label: "test",
		AuthType: domain.AuthOAuth, Status: domain.CredentialConnected,
		KeychainAccount: "cred_chatgpt", CreatedAt: svc.now(),
	}
	if err := svc.Store.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	check := func(wantCatalog bool) {
		t.Helper()
		if _, err := svc.writeGeneratedConfig(state, "sbx", domain.NetworkFiltered, "codex", "/tmp/workspace"); err != nil {
			t.Fatal(err)
		}
		config, err := os.ReadFile(filepath.Join(state, "home", ".codex", "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(config), "model_catalog_url") != wantCatalog {
			t.Fatalf("ChatGPT catalog enabled = %v, want %v", !wantCatalog, wantCatalog)
		}
	}
	check(true)
	if _, err := svc.CreateCredential(ctx, CreateCredentialRequest{
		ProviderID: "openai", Label: "API key", AuthType: "api_key", Value: "sk-test-fake",
	}); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestModelUpstreamCodexAPIKeyUsesOpenAI(t *testing.T) {
	cred := domain.Credential{ProviderID: "codex", AuthType: domain.AuthAPIKey, BaseURL: "https://chatgpt.com/backend-api/codex"}
	provider, _ := providerByID("codex")
	if got := modelUpstreamBase(cred, provider, "codex"); got != "https://api.openai.com/v1" {
		t.Fatalf("base = %q", got)
	}
}
