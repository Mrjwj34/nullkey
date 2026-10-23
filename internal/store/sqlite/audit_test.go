package sqlite

import (
	"reflect"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var auditBase = time.Date(2024, 7, 1, 13, 0, 0, 0, time.UTC)

func TestAuditAppendAndList(t *testing.T) {
	s, ctx := openTestStore(t)
	sandboxID := "sb_1"
	mustCreateAudit(t, ctx, s, domain.AuditEvent{
		ID: "evt_1", TS: auditBase, Type: "sandbox.started", SandboxID: &sandboxID,
		Detail: map[string]any{"nested": map[string]any{"phase": "boot"}, "count": float64(3)},
	})
	mustCreateAudit(t, ctx, s, domain.AuditEvent{
		ID: "evt_2", TS: auditBase.Add(time.Minute), Type: "sandbox.stopped",
	})
	mustCreateAudit(t, ctx, s, domain.AuditEvent{
		ID: "evt_3", TS: auditBase.Add(2 * time.Minute), Type: "sandbox.started",
		Detail: map[string]any{},
	})

	events, next, err := s.ListAudit(ctx, 10, "", "")
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	if next != "" || len(events) != 3 {
		t.Fatalf("ListAudit() len = %d, next=%q", len(events), next)
	}
	if events[0].ID != "evt_3" || events[1].ID != "evt_2" || events[2].ID != "evt_1" {
		t.Errorf("ListAudit() ordering = %v", auditIDs(events))
	}
	if events[2].SandboxID == nil || *events[2].SandboxID != sandboxID {
		t.Errorf("SandboxID = %v, want %q", events[2].SandboxID, sandboxID)
	}
	wantDetail := map[string]any{"nested": map[string]any{"phase": "boot"}, "count": float64(3)}
	if !reflect.DeepEqual(events[2].Detail, wantDetail) {
		t.Errorf("Detail = %#v, want %#v", events[2].Detail, wantDetail)
	}
	if len(events[1].Detail) != 0 {
		t.Errorf("nil-detail event Detail = %#v, want empty", events[1].Detail)
	}
	if events[0].Detail == nil || len(events[0].Detail) != 0 {
		t.Errorf("empty-detail event Detail = %#v", events[0].Detail)
	}

	started, _, err := s.ListAudit(ctx, 10, "", "sandbox.started")
	if err != nil {
		t.Fatalf("ListAudit(filter) error = %v", err)
	}
	if len(started) != 2 || started[0].ID != "evt_3" || started[1].ID != "evt_1" {
		t.Errorf("ListAudit(filter) = %v", auditIDs(started))
	}

	var seen []string
	cursor := ""
	for {
		page, next, err := s.ListAudit(ctx, 1, cursor, "")
		if err != nil {
			t.Fatalf("ListAudit(limit=1) error = %v", err)
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		seen = append(seen, page[0].ID)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 || seen[0] != "evt_3" || seen[1] != "evt_2" || seen[2] != "evt_1" {
		t.Errorf("paged audit events = %v", seen)
	}
	if _, _, err := s.ListAudit(ctx, 1, "%%%bad", ""); err == nil {
		t.Error("ListAudit() with invalid cursor error = nil, want error")
	}
}

func TestAuditTieBreakAndEmpty(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateAudit(t, ctx, s, domain.AuditEvent{ID: "evt_a", TS: auditBase, Type: "x"})
	mustCreateAudit(t, ctx, s, domain.AuditEvent{ID: "evt_b", TS: auditBase, Type: "x"})
	events, next, err := s.ListAudit(ctx, 1, "", "")
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt_b" || next == "" {
		t.Fatalf("first page = %v, next=%q", auditIDs(events), next)
	}
	second, _, err := s.ListAudit(ctx, 1, next, "")
	if err != nil {
		t.Fatalf("ListAudit(second) error = %v", err)
	}
	if len(second) != 1 || second[0].ID != "evt_a" {
		t.Errorf("second page = %v", auditIDs(second))
	}

	empty, next, err := s.ListAudit(ctx, 10, "", "missing-type")
	if err != nil {
		t.Fatalf("ListAudit(no match) error = %v", err)
	}
	if empty == nil || len(empty) != 0 || next != "" {
		t.Errorf("ListAudit(no match) = %#v, next=%q", empty, next)
	}
}

func TestAuditDoesNotRedact(t *testing.T) {
	s, ctx := openTestStore(t)
	// Redaction is a caller concern; the store must persist exactly what it is
	// given so audits remain verifiable.
	detail := map[string]any{"authorization": "Bearer sk-test-should-not-be-here"}
	mustCreateAudit(t, ctx, s, domain.AuditEvent{ID: "evt_secret", TS: auditBase, Type: "debug", Detail: detail})
	events, _, err := s.ListAudit(ctx, 10, "", "debug")
	if err != nil {
		t.Fatalf("ListAudit() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ListAudit() len = %d, want 1", len(events))
	}
	if got := events[0].Detail["authorization"]; got != "Bearer sk-test-should-not-be-here" {
		t.Errorf("Detail[authorization] = %v, want the value unchanged", got)
	}
}

func TestAuditAppendMarshalError(t *testing.T) {
	s, ctx := openTestStore(t)
	err := s.AppendAudit(ctx, domain.AuditEvent{
		ID: "evt_bad", TS: auditBase, Type: "bad",
		Detail: map[string]any{"channel": make(chan int)},
	})
	if err == nil {
		t.Error("AppendAudit() with unmarshalable detail error = nil, want error")
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE id='evt_bad'`).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 0 {
		t.Errorf("audit rows inserted = %d, want 0", n)
	}
}

func auditIDs(in []domain.AuditEvent) []string {
	out := make([]string, len(in))
	for i, e := range in {
		out[i] = e.ID
	}
	return out
}
