// Package audit appends redacted, append-only kernel events.
package audit

import (
	"context"
	"log/slog"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/redact"
	"github.com/Mrjwj34/nullkey/internal/store/sqlite"
)

// Sink writes audit events through the store.
type Sink struct {
	store *sqlite.Store
	log   *slog.Logger
}

// New builds a sink.
func New(store *sqlite.Store, logger *slog.Logger) *Sink {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sink{store: store, log: logger}
}

// Record redacts details and appends an event. Failures are logged but do not
// fail the user operation.
func (s *Sink) Record(ctx context.Context, eventType string, sandboxID *string, detail map[string]any) {
	event := domain.AuditEvent{
		ID:        eventID(),
		Type:      eventType,
		SandboxID: sandboxID,
		Detail:    redact.Map(detail),
	}
	event.TS = now()
	if err := s.store.AppendAudit(ctx, event); err != nil {
		s.log.Error("audit_append_failed", "type", eventType, "error", err)
	}
}

// List returns redacted audit events.
func (s *Sink) List(ctx context.Context, limit int, cursor, eventType string) ([]domain.AuditEvent, string, error) {
	events, next, err := s.store.ListAudit(ctx, limit, cursor, eventType)
	if err != nil {
		return nil, "", err
	}
	for i := range events {
		events[i].Detail = redact.Map(events[i].Detail)
	}
	return events, next, nil
}
