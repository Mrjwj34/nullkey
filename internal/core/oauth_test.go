package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/secret"
)

func setOAuthEnv(t *testing.T, provider, authURL, tokenURL, clientID, clientSecret, scopes string) {
	t.Helper()
	prefix := "NULLKEY_OAUTH_" + strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_"
	t.Setenv(prefix+"AUTH_URL", authURL)
	t.Setenv(prefix+"TOKEN_URL", tokenURL)
	t.Setenv(prefix+"CLIENT_ID", clientID)
	t.Setenv(prefix+"CLIENT_SECRET", clientSecret)
	t.Setenv(prefix+"SCOPES", scopes)
}

// unsetOAuthEnv restores the "not configured" state; t.Setenv cannot unset.
func unsetOAuthEnv(t *testing.T, provider string) {
	t.Helper()
	prefix := "NULLKEY_OAUTH_" + strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_"
	for _, suffix := range []string{
		"AUTH_URL", "TOKEN_URL", "CLIENT_ID", "CLIENT_SECRET", "SCOPES",
		"REDIRECT_URL", "CALLBACK_LISTEN",
	} {
		key := prefix + suffix
		old, ok := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	return port
}

func TestCodexOAuthDefaults(t *testing.T) {
	unsetOAuthEnv(t, "codex")
	svc, _ := newTestService(t)
	cfg := svc.oauthConfig("codex")
	if !cfg.available() {
		t.Fatalf("codex OAuth should be available by default: %+v", cfg)
	}
	if cfg.clientID != codexOAuthClientID || cfg.redirectURL != codexOAuthRedirectURL || cfg.callbackAddr != codexOAuthCallbackAddr {
		t.Fatalf("codex defaults = %+v", cfg)
	}
	if cfg.authParams["originator"] != "codex_cli_rs" || cfg.authParams["codex_cli_simplified_flow"] != "true" {
		t.Fatalf("codex auth params = %+v", cfg.authParams)
	}
	joined := strings.Join(cfg.scopes, " ")
	if !strings.Contains(joined, "api.connectors.invoke") {
		t.Fatalf("codex scopes = %v", cfg.scopes)
	}
	providers := svc.OAuthProviders()
	if len(providers) == 0 || providers[0].ID != "codex" || providers[0].Status != "available" {
		t.Fatalf("providers = %+v", providers)
	}
}

func TestCodexOAuthCallbackListener(t *testing.T) {
	svc, _ := newTestService(t)
	t.Cleanup(svc.closeOAuthCallbacks)
	ctx := context.Background()
	server := tokenServer(t, "access-token", "refresh-token", 3600)
	defer server.Close()

	port := freeLoopbackPort(t)
	setOAuthEnv(t, "codex", "https://auth.example.test/authorize", server.URL, "client-id", "client-secret", "openid")
	redirect := fmt.Sprintf("http://127.0.0.1:%s/auth/callback", port)
	t.Setenv("NULLKEY_OAUTH_CODEX_REDIRECT_URL", redirect)
	t.Setenv("NULLKEY_OAUTH_CODEX_CALLBACK_LISTEN", "127.0.0.1:"+port)

	flow, authURL, err := svc.StartOAuth(ctx, "codex", "listener")
	if err != nil {
		t.Fatalf("start with callback listener: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != redirect {
		t.Fatalf("redirect_uri = %q, want %q", got, redirect)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("state missing")
	}

	// A wrong state is rejected without writing a credential.
	resp, err := http.Get(redirect + "?code=the-code&state=wrong")
	if err != nil {
		t.Fatalf("bad callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad callback status = %d", resp.StatusCode)
	}
	if creds, _, err := svc.ListCredentials(ctx, 50, ""); err != nil || len(creds) != 0 {
		t.Fatalf("bad callback wrote credentials: %d, %v", len(creds), err)
	}

	resp, err = http.Get(redirect + "?code=the-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body[:n]), "登录完成") {
		t.Fatalf("callback = %d %q", resp.StatusCode, string(body[:n]))
	}
	stored, err := svc.GetOAuthFlow(ctx, flow.ID)
	if err != nil || stored.Status != domain.OAuthSucceeded {
		t.Fatalf("flow = %+v, %v", stored, err)
	}
	if creds, _, err := svc.ListCredentials(ctx, 50, ""); err != nil || len(creds) != 1 {
		t.Fatalf("credentials = %d, %v", len(creds), err)
	}
}

func TestCodexOAuthCallbackPortInUse(t *testing.T) {
	svc, _ := newTestService(t)
	t.Cleanup(svc.closeOAuthCallbacks)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	setOAuthEnv(t, "codex", "https://auth.example.test/authorize", "https://auth.example.test/token", "client-id", "secret", "openid")
	t.Setenv("NULLKEY_OAUTH_CODEX_REDIRECT_URL", "http://127.0.0.1:"+port+"/auth/callback")
	t.Setenv("NULLKEY_OAUTH_CODEX_CALLBACK_LISTEN", "127.0.0.1:"+port)

	_, _, err = svc.StartOAuth(context.Background(), "codex", "busy")
	wantError(t, err, http.StatusConflict, "oauth_unavailable")
}

// startTestFlow configures a provider and starts an OAuth flow, returning the
// flow and its raw state value (parsed from the authorization URL).
func startTestFlow(t *testing.T, svc *Service, provider, tokenURL string) (domain.OAuthFlow, string) {
	t.Helper()
	setOAuthEnv(t, provider, "https://auth.example.test/authorize", tokenURL, "client-id", "client-secret", "openid repo")
	flow, authURL, err := svc.StartOAuth(context.Background(), provider, "label")
	if err != nil {
		t.Fatalf("start oauth: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("auth url has no state: %s", authURL)
	}
	return flow, state
}

func insertOAuthCredential(t *testing.T, svc *Service, provider, access, refresh string) domain.Credential {
	t.Helper()
	ctx := context.Background()
	id := newID("cred_")
	cred := domain.Credential{
		ID: id, ProviderID: provider, Label: "oauth", AuthType: domain.AuthOAuth,
		Fingerprint: fingerprint(access), Status: domain.CredentialConnected,
		KeychainAccount: id, BaseURL: providerBaseURL(provider), CreatedAt: svc.now(),
	}
	if err := svc.Store.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("create oauth credential: %v", err)
	}
	if err := svc.Secrets.Put(ctx, id, encodeOAuthSecret(access, refresh, time.Time{})); err != nil {
		t.Fatalf("put oauth secret: %v", err)
	}
	return cred
}

func TestNewOAuthBroker(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.oauth == nil || svc.oauth.svc != svc || svc.oauth.verifiers == nil {
		t.Fatalf("broker not initialized: %+v", svc.oauth)
	}
}

func TestStartOAuthUnavailable(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	setOAuthEnv(t, "codex", "", "", "", "", "")
	_, _, err := svc.StartOAuth(ctx, "codex", "")
	wantError(t, err, http.StatusBadRequest, "oauth_unavailable")
	_, _, err = svc.StartOAuth(ctx, "unknown", "")
	wantError(t, err, http.StatusBadRequest, "oauth_unavailable")
}

func TestStartOAuthSuccess(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	setOAuthEnv(t, "codex", "https://auth.example.test/authorize", "https://auth.example.test/token", "client-1", "secret-1", "openid email")
	flow, authURL, err := svc.StartOAuth(ctx, "codex", "my label")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if flow.ProviderID != "codex" || flow.Status != domain.OAuthPending || len(flow.StateHash) != 32 {
		t.Fatalf("flow = %+v", flow)
	}
	if ttl := flow.ExpiresAt.Sub(flow.CreatedAt); ttl < 10*time.Minute || ttl > 10*time.Minute+time.Second {
		t.Fatalf("flow ttl = %v", ttl)
	}
	svc.oauth.mu.Lock()
	verifier := svc.oauth.verifiers[flow.ID]
	svc.oauth.mu.Unlock()
	if verifier == "" {
		t.Fatalf("PKCE verifier not kept")
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "client-1" {
		t.Fatalf("auth query = %v", q)
	}
	if q.Get("redirect_uri") != "http://127.0.0.1:49152/api/v1/oauth/callback" {
		t.Fatalf("redirect = %q", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("PKCE params = %v", q)
	}
	if q.Get("scope") != "openid email" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	if got := q.Get("state"); got == "" {
		t.Fatalf("state missing")
	}

	evs, _, err := svc.Audit.List(ctx, 50, "", "oauth.started")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}

	// An authorization URL that already has a query string is extended with &.
	setOAuthEnv(t, "codex", "https://auth.example.test/authorize?prompt=login", "https://auth.example.test/token", "client-1", "", "")
	_, authURL2, err := svc.StartOAuth(ctx, "codex", "")
	if err != nil {
		t.Fatalf("start with query: %v", err)
	}
	parsed2, err := url.Parse(authURL2)
	if err != nil || parsed2.Query().Get("prompt") != "login" || parsed2.Query().Get("state") == "" {
		t.Fatalf("auth url with query = %s (%v)", authURL2, err)
	}

	// callbackURL honors ControlAddr and falls back to the default.
	svc.ControlAddr = ""
	if got := svc.callbackURL(); got != "http://127.0.0.1:49152/api/v1/oauth/callback" {
		t.Fatalf("default callback = %q", got)
	}
}

func TestStartOAuthStoreError(t *testing.T) {
	svc, _ := newTestService(t)
	setOAuthEnv(t, "codex", "https://auth.example.test/authorize", "https://auth.example.test/token", "client", "", "")
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, _, err := svc.StartOAuth(context.Background(), "codex", "")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestCompleteOAuthStoreError(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := svc.CompleteOAuth(context.Background(), "code", "state")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestCompleteOAuthConfigVanishes(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, state := startTestFlow(t, svc, "codex", "https://auth.example.test/token")
	setOAuthEnv(t, "codex", "", "", "", "", "")
	_, err := svc.CompleteOAuth(ctx, "code", state)
	wantError(t, err, http.StatusBadRequest, "oauth_unavailable")
}

func TestCompleteOAuthInvalidTokenURL(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, state := startTestFlow(t, svc, "codex", "://bad-token-url")
	_, err := svc.CompleteOAuth(ctx, "code", state)
	wantError(t, err, http.StatusBadGateway, "oauth_failed")
}

func TestCompleteOAuthValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.CompleteOAuth(ctx, "", "state")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = svc.CompleteOAuth(ctx, "code", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = svc.CompleteOAuth(ctx, "code", "unknown-state")
	wantError(t, err, http.StatusBadRequest, "oauth_state_invalid")
}

func TestCompleteOAuthExpired(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	state := "expired-state"
	hash := hashToken(state)
	flow := domain.OAuthFlow{
		ID: newID("flow_"), ProviderID: "codex", Status: domain.OAuthPending,
		StateHash: hash, CreatedAt: svc.now().Add(-20 * time.Minute), ExpiresAt: svc.now().Add(-time.Minute),
	}
	if err := svc.Store.CreateOAuthFlow(ctx, flow); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	svc.oauth.mu.Lock()
	svc.oauth.verifiers[flow.ID] = "verifier"
	svc.oauth.mu.Unlock()

	_, err := svc.CompleteOAuth(ctx, "code", state)
	ce := wantError(t, err, http.StatusBadRequest, "oauth_state_invalid")
	if !strings.Contains(ce.Message, "expired") {
		t.Fatalf("message = %q", ce.Message)
	}
	stored, err := svc.Store.GetOAuthFlow(ctx, flow.ID)
	if err != nil || stored.Status != domain.OAuthExpired {
		t.Fatalf("flow = %+v, %v", stored, err)
	}
}

func TestCompleteOAuthNoVerifier(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	state := "no-verifier"
	flow := domain.OAuthFlow{
		ID: newID("flow_"), ProviderID: "codex", Status: domain.OAuthPending,
		StateHash: hashToken(state), CreatedAt: svc.now(), ExpiresAt: svc.now().Add(time.Minute),
	}
	if err := svc.Store.CreateOAuthFlow(ctx, flow); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	_, err := svc.CompleteOAuth(ctx, "code", state)
	ce := wantError(t, err, http.StatusBadRequest, "oauth_state_invalid")
	if !strings.Contains(ce.Message, "PKCE") {
		t.Fatalf("message = %q", ce.Message)
	}
}

func TestCompleteOAuthExchangeFailures(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		},
		"invalid-json": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not-json"))
		},
		"oauth-error": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad"}`))
		},
		"no-access-token": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"token_type":"bearer"}`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _ := newTestService(t)
			ctx := context.Background()
			server := httptest.NewServer(handler)
			defer server.Close()
			flow, state := startTestFlow(t, svc, "codex", server.URL)
			_, err := svc.CompleteOAuth(ctx, "code", state)
			wantError(t, err, http.StatusBadGateway, "oauth_failed")
			stored, err := svc.Store.GetOAuthFlow(ctx, flow.ID)
			if err != nil || stored.Status != domain.OAuthFailed || stored.Error == nil {
				t.Fatalf("flow = %+v, %v", stored, err)
			}
			svc.oauth.mu.Lock()
			_, kept := svc.oauth.verifiers[flow.ID]
			svc.oauth.mu.Unlock()
			if kept {
				t.Fatalf("verifier not cleared")
			}
		})
	}
}

func tokenServer(t *testing.T, access, refresh string, expiresIn int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "the-code" {
			t.Errorf("code = %q", r.Form.Get("code"))
		}
		if r.Form.Get("client_id") != "client-id" || r.Form.Get("client_secret") != "client-secret" {
			t.Errorf("client = %q/%q", r.Form.Get("client_id"), r.Form.Get("client_secret"))
		}
		if r.Form.Get("code_verifier") == "" {
			t.Errorf("missing PKCE verifier")
		}
		if r.Form.Get("redirect_uri") == "" {
			t.Errorf("missing redirect_uri")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"expires_in":    expiresIn,
		})
	}))
}

func TestCompleteOAuthSuccess(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	server := tokenServer(t, "access-token", "refresh-token", 3600)
	defer server.Close()
	flow, state := startTestFlow(t, svc, "codex", server.URL)
	ch, cancel, err := svc.Events.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	completed, err := svc.CompleteOAuth(ctx, "the-code", state)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Status != domain.OAuthSucceeded || completed.CredentialID == nil {
		t.Fatalf("flow = %+v", completed)
	}
	stored, err := svc.Store.GetOAuthFlow(ctx, flow.ID)
	if err != nil || stored.Status != domain.OAuthSucceeded || stored.StateHash != nil {
		t.Fatalf("stored flow = %+v, %v", stored, err)
	}
	cred, err := svc.Store.GetCredential(ctx, *completed.CredentialID)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.AuthType != domain.AuthOAuth || cred.ProviderID != "codex" || cred.ExpiresAt == nil {
		t.Fatalf("credential = %+v", cred)
	}
	if got := cred.ExpiresAt.Sub(svc.now()); got < 59*time.Minute || got > 61*time.Minute {
		t.Fatalf("expiry delta = %v", got)
	}
	value, err := svc.CredentialSecret(ctx, cred)
	if err != nil || value != "access-token" {
		t.Fatalf("secret = %q, %v", value, err)
	}
	raw, err := svc.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil {
		t.Fatalf("raw secret: %v", err)
	}
	access, refresh, expiry := decodeSecret(raw)
	if access != "access-token" || refresh != "refresh-token" || expiry.IsZero() {
		t.Fatalf("stored bundle = %q %q %v", access, refresh, expiry)
	}

	// The one-time state cannot be replayed.
	_, err = svc.CompleteOAuth(ctx, "the-code", state)
	wantError(t, err, http.StatusBadRequest, "oauth_state_invalid")

	select {
	case ev := <-ch:
		if ev.Type != "oauth.succeeded" {
			t.Fatalf("event = %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("success event not published")
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "oauth.succeeded")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestCompleteOAuthStoresIDTokenAccount(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	idToken := testJWT(t, time.Now().Add(time.Hour).Unix(), "acct-oauth")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"id_token":      idToken,
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	setOAuthEnv(t, "codex", "https://auth.example.test/authorize", server.URL, "client-id", "client-secret", "openid")
	_, state := startTestFlow(t, svc, "codex", server.URL)
	completed, err := svc.CompleteOAuth(ctx, "the-code", state)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	cred, err := svc.Store.GetCredential(ctx, *completed.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := svc.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil {
		t.Fatal(err)
	}
	bundle, ok := decodeOAuthSecret(raw)
	if !ok {
		t.Fatalf("stored secret is not an OAuth bundle: %q", raw)
	}
	if bundle.IDToken != idToken || bundle.AccountID != "acct-oauth" {
		t.Fatalf("bundle = id_token %q account %q", bundle.IDToken, bundle.AccountID)
	}
}

func TestCompleteOAuthZeroExpiry(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	server := tokenServer(t, "access", "", 0)
	defer server.Close()
	_, state := startTestFlow(t, svc, "codex", server.URL)
	completed, err := svc.CompleteOAuth(ctx, "the-code", state)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	cred, err := svc.Store.GetCredential(ctx, *completed.CredentialID)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.ExpiresAt != nil {
		t.Fatalf("zero expires_in should not set expiry: %v", cred.ExpiresAt)
	}
}

func TestOAuthCancelVsLateCallback(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	server := tokenServer(t, "access", "refresh", 3600)
	defer server.Close()
	flow, state := startTestFlow(t, svc, "codex", server.URL)

	cancelled, err := svc.CancelOAuthFlow(ctx, flow.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != domain.OAuthCancelled || cancelled.StateHash != nil {
		t.Fatalf("cancelled flow = %+v", cancelled)
	}
	svc.oauth.mu.Lock()
	_, kept := svc.oauth.verifiers[flow.ID]
	svc.oauth.mu.Unlock()
	if kept {
		t.Fatalf("verifier survived cancellation")
	}
	again, err := svc.CancelOAuthFlow(ctx, flow.ID)
	if err != nil || again.ID != flow.ID {
		t.Fatalf("repeat cancel = %+v, %v", again, err)
	}

	_, err = svc.CompleteOAuth(ctx, "the-code", state)
	wantError(t, err, http.StatusBadRequest, "oauth_state_invalid")
	if creds, _, err := svc.ListCredentials(ctx, 50, ""); err != nil || len(creds) != 0 {
		t.Fatalf("late callback wrote a credential: %d, %v", len(creds), err)
	}

	// A completed flow cannot be cancelled afterwards.
	flow2, state2 := startTestFlow(t, svc, "codex", server.URL)
	if _, err := svc.CompleteOAuth(ctx, "the-code", state2); err != nil {
		t.Fatalf("complete second: %v", err)
	}
	_, err = svc.CancelOAuthFlow(ctx, flow2.ID)
	wantError(t, err, http.StatusConflict, "conflict")

	_, err = svc.CancelOAuthFlow(ctx, "flow_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestGetOAuthFlow(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.GetOAuthFlow(ctx, "flow_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	flow := domain.OAuthFlow{
		ID: newID("flow_"), ProviderID: "codex", Status: domain.OAuthPending,
		StateHash: hashToken("state"), CreatedAt: svc.now().Add(-time.Hour), ExpiresAt: svc.now().Add(-time.Minute),
	}
	if err := svc.Store.CreateOAuthFlow(ctx, flow); err != nil {
		t.Fatalf("create flow: %v", err)
	}
	svc.oauth.mu.Lock()
	svc.oauth.verifiers[flow.ID] = "verifier"
	svc.oauth.mu.Unlock()
	got, err := svc.GetOAuthFlow(ctx, flow.ID)
	if err != nil || got.Status != domain.OAuthExpired {
		t.Fatalf("get = %+v, %v", got, err)
	}
	svc.oauth.mu.Lock()
	_, kept := svc.oauth.verifiers[flow.ID]
	svc.oauth.mu.Unlock()
	if kept {
		t.Fatalf("expired flow kept its verifier")
	}
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = svc.GetOAuthFlow(ctx, flow.ID)
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestRefreshOAuth(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.RefreshOAuth(ctx, "cred_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	plain := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "plain", AuthType: "api_key", Value: "v",
	})
	_, err = svc.RefreshOAuth(ctx, plain.ID)
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	missingSecret := domain.Credential{
		ID: newID("cred_"), ProviderID: "github", Label: "x", AuthType: domain.AuthOAuth,
		Status: domain.CredentialConnected, CreatedAt: svc.now(),
	}
	missingSecret.KeychainAccount = missingSecret.ID
	if err := svc.Store.CreateCredential(ctx, missingSecret); err != nil {
		t.Fatalf("create cred: %v", err)
	}
	_, err = svc.RefreshOAuth(ctx, missingSecret.ID)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	noRefresh := insertOAuthCredential(t, svc, "github", "access", "")
	_, err = svc.RefreshOAuth(ctx, noRefresh.ID)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	setOAuthEnv(t, "openai", "", "", "", "", "")
	unavailable := insertOAuthCredential(t, svc, "openai", "access", "refresh")
	_, err = svc.RefreshOAuth(ctx, unavailable.ID)
	wantError(t, err, http.StatusBadRequest, "oauth_unavailable")

	var refreshedAccess, refreshedRefresh string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse: %v", err)
		}
		refreshedAccess = r.Form.Get("grant_type")
		refreshedRefresh = r.Form.Get("refresh_token")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"expires_in":   1800,
		})
	}))
	defer server.Close()
	setOAuthEnv(t, "github", "https://auth.example.test/authorize", server.URL, "client-id", "client-secret", "repo")
	cred := insertOAuthCredential(t, svc, "github", "old-access", "the-refresh")
	expires, err := svc.RefreshOAuth(ctx, cred.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshedAccess != "refresh_token" || refreshedRefresh != "the-refresh" {
		t.Fatalf("form = %q/%q", refreshedAccess, refreshedRefresh)
	}
	if delta := expires.Sub(svc.now()); delta < 29*time.Minute || delta > 31*time.Minute {
		t.Fatalf("expiry = %v", expires)
	}
	raw, err := svc.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	access, refresh, storedExpiry := decodeSecret(raw)
	if access != "new-access" || refresh != "the-refresh" || storedExpiry.IsZero() {
		t.Fatalf("stored bundle = %q %q %v", access, refresh, storedExpiry)
	}
	reread, err := svc.Store.GetCredential(ctx, cred.ID)
	if err != nil || reread.ExpiresAt == nil || reread.Status != domain.CredentialConnected {
		t.Fatalf("credential = %+v, %v", reread, err)
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "oauth.refreshed")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestRefreshOAuthFailures(t *testing.T) {
	t.Setenv("NULLKEY_OAUTH_GITHUB_AUTH_URL", "https://auth.example.test/authorize")
	t.Setenv("NULLKEY_OAUTH_GITHUB_CLIENT_ID", "client-id")

	badStatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer badStatus.Close()
	noToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"refresh_token":"x"}`))
	}))
	defer noToken.Close()

	for name, tokenURL := range map[string]string{
		"status":      badStatus.URL,
		"no-token":    noToken.URL,
		"unreachable": "http://127.0.0.1:1/token",
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := newTestService(t)
			t.Setenv("NULLKEY_OAUTH_GITHUB_TOKEN_URL", tokenURL)
			cred := insertOAuthCredential(t, svc, "github", "access", "refresh")
			_, err := svc.RefreshOAuth(context.Background(), cred.ID)
			wantError(t, err, http.StatusBadGateway, "oauth_failed")
		})
	}

	// An invalid token URL fails at request construction.
	svcBadURL, _ := newTestService(t)
	t.Setenv("NULLKEY_OAUTH_GITHUB_AUTH_URL", "https://auth.example.test/authorize")
	t.Setenv("NULLKEY_OAUTH_GITHUB_TOKEN_URL", "://bad-url")
	t.Setenv("NULLKEY_OAUTH_GITHUB_CLIENT_ID", "client-id")
	badURLCred := insertOAuthCredential(t, svcBadURL, "github", "access", "refresh")
	if _, err := svcBadURL.RefreshOAuth(context.Background(), badURLCred.ID); err == nil {
		t.Fatalf("expected invalid URL error")
	}

	// Missing expires_in falls back to a one hour lifetime.
	oneHour := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new"}`))
	}))
	defer oneHour.Close()
	svcHour, _ := newTestService(t)
	t.Setenv("NULLKEY_OAUTH_GITHUB_AUTH_URL", "https://auth.example.test/authorize")
	t.Setenv("NULLKEY_OAUTH_GITHUB_TOKEN_URL", oneHour.URL)
	t.Setenv("NULLKEY_OAUTH_GITHUB_CLIENT_ID", "client-id")
	hourCred := insertOAuthCredential(t, svcHour, "github", "access", "refresh")
	expires, err := svcHour.RefreshOAuth(context.Background(), hourCred.ID)
	if err != nil {
		t.Fatalf("refresh without expiry: %v", err)
	}
	if delta := expires.Sub(svcHour.now()); delta < 59*time.Minute || delta > 61*time.Minute {
		t.Fatalf("fallback expiry = %v", delta)
	}

	// A keychain write failure after a successful exchange is fail-closed.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":60}`))
	}))
	defer server.Close()
	faults := &faultSecretStore{Store: secret.NewMemory()}
	svc, _ := newTestService(t, func(o *Options) { o.Secrets = faults })
	t.Setenv("NULLKEY_OAUTH_GITHUB_TOKEN_URL", server.URL)
	cred := insertOAuthCredential(t, svc, "github", "access", "refresh")
	faults.putErr = errors.New("keychain down")
	_, err = svc.RefreshOAuth(context.Background(), cred.ID)
	wantError(t, err, http.StatusInternalServerError, "keychain_unavailable")
}

// ------------------------------------------------------------- host import

func writeHostFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestImportableLogins(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, func(o *Options) { o.HostImportRoot = root })

	items := svc.ImportableLogins()
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	for _, item := range items {
		if item.Available {
			t.Fatalf("%s unexpectedly available", item.ProviderID)
		}
	}

	writeHostFile(t, root, ".codex/auth.json", `{"tokens":{"access_token":"codex-token"},"expires_at":"2026-02-03T04:05:06Z"}`)
	writeHostFile(t, root, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"claude-token"}}`)
	writeHostFile(t, root, ".config/gh/hosts.yml", "github.com:\n    oauth_token: ghp_hosttoken1234567890\n")

	items = svc.ImportableLogins()
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	var codex ImportableLogin
	for _, item := range items {
		if !item.Available {
			t.Fatalf("%s not available", item.ProviderID)
		}
		if item.ProviderID == "codex" {
			codex = item
		}
	}
	if codex.ExpiresAt == nil {
		t.Fatalf("codex expiry missing")
	}

	// hostLoginExpiry ignores non-codex providers and invalid files.
	if got := svc.hostLoginExpiry("github", filepath.Join(root, ".config/gh/hosts.yml")); got != nil {
		t.Fatalf("github expiry = %v", got)
	}
	if got := svc.hostLoginExpiry("codex", filepath.Join(root, "missing")); got != nil {
		t.Fatalf("missing expiry = %v", got)
	}
	writeHostFile(t, root, ".codex/bad.json", `{"expires_at":"not-a-time"}`)
	if got := svc.hostLoginExpiry("codex", filepath.Join(root, ".codex/bad.json")); got != nil {
		t.Fatalf("bad expiry = %v", got)
	}

	// A directory is not a regular login file.
	if err := os.MkdirAll(filepath.Join(root, ".codex/auth.json.d"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func TestImportHostLogin(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, func(o *Options) { o.HostImportRoot = root })
	ctx := context.Background()

	_, err := svc.ImportHostLogin(ctx, "nope", "", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = svc.ImportHostLogin(ctx, "codex", "weird-source", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, err = svc.ImportHostLogin(ctx, "codex", "", "")
	wantError(t, err, http.StatusNotFound, "not_found")

	writeHostFile(t, root, ".codex/auth.json", `{"tokens":{"access_token":"codex-token"}}`)
	res, err := svc.ImportHostLogin(ctx, "codex", "host", "")
	if err != nil {
		t.Fatalf("import codex: %v", err)
	}
	cred, err := svc.Store.GetCredential(ctx, res.CredentialID)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if cred.AuthType != domain.AuthOAuth || cred.Label != "Codex (host)" || cred.BaseURL == "" {
		t.Fatalf("credential = %+v", cred)
	}
	value, err := svc.CredentialSecret(ctx, cred)
	if err != nil || value != "codex-token" {
		t.Fatalf("secret = %q, %v", value, err)
	}

	// codex API key fallbacks.
	for name, payload := range map[string]string{
		"openai-key": `{"OPENAI_API_KEY":"sk-openai"}`,
		"api-key":    `{"api_key":"sk-api"}`,
	} {
		writeHostFile(t, root, ".codex/auth.json", payload)
		res, err := svc.ImportHostLogin(ctx, "codex", "", name)
		if err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
		cred, err := svc.Store.GetCredential(ctx, res.CredentialID)
		if err != nil || cred.AuthType != domain.AuthAPIKey {
			t.Fatalf("%s credential = %+v, %v", name, cred, err)
		}
	}

	writeHostFile(t, root, ".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"claude-token"}}`)
	res, err = svc.ImportHostLogin(ctx, "claude", "", "")
	if err != nil || res.Status != "connected" {
		t.Fatalf("import claude = %+v, %v", res, err)
	}
	cred, err = svc.Store.GetCredential(ctx, res.CredentialID)
	if err != nil || cred.AuthType != domain.AuthOAuth {
		t.Fatalf("claude credential = %+v, %v", cred, err)
	}

	writeHostFile(t, root, ".config/gh/hosts.yml", "github.com:\n    oauth_token: ghp_abcdefghijklmnopqrstuvwxyz\n")
	res, err = svc.ImportHostLogin(ctx, "github", "", "")
	if err != nil {
		t.Fatalf("import github: %v", err)
	}
	cred, err = svc.Store.GetCredential(ctx, res.CredentialID)
	if err != nil || cred.AuthType != domain.AuthPAT {
		t.Fatalf("github credential = %+v, %v", cred, err)
	}

	// Unparseable host files are rejected rather than storing garbage.
	writeHostFile(t, root, ".codex/auth.json", "not json")
	_, err = svc.ImportHostLogin(ctx, "codex", "", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	evs, _, err := svc.Audit.List(ctx, 50, "", "credential.imported")
	if err != nil || len(evs) < 3 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestImportHostLoginStoreFailures(t *testing.T) {
	root := t.TempDir()
	writeHostFile(t, root, ".codex/auth.json", `{"tokens":{"access_token":"token"}}`)

	faults := &faultSecretStore{Store: secret.NewMemory(), putErr: errors.New("keychain down")}
	svc, _ := newTestService(t, func(o *Options) {
		o.HostImportRoot = root
		o.Secrets = faults
	})
	_, err := svc.ImportHostLogin(context.Background(), "codex", "", "")
	wantError(t, err, http.StatusInternalServerError, "keychain_unavailable")

	storeSvc, _ := newTestService(t, func(o *Options) { o.HostImportRoot = root })
	if err := storeSvc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = storeSvc.ImportHostLogin(context.Background(), "codex", "", "")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestParseHostLogin(t *testing.T) {
	if token, auth := parseHostLogin("codex", []byte(`{"tokens":{"access_token":"a"}}`)); token != "a" || auth != domain.AuthOAuth {
		t.Fatalf("codex oauth = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("codex", []byte(`{"OPENAI_API_KEY":"b"}`)); token != "b" || auth != domain.AuthAPIKey {
		t.Fatalf("codex openai key = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("codex", []byte(`{"api_key":"c"}`)); token != "c" || auth != domain.AuthAPIKey {
		t.Fatalf("codex api key = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("claude", []byte(`{"claudeAiOauth":{"accessToken":"d"}}`)); token != "d" || auth != domain.AuthOAuth {
		t.Fatalf("claude = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("claude", []byte(`{"access_token":"e"}`)); token != "e" || auth != domain.AuthOAuth {
		t.Fatalf("claude fallback = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("github", []byte("  oauth_token:  f  \n")); token != "f" || auth != domain.AuthPAT {
		t.Fatalf("github = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("github", []byte("nothing here")); token != "" || auth != domain.AuthNone {
		t.Fatalf("github miss = %q %q", token, auth)
	}
	if token, auth := parseHostLogin("unknown", []byte("x")); token != "" || auth != domain.AuthNone {
		t.Fatalf("unknown = %q %q", token, auth)
	}
}

func TestProviderBaseURLAndRandomURL(t *testing.T) {
	if got := providerBaseURL("openai"); got != "https://api.openai.com" {
		t.Fatalf("base = %q", got)
	}
	if got := providerBaseURL("unknown"); got != "" {
		t.Fatalf("unknown base = %q", got)
	}
	raw, err := randomURL(16)
	if err != nil || raw == "" {
		t.Fatalf("randomURL = %q, %v", raw, err)
	}
	if again, err := randomURL(16); err != nil || again == raw {
		t.Fatalf("randomURL collision: %q %q", raw, again)
	}
}
