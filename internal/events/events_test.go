package events

import (
	"context"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func recvEvent(t *testing.T, ch <-chan domain.Event) (domain.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return domain.Event{}, false
	}
}

func TestNewBufferDefaults(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{
		{0, 64},
		{-1, 64},
		{-100, 64},
		{1, 1},
		{8, 8},
	} {
		b := New(tc.in)
		if b.buffer != tc.want {
			t.Fatalf("New(%d).buffer = %d, want %d", tc.in, b.buffer, tc.want)
		}
		if b.subs == nil {
			t.Fatalf("New(%d) has nil subscriber map", tc.in)
		}
		if got := b.Subscribers(); got != 0 {
			t.Fatalf("New(%d).Subscribers() = %d, want 0", tc.in, got)
		}
	}
}

func TestPublishSubscribe(t *testing.T) {
	b := New(4)
	ctx := context.Background()
	ch, cancel, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if ch == nil {
		t.Fatal("Subscribe returned nil channel")
	}
	if got := b.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() = %d, want 1", got)
	}

	want := domain.Event{Type: "sandbox.started", Data: map[string]any{"id": "sbx_1"}}
	if err := b.Publish(ctx, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, ok := recvEvent(t, ch)
	if !ok {
		t.Fatal("subscriber channel closed after publish")
	}
	if got.Type != want.Type {
		t.Fatalf("event type = %q, want %q", got.Type, want.Type)
	}
	if got.Data["id"] != "sbx_1" {
		t.Fatalf("event data = %+v", got.Data)
	}

	cancel()
	if got := b.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() after cancel = %d, want 0", got)
	}
	if _, ok := recvEvent(t, ch); ok {
		t.Fatal("channel still open after cancel")
	}

	// Cancel is idempotent and must not panic or close twice.
	cancel()
	cancel()
}

func TestPublishMultipleSubscribers(t *testing.T) {
	b := New(4)
	ctx := context.Background()
	ch1, cancel1, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	defer cancel1()
	ch2, cancel2, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	defer cancel2()

	if got := b.Subscribers(); got != 2 {
		t.Fatalf("Subscribers() = %d, want 2", got)
	}

	want := domain.Event{Type: "job.succeeded", Data: map[string]any{"job_id": "job_1"}}
	if err := b.Publish(ctx, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for i, ch := range []<-chan domain.Event{ch1, ch2} {
		got, ok := recvEvent(t, ch)
		if !ok {
			t.Fatalf("subscriber %d channel closed", i+1)
		}
		if got.Type != want.Type {
			t.Fatalf("subscriber %d got %q, want %q", i+1, got.Type, want.Type)
		}
	}

	cancel1()
	if got := b.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() after one cancel = %d, want 1", got)
	}
	if err := b.Publish(ctx, domain.Event{Type: "second"}); err != nil {
		t.Fatalf("Publish after cancel: %v", err)
	}
	got, ok := recvEvent(t, ch2)
	if !ok || got.Type != "second" {
		t.Fatalf("remaining subscriber got %+v ok=%v", got, ok)
	}
}

func TestPublishNoSubscribers(t *testing.T) {
	b := New(0)
	if err := b.Publish(context.Background(), domain.Event{Type: "noop"}); err != nil {
		t.Fatalf("Publish without subscribers: %v", err)
	}
	if got := b.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
}

func TestSlowSubscriberClosedAndRemoved(t *testing.T) {
	b := New(1)
	ctx := context.Background()

	slow, slowCancel, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe slow: %v", err)
	}
	defer slowCancel() // must be safe after the bus already closed the channel
	fast, fastCancel, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe fast: %v", err)
	}
	defer fastCancel()

	if got := b.Subscribers(); got != 2 {
		t.Fatalf("Subscribers() = %d, want 2", got)
	}

	// Fill the slow subscriber's buffer; the fast one is drained after each
	// publish so it stays connected.
	if err := b.Publish(ctx, domain.Event{Type: "first"}); err != nil {
		t.Fatalf("Publish first: %v", err)
	}
	if _, ok := recvEvent(t, fast); !ok {
		t.Fatal("fast subscriber closed prematurely")
	}

	done := make(chan error, 1)
	go func() { done <- b.Publish(ctx, domain.Event{Type: "second"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Publish second: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	if got := b.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() after slow drop = %d, want 1", got)
	}

	// The fast subscriber still receives the triggering event.
	got, ok := recvEvent(t, fast)
	if !ok || got.Type != "second" {
		t.Fatalf("fast subscriber got %+v ok=%v", got, ok)
	}

	// The slow subscriber keeps the already-buffered event, then is closed.
	got, ok = recvEvent(t, slow)
	if !ok || got.Type != "first" {
		t.Fatalf("slow subscriber buffered event = %+v ok=%v", got, ok)
	}
	if _, ok := recvEvent(t, slow); ok {
		t.Fatal("slow subscriber channel was not closed after drop")
	}
}

func TestCancelAfterSlowDropIsSafe(t *testing.T) {
	b := New(1)
	ctx := context.Background()
	ch, cancel, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := b.Publish(ctx, domain.Event{Type: "fill"}); err != nil {
		t.Fatalf("Publish fill: %v", err)
	}
	if err := b.Publish(ctx, domain.Event{Type: "drop"}); err != nil {
		t.Fatalf("Publish drop: %v", err)
	}
	if got := b.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
	// The Publish path already closed the channel and removed the subscriber;
	// cancel must not close it a second time. The buffered event is still
	// readable before the channel reports closed.
	if got, ok := recvEvent(t, ch); !ok || got.Type != "fill" {
		t.Fatalf("buffered event = %+v ok=%v", got, ok)
	}
	cancel()
	cancel()
	if _, ok := recvEvent(t, ch); ok {
		t.Fatal("expected closed channel")
	}
}

func TestPublishContextIsIgnored(t *testing.T) {
	b := New(2)
	ch, cancel, err := b.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	cancelled, done := context.WithCancel(context.Background())
	done()
	if err := b.Publish(cancelled, domain.Event{Type: "still-delivered"}); err != nil {
		t.Fatalf("Publish with cancelled context: %v", err)
	}
	got, ok := recvEvent(t, ch)
	if !ok || got.Type != "still-delivered" {
		t.Fatalf("event = %+v ok=%v", got, ok)
	}
}

func TestSubscriberIDsAreUnique(t *testing.T) {
	b := New(1)
	_, cancel1, err := b.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	_, cancel2, err := b.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	if got := b.Subscribers(); got != 2 {
		t.Fatalf("Subscribers() = %d, want 2", got)
	}
	cancel1()
	cancel2()
	if got := b.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
}
