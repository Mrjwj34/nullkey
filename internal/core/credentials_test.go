package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/secret"
)

// faultSecretStore wraps a real store and injects failures.
type faultSecretStore struct {
	secret.Store
	putErr    error
	getErr    error
	deleteErr error
}

func (f *faultSecretStore) Put(ctx context.Context, account string, value []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.Store.Put(ctx, account, value)
}

func (f *faultSecretStore) Get(ctx context.Context, account string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.Store.Get(ctx, account)
}

func (f *faultSecretStore) Delete(ctx context.Context, account string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.Store.Delete(ctx, account)
}

func createCred(t *testing.T, s *Service, req CreateCredentialRequest) domain.Credential {
	t.Helper()
	cred, err := s.CreateCredential(context.Background(), req)
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return cred
}

func TestCreateCredentialValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cases := []CreateCredentialRequest{
		{ProviderID: "nope", Label: "x", AuthType: "api_key", Value: "v"},
		{ProviderID: "openai", Label: "  ", AuthType: "api_key", Value: "v"},
		{ProviderID: "openai", Label: "x", Value: "v"},
		{ProviderID: "openai", Label: "x", AuthType: "oauth", Value: "v"},
		{ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "  "},
		{ProviderID: "anthropic", Label: "x", AuthType: "header", Value: "v"},
		{ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v", BaseURL: "ftp://example.test"},
	}
	for i, req := range cases {
		_, err := svc.CreateCredential(ctx, req)
		if err == nil {
			t.Fatalf("case %d accepted: %+v", i, req)
		}
		wantError(t, err, http.StatusBadRequest, "invalid_request")
	}
}

func TestCreateCredentialDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ch, cancel, err := svc.Events.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "  main key  ", AuthType: "api_key", Value: "sk-test-1234",
	})
	if cred.ProviderID != "openai" || cred.Label != "main key" || cred.AuthType != domain.AuthAPIKey {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.Status != domain.CredentialConnected || cred.KeychainAccount != cred.ID || !strings.HasPrefix(cred.ID, "cred_") {
		t.Fatalf("credential metadata = %+v", cred)
	}
	if cred.BaseURL != "https://api.openai.com" {
		t.Fatalf("base url = %q", cred.BaseURL)
	}
	if !strings.HasSuffix(cred.Fingerprint, "…1234") || strings.Contains(cred.Fingerprint, "sk-test") {
		t.Fatalf("fingerprint = %q", cred.Fingerprint)
	}
	raw, err := svc.Secrets.Get(ctx, cred.KeychainAccount)
	if err != nil || string(raw) != "sk-test-1234" {
		t.Fatalf("secret = %q, %v", raw, err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "credential.created" {
			t.Fatalf("event = %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("event not published")
	}

	header := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "anthropic", Label: "header", AuthType: "header", Value: "v", HeaderName: "x-custom", BaseURL: "http://127.0.0.1:9",
	})
	if header.HeaderName != "x-custom" || header.BaseURL != "http://127.0.0.1:9" {
		t.Fatalf("header credential = %+v", header)
	}
	bearer := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "bearer", AuthType: "bearer", Value: "v",
	})
	if bearer.AuthType != domain.AuthBearer {
		t.Fatalf("bearer = %+v", bearer)
	}

	items, next, err := svc.ListCredentials(ctx, 10, "")
	if err != nil || next != "" || len(items) != 3 {
		t.Fatalf("list = %d, %q, %v", len(items), next, err)
	}
}

func TestCreateCredentialSecretStoreFailure(t *testing.T) {
	faults := &faultSecretStore{Store: secret.NewMemory(), putErr: errors.New("keychain down")}
	svc, _ := newTestService(t, func(o *Options) { o.Secrets = faults })
	_, err := svc.CreateCredential(context.Background(), CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})
	wantError(t, err, http.StatusInternalServerError, "keychain_unavailable")
}

func TestCreateCredentialStoreFailure(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := svc.CreateCredential(context.Background(), CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestListCredentialsError(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, _, err := svc.ListCredentials(context.Background(), 10, "")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestDeleteCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})
	if err := svc.DeleteCredential(ctx, cred.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Secrets.Get(ctx, cred.KeychainAccount); err == nil {
		t.Fatalf("secret survived delete")
	}
	if _, err := svc.Store.GetCredential(ctx, cred.ID); err == nil {
		t.Fatalf("metadata survived delete")
	}
	err := svc.DeleteCredential(ctx, cred.ID)
	wantError(t, err, http.StatusNotFound, "not_found")

	evs, _, err := svc.Audit.List(ctx, 50, "", "credential.deleted")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestDeleteCredentialStoreFailures(t *testing.T) {
	faults := &faultSecretStore{Store: secret.NewMemory(), deleteErr: errors.New("keychain down")}
	svc, _ := newTestService(t, func(o *Options) { o.Secrets = faults })
	// Put succeeds, then delete fails.
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})
	err := svc.DeleteCredential(context.Background(), cred.ID)
	wantError(t, err, http.StatusInternalServerError, "keychain_unavailable")
}

func TestTestCredential(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})

	ok, ms, msg, err := svc.TestCredential(ctx, cred.ID)
	if err != nil || !ok || msg != "" || ms == nil || *ms < 0 {
		t.Fatalf("test = %v %v %q %v", ok, ms, msg, err)
	}

	// Missing secret.
	if err := svc.Secrets.Delete(ctx, cred.KeychainAccount); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	ok, _, msg, err = svc.TestCredential(ctx, cred.ID)
	if err != nil || ok || msg != "credential value is unavailable" {
		t.Fatalf("missing secret test = %v %q %v", ok, msg, err)
	}

	// Expired status short-circuits before reading the secret.
	if err := svc.Store.SetCredentialStatus(ctx, cred.ID, domain.CredentialExpired, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	ok, _, msg, err = svc.TestCredential(ctx, cred.ID)
	if err != nil || ok || msg != "credential expired" {
		t.Fatalf("expired test = %v %q %v", ok, msg, err)
	}

	_, _, _, err = svc.TestCredential(ctx, "cred_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	// A secret-store read failure is reported as unavailable, not as success.
	faults := &faultSecretStore{Store: secret.NewMemory(), getErr: errors.New("boom")}
	svc2, _ := newTestService(t, func(o *Options) { o.Secrets = faults })
	cred2 := createCred(t, svc2, CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})
	ok, _, msg, err = svc2.TestCredential(ctx, cred2.ID)
	if err != nil || ok || msg != "credential value is unavailable" {
		t.Fatalf("fault test = %v %q %v", ok, msg, err)
	}
}

func TestCredentialSecret(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "sk-test-raw",
	})
	value, err := svc.CredentialSecret(ctx, cred)
	if err != nil || value != "sk-test-raw" {
		t.Fatalf("secret = %q, %v", value, err)
	}

	oauth := domain.Credential{ID: "cred_oauth", KeychainAccount: "cred_oauth"}
	if err := svc.Secrets.Put(ctx, oauth.KeychainAccount, encodeOAuthSecret("access", "refresh", time.Time{})); err != nil {
		t.Fatalf("put oauth: %v", err)
	}
	value, err = svc.CredentialSecret(ctx, oauth)
	if err != nil || value != "access" {
		t.Fatalf("oauth secret = %q, %v", value, err)
	}

	missing := domain.Credential{ID: "cred_missing", KeychainAccount: "cred_missing"}
	_, err = svc.CredentialSecret(ctx, missing)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	empty := domain.Credential{ID: "cred_empty", KeychainAccount: "cred_empty"}
	if err := svc.Secrets.Put(ctx, empty.KeychainAccount, []byte{}); err != nil {
		t.Fatalf("put empty: %v", err)
	}
	_, err = svc.CredentialSecret(ctx, empty)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	refreshOnly := domain.Credential{ID: "cred_refresh", KeychainAccount: "cred_refresh"}
	if err := svc.Secrets.Put(ctx, refreshOnly.KeychainAccount, encodeOAuthSecret("", "refresh", time.Time{})); err != nil {
		t.Fatalf("put refresh: %v", err)
	}
	_, err = svc.CredentialSecret(ctx, refreshOnly)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	faults := &faultSecretStore{Store: secret.NewMemory(), getErr: errors.New("boom")}
	svc2, _ := newTestService(t, func(o *Options) { o.Secrets = faults })
	_, err = svc2.CredentialSecret(ctx, domain.Credential{ID: "cred_x", KeychainAccount: "cred_x"})
	wantError(t, err, http.StatusUnauthorized, "reauth_required")
}

func TestCredentialForProvider(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.credentialForProvider(ctx, "openai")
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "x", AuthType: "api_key", Value: "v",
	})
	got, err := svc.credentialForProvider(ctx, "openai")
	if err != nil || got.ID != cred.ID {
		t.Fatalf("selected = %+v, %v", got, err)
	}
	if got.LastUsedAt == nil {
		// TouchCredential writes asynchronously at the store level; read again.
		reread, err := svc.Store.GetCredential(ctx, cred.ID)
		if err != nil || reread.LastUsedAt == nil {
			t.Fatalf("credential not touched: %+v, %v", reread, err)
		}
	}

	// Expired credentials fail closed.
	past := svc.now().Add(-time.Hour)
	if err := svc.Store.SetCredentialStatus(ctx, cred.ID, domain.CredentialConnected, &past); err != nil {
		t.Fatalf("expire: %v", err)
	}
	_, err = svc.credentialForProvider(ctx, "openai")
	ce := wantError(t, err, http.StatusUnauthorized, "reauth_required")
	if !strings.Contains(ce.Message, "expired") {
		t.Fatalf("message = %q", ce.Message)
	}

	// Disconnected credentials are not selected.
	if err := svc.Store.SetCredentialStatus(ctx, cred.ID, domain.CredentialExpired, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	_, err = svc.credentialForProvider(ctx, "openai")
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = svc.credentialForProvider(ctx, "openai")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestCredentialStoreReadErrors(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, _, _, err := svc.TestCredential(ctx, "cred_x")
	wantError(t, err, http.StatusInternalServerError, "internal")
	err = svc.DeleteCredential(ctx, "cred_x")
	wantError(t, err, http.StatusInternalServerError, "internal")
}
