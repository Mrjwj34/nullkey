package secret

import (
	"context"
	"errors"
	"testing"
	"time"
)

type countingStore struct {
	Store
	gets int
	put  int
}

func (s *countingStore) Get(ctx context.Context, account string) ([]byte, error) {
	s.gets++
	return s.Store.Get(ctx, account)
}

func (s *countingStore) Put(ctx context.Context, account string, value []byte) error {
	s.put++
	return s.Store.Put(ctx, account, value)
}

func TestCachedStoreReadsOnceAndInvalidatesOnWrite(t *testing.T) {
	backing := &countingStore{Store: NewMemory()}
	cache := NewCached(backing, time.Minute)
	ctx := context.Background()
	if err := cache.Put(ctx, "cred_1", []byte("one")); err != nil {
		t.Fatalf("put: %v", err)
	}
	for i := 0; i < 3; i++ {
		value, err := cache.Get(ctx, "cred_1")
		if err != nil || string(value) != "one" {
			t.Fatalf("get = %q, %v", value, err)
		}
	}
	if backing.gets != 0 {
		t.Fatalf("cache miss after put: gets=%d", backing.gets)
	}
	if err := cache.Put(ctx, "cred_1", []byte("two")); err != nil {
		t.Fatalf("put2: %v", err)
	}
	value, _ := cache.Get(ctx, "cred_1")
	if string(value) != "two" {
		t.Fatalf("stale value = %q", value)
	}
	if err := cache.Delete(ctx, "cred_1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cache.Get(ctx, "cred_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestCachedStoreExpires(t *testing.T) {
	backing := &countingStore{Store: NewMemory()}
	cache := NewCached(backing, time.Nanosecond)
	ctx := context.Background()
	_ = backing.Store.Put(ctx, "cred", []byte("x"))
	_, _ = cache.Get(ctx, "cred")
	time.Sleep(time.Millisecond)
	if _, err := cache.Get(ctx, "cred"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if backing.gets != 2 {
		t.Fatalf("gets = %d, want 2", backing.gets)
	}
}

type failingStore struct {
	Store
	err   error
	calls int
}

func (s *failingStore) Get(context.Context, string) ([]byte, error) {
	s.calls++
	return nil, s.err
}

func TestCachedStoreNegativeCachesDeniedReads(t *testing.T) {
	backing := &failingStore{Store: NewMemory(), err: errors.New("keychain denied")}
	cache := NewCached(backing, time.Minute)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := cache.Get(ctx, "cred_denied"); err == nil {
			t.Fatal("expected failure")
		}
	}
	if backing.calls != 1 {
		t.Fatalf("underlying reads = %d, want 1", backing.calls)
	}
	if err := cache.Put(ctx, "cred_denied", []byte("recovered")); err != nil {
		t.Fatalf("put: %v", err)
	}
	value, err := cache.Get(ctx, "cred_denied")
	if err != nil || string(value) != "recovered" {
		t.Fatalf("get after put = %q, %v", value, err)
	}
}
