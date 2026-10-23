package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var credBase = time.Date(2024, 4, 1, 10, 0, 0, 0, time.UTC)

func TestCredentialCreateGet(t *testing.T) {
	s, ctx := openTestStore(t)
	expires := credBase.Add(24 * time.Hour)
	lastUsed := credBase.Add(time.Hour)
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID:              "cred_1",
		ProviderID:      "openai",
		Label:           "personal",
		KeychainAccount: "acct-1",
		Fingerprint:     "sk-test-...abcd",
		AuthType:        domain.AuthAPIKey,
		Status:          domain.CredentialConnected,
		BaseURL:         "https://api.example.test",
		HeaderName:      "X-Api-Key",
		ExpiresAt:       &expires,
		CreatedAt:       credBase,
		LastUsedAt:      &lastUsed,
	})
	got, err := s.GetCredential(ctx, "cred_1")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if got.ProviderID != "openai" || got.Label != "personal" || got.KeychainAccount != "acct-1" {
		t.Errorf("GetCredential() = %+v", got)
	}
	if got.Fingerprint != "sk-test-...abcd" || got.AuthType != domain.AuthAPIKey || got.Status != domain.CredentialConnected {
		t.Errorf("fingerprint/auth/status = %q/%q/%q", got.Fingerprint, got.AuthType, got.Status)
	}
	if got.BaseURL != "https://api.example.test" || got.HeaderName != "X-Api-Key" {
		t.Errorf("base URL/header = %q/%q", got.BaseURL, got.HeaderName)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) || got.LastUsedAt == nil || !got.LastUsedAt.Equal(lastUsed) {
		t.Errorf("expires/lastUsed = %v/%v", got.ExpiresAt, got.LastUsedAt)
	}

	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_nullable", ProviderID: "anthropic", Label: "bare", AuthType: domain.AuthOAuth,
		KeychainAccount: "acct-2", Fingerprint: "fp", CreatedAt: credBase,
	})
	nullable, err := s.GetCredential(ctx, "cred_nullable")
	if err != nil {
		t.Fatalf("GetCredential(nullable) error = %v", err)
	}
	if nullable.ExpiresAt != nil || nullable.LastUsedAt != nil || nullable.Status != "" ||
		nullable.BaseURL != "" || nullable.HeaderName != "" {
		t.Errorf("nullable credential = %+v", nullable)
	}
	if _, err := s.GetCredential(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetCredential(missing) error = %v, want sql.ErrNoRows", err)
	}
	if err := s.CreateCredential(ctx, domain.Credential{
		ID: "cred_1", ProviderID: "dup", Label: "dup", KeychainAccount: "x", Fingerprint: "x",
		AuthType: domain.AuthNone, CreatedAt: credBase,
	}); err == nil {
		t.Error("CreateCredential() duplicate id error = nil, want error")
	}
}

func TestCredentialListPagination(t *testing.T) {
	s, ctx := openTestStore(t)
	for i, id := range []string{"cred_a", "cred_b", "cred_c"} {
		mustCreateCredential(t, ctx, s, domain.Credential{
			ID: id, ProviderID: "p", Label: id, KeychainAccount: "acct-" + id, Fingerprint: "fp-" + id,
			AuthType: domain.AuthAPIKey, Status: domain.CredentialConnected,
			CreatedAt: credBase.Add(time.Duration(i) * time.Minute),
		})
	}
	all, next, err := s.ListCredentials(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListCredentials() error = %v", err)
	}
	if next != "" || len(all) != 3 || all[0].ID != "cred_c" || all[2].ID != "cred_a" {
		t.Fatalf("ListCredentials() = %v, next=%q", credentialIDs(all), next)
	}
	var seen []string
	cursor := ""
	for {
		page, next, err := s.ListCredentials(ctx, 1, cursor)
		if err != nil {
			t.Fatalf("ListCredentials(limit=1) error = %v", err)
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		seen = append(seen, page[0].ID)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 || seen[0] != "cred_c" || seen[1] != "cred_b" || seen[2] != "cred_a" {
		t.Errorf("paged credentials = %v", seen)
	}
	if _, _, err := s.ListCredentials(ctx, 1, "%%%bad"); err == nil {
		t.Error("ListCredentials() with invalid cursor error = nil, want error")
	}
}

func TestCredentialForProvider(t *testing.T) {
	s, ctx := openTestStore(t)
	older := credBase.Add(time.Hour)
	newer := credBase.Add(2 * time.Hour)
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_older", ProviderID: "p1", Label: "older", KeychainAccount: "a", Fingerprint: "f",
		AuthType: domain.AuthAPIKey, Status: domain.CredentialConnected, CreatedAt: credBase, LastUsedAt: &older,
	})
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_newer", ProviderID: "p1", Label: "newer", KeychainAccount: "b", Fingerprint: "f",
		AuthType: domain.AuthAPIKey, Status: domain.CredentialConnected, CreatedAt: credBase, LastUsedAt: &newer,
	})
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_revoked", ProviderID: "p1", Label: "revoked", KeychainAccount: "c", Fingerprint: "f",
		AuthType: domain.AuthAPIKey, Status: domain.CredentialRevoked, CreatedAt: credBase.Add(3 * time.Hour),
	})
	got, err := s.CredentialForProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("CredentialForProvider() error = %v", err)
	}
	if got.ID != "cred_newer" {
		t.Errorf("CredentialForProvider() = %q, want cred_newer", got.ID)
	}

	// created_at breaks ties when last_used_at is equal.
	tieUsed := credBase.Add(5 * time.Hour)
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_tie_old", ProviderID: "p2", Label: "old", KeychainAccount: "d", Fingerprint: "f",
		AuthType: domain.AuthOAuth, Status: domain.CredentialConnected, CreatedAt: credBase.Add(time.Hour), LastUsedAt: &tieUsed,
	})
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_tie_new", ProviderID: "p2", Label: "new", KeychainAccount: "e", Fingerprint: "f",
		AuthType: domain.AuthOAuth, Status: domain.CredentialConnected, CreatedAt: credBase.Add(2 * time.Hour), LastUsedAt: &tieUsed,
	})
	tied, err := s.CredentialForProvider(ctx, "p2")
	if err != nil {
		t.Fatalf("CredentialForProvider(p2) error = %v", err)
	}
	if tied.ID != "cred_tie_new" {
		t.Errorf("CredentialForProvider(p2) = %q, want cred_tie_new", tied.ID)
	}

	if _, err := s.CredentialForProvider(ctx, "no-provider"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("CredentialForProvider(no provider) error = %v, want sql.ErrNoRows", err)
	}
}

func TestCredentialMutations(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateCredential(t, ctx, s, domain.Credential{
		ID: "cred_mut", ProviderID: "p", Label: "mut", KeychainAccount: "acct", Fingerprint: "fp",
		AuthType: domain.AuthOAuth, Status: domain.CredentialConnected, CreatedAt: credBase,
	})
	if err := s.TouchCredential(ctx, "cred_mut"); err != nil {
		t.Fatalf("TouchCredential() error = %v", err)
	}
	got, err := s.GetCredential(ctx, "cred_mut")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("LastUsedAt = nil after TouchCredential")
	}
	if err := s.TouchCredential(ctx, "missing"); err != nil {
		t.Errorf("TouchCredential(missing) error = %v, want nil", err)
	}

	expires := credBase.Add(48 * time.Hour)
	if err := s.SetCredentialStatus(ctx, "cred_mut", domain.CredentialExpired, &expires); err != nil {
		t.Fatalf("SetCredentialStatus() error = %v", err)
	}
	got, err = s.GetCredential(ctx, "cred_mut")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if got.Status != domain.CredentialExpired || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Errorf("after SetCredentialStatus() = %+v", got)
	}
	if err := s.SetCredentialStatus(ctx, "cred_mut", domain.CredentialRevoked, nil); err != nil {
		t.Fatalf("SetCredentialStatus(nil) error = %v", err)
	}
	got, err = s.GetCredential(ctx, "cred_mut")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if got.Status != domain.CredentialRevoked || got.ExpiresAt != nil {
		t.Errorf("after clearing expiry = %+v", got)
	}
	if err := s.SetCredentialStatus(ctx, "missing", domain.CredentialRevoked, nil); err != nil {
		t.Errorf("SetCredentialStatus(missing) error = %v, want nil", err)
	}

	if err := s.DeleteCredential(ctx, "cred_mut"); err != nil {
		t.Fatalf("DeleteCredential() error = %v", err)
	}
	if _, err := s.GetCredential(ctx, "cred_mut"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetCredential() after delete error = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteCredential(ctx, "cred_mut"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteCredential(missing) error = %v, want domain.ErrNotFound", err)
	}
}

func credentialIDs(in []domain.Credential) []string {
	out := make([]string, len(in))
	for i, c := range in {
		out[i] = c.ID
	}
	return out
}
