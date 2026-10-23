package sqlite

import (
	"context"
	"fmt"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

const workspaceColumns = `w.id, w.name, w.root_path, w.storage_kind, w.default_mode, w.created_at, w.last_used_at,
	COALESCE((SELECT sandbox_id FROM workspace_locks WHERE workspace_id = w.id ORDER BY acquired_at ASC LIMIT 1), ''),
	COALESCE(w.last_sandbox_id, '')`

func (s *Store) scanWorkspace(sc interface{ Scan(...any) error }, w *domain.Workspace) error {
	var created string
	var lastUsed *string
	var lockedBy string
	var lastSandbox string
	if err := sc.Scan(&w.ID, &w.Name, &w.RootPath, &w.StorageKind, &w.DefaultMode, &created, &lastUsed, &lockedBy, &lastSandbox); err != nil {
		return err
	}
	w.CreatedAt = scanTimeValue(created)
	if lastUsed != nil {
		t := scanTimeValue(*lastUsed)
		w.LastUsedAt = &t
	}
	if lockedBy != "" {
		w.LockedBy = &lockedBy
	}
	if lastSandbox != "" {
		w.LastSandboxID = &lastSandbox
	}
	return nil
}

// CreateWorkspace inserts a workspace.
func (s *Store) CreateWorkspace(ctx context.Context, w domain.Workspace) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id,name,root_path,storage_kind,default_mode,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?)`, w.ID, w.Name, w.RootPath, string(w.StorageKind), w.DefaultMode, formatTime(w.CreatedAt), nullableTime(w.LastUsedAt))
	if err != nil {
		return fmt.Errorf("insert workspace: %w", err)
	}
	return nil
}

// GetWorkspace returns one workspace with its current lock.
func (s *Store) GetWorkspace(ctx context.Context, id string) (domain.Workspace, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+workspaceColumns+` FROM workspaces w WHERE w.id = ?`, id)
	var w domain.Workspace
	if err := s.scanWorkspace(row, &w); err != nil {
		return w, err
	}
	return w, nil
}

// ListWorkspaces returns a page ordered by created_at DESC.
func (s *Store) ListWorkspaces(ctx context.Context, limit int, cursor string) ([]domain.Workspace, string, error) {
	limit, ts, cid, err := Page(limit, cursor, 50, 200)
	if err != nil {
		return nil, "", err
	}
	query := `SELECT ` + workspaceColumns + ` FROM workspaces w`
	args := []any{}
	if ts != "" {
		query += ` WHERE (w.created_at < ? OR (w.created_at = ? AND w.id < ?))`
		args = append(args, ts, ts, cid)
	}
	query += ` ORDER BY w.created_at DESC, w.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	var out []domain.Workspace
	for rows.Next() {
		var w domain.Workspace
		if err := s.scanWorkspace(rows, &w); err != nil {
			return nil, "", err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(formatTime(last.CreatedAt), last.ID)
	}
	if out == nil {
		out = []domain.Workspace{}
	}
	return out, next, nil
}

// DeleteWorkspace removes the record. Files are handled by the service.
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// TouchWorkspace records the last used sandbox and time.
func (s *Store) TouchWorkspace(ctx context.Context, id, sandboxID string) error {
	var err error
	if sandboxID != "" {
		_, err = s.db.ExecContext(ctx, `UPDATE workspaces SET last_used_at = ?, last_sandbox_id = ? WHERE id = ?`, now(), sandboxID, id)
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE workspaces SET last_used_at = ? WHERE id = ?`, now(), id)
	}
	if err != nil {
		return fmt.Errorf("touch workspace: %w", err)
	}
	return nil
}

// LockWorkspace atomically acquires a workspace lease. A rw lease is exclusive;
// ro leases may coexist.
func (s *Store) LockWorkspace(ctx context.Context, workspaceID, sandboxID, mode string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace lock: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT mode FROM workspace_locks WHERE workspace_id = ? AND sandbox_id <> ?`, workspaceID, sandboxID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		if mode == "rw" || existing == "rw" {
			return domain.ErrStateLocked
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO workspace_locks(workspace_id,sandbox_id,mode,acquired_at) VALUES(?,?,?,?)`,
		workspaceID, sandboxID, mode, now()); err != nil {
		return fmt.Errorf("acquire workspace lock: %w", err)
	}
	return tx.Commit()
}

// UnlockWorkspace releases a sandbox's workspace lease.
func (s *Store) UnlockWorkspace(ctx context.Context, workspaceID, sandboxID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_locks WHERE workspace_id = ? AND sandbox_id = ?`, workspaceID, sandboxID)
	return err
}

// ReleaseSandboxLocks removes every lease held by a sandbox.
func (s *Store) ReleaseSandboxLocks(ctx context.Context, sandboxID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_locks WHERE sandbox_id = ?`, sandboxID)
	return err
}

// ActiveSandboxForWorkspace reports whether a live sandbox references a workspace.
func (s *Store) ActiveSandboxForWorkspace(ctx context.Context, workspaceID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM sandboxes WHERE workspace_id = ? AND status IN ('creating','starting','running','stopping') LIMIT 1`, workspaceID).Scan(&id)
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// ActiveSandboxForState reports whether a live sandbox references a state.
func (s *Store) ActiveSandboxForState(ctx context.Context, stateID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM sandboxes WHERE state_id = ? AND status IN ('creating','starting','running','stopping') LIMIT 1`, stateID).Scan(&id)
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// ---------------------------------------------------------------- states

const stateColumns = `id, harness_id, name, path, created_at, last_used_at, COALESCE(last_sandbox_id, '')`

func (s *Store) scanState(sc interface{ Scan(...any) error }, st *domain.State) error {
	var created string
	var lastUsed *string
	var lastSandbox string
	if err := sc.Scan(&st.ID, &st.HarnessID, &st.Name, &st.Path, &created, &lastUsed, &lastSandbox); err != nil {
		return err
	}
	if lastSandbox != "" {
		st.LastSandboxID = &lastSandbox
	}
	st.CreatedAt = scanTimeValue(created)
	if lastUsed != nil {
		t := scanTimeValue(*lastUsed)
		st.LastUsedAt = &t
	}
	return nil
}

// CreateState inserts a state record.
func (s *Store) CreateState(ctx context.Context, st domain.State) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO states(id,harness_id,name,path,created_at,last_used_at) VALUES(?,?,?,?,?,?)`,
		st.ID, st.HarnessID, st.Name, st.Path, formatTime(st.CreatedAt), nullableTime(st.LastUsedAt))
	if err != nil {
		return fmt.Errorf("insert state: %w", err)
	}
	return nil
}

// GetState returns a state by id.
func (s *Store) GetState(ctx context.Context, id string) (domain.State, error) {
	var st domain.State
	err := s.scanState(s.db.QueryRowContext(ctx, `SELECT `+stateColumns+` FROM states WHERE id = ?`, id), &st)
	return st, err
}

// ListStates lists states, optionally filtered by harness.
func (s *Store) ListStates(ctx context.Context, harnessID string, limit int, cursor string) ([]domain.State, string, error) {
	limit, ts, cid, err := Page(limit, cursor, 50, 200)
	if err != nil {
		return nil, "", err
	}
	query := `SELECT ` + stateColumns + ` FROM states WHERE 1=1`
	args := []any{}
	if harnessID != "" {
		query += ` AND harness_id = ?`
		args = append(args, harnessID)
	}
	if ts != "" {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, ts, ts, cid)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.State
	for rows.Next() {
		var st domain.State
		if err := s.scanState(rows, &st); err != nil {
			return nil, "", err
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(formatTime(last.CreatedAt), last.ID)
	}
	if out == nil {
		out = []domain.State{}
	}
	return out, next, nil
}

// DeleteState removes a state record.
func (s *Store) DeleteState(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM states WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// TouchState updates last_used_at.
func (s *Store) TouchState(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE states SET last_used_at = ? WHERE id = ?`, now(), id)
	return err
}

// TouchStateSandbox records the last sandbox that used a state.
func (s *Store) TouchStateSandbox(ctx context.Context, id, sandboxID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE states SET last_used_at = ?, last_sandbox_id = ? WHERE id = ?`, now(), sandboxID, id)
	return err
}
