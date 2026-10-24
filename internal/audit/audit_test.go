package audit

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/store/sqlite"
)

const fakeSecret = "sk-test-abcdef123456"

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func findEvent(t *testing.T, events []domain.AuditEvent, eventType string) domain.AuditEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Type == eventType {
			return ev
		}
	}
	t.Fatalf("event type %q not found in %d events", eventType, len(events))
	return domain.AuditEvent{}
}

func TestNewNilLogger(t *testing.T) {
	sink := New(newStore(t), nil)
	if sink.log == nil {
		t.Fatal("New with nil logger left log unset")
	}
	if sink.store == nil {
		t.Fatal("New with nil store: store field unset")
	}
}

func TestRecordAndListRedacts(t *testing.T) {
	st := newStore(t)
	sink := New(st, discardLogger())
	ctx := context.Background()
	sandboxID := "sbx_0123456789ab"

	sink.Record(ctx, "sandbox.started", &sandboxID, map[string]any{
		"command": "echo " + fakeSecret,
		"nested": map[string]any{
			"token":  fakeSecret,
			"count":  3,
			"enable": true,
		},
		"items":  []any{fakeSecret, 7, nil},
		"labels": []string{fakeSecret, "visible"},
		"plain":  "no secret here",
	})

	sink.Record(ctx, "sandbox.stopped", nil, nil)

	events, next, err := sink.List(ctx, 10, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if next != "" {
		t.Fatalf("next cursor = %q, want empty", next)
	}
	if len(events) != 2 {
		t.Fatalf("List returned %d events, want 2", len(events))
	}

	for _, ev := range events {
		if !strings.HasPrefix(ev.ID, "evt_") {
			t.Fatalf("event id %q lacks evt_ prefix", ev.ID)
		}
		if ev.TS.IsZero() {
			t.Fatalf("event %s has zero timestamp", ev.ID)
		}
		if ev.TS.Location() != time.UTC {
			t.Fatalf("event %s timestamp location = %v, want UTC", ev.ID, ev.TS.Location())
		}
	}
	if events[0].ID == events[1].ID {
		t.Fatalf("duplicate event ids: %s", events[0].ID)
	}

	started := findEvent(t, events, "sandbox.started")
	if started.SandboxID == nil || *started.SandboxID != sandboxID {
		t.Fatalf("sandbox id = %v, want %q", started.SandboxID, sandboxID)
	}
	if got := started.Detail["command"]; got != "echo [REDACTED]" {
		t.Fatalf("command detail = %q, want redacted", got)
	}
	if strings.Contains(toString(t, started.Detail), fakeSecret) {
		t.Fatalf("raw secret leaked into listed detail: %s", toString(t, started.Detail))
	}
	if got := started.Detail["plain"]; got != "no secret here" {
		t.Fatalf("non-secret detail = %v", got)
	}

	nested, ok := started.Detail["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested detail type = %T", started.Detail["nested"])
	}
	if nested["token"] != "[REDACTED]" {
		t.Fatalf("nested token = %v, want [REDACTED]", nested["token"])
	}
	if nested["count"] != float64(3) {
		t.Fatalf("nested count = %v (%T), want 3", nested["count"], nested["count"])
	}
	if nested["enable"] != true {
		t.Fatalf("nested enable = %v, want true", nested["enable"])
	}

	items, ok := started.Detail["items"].([]any)
	if !ok {
		t.Fatalf("items type = %T", started.Detail["items"])
	}
	if items[0] != "[REDACTED]" || items[1] != float64(7) || items[2] != nil {
		t.Fatalf("items = %#v", items)
	}

	labels, ok := started.Detail["labels"].([]any)
	if !ok {
		t.Fatalf("labels type = %T", started.Detail["labels"])
	}
	if labels[0] != "[REDACTED]" || labels[1] != "visible" {
		t.Fatalf("labels = %#v", labels)
	}

	stopped := findEvent(t, events, "sandbox.stopped")
	if stopped.SandboxID != nil {
		t.Fatalf("nil sandbox id became %v", stopped.SandboxID)
	}
	if len(stopped.Detail) != 0 {
		t.Fatalf("nil detail round-tripped as %#v, want empty", stopped.Detail)
	}

	// Record must redact before persisting, not only on the way out of List.
	stored, _, err := st.ListAudit(ctx, 10, "", "sandbox.started")
	if err != nil {
		t.Fatalf("list raw stored audit events: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored sandbox.started events = %d, want 1", len(stored))
	}
	if strings.Contains(toString(t, stored[0].Detail), fakeSecret) {
		t.Fatalf("secret persisted to audit store unredacted: %#v", stored[0].Detail)
	}
	if got := stored[0].Detail["command"]; got != "echo [REDACTED]" {
		t.Fatalf("stored command detail = %v, want redacted at write time", got)
	}
}

// toString is only used for assertion failure messages.
func toString(t *testing.T, detail map[string]any) string {
	t.Helper()
	var b strings.Builder
	for k, v := range detail {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(strings.TrimSpace(slog.AnyValue(v).String()))
		b.WriteString(" ")
	}
	return b.String()
}

func TestListRedactsDetailsAlreadyInStore(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ts := time.Now().UTC()
	if err := st.AppendAudit(ctx, domain.AuditEvent{
		ID:   "evt_direct",
		TS:   ts,
		Type: "credential.imported",
		Detail: map[string]any{
			"header": "Authorization: Bearer " + fakeSecret,
			"nested": map[string]any{"key": fakeSecret},
		},
	}); err != nil {
		t.Fatalf("append raw audit event: %v", err)
	}

	sink := New(st, discardLogger())
	events, _, err := sink.List(ctx, 10, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("List returned %d events, want 1", len(events))
	}
	if strings.Contains(toString(t, events[0].Detail), fakeSecret) {
		t.Fatalf("List did not redact stored detail: %#v", events[0].Detail)
	}
	if got := events[0].Detail["header"]; got != "Authorization: Bearer [REDACTED]" {
		t.Fatalf("header = %v, want redacted", got)
	}
	nested, ok := events[0].Detail["nested"].(map[string]any)
	if !ok || nested["key"] != "[REDACTED]" {
		t.Fatalf("nested = %#v", events[0].Detail["nested"])
	}
}

func TestListFilterAndLimit(t *testing.T) {
	st := newStore(t)
	sink := New(st, discardLogger())
	ctx := context.Background()
	for _, eventType := range []string{"sandbox.started", "sandbox.stopped", "sandbox.started"} {
		sink.Record(ctx, eventType, nil, map[string]any{"secret": fakeSecret})
	}

	filtered, _, err := sink.List(ctx, 10, "", "sandbox.stopped")
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Type != "sandbox.stopped" {
		t.Fatalf("filtered events = %+v", filtered)
	}

	page, next, err := sink.List(ctx, 2, "", "")
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page 1 len = %d, want 2", len(page))
	}
	if next == "" {
		t.Fatal("page 1 next cursor is empty")
	}
	for _, ev := range page {
		if strings.Contains(toString(t, ev.Detail), fakeSecret) {
			t.Fatalf("paged event leaked secret: %#v", ev.Detail)
		}
	}
	page2, next2, err := sink.List(ctx, 2, next, "")
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page 2 len = %d, want 1", len(page2))
	}
	if next2 != "" {
		t.Fatalf("page 2 next cursor = %q, want empty", next2)
	}
}

func TestListErrorPropagates(t *testing.T) {
	st := newStore(t)
	sink := New(st, discardLogger())
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, _, err := sink.List(context.Background(), 10, "", ""); err == nil {
		t.Fatal("List on closed store returned nil error")
	}
}

func TestRecordFailureDoesNotPanic(t *testing.T) {
	st := newStore(t)
	sink := New(st, discardLogger())
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	// Record logs and returns when the store is unavailable.
	sink.Record(context.Background(), "sandbox.started", nil, map[string]any{"key": fakeSecret})
}

func TestEventIDAndNow(t *testing.T) {
	if got := eventID(); !strings.HasPrefix(got, "evt_") {
		t.Fatalf("eventID() = %q, want evt_ prefix", got)
	}
	if got := eventID(); got == eventID() {
		t.Fatal("eventID() returned a duplicate")
	}
	ts := now()
	if ts.Location() != time.UTC {
		t.Fatalf("now() location = %v, want UTC", ts.Location())
	}
}

func TestRecordSurvivesCancelledContext(t *testing.T) {
	store := newStore(t)
	sink := New(store, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink.Record(ctx, "cancelled.request", nil, map[string]any{"ok": true})
	events, _, err := sink.List(context.Background(), 10, "", "cancelled.request")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}
