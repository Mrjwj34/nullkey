package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

const sandboxColumns = `id, harness_id, COALESCE(workspace_id,''), COALESCE(state_id,''), runtime, network_mode, status,
	COALESCE(pid,0), last_error, token_hash, created_at, started_at, stopped_at`

func (s *Store) scanSandbox(sc interface{ Scan(...any) error }, sb *domain.Sandbox) error {
	var created string
	var started, stopped *string
	if err := sc.Scan(&sb.ID, &sb.HarnessID, &sb.WorkspaceID, &sb.StateID, &sb.Runtime, &sb.NetworkMode, &sb.Status,
		&sb.PID, &sb.LastError, &sb.TokenHash, &created, &started, &stopped); err != nil {
		return err
	}
	sb.CreatedAt = scanTimeValue(created)
	if started != nil {
		t := scanTimeValue(*started)
		sb.StartedAt = &t
	}
	if stopped != nil {
		t := scanTimeValue(*stopped)
		sb.StoppedAt = &t
	}
	return nil
}

// CreateSandbox inserts a sandbox row.
func (s *Store) CreateSandbox(ctx context.Context, sb domain.Sandbox) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sandboxes(id,harness_id,workspace_id,state_id,runtime,network_mode,status,token_hash,pid,last_error,created_at,started_at,stopped_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sb.ID, sb.HarnessID, sb.WorkspaceID, nullableString(sb.StateID), string(sb.Runtime), string(sb.NetworkMode), string(sb.Status),
		sb.TokenHash, sb.PID, sb.LastError, formatTime(sb.CreatedAt), nullableTime(sb.StartedAt), nullableTime(sb.StoppedAt))
	if err != nil {
		return fmt.Errorf("insert sandbox: %w", err)
	}
	return nil
}

// GetSandbox returns a sandbox.
func (s *Store) GetSandbox(ctx context.Context, id string) (domain.Sandbox, error) {
	var sb domain.Sandbox
	err := s.scanSandbox(s.db.QueryRowContext(ctx, `SELECT `+sandboxColumns+` FROM sandboxes WHERE id = ?`, id), &sb)
	return sb, err
}

// ListSandboxes returns a page.
func (s *Store) ListSandboxes(ctx context.Context, limit int, cursor string) ([]domain.Sandbox, string, error) {
	limit, ts, cid, err := Page(limit, cursor, 50, 200)
	if err != nil {
		return nil, "", err
	}
	query := `SELECT ` + sandboxColumns + ` FROM sandboxes`
	args := []any{}
	if ts != "" {
		query += ` WHERE (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, ts, ts, cid)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.Sandbox
	for rows.Next() {
		var sb domain.Sandbox
		if err := s.scanSandbox(rows, &sb); err != nil {
			return nil, "", err
		}
		out = append(out, sb)
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
		out = []domain.Sandbox{}
	}
	return out, next, nil
}

// ActiveSandboxes lists sandboxes that may still have processes or locks.
func (s *Store) ActiveSandboxes(ctx context.Context) ([]domain.Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sandboxColumns+` FROM sandboxes WHERE status IN ('creating','starting','running','stopping')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Sandbox
	for rows.Next() {
		var sb domain.Sandbox
		if err := s.scanSandbox(rows, &sb); err != nil {
			return nil, err
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}

// UpdateSandbox persists mutable lifecycle fields.
func (s *Store) UpdateSandbox(ctx context.Context, sb domain.Sandbox) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET status=?, pid=?, last_error=?, token_hash=?, started_at=?, stopped_at=? WHERE id=?`,
		string(sb.Status), sb.PID, sb.LastError, sb.TokenHash, nullableTime(sb.StartedAt), nullableTime(sb.StoppedAt), sb.ID)
	if err != nil {
		return fmt.Errorf("update sandbox: %w", err)
	}
	return nil
}

// SetSandboxStatus updates only the status and error message.
func (s *Store) SetSandboxStatus(ctx context.Context, id string, status domain.SandboxStatus, lastErr *string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET status=?, last_error=? WHERE id=?`, string(status), lastErr, id)
	return err
}

// SetSandboxStopped marks a sandbox stopped and clears its token hash.
func (s *Store) SetSandboxStopped(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET status='stopped', pid=0, token_hash=NULL, stopped_at=? WHERE id=?`, formatTime(at), id)
	return err
}

// DeleteSandbox removes the record.
func (s *Store) DeleteSandbox(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM workspace_locks WHERE sandbox_id=?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM services WHERE sandbox_id=?`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sandboxes WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// FindSandboxByTokenHash resolves a sandbox token hash. It only considers
// running sandboxes; stopped sandboxes fail closed.
func (s *Store) FindSandboxByTokenHash(ctx context.Context, hash []byte) (domain.Sandbox, error) {
	var sb domain.Sandbox
	err := s.scanSandbox(s.db.QueryRowContext(ctx, `SELECT `+sandboxColumns+` FROM sandboxes WHERE token_hash=? AND status IN ('starting','running')`, hash), &sb)
	return sb, err
}

// SaveTokenHash stores a sandbox token hash.
func (s *Store) SaveTokenHash(ctx context.Context, id string, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET token_hash=? WHERE id=?`, hash, id)
	return err
}
