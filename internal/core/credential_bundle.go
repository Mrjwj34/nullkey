package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// credentialBundle is the in-memory projection of an OAuth credential. It is
// never logged or serialized into an API response.
type credentialBundle struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	ExpiresAt    time.Time
}

func (s *Service) loadCredentialBundle(ctx context.Context, cred domain.Credential) (credentialBundle, bool, error) {
	raw, err := s.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil || len(raw) == 0 {
		return credentialBundle{}, false, E(http.StatusUnauthorized, "reauth_required", "credential is unavailable; re-authenticate")
	}
	if bundle, ok := decodeOAuthSecret(raw); ok {
		out := credentialBundle{
			AccessToken:  bundle.AccessToken,
			RefreshToken: bundle.RefreshToken,
			IDToken:      bundle.IDToken,
			AccountID:    bundle.AccountID,
			ExpiresAt:    bundle.ExpiresAt,
		}
		if out.ExpiresAt.IsZero() {
			if expiry := jwtExpiry(out.AccessToken); expiry != nil {
				out.ExpiresAt = *expiry
			} else if expiry := jwtExpiry(out.IDToken); expiry != nil {
				out.ExpiresAt = *expiry
			}
		}
		if out.AccountID == "" {
			if account := jwtAccountID(out.IDToken); account != "" {
				out.AccountID = account
			} else if account := jwtAccountID(out.AccessToken); account != "" {
				out.AccountID = account
			}
		}
		return out, true, nil
	}
	return credentialBundle{AccessToken: string(raw)}, false, nil
}

// credentialBundleFor returns a usable access token, refreshing a ChatGPT
// OAuth login shortly before (or after) its JWT expiry. API keys are returned
// as-is. The refresh is serialized because OpenAI rotates refresh tokens.
func (s *Service) credentialBundleFor(ctx context.Context, cred domain.Credential) (credentialBundle, error) {
	bundle, oauth, err := s.loadCredentialBundle(ctx, cred)
	if err != nil {
		return credentialBundle{}, err
	}
	if !oauth {
		return bundle, nil
	}
	if bundle.ExpiresAt.IsZero() || s.now().Add(2*time.Minute).Before(bundle.ExpiresAt) {
		return bundle, nil
	}
	if bundle.RefreshToken == "" {
		if bundle.ExpiresAt.Before(s.now()) || bundle.AccessToken == "" {
			return credentialBundle{}, E(http.StatusUnauthorized, "reauth_required", "credential has expired; re-authenticate")
		}
		return bundle, nil
	}
	refreshed, err := s.refreshOAuthBundle(ctx, cred, false)
	if err != nil {
		if bundle.AccessToken != "" && bundle.ExpiresAt.After(s.now()) {
			s.Log.Warn("oauth_refresh_failed_using_valid_access_token", "credential_id", cred.ID, "provider_id", cred.ProviderID)
			return bundle, nil
		}
		return credentialBundle{}, err
	}
	return refreshed, nil
}

// refreshOAuthBundle refreshes an OAuth credential and persists the rotated
// bundle. Callers must not hold refreshMu.
func (s *Service) refreshOAuthBundle(ctx context.Context, cred domain.Credential, force bool) (credentialBundle, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	// Another request may have refreshed while this one waited. An explicit
	// /oauth/connections/{id}/refresh is a forced operation and must still call
	// the token endpoint.
	current, _, err := s.loadCredentialBundle(ctx, cred)
	if err != nil {
		return credentialBundle{}, err
	}
	if !force && !current.ExpiresAt.IsZero() && s.now().Add(2*time.Minute).Before(current.ExpiresAt) {
		return current, nil
	}
	if current.RefreshToken == "" {
		return credentialBundle{}, E(http.StatusUnauthorized, "reauth_required", "OAuth connection has no refresh token")
	}

	cfg := s.oauthConfig(cred.ProviderID)
	tokenURL := cfg.tokenURL
	clientID := cfg.clientID
	if cred.ProviderID == "codex" {
		if tokenURL == "" {
			tokenURL = "https://auth.openai.com/oauth/token"
		}
		if clientID == "" {
			clientID = codexOAuthClientID
		}
	}
	if tokenURL == "" || clientID == "" {
		return credentialBundle{}, E(http.StatusBadRequest, "oauth_unavailable", "OAuth is not configured for this provider")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", current.RefreshToken)
	form.Set("client_id", clientID)
	if cfg.clientSecret != "" {
		form.Set("client_secret", cfg.clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return credentialBundle{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return credentialBundle{}, E(http.StatusBadGateway, "oauth_failed", "token refresh failed")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || json.Unmarshal(body, &payload) != nil || payload.AccessToken == "" {
		_ = s.Store.SetCredentialStatus(context.WithoutCancel(ctx), cred.ID, domain.CredentialExpired, cred.ExpiresAt)
		return credentialBundle{}, E(http.StatusBadGateway, "oauth_failed", "token refresh failed")
	}
	refreshed := credentialBundle{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		AccountID:    current.AccountID,
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = current.RefreshToken
	}
	if refreshed.IDToken == "" {
		refreshed.IDToken = current.IDToken
	}
	if account := jwtAccountID(refreshed.IDToken); account != "" {
		refreshed.AccountID = account
	}
	if account := jwtAccountID(refreshed.AccessToken); account != "" {
		refreshed.AccountID = account
	}
	if payload.ExpiresIn > 0 {
		refreshed.ExpiresAt = s.now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	} else if expiry := jwtExpiry(refreshed.AccessToken); expiry != nil {
		refreshed.ExpiresAt = *expiry
	} else if expiry := jwtExpiry(refreshed.IDToken); expiry != nil {
		refreshed.ExpiresAt = *expiry
	} else {
		refreshed.ExpiresAt = s.now().Add(time.Hour)
	}
	raw := encodeOAuthBundle(oauthSecret{
		AccessToken:  refreshed.AccessToken,
		RefreshToken: refreshed.RefreshToken,
		IDToken:      refreshed.IDToken,
		AccountID:    refreshed.AccountID,
		ExpiresAt:    refreshed.ExpiresAt,
	})
	if err := s.Secrets.Put(context.WithoutCancel(ctx), cred.KeychainAccount, raw); err != nil {
		return credentialBundle{}, Wrap(http.StatusInternalServerError, "keychain_unavailable", "could not update OAuth credential", err)
	}
	_ = s.Store.SetCredentialStatus(context.WithoutCancel(ctx), cred.ID, domain.CredentialConnected, &refreshed.ExpiresAt)
	s.Audit.Record(context.WithoutCancel(ctx), "oauth.refreshed", nil, map[string]any{"credential_id": cred.ID, "provider_id": cred.ProviderID})
	return refreshed, nil
}
