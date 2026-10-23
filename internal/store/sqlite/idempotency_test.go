package sqlite

import (
	"bytes"
	"testing"
	"time"
)

func TestIdempotencyMissAndHit(t *testing.T) {
	s, ctx := openTestStore(t)
	if _, ok, err := s.GetIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", "hash"); err != nil || ok {
		t.Fatalf("GetIdempotent(miss) = (%v, %v), want (false, nil)", ok, err)
	}
	body := []byte(`{"id":"ws_1"}`)
	if err := s.PutIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", "hash", 201, body); err != nil {
		t.Fatalf("PutIdempotent() error = %v", err)
	}
	stored, ok, err := s.GetIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", "hash")
	if err != nil {
		t.Fatalf("GetIdempotent() error = %v", err)
	}
	if !ok {
		t.Fatal("GetIdempotent() hit = false, want true")
	}
	if stored.Status != 201 || !bytes.Equal(stored.Body, body) {
		t.Errorf("stored response = %d %q, want 201 %q", stored.Status, stored.Body, body)
	}

	binary := []byte{0x00, 0x01, 0xff, 'a'}
	if err := s.PutIdempotent(ctx, "key-binary", "POST", "/p", "hash", 200, binary); err != nil {
		t.Fatalf("PutIdempotent(binary) error = %v", err)
	}
	got, ok, err := s.GetIdempotent(ctx, "key-binary", "POST", "/p", "hash")
	if err != nil || !ok || !bytes.Equal(got.Body, binary) {
		t.Errorf("binary body = %v, ok=%v, err=%v", got.Body, ok, err)
	}

	empty := []byte{}
	if err := s.PutIdempotent(ctx, "key-empty", "POST", "/p", "hash", 204, empty); err != nil {
		t.Fatalf("PutIdempotent(empty) error = %v", err)
	}
	got, ok, err = s.GetIdempotent(ctx, "key-empty", "POST", "/p", "hash")
	if err != nil || !ok || len(got.Body) != 0 || got.Status != 204 {
		t.Errorf("empty body = %v, ok=%v, status=%d, err=%v", got.Body, ok, got.Status, err)
	}
}

func TestIdempotencyIsolation(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.PutIdempotent(ctx, "key-1", "POST", "/path-a", "hash", 200, []byte("a")); err != nil {
		t.Fatalf("PutIdempotent() error = %v", err)
	}
	if _, ok, err := s.GetIdempotent(ctx, "key-1", "PUT", "/path-a", "hash"); err != nil || ok {
		t.Errorf("same key different method = (ok %v, err %v), want miss", ok, err)
	}
	if _, ok, err := s.GetIdempotent(ctx, "key-1", "POST", "/path-b", "hash"); err != nil || ok {
		t.Errorf("same key different path = (ok %v, err %v), want miss", ok, err)
	}
	if _, ok, err := s.GetIdempotent(ctx, "key-2", "POST", "/path-a", "hash"); err != nil || ok {
		t.Errorf("different key = (ok %v, err %v), want miss", ok, err)
	}
}

func TestIdempotencyBodyHashMismatch(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.PutIdempotent(ctx, "key-1", "POST", "/p", "hash-a", 200, []byte("a")); err != nil {
		t.Fatalf("PutIdempotent() error = %v", err)
	}
	_, ok, err := s.GetIdempotent(ctx, "key-1", "POST", "/p", "hash-b")
	if err == nil {
		t.Fatal("GetIdempotent() with mismatched body hash error = nil, want error")
	}
	if ok {
		t.Error("GetIdempotent() with mismatched body hash reported a hit")
	}
	// The same key with the same request is still readable.
	if _, ok, err := s.GetIdempotent(ctx, "key-1", "POST", "/p", "hash-a"); err != nil || !ok {
		t.Errorf("GetIdempotent(same hash) = (ok %v, err %v)", ok, err)
	}
}

func TestIdempotencyPutReplaces(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.PutIdempotent(ctx, "key-1", "POST", "/p", "hash-1", 200, []byte("first")); err != nil {
		t.Fatalf("PutIdempotent(first) error = %v", err)
	}
	if err := s.PutIdempotent(ctx, "key-1", "POST", "/p", "hash-2", 201, []byte("second")); err != nil {
		t.Fatalf("PutIdempotent(second) error = %v", err)
	}
	got, ok, err := s.GetIdempotent(ctx, "key-1", "POST", "/p", "hash-2")
	if err != nil || !ok {
		t.Fatalf("GetIdempotent() = (ok %v, err %v)", ok, err)
	}
	if got.Status != 201 || string(got.Body) != "second" {
		t.Errorf("replaced response = %d %q", got.Status, got.Body)
	}
}

func TestCleanupIdempotency(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.PutIdempotent(ctx, "key-old", "POST", "/old", "h", 200, []byte("old")); err != nil {
		t.Fatalf("PutIdempotent(old) error = %v", err)
	}
	if err := s.PutIdempotent(ctx, "key-new", "POST", "/new", "h", 200, []byte("new")); err != nil {
		t.Fatalf("PutIdempotent(new) error = %v", err)
	}
	cutoff := time.Now().UTC().Add(-time.Hour)
	if _, err := s.db.ExecContext(ctx, `UPDATE idempotency SET created_at=? WHERE key='key-old'`, formatTime(cutoff.Add(-2*time.Hour))); err != nil {
		t.Fatalf("seed old timestamp: %v", err)
	}
	if err := s.CleanupIdempotency(ctx, cutoff); err != nil {
		t.Fatalf("CleanupIdempotency() error = %v", err)
	}
	if _, ok, err := s.GetIdempotent(ctx, "key-old", "POST", "/old", "h"); err != nil || ok {
		t.Errorf("old entry after cleanup = (ok %v, err %v), want miss", ok, err)
	}
	if _, ok, err := s.GetIdempotent(ctx, "key-new", "POST", "/new", "h"); err != nil || !ok {
		t.Errorf("new entry after cleanup = (ok %v, err %v), want hit", ok, err)
	}
	if err := s.CleanupIdempotency(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("CleanupIdempotency(future) error = %v", err)
	}
	if _, ok, err := s.GetIdempotent(ctx, "key-new", "POST", "/new", "h"); err != nil || ok {
		t.Errorf("entry after future cleanup = (ok %v, err %v), want miss", ok, err)
	}
}
