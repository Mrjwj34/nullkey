package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMemoryCRUD(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	const account = "cred-1234"
	original := []byte("sk-test-0123456789abcdef")

	if err := store.Put(ctx, account, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("Get = %q, want %q", got, original)
	}

	// Input and output slices must not alias the stored value.
	original[0] = 'X'
	got[0] = 'Y'
	again, err := store.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get after mutation: %v", err)
	}
	if !bytes.Equal(again, []byte("sk-test-0123456789abcdef")) {
		t.Fatalf("stored value was mutated: %q", again)
	}

	replacement := []byte("sk-test-replacement")
	if err := store.Put(ctx, account, replacement); err != nil {
		t.Fatalf("overwrite Put: %v", err)
	}
	again, err = store.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !bytes.Equal(again, replacement) {
		t.Fatalf("Get after overwrite = %q, want %q", again, replacement)
	}

	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, account); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, account); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func TestMemoryInvalidAccount(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	cases := []struct {
		name    string
		account string
	}{
		{"empty", ""},
		{"nul", "cred\x00evil"},
		{"invalid utf8", string([]byte{'c', 0xff})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Put(ctx, tc.account, []byte("x")); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("Put error = %v, want ErrInvalidAccount", err)
			}
			if _, err := store.Get(ctx, tc.account); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("Get error = %v, want ErrInvalidAccount", err)
			}
			if err := store.Delete(ctx, tc.account); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("Delete error = %v, want ErrInvalidAccount", err)
			}
		})
	}
}

func TestMemoryCanceledContext(t *testing.T) {
	store := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Put(ctx, "cred", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, "cred"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, "cred"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete error = %v, want context.Canceled", err)
	}
}

func TestMemoryConcurrent(t *testing.T) {
	const goroutines = 24
	const iterations = 40
	ctx := context.Background()
	store := NewMemory()

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			account := fmt.Sprintf("cred-%d", g)
			for i := 0; i < iterations; i++ {
				value := []byte(fmt.Sprintf("value-%d-%d", g, i))
				if err := store.Put(ctx, account, value); err != nil {
					errs <- fmt.Errorf("Put: %w", err)
					return
				}
				got, err := store.Get(ctx, account)
				if err != nil {
					errs <- fmt.Errorf("Get: %w", err)
					return
				}
				if !bytes.Equal(got, value) {
					errs <- fmt.Errorf("Get = %q, want %q", got, value)
					return
				}
				if i%5 == 0 {
					if err := store.Delete(ctx, account); err != nil {
						errs <- fmt.Errorf("Delete: %w", err)
						return
					}
					if _, err := store.Get(ctx, account); !errors.Is(err, ErrNotFound) {
						errs <- fmt.Errorf("Get after Delete error = %v, want ErrNotFound", err)
						return
					}
				}
			}
			if err := store.Delete(ctx, account); err != nil {
				errs <- fmt.Errorf("final Delete: %w", err)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestFingerprint(t *testing.T) {
	// Known SHA-256 prefix for "hello" guards against accidental format drift.
	if got, want := Fingerprint([]byte("hello")), "sha256:2cf24dba5fb0a30e"; got != want {
		t.Fatalf("Fingerprint(hello) = %q, want %q", got, want)
	}
	value := []byte("sk-test-0123456789abcdefghijklmnopqrstuvwxyz")
	fp := Fingerprint(value)
	if fp != Fingerprint(value) {
		t.Fatal("Fingerprint is not deterministic")
	}
	if !strings.HasPrefix(fp, "sha256:") {
		t.Fatalf("Fingerprint = %q, want sha256: prefix", fp)
	}
	if strings.Contains(fp, string(value)) {
		t.Fatalf("Fingerprint leaked the full secret: %q", fp)
	}
	if fp == string(value) {
		t.Fatal("Fingerprint equals the secret")
	}
	if Fingerprint([]byte("a")) == Fingerprint([]byte("b")) {
		t.Fatal("different values produced the same fingerprint")
	}
	if Fingerprint(nil) == "" {
		t.Fatal("empty value produced an empty fingerprint")
	}
}

func TestOpenRejectsUnknownBackend(t *testing.T) {
	t.Setenv(BackendEnv, "keychain")
	if _, err := Open(t.TempDir(), testLogger()); err == nil {
		t.Fatal("Open accepted an unsupported backend")
	}
}

func TestOpenFileBackendCaseInsensitiveOverride(t *testing.T) {
	t.Setenv(BackendEnv, "  FILE  ")
	home := t.TempDir()
	store, err := Open(home, testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := store.(*fileStore); !ok {
		t.Fatalf("Open returned %T, want *fileStore", store)
	}
	if err := store.Put(context.Background(), "cred", []byte("sk-test-value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestOpenFileBackendEmptyHome(t *testing.T) {
	t.Setenv(BackendEnv, "file")
	if _, err := Open("", testLogger()); err == nil {
		t.Fatal("Open accepted an empty home directory")
	}
}

func TestOpenFileBackendNilLogger(t *testing.T) {
	t.Setenv(BackendEnv, "file")
	store, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Open with nil logger: %v", err)
	}
	if err := store.Put(context.Background(), "cred", []byte("sk-test-value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}
