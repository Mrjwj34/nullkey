package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// ------------------------------------------------------------------ jobs

func (s *Store) CreateJob(ctx context.Context, job domain.Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,type,status,cancellable,progress,message,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, job.ID, job.Type, string(job.Status), boolInt(job.Cancellable), job.Progress,
		job.Message, job.Error, formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

// GetJob returns a job.
func (s *Store) GetJob(ctx context.Context, id string) (domain.Job, error) {
	var job domain.Job
	var created, updated string
	var cancellable int
	err := s.db.QueryRowContext(ctx, `SELECT id,type,status,cancellable,progress,message,error,created_at,updated_at FROM jobs WHERE id=?`, id).
		Scan(&job.ID, &job.Type, &job.Status, &cancellable, &job.Progress, &job.Message, &job.Error, &created, &updated)
	if err != nil {
		return job, err
	}
	job.Cancellable = cancellable != 0
	job.CreatedAt = scanTimeValue(created)
	job.UpdatedAt = scanTimeValue(updated)
	return job, nil
}

// UpdateJob persists mutable job fields.
func (s *Store) UpdateJob(ctx context.Context, job domain.Job) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status=?, progress=?, message=?, error=?, updated_at=? WHERE id=?`,
		string(job.Status), job.Progress, job.Message, job.Error, formatTime(job.UpdatedAt), job.ID)
	return err
}

// ListJobsForType is used by the installer to detect in-flight work.
func (s *Store) ListJobsForType(ctx context.Context, jobType string) ([]domain.Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,type,status,cancellable,progress,message,error,created_at,updated_at FROM jobs WHERE type=? ORDER BY created_at DESC LIMIT 50`, jobType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		var job domain.Job
		var created, updated string
		var cancellable int
		if err := rows.Scan(&job.ID, &job.Type, &job.Status, &cancellable, &job.Progress, &job.Message, &job.Error, &created, &updated); err != nil {
			return nil, err
		}
		job.Cancellable = cancellable != 0
		job.CreatedAt = scanTimeValue(created)
		job.UpdatedAt = scanTimeValue(updated)
		out = append(out, job)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------- credentials

const credentialColumns = `id,provider_id,label,keychain_account,fingerprint,auth_type,COALESCE(oauth_status,''),COALESCE(base_url,''),COALESCE(header_name,''),expires_at,created_at,last_used_at`

func (s *Store) scanCredential(sc interface{ Scan(...any) error }, c *domain.Credential) error {
	var created string
	var expires, lastUsed *string
	if err := sc.Scan(&c.ID, &c.ProviderID, &c.Label, &c.KeychainAccount, &c.Fingerprint, &c.AuthType, &c.Status,
		&c.BaseURL, &c.HeaderName, &expires, &created, &lastUsed); err != nil {
		return err
	}
	if expires != nil {
		t := scanTimeValue(*expires)
		c.ExpiresAt = &t
	}
	c.CreatedAt = scanTimeValue(created)
	if lastUsed != nil {
		t := scanTimeValue(*lastUsed)
		c.LastUsedAt = &t
	}
	return nil
}

// CreateCredential inserts credential metadata.
func (s *Store) CreateCredential(ctx context.Context, c domain.Credential) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO credentials(id,provider_id,label,keychain_account,fingerprint,auth_type,oauth_status,base_url,header_name,expires_at,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.ProviderID, c.Label, c.KeychainAccount, c.Fingerprint, string(c.AuthType),
		string(c.Status), c.BaseURL, c.HeaderName, nullableTime(c.ExpiresAt), formatTime(c.CreatedAt), nullableTime(c.LastUsedAt))
	if err != nil {
		return fmt.Errorf("insert credential: %w", err)
	}
	return nil
}

// GetCredential returns one credential.
func (s *Store) GetCredential(ctx context.Context, id string) (domain.Credential, error) {
	var c domain.Credential
	err := s.scanCredential(s.db.QueryRowContext(ctx, `SELECT `+credentialColumns+` FROM credentials WHERE id=?`, id), &c)
	return c, err
}

// ListCredentials lists metadata.
func (s *Store) ListCredentials(ctx context.Context, limit int, cursor string) ([]domain.Credential, string, error) {
	limit, ts, cid, err := Page(limit, cursor, 50, 200)
	if err != nil {
		return nil, "", err
	}
	q := `SELECT ` + credentialColumns + ` FROM credentials`
	args := []any{}
	if ts != "" {
		q += ` WHERE (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, ts, ts, cid)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.Credential
	for rows.Next() {
		var c domain.Credential
		if err := s.scanCredential(rows, &c); err != nil {
			return nil, "", err
		}
		out = append(out, c)
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
		out = []domain.Credential{}
	}
	return out, next, nil
}

// CredentialForProvider selects a connected credential for a provider.
func (s *Store) CredentialForProvider(ctx context.Context, providerID string) (domain.Credential, error) {
	var c domain.Credential
	err := s.scanCredential(s.db.QueryRowContext(ctx, `SELECT `+credentialColumns+` FROM credentials
		WHERE provider_id=? AND oauth_status='connected' ORDER BY last_used_at DESC, created_at DESC LIMIT 1`, providerID), &c)
	return c, err
}

// RefreshableCredentialForProvider selects a connected or expired OAuth
// credential. Expired entries remain eligible so the host can refresh them
// without asking the user to sign in again.
func (s *Store) RefreshableCredentialForProvider(ctx context.Context, providerID string) (domain.Credential, error) {
	var c domain.Credential
	err := s.scanCredential(s.db.QueryRowContext(ctx, `SELECT `+credentialColumns+` FROM credentials
		WHERE provider_id=? AND auth_type='oauth' AND oauth_status IN ('connected','expired')
		ORDER BY last_used_at DESC, created_at DESC LIMIT 1`, providerID), &c)
	return c, err
}

// DeleteCredential removes metadata.
func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SetCredentialStatus updates oauth_status/expiry.
func (s *Store) SetCredentialStatus(ctx context.Context, id string, status domain.CredentialStatus, expires *time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET oauth_status=?, expires_at=? WHERE id=?`, string(status), nullableTime(expires), id)
	return err
}

// TouchCredential records use.
func (s *Store) TouchCredential(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE credentials SET last_used_at=? WHERE id=?`, now(), id)
	return err
}

// ------------------------------------------------------------ MCP connections

const mcpColumns = `id,server_id,label,credential_id,config_json,status,COALESCE(sandbox_id,''),created_at`

func (s *Store) scanMCP(sc interface{ Scan(...any) error }, m *domain.MCPConnection) error {
	var created string
	if err := sc.Scan(&m.ID, &m.ServerID, &m.Label, &m.CredentialID, &m.ConfigJSON, &m.Status, &m.SandboxID, &created); err != nil {
		return err
	}
	m.CreatedAt = scanTimeValue(created)
	var cfg struct {
		Tools []string `json:"tools"`
	}
	_ = json.Unmarshal([]byte(m.ConfigJSON), &cfg)
	m.Tools = cfg.Tools
	return nil
}

// CreateMCPConnection inserts a connection.
func (s *Store) CreateMCPConnection(ctx context.Context, m domain.MCPConnection) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_connections(id,server_id,label,credential_id,config_json,status,sandbox_id,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, m.ID, m.ServerID, m.Label, m.CredentialID, m.ConfigJSON, m.Status, nullableString(m.SandboxID), formatTime(m.CreatedAt))
	if err != nil {
		return err
	}
	return nil
}

// GetMCPConnection returns one connection.
func (s *Store) GetMCPConnection(ctx context.Context, id string) (domain.MCPConnection, error) {
	var m domain.MCPConnection
	err := s.scanMCP(s.db.QueryRowContext(ctx, `SELECT `+mcpColumns+` FROM mcp_connections WHERE id=?`, id), &m)
	return m, err
}

// ListMCPConnections lists connections.
func (s *Store) ListMCPConnections(ctx context.Context, limit int, cursor string) ([]domain.MCPConnection, string, error) {
	limit, ts, cid, err := Page(limit, cursor, 50, 200)
	if err != nil {
		return nil, "", err
	}
	q := `SELECT ` + mcpColumns + ` FROM mcp_connections`
	args := []any{}
	if ts != "" {
		q += ` WHERE (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, ts, ts, cid)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.MCPConnection
	for rows.Next() {
		var m domain.MCPConnection
		if err := s.scanMCP(rows, &m); err != nil {
			return nil, "", err
		}
		out = append(out, m)
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
		out = []domain.MCPConnection{}
	}
	return out, next, nil
}

// DeleteMCPConnection removes a connection.
func (s *Store) DeleteMCPConnection(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_connections WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// BindMCPConnection binds a connection to the first sandbox that uses it.
func (s *Store) BindMCPConnection(ctx context.Context, id, sandboxID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_connections SET sandbox_id=? WHERE id=? AND (sandbox_id IS NULL OR sandbox_id='' OR sandbox_id=?)`, sandboxID, id, sandboxID)
	return err
}

// ClaimMCPConnection atomically binds an unbound connection. It returns false
// when another sandbox won the race, preventing cross-sandbox TOCTOU reuse.
func (s *Store) ClaimMCPConnection(ctx context.Context, id, sandboxID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE mcp_connections SET sandbox_id=? WHERE id=? AND (sandbox_id IS NULL OR sandbox_id='' OR sandbox_id=?)`, sandboxID, id, sandboxID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetMCPConnectionStatus updates status and cached tools.
func (s *Store) SetMCPConnectionStatus(ctx context.Context, id, status string, tools []string) error {
	var existing string
	if err := s.db.QueryRowContext(ctx, `SELECT config_json FROM mcp_connections WHERE id=?`, id).Scan(&existing); err != nil {
		return err
	}
	cfg := map[string]any{}
	_ = json.Unmarshal([]byte(existing), &cfg)
	cfg["tools"] = tools
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE mcp_connections SET status=?, config_json=? WHERE id=?`, status, string(raw), id)
	return err
}

// ------------------------------------------------------------- OAuth flows

const flowColumns = `id,provider_id,status,credential_id,error,state_hash,created_at,expires_at`

func (s *Store) scanFlow(sc interface{ Scan(...any) error }, f *domain.OAuthFlow) error {
	var created, expires string
	if err := sc.Scan(&f.ID, &f.ProviderID, &f.Status, &f.CredentialID, &f.Error, &f.StateHash, &created, &expires); err != nil {
		return err
	}
	f.CreatedAt = scanTimeValue(created)
	f.ExpiresAt = scanTimeValue(expires)
	return nil
}

// CreateOAuthFlow inserts a pending flow.
func (s *Store) CreateOAuthFlow(ctx context.Context, f domain.OAuthFlow) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_flows(id,provider_id,status,credential_id,error,state_hash,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?)`, f.ID, f.ProviderID, string(f.Status), f.CredentialID, f.Error, f.StateHash, formatTime(f.CreatedAt), formatTime(f.ExpiresAt))
	return err
}

// GetOAuthFlow returns one flow.
func (s *Store) GetOAuthFlow(ctx context.Context, id string) (domain.OAuthFlow, error) {
	var f domain.OAuthFlow
	err := s.scanFlow(s.db.QueryRowContext(ctx, `SELECT `+flowColumns+` FROM oauth_flows WHERE id=?`, id), &f)
	return f, err
}

// FindOAuthFlowByStateHash finds a pending flow by state hash.
func (s *Store) FindOAuthFlowByStateHash(ctx context.Context, hash []byte) (domain.OAuthFlow, error) {
	var f domain.OAuthFlow
	err := s.scanFlow(s.db.QueryRowContext(ctx, `SELECT `+flowColumns+` FROM oauth_flows WHERE state_hash=? AND status='pending' LIMIT 1`, hash), &f)
	return f, err
}

// UpdateOAuthFlow persists flow status.
func (s *Store) UpdateOAuthFlow(ctx context.Context, f domain.OAuthFlow) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oauth_flows SET status=?, credential_id=?, error=? WHERE id=?`, string(f.Status), f.CredentialID, f.Error, f.ID)
	return err
}

// ExpireOAuthFlows marks pending flows past expiry as expired.
func (s *Store) ExpireOAuthFlows(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oauth_flows SET status='expired' WHERE status='pending' AND expires_at < ?`, now())
	return err
}

// ------------------------------------------------------------------ audit

// AppendAudit inserts a redacted audit event.
func (s *Store) AppendAudit(ctx context.Context, e domain.AuditEvent) error {
	detail := "{}"
	if e.Detail != nil {
		raw, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(raw)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(id,ts,type,sandbox_id,detail_json) VALUES(?,?,?,?,?)`,
		e.ID, formatTime(e.TS), e.Type, e.SandboxID, detail)
	return err
}

// ListAudit returns a page filtered by type.
func (s *Store) ListAudit(ctx context.Context, limit int, cursor, eventType string) ([]domain.AuditEvent, string, error) {
	limit, ts, cid, err := Page(limit, cursor, 50, 200)
	if err != nil {
		return nil, "", err
	}
	q := `SELECT id,ts,type,sandbox_id,detail_json FROM audit_events WHERE 1=1`
	args := []any{}
	if eventType != "" {
		q += ` AND type=?`
		args = append(args, eventType)
	}
	if ts != "" {
		q += ` AND (ts < ? OR (ts = ? AND id < ?))`
		args = append(args, ts, ts, cid)
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.AuditEvent
	for rows.Next() {
		var e domain.AuditEvent
		var ts, detail string
		if err := rows.Scan(&e.ID, &ts, &e.Type, &e.SandboxID, &detail); err != nil {
			return nil, "", err
		}
		e.TS = scanTimeValue(ts)
		_ = json.Unmarshal([]byte(detail), &e.Detail)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(formatTime(last.TS), last.ID)
	}
	if out == nil {
		out = []domain.AuditEvent{}
	}
	return out, next, nil
}

// ---------------------------------------------------------------- services

// ReplaceServices replaces the declared service set for a sandbox.
func (s *Store) ReplaceServices(ctx context.Context, sandboxID string, services []domain.Service) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM services WHERE sandbox_id=?`, sandboxID); err != nil {
		return err
	}
	for _, svc := range services {
		cmd, _ := json.Marshal(svc.Command)
		ports, _ := json.Marshal(svc.Ports)
		createdAt := svc.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO services(id,sandbox_id,name,protocol,target_host,target_port,health_path,websocket,command_json,ports_json,status,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, svc.ID, sandboxID, svc.Name, svc.Protocol, svc.TargetHost, svc.TargetPort,
			svc.HealthPath, boolInt(svc.WebSocket), string(cmd), string(ports), string(svc.Status), formatTime(createdAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListServices returns declared services for a sandbox.
func (s *Store) ListServices(ctx context.Context, sandboxID string) ([]domain.Service, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,sandbox_id,name,protocol,target_host,target_port,COALESCE(health_path,''),websocket,command_json,ports_json,status,created_at
		FROM services WHERE sandbox_id=? ORDER BY created_at ASC`, sandboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		var svc domain.Service
		var websocket int
		var cmdJSON, portsJSON, created string
		if err := rows.Scan(&svc.ID, &svc.SandboxID, &svc.Name, &svc.Protocol, &svc.TargetHost, &svc.TargetPort, &svc.HealthPath,
			&websocket, &cmdJSON, &portsJSON, &svc.Status, &created); err != nil {
			return nil, err
		}
		svc.WebSocket = websocket != 0
		_ = json.Unmarshal([]byte(cmdJSON), &svc.Command)
		_ = json.Unmarshal([]byte(portsJSON), &svc.Ports)
		svc.CreatedAt = scanTimeValue(created)
		out = append(out, svc)
	}
	if out == nil {
		out = []domain.Service{}
	}
	return out, rows.Err()
}

// GetService returns a declared service.
func (s *Store) GetService(ctx context.Context, sandboxID, serviceID string) (domain.Service, error) {
	all, err := s.ListServices(ctx, sandboxID)
	if err != nil {
		return domain.Service{}, err
	}
	for _, svc := range all {
		if svc.ID == serviceID || svc.Name == serviceID {
			return svc, nil
		}
	}
	return domain.Service{}, domain.ErrNotFound
}

// SetServiceStatus updates one service status.
func (s *Store) SetServiceStatus(ctx context.Context, id string, status domain.ServiceStatus) error {
	_, err := s.db.ExecContext(ctx, `UPDATE services SET status=? WHERE id=?`, string(status), id)
	return err
}

// ------------------------------------------------------------- idempotency

// ErrIdempotencyKeyReuse means the same key was used with a different body.
var ErrIdempotencyKeyReuse = errors.New("idempotency key reused with a different request")

// IdempotentResponse is a stored POST result.
type IdempotentResponse struct {
	Status int
	Body   []byte
}

// GetIdempotent returns a stored response for a key.
func (s *Store) GetIdempotent(ctx context.Context, key, method, path, requestHash string) (IdempotentResponse, bool, error) {
	var out IdempotentResponse
	var storedHash string
	err := s.db.QueryRowContext(ctx, `SELECT status,body,request_hash FROM idempotency WHERE key=? AND method=? AND path=?`, key, method, path).
		Scan(&out.Status, &out.Body, &storedHash)
	if isNoRows(err) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if storedHash != requestHash {
		return out, false, ErrIdempotencyKeyReuse
	}
	return out, true, nil
}

// PutIdempotent stores a response.
func (s *Store) PutIdempotent(ctx context.Context, key, method, path, requestHash string, status int, body []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO idempotency(key,method,path,request_hash,status,body,created_at) VALUES(?,?,?,?,?,?,?)`,
		key, method, path, requestHash, status, body, now())
	return err
}

// CleanupIdempotency removes expired entries.
func (s *Store) CleanupIdempotency(ctx context.Context, olderThan time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM idempotency WHERE created_at < ?`, formatTime(olderThan))
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

var _ = sql.ErrNoRows

// CompleteOAuthFlow atomically moves a pending flow to succeeded. It returns
// false when the flow was cancelled, expired, or already terminal so callers
// must not persist a late callback's credential.
func (s *Store) CompleteOAuthFlow(ctx context.Context, id, credentialID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE oauth_flows SET status='succeeded', credential_id=?, state_hash=NULL
		WHERE id=? AND status='pending' AND expires_at >= ?`, credentialID, id, now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CancelOAuthFlow atomically moves a pending flow to cancelled and invalidates
// its state hash.
func (s *Store) CancelOAuthFlow(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE oauth_flows SET status='cancelled', state_hash=NULL
		WHERE id=? AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
