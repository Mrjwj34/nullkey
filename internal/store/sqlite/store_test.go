package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	s, err := Open(ctx, home)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

func mustCreateWorkspace(t *testing.T, ctx context.Context, s *Store, w domain.Workspace) {
	t.Helper()
	if w.StorageKind == "" {
		w.StorageKind = domain.StorageManaged
	}
	if w.DefaultMode == "" {
		w.DefaultMode = "filtered"
	}
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace(%q) error = %v", w.ID, err)
	}
}

func mustCreateSandbox(t *testing.T, ctx context.Context, s *Store, sb domain.Sandbox) {
	t.Helper()
	if sb.Runtime == "" {
		sb.Runtime = domain.RuntimeNative
	}
	if sb.NetworkMode == "" {
		sb.NetworkMode = domain.NetworkFiltered
	}
	if err := s.CreateSandbox(ctx, sb); err != nil {
		t.Fatalf("CreateSandbox(%q) error = %v", sb.ID, err)
	}
}

func mustCreateState(t *testing.T, ctx context.Context, s *Store, st domain.State) {
	t.Helper()
	if err := s.CreateState(ctx, st); err != nil {
		t.Fatalf("CreateState(%q) error = %v", st.ID, err)
	}
}

func mustCreateJob(t *testing.T, ctx context.Context, s *Store, job domain.Job) {
	t.Helper()
	if err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob(%q) error = %v", job.ID, err)
	}
}

func mustCreateCredential(t *testing.T, ctx context.Context, s *Store, c domain.Credential) {
	t.Helper()
	if c.AuthType == "" {
		c.AuthType = domain.AuthOAuth
	}
	if err := s.CreateCredential(ctx, c); err != nil {
		t.Fatalf("CreateCredential(%q) error = %v", c.ID, err)
	}
}

func mustCreateMCP(t *testing.T, ctx context.Context, s *Store, m domain.MCPConnection) {
	t.Helper()
	if m.ConfigJSON == "" {
		m.ConfigJSON = `{"tools":[]}`
	}
	if err := s.CreateMCPConnection(ctx, m); err != nil {
		t.Fatalf("CreateMCPConnection(%q) error = %v", m.ID, err)
	}
}

func mustCreateFlow(t *testing.T, ctx context.Context, s *Store, f domain.OAuthFlow) {
	t.Helper()
	if err := s.CreateOAuthFlow(ctx, f); err != nil {
		t.Fatalf("CreateOAuthFlow(%q) error = %v", f.ID, err)
	}
}

func mustCreateAudit(t *testing.T, ctx context.Context, s *Store, e domain.AuditEvent) {
	t.Helper()
	if err := s.AppendAudit(ctx, e); err != nil {
		t.Fatalf("AppendAudit(%q) error = %v", e.ID, err)
	}
}

func strPtr(s string) *string { return &s }

func timePtr(t time.Time) *time.Time { return &t }

func countsByStatus(t *testing.T, ctx context.Context, s *Store, status domain.SandboxStatus) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandboxes WHERE status=?`, string(status)).Scan(&n); err != nil {
		t.Fatalf("count sandboxes: %v", err)
	}
	return n
}

func TestOpenPingPathClose(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	s, err := Open(ctx, home)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got, want := s.Path(), filepath.Join(home, "nullkey.db"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
	if err := s.Ping(ctx); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestOpenMigrationIdempotent(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	first, err := Open(ctx, home)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	mustCreateWorkspace(t, ctx, first, domain.Workspace{
		ID:        "ws_first",
		Name:      "first",
		RootPath:  filepath.Join(home, "first"),
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := Open(ctx, home)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()

	var versions int
	if err := second.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if versions != len(migrations) {
		t.Errorf("schema_migrations count = %d, want %d", versions, len(migrations))
	}
	got, err := second.GetWorkspace(ctx, "ws_first")
	if err != nil {
		t.Fatalf("GetWorkspace() after reopen error = %v", err)
	}
	if got.Name != "first" {
		t.Errorf("workspace name after reopen = %q, want first", got.Name)
	}
}

func TestOpenPingError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, t.TempDir()); err == nil {
		t.Fatal("Open() with cancelled context error = nil, want error")
	}

	fileHome := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(fileHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := Open(context.Background(), fileHome); err == nil {
		t.Fatal("Open() below a regular file error = nil, want error")
	}
}

func TestOpenMigrationError(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	path := filepath.Join(home, "nullkey.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); CREATE TABLE workspaces (broken TEXT)`); err != nil {
		db.Close()
		t.Fatalf("seed conflicting schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	if _, err := Open(ctx, home); err == nil {
		t.Fatal("Open() with conflicting schema error = nil, want error")
	}
}

func TestPageCursor(t *testing.T) {
	ts := formatTime(time.Date(2024, 5, 6, 7, 8, 9, 10, time.UTC))
	cursor := encodeCursor(ts, "ws_1")
	gotTS, gotID, err := decodeCursor(cursor)
	if err != nil {
		t.Fatalf("decodeCursor() error = %v", err)
	}
	if gotTS != ts || gotID != "ws_1" {
		t.Errorf("decodeCursor() = (%q, %q), want (%q, ws_1)", gotTS, gotID, ts)
	}

	if limit, _, _, err := Page(0, "", 50, 200); err != nil || limit != 50 {
		t.Errorf("Page(0) = (%d, %v), want (50, nil)", limit, err)
	}
	if limit, _, _, err := Page(-7, "", 25, 200); err != nil || limit != 25 {
		t.Errorf("Page(-7) = (%d, %v), want (25, nil)", limit, err)
	}
	if limit, _, _, err := Page(9000, "", 50, 200); err != nil || limit != 200 {
		t.Errorf("Page(9000) = (%d, %v), want (200, nil)", limit, err)
	}
	limit, gotTS, gotID, err := Page(5, cursor, 50, 200)
	if err != nil || limit != 5 || gotTS != ts || gotID != "ws_1" {
		t.Errorf("Page(cursor) = (%d, %q, %q, %v)", limit, gotTS, gotID, err)
	}

	if _, _, _, err := Page(10, "not-base64!!!", 50, 200); err == nil {
		t.Error("Page() with invalid base64 error = nil, want error")
	}
	noSep := base64.RawURLEncoding.EncodeToString([]byte("missing-separator"))
	if _, _, _, err := Page(10, noSep, 50, 200); err == nil {
		t.Error("Page() without separator error = nil, want error")
	}
	emptyTS := base64.RawURLEncoding.EncodeToString([]byte("|ws_1"))
	if _, _, _, err := Page(10, emptyTS, 50, 200); err == nil {
		t.Error("Page() with empty timestamp error = nil, want error")
	}
	emptyID := base64.RawURLEncoding.EncodeToString([]byte(ts + "|"))
	if _, _, _, err := Page(10, emptyID, 50, 200); err == nil {
		t.Error("Page() with empty id error = nil, want error")
	}
	badTS := base64.RawURLEncoding.EncodeToString([]byte("yesterday|ws_1"))
	if _, _, _, err := Page(10, badTS, 50, 200); err == nil {
		t.Error("Page() with bad timestamp error = nil, want error")
	}
}

func TestScanTimeHelper(t *testing.T) {
	if got := scanTime(sql.NullString{}); got != nil {
		t.Errorf("scanTime(invalid) = %v, want nil", got)
	}
	if got := scanTime(sql.NullString{Valid: true, String: ""}); got != nil {
		t.Errorf("scanTime(empty) = %v, want nil", got)
	}
	if got := scanTime(sql.NullString{Valid: true, String: "not-a-time"}); got != nil {
		t.Errorf("scanTime(bad) = %v, want nil", got)
	}
	want := time.Date(2024, 3, 4, 5, 6, 7, 8, time.UTC)
	got := scanTime(sql.NullString{Valid: true, String: formatTime(want)})
	if got == nil || !got.Equal(want) {
		t.Errorf("scanTime(valid) = %v, want %v", got, want)
	}
}

func TestResetTokenHashes(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_hash", HarnessID: "h1", WorkspaceID: "ws1",
		Status: domain.SandboxRunning, CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		TokenHash: []byte("sha256:abc"),
	})
	if err := s.ResetTokenHashes(ctx); err != nil {
		t.Fatalf("ResetTokenHashes() error = %v", err)
	}
	got, err := s.GetSandbox(ctx, "sb_hash")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got.TokenHash != nil {
		t.Errorf("TokenHash after reset = %v, want nil", got.TokenHash)
	}
	if err := s.ResetTokenHashes(ctx); err != nil {
		t.Errorf("ResetTokenHashes() with nothing to clear error = %v", err)
	}
}

func TestCleanupOrphanLocks(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateWorkspace(t, ctx, s, domain.Workspace{
		ID: "ws_lock", Name: "lock", RootPath: "/tmp/ws_lock",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_active", HarnessID: "h1", WorkspaceID: "ws_lock",
		Status: domain.SandboxRunning, CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	mustCreateSandbox(t, ctx, s, domain.Sandbox{
		ID: "sb_stopped", HarnessID: "h1", WorkspaceID: "ws_lock",
		Status: domain.SandboxRunning, CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_active", "ro"); err != nil {
		t.Fatalf("LockWorkspace(active) error = %v", err)
	}
	if err := s.LockWorkspace(ctx, "ws_lock", "sb_stopped", "ro"); err != nil {
		t.Fatalf("LockWorkspace(stopped) error = %v", err)
	}
	if err := s.SetSandboxStatus(ctx, "sb_stopped", domain.SandboxStopped, nil); err != nil {
		t.Fatalf("SetSandboxStatus() error = %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workspace_locks(workspace_id,sandbox_id,mode,acquired_at) VALUES(?,?,?,?)`,
		"ws_lock", "sb_missing", "ro", now()); err != nil {
		t.Fatalf("insert orphan lock: %v", err)
	}
	if err := s.CleanupOrphanLocks(ctx); err != nil {
		t.Fatalf("CleanupOrphanLocks() error = %v", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sandbox_id FROM workspace_locks ORDER BY sandbox_id`)
	if err != nil {
		t.Fatalf("query locks: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan lock: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != "sb_active" {
		t.Errorf("remaining locks = %v, want [sb_active]", ids)
	}
}

func TestHelpers(t *testing.T) {
	base := time.Date(2024, 2, 3, 4, 5, 6, 7, time.UTC)
	if got := formatTime(base); got != "2024-02-03T04:05:06.000000007Z" {
		t.Errorf("formatTime() = %q", got)
	}
	if got := nullableTime(nil); got != nil {
		t.Errorf("nullableTime(nil) = %v, want nil", got)
	}
	if got := nullableTime(timePtr(base)); got != formatTime(base) {
		t.Errorf("nullableTime(set) = %v", got)
	}
	if got := nullableString(""); got != nil {
		t.Errorf("nullableString(empty) = %v, want nil", got)
	}
	if got := nullableString("x"); got != "x" {
		t.Errorf("nullableString(x) = %v, want x", got)
	}
	if !isNoRows(sql.ErrNoRows) || isNoRows(errors.New("boom")) {
		t.Error("isNoRows() mismatch")
	}
	if ts := scanTimeValue("not-a-time"); !ts.IsZero() {
		t.Errorf("scanTimeValue(bad) = %v, want zero", ts)
	}
	if got := boolInt(true); got != 1 {
		t.Errorf("boolInt(true) = %d, want 1", got)
	}
	if got := boolInt(false); got != 0 {
		t.Errorf("boolInt(false) = %d, want 0", got)
	}
}

func TestClosedStoreErrors(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := s.Ping(ctx); err == nil {
		t.Error("Ping() on closed store error = nil, want error")
	}
	if err := s.ResetTokenHashes(ctx); err == nil {
		t.Error("ResetTokenHashes() on closed store error = nil, want error")
	}
	if err := s.CleanupOrphanLocks(ctx); err == nil {
		t.Error("CleanupOrphanLocks() on closed store error = nil, want error")
	}
	if err := s.CleanupIdempotency(ctx, time.Now()); err == nil {
		t.Error("CleanupIdempotency() on closed store error = nil, want error")
	}
}

func TestClosedStoreOperationErrors(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	hash := []byte("hash")

	ops := []struct {
		name string
		call func() error
	}{
		{"CreateWorkspace", func() error {
			return s.CreateWorkspace(ctx, domain.Workspace{ID: "w", RootPath: "/r", CreatedAt: ts})
		}},
		{"GetWorkspace", func() error { _, err := s.GetWorkspace(ctx, "w"); return err }},
		{"ListWorkspaces", func() error { _, _, err := s.ListWorkspaces(ctx, 1, ""); return err }},
		{"DeleteWorkspace", func() error { return s.DeleteWorkspace(ctx, "w") }},
		{"TouchWorkspace", func() error { return s.TouchWorkspace(ctx, "w", "sb") }},
		{"LockWorkspace", func() error { return s.LockWorkspace(ctx, "w", "sb", "rw") }},
		{"UnlockWorkspace", func() error { return s.UnlockWorkspace(ctx, "w", "sb") }},
		{"ReleaseSandboxLocks", func() error { return s.ReleaseSandboxLocks(ctx, "sb") }},
		{"ActiveSandboxForWorkspace", func() error { _, err := s.ActiveSandboxForWorkspace(ctx, "w"); return err }},
		{"ActiveSandboxForState", func() error { _, err := s.ActiveSandboxForState(ctx, "st"); return err }},
		{"CreateState", func() error { return s.CreateState(ctx, domain.State{ID: "st", Path: "/p", CreatedAt: ts}) }},
		{"GetState", func() error { _, err := s.GetState(ctx, "st"); return err }},
		{"ListStates", func() error { _, _, err := s.ListStates(ctx, "", 1, ""); return err }},
		{"DeleteState", func() error { return s.DeleteState(ctx, "st") }},
		{"TouchState", func() error { return s.TouchState(ctx, "st") }},
		{"CreateSandbox", func() error { return s.CreateSandbox(ctx, domain.Sandbox{ID: "sb", CreatedAt: ts}) }},
		{"GetSandbox", func() error { _, err := s.GetSandbox(ctx, "sb"); return err }},
		{"ListSandboxes", func() error { _, _, err := s.ListSandboxes(ctx, 1, ""); return err }},
		{"ActiveSandboxes", func() error { _, err := s.ActiveSandboxes(ctx); return err }},
		{"UpdateSandbox", func() error { return s.UpdateSandbox(ctx, domain.Sandbox{ID: "sb"}) }},
		{"SetSandboxStatus", func() error { return s.SetSandboxStatus(ctx, "sb", domain.SandboxFailed, nil) }},
		{"SetSandboxStopped", func() error { return s.SetSandboxStopped(ctx, "sb", ts) }},
		{"DeleteSandbox", func() error { return s.DeleteSandbox(ctx, "sb") }},
		{"FindSandboxByTokenHash", func() error { _, err := s.FindSandboxByTokenHash(ctx, hash); return err }},
		{"SaveTokenHash", func() error { return s.SaveTokenHash(ctx, "sb", hash) }},
		{"CreateJob", func() error {
			return s.CreateJob(ctx, domain.Job{ID: "j", Status: domain.JobQueued, CreatedAt: ts, UpdatedAt: ts})
		}},
		{"GetJob", func() error { _, err := s.GetJob(ctx, "j"); return err }},
		{"UpdateJob", func() error { return s.UpdateJob(ctx, domain.Job{ID: "j", UpdatedAt: ts}) }},
		{"ListJobsForType", func() error { _, err := s.ListJobsForType(ctx, "install"); return err }},
		{"CreateCredential", func() error { return s.CreateCredential(ctx, domain.Credential{ID: "c", CreatedAt: ts}) }},
		{"GetCredential", func() error { _, err := s.GetCredential(ctx, "c"); return err }},
		{"ListCredentials", func() error { _, _, err := s.ListCredentials(ctx, 1, ""); return err }},
		{"CredentialForProvider", func() error { _, err := s.CredentialForProvider(ctx, "p"); return err }},
		{"DeleteCredential", func() error { return s.DeleteCredential(ctx, "c") }},
		{"SetCredentialStatus", func() error { return s.SetCredentialStatus(ctx, "c", domain.CredentialRevoked, nil) }},
		{"TouchCredential", func() error { return s.TouchCredential(ctx, "c") }},
		{"CreateMCPConnection", func() error {
			return s.CreateMCPConnection(ctx, domain.MCPConnection{ID: "m", ConfigJSON: "{}", CreatedAt: ts})
		}},
		{"GetMCPConnection", func() error { _, err := s.GetMCPConnection(ctx, "m"); return err }},
		{"ListMCPConnections", func() error { _, _, err := s.ListMCPConnections(ctx, 1, ""); return err }},
		{"DeleteMCPConnection", func() error { return s.DeleteMCPConnection(ctx, "m") }},
		{"BindMCPConnection", func() error { return s.BindMCPConnection(ctx, "m", "sb") }},
		{"SetMCPConnectionStatus", func() error { return s.SetMCPConnectionStatus(ctx, "m", "connected", nil) }},
		{"CreateOAuthFlow", func() error {
			return s.CreateOAuthFlow(ctx, domain.OAuthFlow{ID: "f", ProviderID: "p", Status: domain.OAuthPending, CreatedAt: ts, ExpiresAt: ts})
		}},
		{"GetOAuthFlow", func() error { _, err := s.GetOAuthFlow(ctx, "f"); return err }},
		{"FindOAuthFlowByStateHash", func() error { _, err := s.FindOAuthFlowByStateHash(ctx, hash); return err }},
		{"UpdateOAuthFlow", func() error { return s.UpdateOAuthFlow(ctx, domain.OAuthFlow{ID: "f"}) }},
		{"ExpireOAuthFlows", func() error { return s.ExpireOAuthFlows(ctx) }},
		{"CompleteOAuthFlow", func() error { _, err := s.CompleteOAuthFlow(ctx, "f", "c"); return err }},
		{"CancelOAuthFlow", func() error { _, err := s.CancelOAuthFlow(ctx, "f"); return err }},
		{"AppendAudit", func() error { return s.AppendAudit(ctx, domain.AuditEvent{ID: "e", TS: ts, Type: "t"}) }},
		{"ListAudit", func() error { _, _, err := s.ListAudit(ctx, 1, "", ""); return err }},
		{"ReplaceServices", func() error { return s.ReplaceServices(ctx, "sb", nil) }},
		{"ListServices", func() error { _, err := s.ListServices(ctx, "sb"); return err }},
		{"GetService", func() error { _, err := s.GetService(ctx, "sb", "svc"); return err }},
		{"SetServiceStatus", func() error { return s.SetServiceStatus(ctx, "svc", domain.ServiceReady) }},
		{"GetIdempotent", func() error { _, _, err := s.GetIdempotent(ctx, "k", "POST", "/p", "h"); return err }},
		{"PutIdempotent", func() error { return s.PutIdempotent(ctx, "k", "POST", "/p", "h", 200, []byte("{}")) }},
	}
	for _, op := range ops {
		if err := op.call(); err == nil {
			t.Errorf("%s() on closed store error = nil, want error", op.name)
		}
	}
}
