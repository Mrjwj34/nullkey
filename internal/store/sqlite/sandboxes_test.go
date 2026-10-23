package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var sbBase = time.Date(2024, 2, 1, 8, 0, 0, 0, time.UTC)

func TestSandboxCreateGet(t *testing.T) {
	s, ctx := openTestStore(t)
	started := sbBase.Add(time.Minute)
	stopped := sbBase.Add(2 * time.Minute)
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID:          "sb_1",
		HarnessID:   "claude",
		WorkspaceID: "ws_1",
		StateID:     "st_1",
		Runtime:     domain.RuntimeDesktop,
		NetworkMode: domain.NetworkServicesOnly,
		Status:      domain.SandboxStarting,
		PID:         4242,
		LastError:   strPtr("starting up"),
		TokenHash:   []byte("hash-1"),
		CreatedAt:   sbBase,
		StartedAt:   &started,
		StoppedAt:   &stopped,
	})
	got, err := s.GetSandbox(ctx, "sb_1")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got.ID != "sb_1" || got.HarnessID != "claude" || got.WorkspaceID != "ws_1" || got.StateID != "st_1" {
		t.Errorf("GetSandbox() = %+v", got)
	}
	if got.Runtime != domain.RuntimeDesktop || got.NetworkMode != domain.NetworkServicesOnly || got.Status != domain.SandboxStarting {
		t.Errorf("runtime/network/status = %q/%q/%q", got.Runtime, got.NetworkMode, got.Status)
	}
	if got.PID != 4242 {
		t.Errorf("PID = %d, want 4242", got.PID)
	}
	if got.LastError == nil || *got.LastError != "starting up" {
		t.Errorf("LastError = %v", got.LastError)
	}
	if string(got.TokenHash) != "hash-1" {
		t.Errorf("TokenHash = %q, want hash-1", got.TokenHash)
	}
	if !got.CreatedAt.Equal(sbBase) || got.StartedAt == nil || !got.StartedAt.Equal(started) || got.StoppedAt == nil || !got.StoppedAt.Equal(stopped) {
		t.Errorf("timestamps = %v / %v / %v", got.CreatedAt, got.StartedAt, got.StoppedAt)
	}

	// Nullable state and timestamps round-trip as zero values.
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_nullable", HarnessID: "h", WorkspaceID: "ws_1",
		Status: domain.SandboxCreating, CreatedAt: sbBase,
	})
	nullable, err := s.GetSandbox(ctx, "sb_nullable")
	if err != nil {
		t.Fatalf("GetSandbox(nullable) error = %v", err)
	}
	if nullable.StateID != "" || nullable.StartedAt != nil || nullable.StoppedAt != nil || nullable.LastError != nil || nullable.TokenHash != nil {
		t.Errorf("nullable sandbox = %+v", nullable)
	}

	if err := s.CreateSandbox(ctx, domain.Sandbox{
		ID: "sb_1", HarnessID: "h", WorkspaceID: "ws_1", Runtime: domain.RuntimeNative,
		NetworkMode: domain.NetworkFiltered, Status: domain.SandboxCreating, CreatedAt: sbBase,
	}); err == nil {
		t.Error("CreateSandbox() duplicate id error = nil, want error")
	}
	if _, err := s.GetSandbox(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetSandbox(missing) error = %v, want sql.ErrNoRows", err)
	}
}

func TestSandboxListPagination(t *testing.T) {
	s, ctx := openTestStore(t)
	ids := []string{"sb_a", "sb_b", "sb_c"}
	for i, id := range ids {
		mustCreateSandbox(t, ctx, s, domain.Sandbox{
			ID: id, HarnessID: "h", WorkspaceID: "ws", Status: domain.SandboxRunning,
			CreatedAt: sbBase.Add(time.Duration(i) * time.Minute),
		})
	}
	all, next, err := s.ListSandboxes(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListSandboxes() error = %v", err)
	}
	if next != "" || len(all) != 3 || all[0].ID != "sb_c" || all[1].ID != "sb_b" || all[2].ID != "sb_a" {
		t.Fatalf("ListSandboxes() = %v, next=%q", sandboxIDs(all), next)
	}
	var seen []string
	cursor := ""
	for {
		page, next, err := s.ListSandboxes(ctx, 1, cursor)
		if err != nil {
			t.Fatalf("ListSandboxes(limit=1) error = %v", err)
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
	if len(seen) != 3 || seen[0] != "sb_c" || seen[1] != "sb_b" || seen[2] != "sb_a" {
		t.Errorf("paged sandboxes = %v", seen)
	}
	if _, _, err := s.ListSandboxes(ctx, 1, "not a cursor!"); err == nil {
		t.Error("ListSandboxes() with invalid cursor error = nil, want error")
	}
}

func TestActiveSandboxes(t *testing.T) {
	s, ctx := openTestStore(t)
	active, err := s.ActiveSandboxes(ctx)
	if err != nil {
		t.Fatalf("ActiveSandboxes(empty) error = %v", err)
	}
	if len(active) != 0 {
		t.Errorf("ActiveSandboxes(empty) len = %d, want 0", len(active))
	}

	statuses := []domain.SandboxStatus{
		domain.SandboxCreating, domain.SandboxStarting, domain.SandboxRunning, domain.SandboxStopping,
		domain.SandboxStopped, domain.SandboxFailed,
	}
	for i, status := range statuses {
		mustCreateSandbox(t, ctx, s, domain.Sandbox{
			ID: "sb_" + string(status), HarnessID: "h", WorkspaceID: "ws", Status: status,
			CreatedAt: sbBase.Add(time.Duration(i) * time.Second),
		})
	}
	got, err := s.ActiveSandboxes(ctx)
	if err != nil {
		t.Fatalf("ActiveSandboxes() error = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ActiveSandboxes() len = %d, want 4 (%v)", len(got), sandboxIDs(got))
	}
	for _, sb := range got {
		if sb.Status == domain.SandboxStopped || sb.Status == domain.SandboxFailed {
			t.Errorf("ActiveSandboxes() included terminal status %q", sb.Status)
		}
	}
}

func TestSandboxUpdateAndStatus(t *testing.T) {
	s, ctx := openTestStore(t)
	started := sbBase.Add(time.Minute)
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_update", HarnessID: "h", WorkspaceID: "ws", Status: domain.SandboxStarting,
		CreatedAt: sbBase, StartedAt: &started,
	})
	stopped := sbBase.Add(5 * time.Minute)
	update := domain.Sandbox{
		ID: "sb_update", Status: domain.SandboxStopped, PID: 99, LastError: strPtr("crashed"),
		TokenHash: []byte("hash-2"), StartedAt: &started, StoppedAt: &stopped,
	}
	if err := s.UpdateSandbox(ctx, update); err != nil {
		t.Fatalf("UpdateSandbox() error = %v", err)
	}
	got, err := s.GetSandbox(ctx, "sb_update")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got.Status != domain.SandboxStopped || got.PID != 99 || got.LastError == nil || *got.LastError != "crashed" {
		t.Errorf("updated sandbox = %+v", got)
	}
	if string(got.TokenHash) != "hash-2" || got.StartedAt == nil || got.StoppedAt == nil {
		t.Errorf("updated sandbox token/timestamps = %q / %v / %v", got.TokenHash, got.StartedAt, got.StoppedAt)
	}

	if err := s.UpdateSandbox(ctx, domain.Sandbox{
		ID: "sb_update", Status: domain.SandboxFailed, PID: 0,
	}); err != nil {
		t.Fatalf("UpdateSandbox(clear) error = %v", err)
	}
	cleared, err := s.GetSandbox(ctx, "sb_update")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if cleared.Status != domain.SandboxFailed || cleared.PID != 0 || cleared.TokenHash != nil ||
		cleared.StartedAt != nil || cleared.StoppedAt != nil || cleared.LastError != nil {
		t.Errorf("cleared sandbox = %+v", cleared)
	}

	if err := s.SetSandboxStatus(ctx, "sb_update", domain.SandboxStopping, strPtr("draining")); err != nil {
		t.Fatalf("SetSandboxStatus() error = %v", err)
	}
	got, err = s.GetSandbox(ctx, "sb_update")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got.Status != domain.SandboxStopping || got.LastError == nil || *got.LastError != "draining" {
		t.Errorf("after SetSandboxStatus() = %+v", got)
	}
	if err := s.SetSandboxStatus(ctx, "sb_update", domain.SandboxFailed, nil); err != nil {
		t.Fatalf("SetSandboxStatus(nil error) error = %v", err)
	}
	got, err = s.GetSandbox(ctx, "sb_update")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got.LastError != nil {
		t.Errorf("LastError = %v, want nil", got.LastError)
	}
}

func TestSandboxStoppedAndTokenHash(t *testing.T) {
	s, ctx := openTestStore(t)
	started := sbBase.Add(time.Minute)
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_stop", HarnessID: "h", WorkspaceID: "ws", Status: domain.SandboxRunning,
		PID: 1234, TokenHash: []byte("hash-3"), CreatedAt: sbBase, StartedAt: &started,
	})
	stoppedAt := sbBase.Add(3 * time.Minute)
	if err := s.SetSandboxStopped(ctx, "sb_stop", stoppedAt); err != nil {
		t.Fatalf("SetSandboxStopped() error = %v", err)
	}
	got, err := s.GetSandbox(ctx, "sb_stop")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got.Status != domain.SandboxStopped {
		t.Errorf("Status = %q, want stopped", got.Status)
	}
	if got.PID != 0 {
		t.Errorf("PID = %d, want 0", got.PID)
	}
	if got.TokenHash != nil {
		t.Errorf("TokenHash = %q, want nil", got.TokenHash)
	}
	if got.StoppedAt == nil || !got.StoppedAt.Equal(stoppedAt) {
		t.Errorf("StoppedAt = %v, want %v", got.StoppedAt, stoppedAt)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}

	if err := s.SaveTokenHash(ctx, "sb_stop", []byte("hash-4")); err != nil {
		t.Fatalf("SaveTokenHash() error = %v", err)
	}
	if _, err := s.FindSandboxByTokenHash(ctx, []byte("hash-4")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindSandboxByTokenHash(stopped) error = %v, want sql.ErrNoRows", err)
	}
}

func TestFindSandboxByTokenHash(t *testing.T) {
	s, ctx := openTestStore(t)
	statuses := map[string]domain.SandboxStatus{
		"sb_starting": domain.SandboxStarting,
		"sb_running":  domain.SandboxRunning,
		"sb_stopping": domain.SandboxStopping,
		"sb_stopped":  domain.SandboxStopped,
		"sb_failed":   domain.SandboxFailed,
	}
	i := 0
	for id, status := range statuses {
		mustCreateSandbox(t, ctx, s, domain.Sandbox{
			ID: id, HarnessID: "h", WorkspaceID: "ws", Status: status,
			TokenHash: []byte("hash-" + id), CreatedAt: sbBase.Add(time.Duration(i) * time.Second),
		})
		i++
	}
	for _, id := range []string{"sb_starting", "sb_running"} {
		got, err := s.FindSandboxByTokenHash(ctx, []byte("hash-"+id))
		if err != nil {
			t.Fatalf("FindSandboxByTokenHash(%s) error = %v", id, err)
		}
		if got.ID != id {
			t.Errorf("FindSandboxByTokenHash(%s) = %q", id, got.ID)
		}
	}
	for _, id := range []string{"sb_stopping", "sb_stopped", "sb_failed"} {
		if _, err := s.FindSandboxByTokenHash(ctx, []byte("hash-"+id)); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("FindSandboxByTokenHash(%s) error = %v, want sql.ErrNoRows", id, err)
		}
	}
	if _, err := s.FindSandboxByTokenHash(ctx, []byte("hash-unknown")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindSandboxByTokenHash(unknown) error = %v, want sql.ErrNoRows", err)
	}
	if err := s.SaveTokenHash(ctx, "sb_running", []byte("hash-new")); err != nil {
		t.Fatalf("SaveTokenHash() error = %v", err)
	}
	got, err := s.FindSandboxByTokenHash(ctx, []byte("hash-new"))
	if err != nil || got.ID != "sb_running" {
		t.Errorf("FindSandboxByTokenHash(new) = (%q, %v)", got.ID, err)
	}
}

func TestSandboxDeleteCascade(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_del", Name: "del", RootPath: "/tmp/ws_del", CreatedAt: wsBase})
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_del", HarnessID: "h", WorkspaceID: "ws_del", Status: domain.SandboxRunning, CreatedAt: sbBase,
	})
	if err := s.LockWorkspace(ctx, "ws_del", "sb_del", "rw"); err != nil {
		t.Fatalf("LockWorkspace() error = %v", err)
	}
	if err := s.ReplaceServices(ctx, "sb_del", []domain.Service{{
		ID: "svc_1", Name: "one", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 8080,
		Status: domain.ServiceReady,
	}}); err != nil {
		t.Fatalf("ReplaceServices() error = %v", err)
	}
	if err := s.DeleteSandbox(ctx, "sb_del"); err != nil {
		t.Fatalf("DeleteSandbox() error = %v", err)
	}
	if _, err := s.GetSandbox(ctx, "sb_del"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetSandbox() after delete error = %v, want sql.ErrNoRows", err)
	}
	var locks, services int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_locks WHERE sandbox_id='sb_del'`).Scan(&locks); err != nil {
		t.Fatalf("count locks: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE sandbox_id='sb_del'`).Scan(&services); err != nil {
		t.Fatalf("count services: %v", err)
	}
	if locks != 0 || services != 0 {
		t.Errorf("cascade counts = locks %d, services %d, want 0/0", locks, services)
	}
	if err := s.DeleteSandbox(ctx, "sb_del"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteSandbox(missing) error = %v, want domain.ErrNotFound", err)
	}
}

func sandboxIDs(in []domain.Sandbox) []string {
	out := make([]string, len(in))
	for i, sb := range in {
		out[i] = sb.ID
	}
	return out
}
