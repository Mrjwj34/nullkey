//go:build darwin && cgo

package secret

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestKeychainStoreValidationDoesNotTouchKeychain(t *testing.T) {
	store := newKeychainStore()
	calls := 0
	store.ops = keychainOps{
		put:    func(string, []byte) error { calls++; return nil },
		get:    func(string) ([]byte, error) { calls++; return nil, nil },
		delete: func(string) error { calls++; return nil },
	}
	ctx := context.Background()
	for _, account := range []string{"", "a\x00b", string([]byte{0xff})} {
		if err := store.Put(ctx, account, []byte("sk-test")); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Put(%q) error = %v, want ErrInvalidAccount", account, err)
		}
		if _, err := store.Get(ctx, account); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Get(%q) error = %v, want ErrInvalidAccount", account, err)
		}
		if err := store.Delete(ctx, account); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Delete(%q) error = %v, want ErrInvalidAccount", account, err)
		}
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Put(canceled, "cred", []byte("sk-test")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put canceled error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(canceled, "cred"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get canceled error = %v, want context.Canceled", err)
	}
	if err := store.Delete(canceled, "cred"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete canceled error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("Keychain operations called %d times for invalid input", calls)
	}
}

func TestKeychainStoreDelegates(t *testing.T) {
	ctx := context.Background()
	store := newKeychainStore()
	sentinel := errors.New("sentinel")

	var gotAccount string
	var putValue []byte
	store.ops.put = func(account string, value []byte) error {
		gotAccount = account
		putValue = append([]byte(nil), value...)
		return sentinel
	}
	if err := store.Put(ctx, "cred", []byte("sk-test-value")); !errors.Is(err, sentinel) {
		t.Fatalf("Put error = %v, want sentinel", err)
	}
	if gotAccount != "cred" {
		t.Fatalf("Put account = %q, want cred", gotAccount)
	}
	if !bytes.Equal(putValue, []byte("sk-test-value")) {
		t.Fatalf("Put value = %q", putValue)
	}

	store.ops.get = func(account string) ([]byte, error) {
		if account != "cred" {
			t.Fatalf("Get account = %q, want cred", account)
		}
		return []byte("sk-test-value"), nil
	}
	first, err := store.Get(ctx, "cred")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	first[0] = 'X'
	second, err := store.Get(ctx, "cred")
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if !bytes.Equal(second, []byte("sk-test-value")) {
		t.Fatalf("Get aliased the backend slice: %q", second)
	}

	store.ops.get = func(string) ([]byte, error) { return nil, sentinel }
	if _, err := store.Get(ctx, "cred"); !errors.Is(err, sentinel) {
		t.Fatalf("Get error = %v, want sentinel", err)
	}

	store.ops.delete = func(account string) error {
		if account != "cred" {
			t.Fatalf("Delete account = %q, want cred", account)
		}
		return sentinel
	}
	if err := store.Delete(ctx, "cred"); !errors.Is(err, sentinel) {
		t.Fatalf("Delete error = %v, want sentinel", err)
	}
}

func TestKeychainErrorFormatting(t *testing.T) {
	err := keychainError("put", -50) // errSecParam
	if err == nil {
		t.Fatal("keychainError returned nil")
	}
	message := err.Error()
	if !strings.Contains(message, "put") || !strings.Contains(message, "keychain") {
		t.Fatalf("keychainError = %q, want operation context", message)
	}
	if !strings.Contains(message, "-50") {
		t.Fatalf("keychainError = %q, want OSStatus", message)
	}
}

func TestOpenDarwinDefaultsToKeychainWithoutTouchingIt(t *testing.T) {
	t.Setenv(BackendEnv, "")
	store, err := Open(t.TempDir(), testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := store.(*keychainStore); !ok {
		t.Fatalf("Open returned %T, want *keychainStore", store)
	}
	// Do not call Put/Get/Delete: the real login Keychain must stay untouched.
}
