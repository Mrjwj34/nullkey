package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// CreateCredentialRequest is the write-only credential payload.
type CreateCredentialRequest struct {
	ProviderID string
	Label      string
	AuthType   string
	Value      string
	BaseURL    string
	HeaderName string
}

func fingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	short := hex.EncodeToString(sum[:4])
	if len(value) <= 4 {
		return "sha256:" + short
	}
	return "sha256:" + short + "…" + value[len(value)-4:]
}

// CreateCredential stores the secret in the host secret store and metadata in
// SQLite. The response never contains the value.
func (s *Service) CreateCredential(ctx context.Context, req CreateCredentialRequest) (domain.Credential, error) {
	provider, ok := providerByID(req.ProviderID)
	if !ok {
		return domain.Credential{}, Invalid("unknown provider_id")
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		return domain.Credential{}, Invalid("label is required")
	}
	authType := domain.AuthType(req.AuthType)
	if authType == "" {
		return domain.Credential{}, Invalid("auth_type is required")
	}
	allowed := false
	for _, candidate := range provider.AuthTypes {
		if candidate == string(authType) {
			allowed = true
		}
	}
	if !allowed {
		return domain.Credential{}, Invalid("auth_type is not supported by this provider")
	}
	if strings.TrimSpace(req.Value) == "" {
		return domain.Credential{}, Invalid("value is required")
	}
	if authType == domain.AuthHeader && strings.TrimSpace(req.HeaderName) == "" {
		return domain.Credential{}, Invalid("header_name is required for header auth")
	}
	if req.BaseURL != "" && !strings.HasPrefix(req.BaseURL, "https://") && !strings.HasPrefix(req.BaseURL, "http://") {
		return domain.Credential{}, Invalid("base_url must be an http(s) URL")
	}
	baseURL := req.BaseURL
	if baseURL == "" && provider.BaseURL != nil {
		baseURL = *provider.BaseURL
	}
	id := newID("cred_")
	cred := domain.Credential{
		ID:              id,
		ProviderID:      provider.ID,
		Label:           req.Label,
		AuthType:        authType,
		Fingerprint:     fingerprint(req.Value),
		Status:          domain.CredentialConnected,
		KeychainAccount: id,
		BaseURL:         baseURL,
		HeaderName:      req.HeaderName,
		CreatedAt:       s.now(),
	}
	if err := s.Secrets.Put(ctx, cred.KeychainAccount, []byte(req.Value)); err != nil {
		return domain.Credential{}, Wrap(http.StatusInternalServerError, "keychain_unavailable", "could not store credential in the host secret store", err)
	}
	if err := s.Store.CreateCredential(ctx, cred); err != nil {
		_ = s.Secrets.Delete(ctx, cred.KeychainAccount)
		return domain.Credential{}, Wrap(http.StatusInternalServerError, "internal", "could not save credential metadata", err)
	}
	s.Audit.Record(ctx, "credential.created", nil, map[string]any{
		"credential_id": cred.ID,
		"provider_id":   cred.ProviderID,
		"auth_type":     string(cred.AuthType),
	})
	s.PublishEvent("credential.created", map[string]any{"credential_id": cred.ID, "status": "connected"})
	return cred, nil
}

// ListCredentials returns metadata only.
func (s *Service) ListCredentials(ctx context.Context, limit int, cursor string) ([]domain.Credential, string, error) {
	items, next, err := s.Store.ListCredentials(ctx, limit, cursor)
	if err != nil {
		return nil, "", Wrap(http.StatusInternalServerError, "internal", "could not list credentials", err)
	}
	return items, next, nil
}

// DeleteCredential removes the secret and marks the record revoked by deleting
// it. Gateway lookups then fail closed.
func (s *Service) DeleteCredential(ctx context.Context, id string) error {
	cred, err := s.Store.GetCredential(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return NotFound("credential not found")
		}
		return Wrap(http.StatusInternalServerError, "internal", "could not read credential", err)
	}
	if err := s.Secrets.Delete(ctx, cred.KeychainAccount); err != nil {
		return Wrap(http.StatusInternalServerError, "keychain_unavailable", "could not delete credential from the host secret store", err)
	}
	if err := s.Store.DeleteCredential(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not delete credential", err)
	}
	s.Audit.Record(ctx, "credential.deleted", nil, map[string]any{"credential_id": id, "provider_id": cred.ProviderID})
	s.PublishEvent("credential.deleted", map[string]any{"credential_id": id, "status": "revoked"})
	return nil
}

// TestCredential verifies that the secret is readable and not expired. It does
// not send the credential to a remote provider just to prove liveness.
func (s *Service) TestCredential(ctx context.Context, id string) (bool, *int64, string, error) {
	cred, err := s.Store.GetCredential(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return false, nil, "", NotFound("credential not found")
		}
		return false, nil, "", Wrap(http.StatusInternalServerError, "internal", "could not read credential", err)
	}
	if cred.Status == domain.CredentialExpired || (cred.ExpiresAt != nil && cred.ExpiresAt.Before(s.now())) {
		_ = s.Store.SetCredentialStatus(ctx, cred.ID, domain.CredentialExpired, cred.ExpiresAt)
		return false, nil, "credential expired", nil
	}
	start := time.Now()
	value, err := s.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil || len(value) == 0 {
		return false, nil, "credential value is unavailable", nil
	}
	ms := time.Since(start).Milliseconds()
	return true, &ms, "", nil
}

// CredentialSecret returns the raw secret for a credential. Callers must never
// log or return it.
func (s *Service) CredentialSecret(ctx context.Context, cred domain.Credential) (string, error) {
	raw, err := s.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil || len(raw) == 0 {
		return "", E(http.StatusUnauthorized, "reauth_required", "credential is unavailable; re-authenticate")
	}
	access, _, _ := decodeSecret(raw)
	if access == "" {
		return "", E(http.StatusUnauthorized, "reauth_required", "credential is unavailable; re-authenticate")
	}
	return access, nil
}

// credentialForProvider selects a connected credential and marks it used.
func (s *Service) credentialForProvider(ctx context.Context, providerID string) (domain.Credential, error) {
	cred, err := s.Store.CredentialForProvider(ctx, providerID)
	if err != nil {
		if isNotFound(err) {
			return domain.Credential{}, E(http.StatusUnauthorized, "reauth_required", "no credential is connected for this provider")
		}
		return domain.Credential{}, Wrap(http.StatusInternalServerError, "internal", "could not read credential", err)
	}
	if cred.ExpiresAt != nil && cred.ExpiresAt.Before(s.now()) {
		return domain.Credential{}, E(http.StatusUnauthorized, "reauth_required", "credential has expired")
	}
	_ = s.Store.TouchCredential(ctx, cred.ID)
	return cred, nil
}
