package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var wsBase = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

func TestWorkspaceCreateGet(t *testing.T) {
	s, ctx := openTestStore(t)
	lastUsed := wsBase.Add(2 * time.Hour)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{
		ID:          "ws_1",
		Name:        "project one",
		RootPath:    "/tmp/project-one",
		StorageKind: domain.StorageExternal,
		DefaultMode: "services_only",
		CreatedAt:   wsBase,
		LastUsedAt:  &lastUsed,
	})
	got, err := s.GetWorkspace(ctx, "ws_1")
	if err != nil {
		t.Fatalf("GetWorkspace() error = %v", err)
	}
	if got.ID != "ws_1" || got.Name != "project one" || got.RootPath != "/tmp/project-one" {
		t.Errorf("GetWorkspace() = %+v", got)
	}
	if got.StorageKind != domain.StorageExternal || got.DefaultMode != "services_only" {
		t.Errorf("StorageKind/DefaultMode = %q/%q", got.StorageKind, got.DefaultMode)
	}
	if !got.CreatedAt.Equal(wsBase) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, wsBase)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(lastUsed) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, lastUsed)
	}
	if got.LockedBy != nil {
		t.Errorf("LockedBy = %v, want nil", got.LockedBy)
	}

	mustCreateWorkspace(t, ctx, s, domain.Workspace{
		ID: "ws_2", Name: "no last used", RootPath: "/tmp/project-two", CreatedAt: wsBase.Add(time.Minute),
	})
	fresh, err := s.GetWorkspace(ctx, "ws_2")
	if err != nil {
		t.Fatalf("GetWorkspace(ws_2) error = %v", err)
	}
	if fresh.LastUsedAt != nil {
		t.Errorf("LastUsedAt = %v, want nil", fresh.LastUsedAt)
	}
}

func TestWorkspaceUniqueRoot(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{
		ID: "ws_a", Name: "a", RootPath: "/tmp/duplicate-root", CreatedAt: wsBase,
	})
	err := s.CreateWorkspace(ctx, domain.Workspace{
		ID: "ws_b", Name: "b", RootPath: "/tmp/duplicate-root", StorageKind: domain.StorageManaged,
		DefaultMode: "filtered", CreatedAt: wsBase,
	})
	if err == nil {
		t.Fatal("CreateWorkspace() with duplicate root error = nil, want error")
	}
}

func TestWorkspaceGetNotFound(t *testing.T) {
	s, ctx := openTestStore(t)
	_, err := s.GetWorkspace(ctx, "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetWorkspace(missing) error = %v, want sql.ErrNoRows", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetWorkspace(missing) must not be domain.ErrNotFound")
	}
}

func TestWorkspaceListPagination(t *testing.T) {
	s, ctx := openTestStore(t)
	ids := []string{"ws_a", "ws_b", "ws_c"}
	for i, id := range ids {
		mustCreateWorkspace(t, ctx, s, domain.Workspace{
			ID: id, Name: id, RootPath: "/tmp/" + id, CreatedAt: wsBase.Add(time.Duration(i) * time.Minute),
		})
	}
	all, next, err := s.ListWorkspaces(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListWorkspaces() error = %v", err)
	}
	if next != "" {
		t.Errorf("next cursor = %q, want empty", next)
	}
	if len(all) != 3 || all[0].ID != "ws_c" || all[1].ID != "ws_b" || all[2].ID != "ws_a" {
		t.Fatalf("ListWorkspaces() = %v", workspaceIDs(all))
	}

	var seen []string
	cursor := ""
	for {
		page, next, err := s.ListWorkspaces(ctx, 1, cursor)
		if err != nil {
			t.Fatalf("ListWorkspaces(limit=1, cursor=%q) error = %v", cursor, err)
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
	if len(seen) != 3 || seen[0] != "ws_c" || seen[1] != "ws_b" || seen[2] != "ws_a" {
		t.Errorf("paged IDs = %v", seen)
	}

	mustCreateWorkspace(t, ctx, s, domain.Workspace{
		ID: "ws_d", Name: "d", RootPath: "/tmp/ws_d", CreatedAt: wsBase,
	})
	all, _, err = s.ListWorkspaces(ctx, 9000, "")
	if err != nil {
		t.Fatalf("ListWorkspaces(limit clamp) error = %v", err)
	}
	if len(all) != 4 {
		t.Errorf("ListWorkspaces(limit clamp) len = %d, want 4", len(all))
	}

	if _, _, err := s.ListWorkspaces(ctx, 1, "%%%not-a-cursor"); err == nil {
		t.Error("ListWorkspaces() with invalid cursor error = nil, want error")
	}
}

func TestWorkspaceListTieBreak(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_a", Name: "a", RootPath: "/tmp/a", CreatedAt: wsBase})
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_b", Name: "b", RootPath: "/tmp/b", CreatedAt: wsBase})
	page, next, err := s.ListWorkspaces(ctx, 1, "")
	if err != nil {
		t.Fatalf("ListWorkspaces() error = %v", err)
	}
	if len(page) != 1 || page[0].ID != "ws_b" {
		t.Fatalf("first page = %v, want [ws_b]", workspaceIDs(page))
	}
	if next == "" {
		t.Fatal("next cursor empty, want a second page")
	}
	second, _, err := s.ListWorkspaces(ctx, 1, next)
	if err != nil {
		t.Fatalf("ListWorkspaces(second) error = %v", err)
	}
	if len(second) != 1 || second[0].ID != "ws_a" {
		t.Errorf("second page = %v, want [ws_a]", workspaceIDs(second))
	}
}

func TestWorkspaceDelete(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_del", Name: "del", RootPath: "/tmp/del", CreatedAt: wsBase})
	if err := s.DeleteWorkspace(ctx, "ws_del"); err != nil {
		t.Fatalf("DeleteWorkspace() error = %v", err)
	}
	if _, err := s.GetWorkspace(ctx, "ws_del"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetWorkspace() after delete error = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteWorkspace(ctx, "ws_del"); err == nil {
		t.Error("DeleteWorkspace() on missing row error = nil, want error")
	}
	if err := s.DeleteWorkspace(ctx, "missing"); err == nil {
		t.Error("DeleteWorkspace(missing) error = nil, want error")
	}
}

func TestWorkspaceTouch(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_touch", Name: "touch", RootPath: "/tmp/touch", CreatedAt: wsBase})
	if err := s.TouchWorkspace(ctx, "ws_touch", ""); err != nil {
		t.Fatalf("TouchWorkspace() error = %v", err)
	}
	got, err := s.GetWorkspace(ctx, "ws_touch")
	if err != nil {
		t.Fatalf("GetWorkspace() error = %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("LastUsedAt = nil after touch")
	}
	if got.LastUsedAt.Before(time.Now().Add(-time.Hour)) {
		t.Errorf("LastUsedAt = %v, want a recent timestamp", got.LastUsedAt)
	}

	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_touch", HarnessID: "h1", WorkspaceID: "ws_touch",
		Status: domain.SandboxRunning, CreatedAt: wsBase,
	})
	if err := s.TouchWorkspace(ctx, "ws_touch", "sb_touch"); err != nil {
		t.Fatalf("TouchWorkspace(with sandbox) error = %v", err)
	}
	if err := s.TouchWorkspace(ctx, "missing", "sb_touch"); err != nil {
		t.Errorf("TouchWorkspace(missing workspace) error = %v, want nil", err)
	}
}

func TestWorkspaceLockConflicts(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_lock", Name: "lock", RootPath: "/tmp/lock", CreatedAt: wsBase})

	if err := s.LockWorkspace(ctx, "ws_lock", "sb_1", "rw"); err != nil {
		t.Fatalf("LockWorkspace(rw) error = %v", err)
	}
	got, err := s.GetWorkspace(ctx, "ws_lock")
	if err != nil {
		t.Fatalf("GetWorkspace() error = %v", err)
	}
	if got.LockedBy == nil || *got.LockedBy != "sb_1" {
		t.Errorf("LockedBy = %v, want sb_1", got.LockedBy)
	}
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_2", "rw"); !errors.Is(err, domain.ErrStateLocked) {
		t.Errorf("LockWorkspace(second rw) error = %v, want ErrStateLocked", err)
	}
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_2", "ro"); !errors.Is(err, domain.ErrStateLocked) {
		t.Errorf("LockWorkspace(ro with rw held) error = %v, want ErrStateLocked", err)
	}
	// Re-locking the same sandbox replaces its own lease.
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_1", "ro"); err != nil {
		t.Fatalf("LockWorkspace(same sandbox, ro) error = %v", err)
	}

	if err := s.UnlockWorkspace(ctx, "ws_lock", "sb_1"); err != nil {
		t.Fatalf("UnlockWorkspace() error = %v", err)
	}
	got, err = s.GetWorkspace(ctx, "ws_lock")
	if err != nil {
		t.Fatalf("GetWorkspace() after unlock error = %v", err)
	}
	if got.LockedBy != nil {
		t.Errorf("LockedBy = %v after unlock, want nil", got.LockedBy)
	}

	if err := s.LockWorkspace(ctx, "ws_lock", "sb_2", "ro"); err != nil {
		t.Fatalf("LockWorkspace(ro) error = %v", err)
	}
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_3", "ro"); err != nil {
		t.Fatalf("LockWorkspace(second ro) error = %v", err)
	}
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_4", "rw"); !errors.Is(err, domain.ErrStateLocked) {
		t.Errorf("LockWorkspace(rw with ro held) error = %v, want ErrStateLocked", err)
	}
	if err := s.ReleaseSandboxLocks(ctx, "sb_2"); err != nil {
		t.Fatalf("ReleaseSandboxLocks() error = %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_locks WHERE sandbox_id='sb_2'`).Scan(&n); err != nil {
		t.Fatalf("count sb_2 locks: %v", err)
	}
	if n != 0 {
		t.Errorf("sb_2 locks after release = %d, want 0", n)
	}
	if err := s.ReleaseSandboxLocks(ctx, "no-such-sandbox"); err != nil {
		t.Errorf("ReleaseSandboxLocks(missing) error = %v", err)
	}
	if err := s.UnlockWorkspace(ctx, "ws_lock", "sb_3"); err != nil {
		t.Errorf("UnlockWorkspace() error = %v", err)
	}
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_4", "rw"); err != nil {
		t.Errorf("LockWorkspace(rw after release) error = %v", err)
	}
}

func TestActiveSandboxForWorkspaceAndState(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{ID: "ws_active", Name: "active", RootPath: "/tmp/active", CreatedAt: wsBase})

	if id, err := s.ActiveSandboxForWorkspace(ctx, "ws_active"); err != nil || id != "" {
		t.Errorf("ActiveSandboxForWorkspace(empty) = (%q, %v), want empty, nil", id, err)
	}
	if id, err := s.ActiveSandboxForState(ctx, "st_missing"); err != nil || id != "" {
		t.Errorf("ActiveSandboxForState(empty) = (%q, %v), want empty, nil", id, err)
	}

	for i, status := range []domain.SandboxStatus{
		domain.SandboxCreating, domain.SandboxStarting, domain.SandboxRunning, domain.SandboxStopping,
	} {
		mustCreateSandbox(t, ctx, s, domain.Sandbox{
			ID: "sb_live_" + string(status), HarnessID: "h1", WorkspaceID: "ws_active", StateID: "st_1",
			Status: status, CreatedAt: wsBase.Add(time.Duration(i) * time.Second),
		})
	}
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_dead", HarnessID: "h1", WorkspaceID: "ws_active", StateID: "st_1",
		Status: domain.SandboxStopped, CreatedAt: wsBase.Add(10 * time.Second),
	})
	if id, err := s.ActiveSandboxForWorkspace(ctx, "ws_active"); err != nil || id == "" {
		t.Errorf("ActiveSandboxForWorkspace() = (%q, %v), want non-empty, nil", id, err)
	}
	if id, err := s.ActiveSandboxForState(ctx, "st_1"); err != nil || id == "" {
		t.Errorf("ActiveSandboxForState() = (%q, %v), want non-empty, nil", id, err)
	}
	if id, err := s.ActiveSandboxForState(ctx, "st_other"); err != nil || id != "" {
		t.Errorf("ActiveSandboxForState(other) = (%q, %v), want empty, nil", id, err)
	}
}

func TestStateCreateGetList(t *testing.T) {
	s, ctx := openTestStore(t)
	lastUsed := wsBase.Add(time.Hour)
	mustCreateState(t, ctx, s, domain.State{
		ID: "st_1", HarnessID: "claude", Name: "one", Path: "/tmp/st_1",
		CreatedAt: wsBase, LastUsedAt: &lastUsed,
	})
	mustCreateState(t, ctx, s, domain.State{
		ID: "st_2", HarnessID: "claude", Name: "two", Path: "/tmp/st_2", CreatedAt: wsBase.Add(time.Minute),
	})
	mustCreateState(t, ctx, s, domain.State{
		ID: "st_3", HarnessID: "codex", Name: "three", Path: "/tmp/st_3", CreatedAt: wsBase.Add(2 * time.Minute),
	})

	got, err := s.GetState(ctx, "st_1")
	if err != nil {
		t.Fatalf("GetState() error = %v", err)
	}
	if got.HarnessID != "claude" || got.Name != "one" || got.Path != "/tmp/st_1" {
		t.Errorf("GetState() = %+v", got)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(lastUsed) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, lastUsed)
	}
	if _, err := s.GetState(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetState(missing) error = %v, want sql.ErrNoRows", err)
	}

	all, next, err := s.ListStates(ctx, "", 10, "")
	if err != nil {
		t.Fatalf("ListStates() error = %v", err)
	}
	if next != "" || len(all) != 3 || all[0].ID != "st_3" || all[2].ID != "st_1" {
		t.Errorf("ListStates() = %v, next=%q", stateIDs(all), next)
	}
	filtered, _, err := s.ListStates(ctx, "claude", 10, "")
	if err != nil {
		t.Fatalf("ListStates(filtered) error = %v", err)
	}
	if len(filtered) != 2 || filtered[0].ID != "st_2" || filtered[1].ID != "st_1" {
		t.Errorf("ListStates(filtered) = %v", stateIDs(filtered))
	}
	if filtered[0].LastUsedAt != nil {
		t.Errorf("st_2 LastUsedAt = %v, want nil", filtered[0].LastUsedAt)
	}
	if filtered[1].LastUsedAt == nil {
		t.Error("st_1 LastUsedAt = nil, want a timestamp")
	}

	cursor := ""
	var seen []string
	for {
		page, next, err := s.ListStates(ctx, "", 1, cursor)
		if err != nil {
			t.Fatalf("ListStates(limit=1) error = %v", err)
		}
		if len(page) != 1 {
			t.Fatalf("ListStates page size = %d, want 1", len(page))
		}
		seen = append(seen, page[0].ID)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 || seen[0] != "st_3" || seen[1] != "st_2" || seen[2] != "st_1" {
		t.Errorf("paged states = %v", seen)
	}
	if _, _, err := s.ListStates(ctx, "", 1, "%%%"); err == nil {
		t.Error("ListStates() with invalid cursor error = nil, want error")
	}

	if err := s.CreateState(ctx, domain.State{ID: "st_dup_path", HarnessID: "claude", Name: "dup", Path: "/tmp/st_1", CreatedAt: wsBase}); err == nil {
		t.Error("CreateState() with duplicate path error = nil, want error")
	}
}

func TestStateDeleteAndTouch(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateState(t, ctx, s, domain.State{ID: "st_del", HarnessID: "h", Name: "del", Path: "/tmp/st_del", CreatedAt: wsBase})
	if err := s.TouchState(ctx, "st_del"); err != nil {
		t.Fatalf("TouchState() error = %v", err)
	}
	got, err := s.GetState(ctx, "st_del")
	if err != nil {
		t.Fatalf("GetState() error = %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("LastUsedAt = nil after TouchState")
	}
	if err := s.TouchState(ctx, "missing"); err != nil {
		t.Errorf("TouchState(missing) error = %v, want nil", err)
	}
	if err := s.DeleteState(ctx, "st_del"); err != nil {
		t.Fatalf("DeleteState() error = %v", err)
	}
	if _, err := s.GetState(ctx, "st_del"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetState() after delete error = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteState(ctx, "st_del"); err == nil {
		t.Error("DeleteState() on missing row error = nil, want error")
	}
	if err := s.DeleteState(ctx, "missing"); err == nil {
		t.Error("DeleteState(missing) error = nil, want error")
	}
}

func workspaceIDs(in []domain.Workspace) []string {
	out := make([]string, len(in))
	for i, w := range in {
		out[i] = w.ID
	}
	return out
}

func stateIDs(in []domain.State) []string {
	out := make([]string, len(in))
	for i, st := range in {
		out[i] = st.ID
	}
	return out
}
