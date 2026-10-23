package secret

import (
	"context"
	"fmt"
	"sync"
)

// memoryStore is an in-memory Store used by tests and by callers that must not
// touch persistent storage (for example, unit tests of the credentials API).
type memoryStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

// NewMemory returns a concurrency-safe in-memory Store. Values are copied on
// write and on read; nothing is written to disk.
func NewMemory() Store {
	return &memoryStore{values: make(map[string][]byte)}
}

func (m *memoryStore) Put(ctx context.Context, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAccount(account); err != nil {
		return err
	}
	copied := append([]byte(nil), value...)
	m.mu.Lock()
	m.values[account] = copied
	m.mu.Unlock()
	return nil
}

func (m *memoryStore) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAccount(account); err != nil {
		return nil, err
	}
	m.mu.RLock()
	stored, ok := m.values[account]
	var copied []byte
	if ok {
		copied = append([]byte(nil), stored...)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: account not found", ErrNotFound)
	}
	return copied, nil
}

// Delete removes an account. Deleting an absent account is not an error, which
// makes credential revocation idempotent.
func (m *memoryStore) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAccount(account); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.values, account)
	m.mu.Unlock()
	return nil
}
