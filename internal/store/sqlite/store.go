// Package sqlite implements the kernel's persistent store on SQLite (WAL).
//
// The database never contains plaintext credentials or sandbox tokens: token
// hashes only, and secret values live in the platform secret store. All
// timestamps are UTC RFC 3339 with nanoseconds so lexical ordering is stable.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the SQLite-backed persistence adapter.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (and creates) the database below home, then applies migrations.
func Open(ctx context.Context, home string) (*Store, error) {
	path := filepath.Join(home, "nullkey.db")
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	s := &Store{db: db, path: path}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path (used in diagnostics; never a secret).
func (s *Store) Path() string { return s.path }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for i, stmt := range migrations {
		version := i + 1
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if exists > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, now()); err != nil {
			return fmt.Errorf("record migration %d: %w", version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

var migrations = []string{
	// 1: core resources.
	`CREATE TABLE workspaces (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		root_path TEXT NOT NULL UNIQUE,
		storage_kind TEXT NOT NULL,
		default_mode TEXT NOT NULL,
		created_at TEXT NOT NULL,
		last_used_at TEXT
	);
	CREATE TABLE states (
		id TEXT PRIMARY KEY,
		harness_id TEXT NOT NULL,
		name TEXT NOT NULL,
		path TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		last_used_at TEXT
	);
	CREATE TABLE sandboxes (
		id TEXT PRIMARY KEY,
		harness_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		state_id TEXT,
		runtime TEXT NOT NULL,
		network_mode TEXT NOT NULL,
		status TEXT NOT NULL,
		token_hash BLOB,
		pid INTEGER NOT NULL DEFAULT 0,
		last_error TEXT,
		created_at TEXT NOT NULL,
		started_at TEXT,
		stopped_at TEXT
	);
	CREATE INDEX sandboxes_workspace ON sandboxes(workspace_id);
	CREATE INDEX sandboxes_state ON sandboxes(state_id);
	CREATE TABLE workspace_locks (
		workspace_id TEXT NOT NULL,
		sandbox_id TEXT NOT NULL,
		mode TEXT NOT NULL,
		acquired_at TEXT NOT NULL,
		PRIMARY KEY (workspace_id, sandbox_id)
	);
	CREATE TABLE credentials (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL,
		label TEXT NOT NULL,
		keychain_account TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		auth_type TEXT NOT NULL,
		oauth_status TEXT,
		base_url TEXT,
		header_name TEXT,
		expires_at TEXT,
		created_at TEXT NOT NULL,
		last_used_at TEXT
	);
	CREATE TABLE jobs (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		status TEXT NOT NULL,
		cancellable INTEGER NOT NULL,
		progress REAL NOT NULL,
		message TEXT,
		error TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE oauth_flows (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL,
		status TEXT NOT NULL,
		credential_id TEXT,
		error TEXT,
		state_hash BLOB,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);
	CREATE TABLE audit_events (
		id TEXT PRIMARY KEY,
		ts TEXT NOT NULL,
		type TEXT NOT NULL,
		sandbox_id TEXT,
		detail_json TEXT NOT NULL
	);
	CREATE INDEX audit_events_ts ON audit_events(ts DESC, id DESC);
	CREATE TABLE mcp_connections (
		id TEXT PRIMARY KEY,
		server_id TEXT NOT NULL,
		label TEXT NOT NULL,
		credential_id TEXT,
		config_json TEXT NOT NULL,
		status TEXT NOT NULL,
		sandbox_id TEXT,
		created_at TEXT NOT NULL
	);
	CREATE TABLE services (
		id TEXT PRIMARY KEY,
		sandbox_id TEXT NOT NULL,
		name TEXT NOT NULL,
		protocol TEXT NOT NULL,
		target_host TEXT NOT NULL,
		target_port INTEGER NOT NULL,
		health_path TEXT,
		websocket INTEGER NOT NULL,
		command_json TEXT NOT NULL,
		ports_json TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX services_sandbox ON services(sandbox_id);
	CREATE TABLE idempotency (
		key TEXT NOT NULL,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		status INTEGER NOT NULL,
		body BLOB NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (key, method, path)
	);`,
	// 2: expose the last sandbox on workspace/state (OpenAPI contract).
	`ALTER TABLE workspaces ADD COLUMN last_sandbox_id TEXT;
	 ALTER TABLE states ADD COLUMN last_sandbox_id TEXT;`,
	// 3: scope idempotency keys by endpoint; the original PK only covered key,
	// so one key reused on another POST path overwrote the first response.
	`CREATE TABLE idempotency_v3 (
		key TEXT NOT NULL,
		method TEXT NOT NULL,
		path TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		status INTEGER NOT NULL,
		body BLOB NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (key, method, path)
	);
	INSERT OR IGNORE INTO idempotency_v3(key,method,path,request_hash,status,body,created_at)
		SELECT key,method,path,request_hash,status,body,created_at FROM idempotency;
	DROP TABLE idempotency;
	ALTER TABLE idempotency_v3 RENAME TO idempotency;`,
}

// timestampLayout is fixed-width so SQLite text comparisons remain chronological.
// RFC3339Nano trims trailing zero fractions, which breaks lexicographic order.
const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func now() string { return time.Now().UTC().Format(timestampLayout) }

func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func scanTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil
	}
	return &t
}

func scanTimeValue(s string) time.Time {
	t, _ := parseTime(s)
	return t
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// Cursor encoding is opaque to clients: base64url of created_at|id.
func encodeCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "|" + id))
}

func decodeCursor(cursor string) (createdAt, id string, err error) {
	if cursor == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid cursor")
	}
	if _, err := parseTime(parts[0]); err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	return parts[0], parts[1], nil
}

// Page normalizes a limit and decodes a cursor.
func Page(limit int, cursor string, fallback, max int) (int, string, string, error) {
	if limit <= 0 {
		limit = fallback
	}
	if limit > max {
		limit = max
	}
	ts, id, err := decodeCursor(cursor)
	return limit, ts, id, err
}

// CleanupOrphanLocks removes locks whose sandbox is gone or no longer active.
func (s *Store) CleanupOrphanLocks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_locks WHERE sandbox_id NOT IN (
		SELECT id FROM sandboxes WHERE status IN ('creating','starting','running','stopping'))`)
	if err != nil {
		return fmt.Errorf("cleanup workspace locks: %w", err)
	}
	return nil
}

// ResetTokenHashes clears all sandbox token hashes on startup so previous
// tokens fail closed after a daemon restart.
func (s *Store) ResetTokenHashes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET token_hash = NULL WHERE token_hash IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("reset token hashes: %w", err)
	}
	return nil
}
