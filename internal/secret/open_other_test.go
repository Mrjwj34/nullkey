//go:build !darwin

package secret

import (
	"bytes"
	"context"
	"testing"
)

func TestOpenDefaultsToEncryptedFile(t *testing.T) {
	t.Setenv(BackendEnv, "")
	home := t.TempDir()
	store, err := Open(home, testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := store.(*fileStore); !ok {
		t.Fatalf("Open returned %T, want *fileStore", store)
	}
	ctx := context.Background()
	value := []byte("sk-test-default-backend")
	if err := store.Put(ctx, "cred", value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, "cred")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get = %q, want %q", got, value)
	}
}
