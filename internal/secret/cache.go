package secret

import (
	"context"
	"sync"
	"time"
)

// Cached wraps a Store with a short-lived, per-account in-memory cache. The
// macOS Keychain can take seconds on first access (and hundreds of
// milliseconds afterwards); the gateway must not pay that cost for every
// parallel Codex request. Writes update the cache atomically so a refreshed
// OAuth token is visible immediately.
type Cached struct {
	store       Store
	ttl         time.Duration
	negativeTTL time.Duration
	mu          sync.Mutex
	items       map[string]*cacheItem
}

type cacheItem struct {
	mu       sync.Mutex
	value    []byte
	expires  time.Time
	valid    bool
	err      error
	errUntil time.Time
}

// NewCached wraps store. A non-positive ttl defaults to five minutes.
func NewCached(store Store, ttl time.Duration) *Cached {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Cached{store: store, ttl: ttl, negativeTTL: 60 * time.Second, items: make(map[string]*cacheItem)}
}

func (c *Cached) item(account string) *cacheItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.items[account]
	if item == nil {
		item = &cacheItem{}
		c.items[account] = item
	}
	return item
}

func (c *Cached) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	item := c.item(account)
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.valid && time.Now().Before(item.expires) {
		return append([]byte(nil), item.value...), nil
	}
	if item.err != nil && time.Now().Before(item.errUntil) {
		return nil, item.err
	}
	value, err := c.store.Get(ctx, account)
	if err != nil {
		// Do not let one denied Keychain prompt turn into a prompt storm from
		// every queued Codex request. The caller still fails closed; a write or
		// delete clears the negative entry immediately.
		item.err = err
		item.errUntil = time.Now().Add(c.negativeTTL)
		return nil, err
	}
	item.value = append([]byte(nil), value...)
	item.expires = time.Now().Add(c.ttl)
	item.valid = true
	item.err = nil
	item.errUntil = time.Time{}
	return append([]byte(nil), value...), nil
}

func (c *Cached) Put(ctx context.Context, account string, value []byte) error {
	if err := c.store.Put(ctx, account, value); err != nil {
		return err
	}
	item := c.item(account)
	item.mu.Lock()
	item.value = append([]byte(nil), value...)
	item.expires = time.Now().Add(c.ttl)
	item.valid = true
	item.err = nil
	item.errUntil = time.Time{}
	item.mu.Unlock()
	return nil
}

func (c *Cached) Delete(ctx context.Context, account string) error {
	if err := c.store.Delete(ctx, account); err != nil {
		return err
	}
	item := c.item(account)
	item.mu.Lock()
	item.value = nil
	item.expires = time.Time{}
	item.valid = false
	item.err = nil
	item.errUntil = time.Time{}
	item.mu.Unlock()
	return nil
}

var _ Store = (*Cached)(nil)
