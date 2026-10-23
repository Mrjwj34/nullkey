package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var oauthBase = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

func TestOAuthFlowCreateGet(t *testing.T) {
	s, ctx := openTestStore(t)
	credID := "cred_1"
	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID:           "flow_1",
		ProviderID:   "openai",
		Status:       domain.OAuthPending,
		CredentialID: &credID,
		Error:        strPtr("prior"),
		StateHash:    []byte("state-hash-1"),
		CreatedAt:    oauthBase,
		ExpiresAt:    oauthBase.Add(10 * time.Minute),
	})
	got, err := s.GetOAuthFlow(ctx, "flow_1")
	if err != nil {
		t.Fatalf("GetOAuthFlow() error = %v", err)
	}
	if got.ProviderID != "openai" || got.Status != domain.OAuthPending {
		t.Errorf("GetOAuthFlow() = %+v", got)
	}
	if got.CredentialID == nil || *got.CredentialID != credID {
		t.Errorf("CredentialID = %v, want %q", got.CredentialID, credID)
	}
	if got.Error == nil || *got.Error != "prior" {
		t.Errorf("Error = %v", got.Error)
	}
	if string(got.StateHash) != "state-hash-1" {
		t.Errorf("StateHash = %q", got.StateHash)
	}
	if !got.CreatedAt.Equal(oauthBase) || !got.ExpiresAt.Equal(oauthBase.Add(10*time.Minute)) {
		t.Errorf("times = %v/%v", got.CreatedAt, got.ExpiresAt)
	}

	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_bare", ProviderID: "anthropic", Status: domain.OAuthPending,
		CreatedAt: oauthBase, ExpiresAt: oauthBase.Add(time.Minute),
	})
	bare, err := s.GetOAuthFlow(ctx, "flow_bare")
	if err != nil {
		t.Fatalf("GetOAuthFlow(bare) error = %v", err)
	}
	if bare.CredentialID != nil || bare.Error != nil || bare.StateHash != nil {
		t.Errorf("bare flow = %+v", bare)
	}
	if _, err := s.GetOAuthFlow(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetOAuthFlow(missing) error = %v, want sql.ErrNoRows", err)
	}
	if err := s.CreateOAuthFlow(ctx, domain.OAuthFlow{
		ID: "flow_1", ProviderID: "dup", Status: domain.OAuthPending,
		CreatedAt: oauthBase, ExpiresAt: oauthBase.Add(time.Minute),
	}); err == nil {
		t.Error("CreateOAuthFlow() duplicate id error = nil, want error")
	}
}

func TestFindOAuthFlowByStateHash(t *testing.T) {
	s, ctx := openTestStore(t)
	statuses := []domain.OAuthStatus{
		domain.OAuthPending, domain.OAuthSucceeded, domain.OAuthFailed, domain.OAuthExpired, domain.OAuthCancelled,
	}
	for _, status := range statuses {
		mustCreateFlow(t, ctx, s, domain.OAuthFlow{
			ID: "flow_" + string(status), ProviderID: "p", Status: status,
			StateHash: []byte("hash-" + string(status)), CreatedAt: oauthBase,
			ExpiresAt: oauthBase.Add(time.Minute),
		})
	}
	got, err := s.FindOAuthFlowByStateHash(ctx, []byte("hash-pending"))
	if err != nil {
		t.Fatalf("FindOAuthFlowByStateHash(pending) error = %v", err)
	}
	if got.ID != "flow_pending" {
		t.Errorf("FindOAuthFlowByStateHash(pending) = %q", got.ID)
	}
	for _, status := range statuses[1:] {
		if _, err := s.FindOAuthFlowByStateHash(ctx, []byte("hash-"+string(status))); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("FindOAuthFlowByStateHash(%s) error = %v, want sql.ErrNoRows", status, err)
		}
	}
	if _, err := s.FindOAuthFlowByStateHash(ctx, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindOAuthFlowByStateHash(nil) error = %v, want sql.ErrNoRows", err)
	}
}

func TestOAuthFlowUpdateAndExpire(t *testing.T) {
	s, ctx := openTestStore(t)
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_past", ProviderID: "p", Status: domain.OAuthPending,
		StateHash: []byte("past"), CreatedAt: oauthBase, ExpiresAt: past,
	})
	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_future", ProviderID: "p", Status: domain.OAuthPending,
		StateHash: []byte("future"), CreatedAt: oauthBase, ExpiresAt: future,
	})
	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_failed", ProviderID: "p", Status: domain.OAuthFailed,
		CreatedAt: oauthBase, ExpiresAt: past,
	})
	if err := s.ExpireOAuthFlows(ctx); err != nil {
		t.Fatalf("ExpireOAuthFlows() error = %v", err)
	}
	pastFlow, err := s.GetOAuthFlow(ctx, "flow_past")
	if err != nil {
		t.Fatalf("GetOAuthFlow(past) error = %v", err)
	}
	if pastFlow.Status != domain.OAuthExpired {
		t.Errorf("past flow status = %q, want expired", pastFlow.Status)
	}
	futureFlow, err := s.GetOAuthFlow(ctx, "flow_future")
	if err != nil {
		t.Fatalf("GetOAuthFlow(future) error = %v", err)
	}
	if futureFlow.Status != domain.OAuthPending {
		t.Errorf("future flow status = %q, want pending", futureFlow.Status)
	}
	failedFlow, err := s.GetOAuthFlow(ctx, "flow_failed")
	if err != nil {
		t.Fatalf("GetOAuthFlow(failed) error = %v", err)
	}
	if failedFlow.Status != domain.OAuthFailed {
		t.Errorf("failed flow status = %q, want failed", failedFlow.Status)
	}

	credID := "cred_x"
	futureFlow.Status = domain.OAuthSucceeded
	futureFlow.CredentialID = &credID
	futureFlow.Error = strPtr("none")
	if err := s.UpdateOAuthFlow(ctx, futureFlow); err != nil {
		t.Fatalf("UpdateOAuthFlow() error = %v", err)
	}
	updated, err := s.GetOAuthFlow(ctx, "flow_future")
	if err != nil {
		t.Fatalf("GetOAuthFlow(future) error = %v", err)
	}
	if updated.Status != domain.OAuthSucceeded || updated.CredentialID == nil || *updated.CredentialID != credID {
		t.Errorf("updated flow = %+v", updated)
	}
	if updated.Error == nil || *updated.Error != "none" {
		t.Errorf("updated error = %v", updated.Error)
	}
}

func TestCompleteOAuthFlow(t *testing.T) {
	s, ctx := openTestStore(t)
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_ok", ProviderID: "p", Status: domain.OAuthPending,
		StateHash: []byte("ok"), CreatedAt: oauthBase, ExpiresAt: future,
	})
	ok, err := s.CompleteOAuthFlow(ctx, "flow_ok", "cred_new")
	if err != nil {
		t.Fatalf("CompleteOAuthFlow() error = %v", err)
	}
	if !ok {
		t.Fatal("CompleteOAuthFlow() = false, want true")
	}
	got, err := s.GetOAuthFlow(ctx, "flow_ok")
	if err != nil {
		t.Fatalf("GetOAuthFlow() error = %v", err)
	}
	if got.Status != domain.OAuthSucceeded {
		t.Errorf("Status = %q, want succeeded", got.Status)
	}
	if got.CredentialID == nil || *got.CredentialID != "cred_new" {
		t.Errorf("CredentialID = %v, want cred_new", got.CredentialID)
	}
	if got.StateHash != nil {
		t.Errorf("StateHash = %q, want nil", got.StateHash)
	}
	// A second completion (late callback) must be rejected.
	if ok, err := s.CompleteOAuthFlow(ctx, "flow_ok", "cred_late"); err != nil || ok {
		t.Errorf("second CompleteOAuthFlow() = (%v, %v), want (false, nil)", ok, err)
	}
	after, err := s.GetOAuthFlow(ctx, "flow_ok")
	if err != nil {
		t.Fatalf("GetOAuthFlow() error = %v", err)
	}
	if after.CredentialID == nil || *after.CredentialID != "cred_new" {
		t.Errorf("late callback overwrote credential: %v", after.CredentialID)
	}

	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_expired", ProviderID: "p", Status: domain.OAuthPending,
		StateHash: []byte("expired"), CreatedAt: oauthBase, ExpiresAt: past,
	})
	if ok, err := s.CompleteOAuthFlow(ctx, "flow_expired", "cred_x"); err != nil || ok {
		t.Errorf("CompleteOAuthFlow(expired) = (%v, %v), want (false, nil)", ok, err)
	}
	expired, err := s.GetOAuthFlow(ctx, "flow_expired")
	if err != nil {
		t.Fatalf("GetOAuthFlow(expired) error = %v", err)
	}
	if expired.Status != domain.OAuthPending || expired.CredentialID != nil {
		t.Errorf("expired completion changed flow: %+v", expired)
	}

	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_cancelled", ProviderID: "p", Status: domain.OAuthCancelled,
		CreatedAt: oauthBase, ExpiresAt: future,
	})
	if ok, err := s.CompleteOAuthFlow(ctx, "flow_cancelled", "cred_x"); err != nil || ok {
		t.Errorf("CompleteOAuthFlow(cancelled) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := s.CompleteOAuthFlow(ctx, "missing", "cred_x"); err != nil || ok {
		t.Errorf("CompleteOAuthFlow(missing) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestCancelOAuthFlow(t *testing.T) {
	s, ctx := openTestStore(t)
	future := time.Now().UTC().Add(time.Hour)
	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_cancel", ProviderID: "p", Status: domain.OAuthPending,
		StateHash: []byte("cancel-me"), CreatedAt: oauthBase, ExpiresAt: future,
	})
	ok, err := s.CancelOAuthFlow(ctx, "flow_cancel")
	if err != nil {
		t.Fatalf("CancelOAuthFlow() error = %v", err)
	}
	if !ok {
		t.Fatal("CancelOAuthFlow() = false, want true")
	}
	got, err := s.GetOAuthFlow(ctx, "flow_cancel")
	if err != nil {
		t.Fatalf("GetOAuthFlow() error = %v", err)
	}
	if got.Status != domain.OAuthCancelled {
		t.Errorf("Status = %q, want cancelled", got.Status)
	}
	if got.StateHash != nil {
		t.Errorf("StateHash = %q, want nil", got.StateHash)
	}
	if _, err := s.FindOAuthFlowByStateHash(ctx, []byte("cancel-me")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindOAuthFlowByStateHash after cancel error = %v, want sql.ErrNoRows", err)
	}
	if ok, err := s.CancelOAuthFlow(ctx, "flow_cancel"); err != nil || ok {
		t.Errorf("second CancelOAuthFlow() = (%v, %v), want (false, nil)", ok, err)
	}
	// A late callback after cancellation must not persist a credential.
	if ok, err := s.CompleteOAuthFlow(ctx, "flow_cancel", "cred_late"); err != nil || ok {
		t.Errorf("CompleteOAuthFlow after cancel = (%v, %v), want (false, nil)", ok, err)
	}

	mustCreateFlow(t, ctx, s, domain.OAuthFlow{
		ID: "flow_done", ProviderID: "p", Status: domain.OAuthSucceeded,
		CreatedAt: oauthBase, ExpiresAt: future,
	})
	if ok, err := s.CancelOAuthFlow(ctx, "flow_done"); err != nil || ok {
		t.Errorf("CancelOAuthFlow(succeeded) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := s.CancelOAuthFlow(ctx, "missing"); err != nil || ok {
		t.Errorf("CancelOAuthFlow(missing) = (%v, %v), want (false, nil)", ok, err)
	}
}
