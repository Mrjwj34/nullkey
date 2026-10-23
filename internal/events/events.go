// Package events is a bounded, best-effort SSE fan-out bus.
package events

import (
	"context"
	"sync"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// Bus fans events out to subscribers. A slow subscriber is disconnected rather
// than blocking publishers.
type Bus struct {
	mu     sync.Mutex
	next   int
	subs   map[int]*subscriber
	buffer int
}

type subscriber struct {
	ch     chan domain.Event
	closed bool
}

// New creates a bus with the given per-subscriber buffer (default 64).
func New(buffer int) *Bus {
	if buffer <= 0 {
		buffer = 64
	}
	return &Bus{subs: make(map[int]*subscriber), buffer: buffer}
}

// Publish delivers an event without blocking. The returned error is always nil;
// it exists to satisfy the ports.EventBus contract.
func (b *Bus) Publish(_ context.Context, event domain.Event) error {
	b.mu.Lock()
	var drop []int
	for id, sub := range b.subs {
		if sub.closed {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			// Slow client: disconnect it so the bus stays non-blocking.
			sub.closed = true
			close(sub.ch)
			drop = append(drop, id)
		}
	}
	for _, id := range drop {
		delete(b.subs, id)
	}
	b.mu.Unlock()
	return nil
}

// Subscribe returns a channel and a cancel function.
func (b *Bus) Subscribe(_ context.Context) (<-chan domain.Event, func(), error) {
	b.mu.Lock()
	id := b.next
	b.next++
	sub := &subscriber{ch: make(chan domain.Event, b.buffer)}
	b.subs[id] = sub
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if current, ok := b.subs[id]; ok && !current.closed {
			current.closed = true
			close(current.ch)
		}
		delete(b.subs, id)
		b.mu.Unlock()
	}
	return sub.ch, cancel, nil
}

// Subscribers returns the current subscriber count (diagnostics/tests).
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
