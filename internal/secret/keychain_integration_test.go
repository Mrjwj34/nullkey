//go:build darwin && cgo

package secret

import (
	"errors"
	"os"
	"testing"

	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

// TestKeychainIntegration exercises the real Security.framework wrapper with a
// throwaway account and a fake value. It is opt-in because writing to the login
// keychain in ordinary unit runs is undesirable. The coverage job sets
// NULLKEY_KEYCHAIN_TEST=1.
func TestKeychainIntegration(t *testing.T) {
	if os.Getenv("NULLKEY_KEYCHAIN_TEST") != "1" {
		t.Skip("set NULLKEY_KEYCHAIN_TEST=1 to exercise the macOS Keychain backend")
	}
	suffix, err := nkid.Random(8)
	if err != nil {
		t.Fatalf("random account: %v", err)
	}
	account := "nullkey-test-" + suffix
	value := []byte("sk-test-keychain-integration-0123456789")
	t.Cleanup(func() { _ = keychainDelete(account) })

	if err := keychainPut(account, value); err != nil {
		// Headless CI runners may have a locked/absent login keychain. The
		// encrypted-file backend remains the CI fallback; skip rather than
		// turning an environment limitation into a product failure.
		t.Skipf("macOS Keychain is unavailable in this environment: %v", err)
	}
	// A second put exercises the update path.
	if err := keychainPut(account, value); err != nil {
		t.Fatalf("keychainPut update: %v", err)
	}
	got, err := keychainGet(account)
	if err != nil {
		t.Fatalf("keychainGet: %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("keychain value mismatch")
	}
	if err := keychainDelete(account); err != nil {
		t.Fatalf("keychainDelete: %v", err)
	}
	if _, err := keychainGet(account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted keychain item error = %v, want ErrNotFound", err)
	}
	// Deleting a missing item is idempotent.
	if err := keychainDelete(account); err != nil {
		t.Fatalf("second keychainDelete: %v", err)
	}
}

func TestKeychainStoreDelegation(t *testing.T) {
	store := newKeychainStore()
	if store == nil {
		t.Fatal("newKeychainStore returned nil")
	}
	// Context cancellation is checked before touching the platform store.
	ctx, cancel := cancelledContext()
	defer cancel()
	if err := store.Put(ctx, "account", []byte("x")); err == nil {
		t.Fatal("Put accepted a cancelled context")
	}
}
