package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

type oauthConfig struct {
	authURL      string
	tokenURL     string
	clientID     string
	clientSecret string
	scopes       []string
	// redirectURL is sent to the provider and must match the token exchange.
	// Empty means the control API's /oauth/callback.
	redirectURL string
	// callbackAddr starts a local callback listener for flows whose provider
	// only redirects to a fixed loopback port (Codex uses localhost:1455).
	callbackAddr string
	// authParams are extra authorization-query parameters required by a
	// provider. They are not secrets.
	authParams map[string]string
}

func (c oauthConfig) available() bool {
	return c.authURL != "" && c.tokenURL != "" && c.clientID != ""
}

type oauthBroker struct {
	svc       *Service
	mu        sync.Mutex
	verifiers map[string]string

	// callbackMu guards the optional provider-specific callback listener.
	callbackMu sync.Mutex
	callback   *oauthCallbackServer
}

func newOAuthBroker(s *Service) *oauthBroker {
	return &oauthBroker{svc: s, verifiers: make(map[string]string)}
}

func (s *Service) oauthConfig(providerID string) oauthConfig {
	prefix := "NULLKEY_OAUTH_" + strings.ToUpper(strings.ReplaceAll(providerID, "-", "_")) + "_"
	clientID, clientIDSet := os.LookupEnv(prefix + "CLIENT_ID")
	redirectURL, redirectSet := os.LookupEnv(prefix + "REDIRECT_URL")
	callbackAddr, callbackSet := os.LookupEnv(prefix + "CALLBACK_LISTEN")
	cfg := oauthConfig{
		authURL:      os.Getenv(prefix + "AUTH_URL"),
		tokenURL:     os.Getenv(prefix + "TOKEN_URL"),
		clientID:     clientID,
		clientSecret: os.Getenv(prefix + "CLIENT_SECRET"),
		scopes:       strings.Fields(os.Getenv(prefix + "SCOPES")),
		redirectURL:  redirectURL,
		callbackAddr: callbackAddr,
	}
	builtinEndpoints := cfg.authURL == "" && cfg.tokenURL == ""
	if builtinEndpoints {
		switch providerID {
		case "codex":
			cfg.authURL = "https://auth.openai.com/oauth/authorize"
			cfg.tokenURL = "https://auth.openai.com/oauth/token"
		case "github":
			cfg.authURL = "https://github.com/login/oauth/authorize"
			cfg.tokenURL = "https://github.com/login/oauth/access_token"
		}
	}
	if len(cfg.scopes) == 0 {
		switch providerID {
		case "codex":
			cfg.scopes = []string{"openid", "profile", "email", "offline_access"}
		case "github":
			cfg.scopes = []string{"repo", "read:user"}
		}
	}
	// An explicitly empty CLIENT_ID disables the built-in Codex app. Only an
	// unset variable gets the public Codex defaults, so custom OAuth apps and
	// tests remain in control of redirect/listener behavior.
	if builtinEndpoints && providerID == "codex" && !clientIDSet {
		cfg.clientID = codexOAuthClientID
		if !redirectSet {
			cfg.redirectURL = codexOAuthRedirectURL
		}
		if !callbackSet {
			cfg.callbackAddr = codexOAuthCallbackAddr
		}
		if _, scopesSet := os.LookupEnv(prefix + "SCOPES"); !scopesSet {
			cfg.scopes = append([]string(nil), codexOAuthScopes...)
		}
		cfg.authParams = codexOAuthAuthParams()
	}
	return cfg
}

func (s *Service) oauthRedirectURL(cfg oauthConfig) string {
	if cfg.redirectURL != "" {
		return cfg.redirectURL
	}
	return s.callbackURL()
}

// StartOAuth creates a PKCE flow.
func (s *Service) StartOAuth(ctx context.Context, providerID, label string) (domain.OAuthFlow, string, error) {
	cfg := s.oauthConfig(providerID)
	if !cfg.available() {
		return domain.OAuthFlow{}, "", E(http.StatusBadRequest, "oauth_unavailable", "OAuth is not configured for this provider")
	}
	state, err := randomURL(32)
	if err != nil {
		return domain.OAuthFlow{}, "", Wrap(http.StatusInternalServerError, "internal", "could not generate OAuth state", err)
	}
	verifier, err := randomURL(32)
	if err != nil {
		return domain.OAuthFlow{}, "", Wrap(http.StatusInternalServerError, "internal", "could not generate PKCE verifier", err)
	}
	challengeRaw := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeRaw[:])
	stateRaw := sha256.Sum256([]byte(state))

	flow := domain.OAuthFlow{
		ID:         newID("flow_"),
		ProviderID: providerID,
		Status:     domain.OAuthPending,
		StateHash:  stateRaw[:],
		CreatedAt:  s.now(),
		ExpiresAt:  s.now().Add(10 * time.Minute),
	}
	if err := s.Store.CreateOAuthFlow(ctx, flow); err != nil {
		return domain.OAuthFlow{}, "", Wrap(http.StatusInternalServerError, "internal", "could not create OAuth flow", err)
	}
	s.oauth.mu.Lock()
	s.oauth.verifiers[flow.ID] = verifier
	s.oauth.mu.Unlock()

	if err := s.startOAuthCallback(flow.ID, cfg.callbackAddr); err != nil {
		s.oauth.mu.Lock()
		delete(s.oauth.verifiers, flow.ID)
		s.oauth.mu.Unlock()
		_, _ = s.Store.CancelOAuthFlow(ctx, flow.ID)
		return domain.OAuthFlow{}, "", err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cfg.clientID)
	q.Set("redirect_uri", s.oauthRedirectURL(cfg))
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(cfg.scopes) > 0 {
		q.Set("scope", strings.Join(cfg.scopes, " "))
	}
	for key, value := range cfg.authParams {
		q.Set(key, value)
	}
	authURL := cfg.authURL
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	s.Audit.Record(ctx, "oauth.started", nil, map[string]any{"flow_id": flow.ID, "provider_id": providerID})
	s.PublishEvent("oauth.started", map[string]any{"flow_id": flow.ID, "status": "pending"})
	return flow, authURL, nil
}

func (s *Service) callbackURL() string {
	host := s.ControlAddr
	if host == "" {
		host = "127.0.0.1:49152"
	}
	return "http://" + host + "/api/v1/oauth/callback"
}

// CompleteOAuth finishes a flow. It is intentionally atomic with cancellation:
// a cancelled or expired flow never persists the credential.
func (s *Service) CompleteOAuth(ctx context.Context, code, state string) (domain.OAuthFlow, error) {
	if code == "" || state == "" {
		return domain.OAuthFlow{}, Invalid("code and state are required")
	}
	stateHash := sha256.Sum256([]byte(state))
	flow, err := s.Store.FindOAuthFlowByStateHash(ctx, stateHash[:])
	if err != nil {
		if isNotFound(err) {
			return domain.OAuthFlow{}, E(http.StatusBadRequest, "oauth_state_invalid", "OAuth state is invalid or already used")
		}
		return domain.OAuthFlow{}, Wrap(http.StatusInternalServerError, "internal", "could not read OAuth flow", err)
	}
	// The callback listener is only needed while this flow can still receive
	// a code. Release it on every exit, including exchange failures.
	defer s.releaseOAuthCallback(flow.ID)
	if s.now().After(flow.ExpiresAt) {
		flow.Status = domain.OAuthExpired
		_ = s.Store.UpdateOAuthFlow(ctx, flow)
		return domain.OAuthFlow{}, E(http.StatusBadRequest, "oauth_state_invalid", "OAuth flow has expired")
	}
	s.oauth.mu.Lock()
	verifier := s.oauth.verifiers[flow.ID]
	s.oauth.mu.Unlock()
	if verifier == "" {
		return domain.OAuthFlow{}, E(http.StatusBadRequest, "oauth_state_invalid", "OAuth flow has no PKCE context")
	}
	cfg := s.oauthConfig(flow.ProviderID)
	if !cfg.available() {
		return domain.OAuthFlow{}, E(http.StatusBadRequest, "oauth_unavailable", "OAuth is not configured for this provider")
	}
	access, refresh, idToken, expiresAt, err := s.exchangeCode(ctx, cfg, code, verifier)
	if err != nil {
		msg := "OAuth token exchange failed"
		flow.Status = domain.OAuthFailed
		flow.Error = &msg
		_ = s.Store.UpdateOAuthFlow(ctx, flow)
		s.oauth.mu.Lock()
		delete(s.oauth.verifiers, flow.ID)
		s.oauth.mu.Unlock()
		return domain.OAuthFlow{}, E(http.StatusBadGateway, "oauth_failed", "OAuth token exchange failed")
	}
	credID := newID("cred_")
	cred := domain.Credential{
		ID:              credID,
		ProviderID:      flow.ProviderID,
		Label:           strings.Title(flow.ProviderID) + " OAuth",
		AuthType:        domain.AuthOAuth,
		Fingerprint:     fingerprint(access),
		Status:          domain.CredentialConnected,
		KeychainAccount: credID,
		BaseURL:         providerBaseURL(flow.ProviderID),
		CreatedAt:       s.now(),
	}
	if !expiresAt.IsZero() {
		cred.ExpiresAt = &expiresAt
	}
	bundle := oauthSecret{AccessToken: access, RefreshToken: refresh, IDToken: idToken, ExpiresAt: expiresAt}
	if account := jwtAccountID(idToken); account != "" {
		bundle.AccountID = account
	} else {
		bundle.AccountID = jwtAccountID(access)
	}
	if err := s.Secrets.Put(ctx, cred.KeychainAccount, encodeOAuthBundle(bundle)); err != nil {
		return domain.OAuthFlow{}, Wrap(http.StatusInternalServerError, "keychain_unavailable", "could not store OAuth credential", err)
	}
	if err := s.Store.CreateCredential(ctx, cred); err != nil {
		_ = s.Secrets.Delete(ctx, cred.KeychainAccount)
		return domain.OAuthFlow{}, Wrap(http.StatusInternalServerError, "internal", "could not save OAuth credential", err)
	}
	ok, err := s.Store.CompleteOAuthFlow(ctx, flow.ID, credID)
	if err != nil {
		_ = s.Secrets.Delete(ctx, cred.KeychainAccount)
		_ = s.Store.DeleteCredential(ctx, credID)
		return domain.OAuthFlow{}, Wrap(http.StatusInternalServerError, "internal", "could not complete OAuth flow", err)
	}
	if !ok {
		_ = s.Secrets.Delete(ctx, cred.KeychainAccount)
		_ = s.Store.DeleteCredential(ctx, credID)
		return domain.OAuthFlow{}, Conflict("conflict", "OAuth flow is no longer pending")
	}
	s.oauth.mu.Lock()
	delete(s.oauth.verifiers, flow.ID)
	s.oauth.mu.Unlock()
	s.Audit.Record(ctx, "oauth.succeeded", nil, map[string]any{"flow_id": flow.ID, "provider_id": flow.ProviderID, "credential_id": credID})
	s.PublishEvent("oauth.succeeded", map[string]any{"flow_id": flow.ID, "credential_id": credID, "status": "succeeded"})
	flow.Status = domain.OAuthSucceeded
	flow.CredentialID = &credID
	return flow, nil
}

func (s *Service) exchangeCode(ctx context.Context, cfg oauthConfig, code, verifier string) (string, string, string, time.Time, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", cfg.clientID)
	form.Set("redirect_uri", s.oauthRedirectURL(cfg))
	form.Set("code_verifier", verifier)
	if cfg.clientSecret != "" {
		form.Set("client_secret", cfg.clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", "", time.Time{}, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", "", time.Time{}, err
	}
	if payload.Error != "" {
		return "", "", "", time.Time{}, fmt.Errorf("token endpoint error")
	}
	if payload.AccessToken == "" {
		return "", "", "", time.Time{}, fmt.Errorf("token endpoint returned no access token")
	}
	var expires time.Time
	if payload.ExpiresIn > 0 {
		expires = s.now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return payload.AccessToken, payload.RefreshToken, payload.IDToken, expires, nil
}

// GetOAuthFlow returns flow status, expiring stale flows.
func (s *Service) GetOAuthFlow(ctx context.Context, id string) (domain.OAuthFlow, error) {
	flow, err := s.Store.GetOAuthFlow(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return domain.OAuthFlow{}, NotFound("OAuth flow not found")
		}
		return domain.OAuthFlow{}, Wrap(http.StatusInternalServerError, "internal", "could not read OAuth flow", err)
	}
	if flow.Status == domain.OAuthPending && s.now().After(flow.ExpiresAt) {
		flow.Status = domain.OAuthExpired
		_ = s.Store.UpdateOAuthFlow(ctx, flow)
		s.oauth.mu.Lock()
		delete(s.oauth.verifiers, id)
		s.oauth.mu.Unlock()
		s.releaseOAuthCallback(id)
	}
	return flow, nil
}

// CancelOAuthFlow invalidates state/PKCE and refuses late credential writes.
func (s *Service) CancelOAuthFlow(ctx context.Context, id string) (domain.OAuthFlow, error) {
	flow, err := s.GetOAuthFlow(ctx, id)
	if err != nil {
		return domain.OAuthFlow{}, err
	}
	if flow.Status == domain.OAuthCancelled {
		return flow, nil
	}
	ok, err := s.Store.CancelOAuthFlow(ctx, id)
	if err != nil {
		return domain.OAuthFlow{}, Wrap(http.StatusInternalServerError, "internal", "could not cancel OAuth flow", err)
	}
	if !ok {
		return domain.OAuthFlow{}, Conflict("conflict", "OAuth flow has already completed")
	}
	s.oauth.mu.Lock()
	delete(s.oauth.verifiers, id)
	s.oauth.mu.Unlock()
	s.releaseOAuthCallback(id)
	flow.Status = domain.OAuthCancelled
	flow.StateHash = nil
	s.Audit.Record(ctx, "oauth.cancelled", nil, map[string]any{"flow_id": id})
	s.PublishEvent("oauth.cancelled", map[string]any{"flow_id": id, "status": "cancelled"})
	return flow, nil
}

// RefreshOAuth refreshes a stored OAuth token.
func (s *Service) RefreshOAuth(ctx context.Context, credentialID string) (time.Time, error) {
	cred, err := s.Store.GetCredential(ctx, credentialID)
	if err != nil {
		if isNotFound(err) {
			return time.Time{}, NotFound("credential not found")
		}
		return time.Time{}, Wrap(http.StatusInternalServerError, "internal", "could not read credential", err)
	}
	if cred.AuthType != domain.AuthOAuth {
		return time.Time{}, Invalid("credential is not an OAuth connection")
	}
	refreshed, err := s.refreshOAuthBundle(ctx, cred, true)
	if err != nil {
		return time.Time{}, err
	}
	return refreshed.ExpiresAt, nil
}

// ImportableLogin describes a host login that can be imported.
type ImportableLogin struct {
	ProviderID string     `json:"provider_id"`
	Source     string     `json:"source"`
	Available  bool       `json:"available"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

var importSources = map[string]string{
	"codex":  ".codex/auth.json",
	"github": ".config/gh/hosts.yml",
	"claude": ".claude/.credentials.json",
}

// ImportableLogins inspects known host login files without exposing values.
func (s *Service) ImportableLogins() []ImportableLogin {
	out := make([]ImportableLogin, 0, len(importSources))
	for _, providerID := range []string{"codex", "github", "claude"} {
		rel := importSources[providerID]
		path := filepath.Join(s.HostImportDir, rel)
		info, err := os.Stat(path)
		item := ImportableLogin{ProviderID: providerID, Source: rel, Available: err == nil && info.Mode().IsRegular()}
		if item.Available {
			item.ExpiresAt = s.hostLoginExpiry(providerID, path)
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) hostLoginExpiry(providerID, path string) *time.Time {
	if providerID != "codex" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if login, ok := parseCodexHostLogin(raw); ok {
		return login.ExpiresAt
	}
	return nil
}

// ImportHostLogin imports an existing host login into the secret store.
func (s *Service) ImportHostLogin(ctx context.Context, providerID, source, label string) (domain.ImportResult, error) {
	rel, ok := importSources[providerID]
	if !ok {
		return domain.ImportResult{}, Invalid("provider does not support host import")
	}
	if source != "" && source != rel && source != "host" {
		return domain.ImportResult{}, Invalid("unknown import source")
	}
	path := filepath.Join(s.HostImportDir, rel)
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.ImportResult{}, NotFound("host login is not available")
	}
	token, authType, expires := parseHostLoginDetails(providerID, raw)
	if token == "" {
		return domain.ImportResult{}, Invalid("host login could not be parsed")
	}
	if label == "" {
		label = strings.Title(providerID) + " (host)"
	}
	status := domain.CredentialConnected
	if expires != nil && expires.Before(s.now()) {
		status = domain.CredentialExpired
	}
	cred := domain.Credential{
		ID:              newID("cred_"),
		ProviderID:      providerID,
		Label:           label,
		AuthType:        authType,
		Fingerprint:     fingerprint(token),
		Status:          status,
		KeychainAccount: "",
		BaseURL:         providerBaseURL(providerID),
		ExpiresAt:       expires,
		CreatedAt:       s.now(),
	}
	cred.KeychainAccount = cred.ID
	secretValue := []byte(token)
	if authType == domain.AuthOAuth {
		bundle := oauthSecret{AccessToken: token, ExpiresAt: derefTime(expires)}
		if providerID == "codex" {
			if login, ok := parseCodexHostLogin(raw); ok {
				bundle.AccessToken = login.AccessToken
				bundle.RefreshToken = login.RefreshToken
				bundle.IDToken = login.IDToken
				bundle.AccountID = login.AccountID
				bundle.ExpiresAt = derefTime(login.ExpiresAt)
			}
		}
		secretValue = encodeOAuthBundle(bundle)
	}
	if err := s.Secrets.Put(ctx, cred.KeychainAccount, secretValue); err != nil {
		return domain.ImportResult{}, Wrap(http.StatusInternalServerError, "keychain_unavailable", "could not store imported credential", err)
	}
	if err := s.Store.CreateCredential(ctx, cred); err != nil {
		_ = s.Secrets.Delete(ctx, cred.KeychainAccount)
		return domain.ImportResult{}, Wrap(http.StatusInternalServerError, "internal", "could not save imported credential", err)
	}
	s.Audit.Record(ctx, "credential.imported", nil, map[string]any{"credential_id": cred.ID, "provider_id": providerID})
	s.PublishEvent("credential.created", map[string]any{"credential_id": cred.ID, "status": "connected"})
	resultStatus := "connected"
	if status == domain.CredentialExpired {
		resultStatus = "expired"
	}
	return domain.ImportResult{CredentialID: cred.ID, Status: resultStatus}, nil
}

// parseHostLogin keeps the historical two-value helper used by tests.
func parseHostLogin(providerID string, raw []byte) (string, domain.AuthType) {
	token, authType, _ := parseHostLoginDetails(providerID, raw)
	return token, authType
}

func parseHostLoginDetails(providerID string, raw []byte) (string, domain.AuthType, *time.Time) {
	switch providerID {
	case "codex":
		if login, ok := parseCodexHostLogin(raw); ok {
			return login.AccessToken, domain.AuthOAuth, login.ExpiresAt
		}
		var payload struct {
			OpenAIAPIKey string `json:"OPENAI_API_KEY"`
			APIKey       string `json:"api_key"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			if payload.OpenAIAPIKey != "" {
				return payload.OpenAIAPIKey, domain.AuthAPIKey, nil
			}
			if payload.APIKey != "" {
				return payload.APIKey, domain.AuthAPIKey, nil
			}
		}
	case "claude":
		var payload struct {
			ClaudeAiOauth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			if payload.ClaudeAiOauth.AccessToken != "" {
				return payload.ClaudeAiOauth.AccessToken, domain.AuthOAuth, nil
			}
			if payload.AccessToken != "" {
				return payload.AccessToken, domain.AuthOAuth, nil
			}
		}
	case "github":
		re := regexp.MustCompile(`(?m)^\s*oauth_token:\s*(\S+)\s*$`)
		if match := re.FindSubmatch(raw); len(match) == 2 {
			return string(match[1]), domain.AuthPAT, nil
		}
	}
	return "", domain.AuthNone, nil
}

func providerBaseURL(providerID string) string {
	if p, ok := providerByID(providerID); ok && p.BaseURL != nil {
		return *p.BaseURL
	}
	return ""
}

func randomURL(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := readRandom(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
