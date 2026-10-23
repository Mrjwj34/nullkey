package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Mrjwj34/nullkey/internal/domain"
	nkid "github.com/Mrjwj34/nullkey/internal/id"
	"github.com/Mrjwj34/nullkey/internal/redact"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
	"github.com/Mrjwj34/nullkey/internal/terminal"
)

// CreateSandboxRequest is the core launch request.
type CreateSandboxRequest struct {
	HarnessID   string
	WorkspaceID string
	StateID     string
	Runtime     string
	NetworkMode string
}

// CreateSandbox starts a native sandbox. The real credentials never enter this
// path: only a freshly generated per-sandbox token is passed to the runtime.
func (s *Service) CreateSandbox(ctx context.Context, req CreateSandboxRequest) (domain.Sandbox, error) {
	if req.HarnessID == "" {
		return domain.Sandbox{}, Invalid("harness_id is required")
	}
	if req.WorkspaceID == "" {
		return domain.Sandbox{}, Invalid("workspace_id is required")
	}
	if req.Runtime == "" {
		req.Runtime = string(domain.RuntimeNative)
	}
	if req.Runtime != string(domain.RuntimeNative) {
		return domain.Sandbox{}, E(http.StatusNotImplemented, "desktop_unavailable", "desktop runtime is not available")
	}
	if s.Runtime == nil || !native.Available() {
		return domain.Sandbox{}, E(http.StatusInternalServerError, "runtime_unavailable", "native runtime is not available on this host")
	}
	mode := domain.NetworkMode(req.NetworkMode)
	if mode == "" {
		mode = domain.NetworkFiltered
	}
	switch mode {
	case domain.NetworkServicesOnly, domain.NetworkFiltered, domain.NetworkOpen:
	default:
		return domain.Sandbox{}, Invalid("network_mode must be services_only, filtered, or open")
	}

	ws, err := s.GetWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return domain.Sandbox{}, err
	}
	state, err := s.resolveState(ctx, req.StateID, req.HarnessID)
	if err != nil {
		return domain.Sandbox{}, err
	}
	if state.HarnessID != req.HarnessID {
		return domain.Sandbox{}, Invalid("state harness_id does not match harness_id")
	}
	command, err := s.harnessCommand(req.HarnessID)
	if err != nil {
		return domain.Sandbox{}, err
	}

	id := newID("sbx_")
	if err := s.Store.LockWorkspace(ctx, ws.ID, id, ws.DefaultMode); err != nil {
		if errors.Is(err, domain.ErrStateLocked) {
			return domain.Sandbox{}, Conflict("state_locked", "workspace is already in use by an active sandbox")
		}
		return domain.Sandbox{}, Wrap(http.StatusInternalServerError, "internal", "could not lock workspace", err)
	}
	unlock := true
	defer func() {
		if unlock {
			_ = s.Store.ReleaseSandboxLocks(ctx, id)
		}
	}()

	token, err := nkid.Random(32)
	if err != nil {
		return domain.Sandbox{}, Wrap(http.StatusInternalServerError, "internal", "could not generate sandbox token", err)
	}
	now := s.now()
	sb := domain.Sandbox{
		ID:          id,
		HarnessID:   req.HarnessID,
		WorkspaceID: ws.ID,
		StateID:     state.ID,
		Runtime:     domain.RuntimeNative,
		NetworkMode: mode,
		Status:      domain.SandboxStarting,
		TokenHash:   hashToken(token),
		CreatedAt:   now,
	}
	if err := s.Store.CreateSandbox(ctx, sb); err != nil {
		return domain.Sandbox{}, Wrap(http.StatusInternalServerError, "internal", "could not create sandbox", err)
	}

	cachePath := filepath.Join(s.Home, "cache", id)
	proc, startErr := s.Runtime.Start(ctx, native.Spec{
		SandboxID:     id,
		HarnessID:     req.HarnessID,
		WorkspacePath: ws.RootPath,
		StatePath:     state.Path,
		CachePath:     cachePath,
		Command:       command,
		Token:         token,
		NetworkMode:   mode,
	})
	if startErr != nil {
		msg := redact.String(startErr.Error())
		sb.Status = domain.SandboxFailed
		sb.LastError = &msg
		sb.TokenHash = nil
		_ = s.Store.UpdateSandbox(ctx, sb)
		_ = s.Store.ReleaseSandboxLocks(ctx, id)
		unlock = false
		s.Log.Error("sandbox_start_failed", "sandbox_id", id, "error", msg)
		s.Audit.Record(ctx, "sandbox.failed", &id, map[string]any{"error": msg})
		s.PublishEvent("sandbox.failed", map[string]any{"sandbox_id": id, "status": "failed"})
		return domain.Sandbox{}, Wrap(http.StatusInternalServerError, "runtime_unavailable", "could not start sandbox runtime", startErr)
	}

	started := s.now()
	sb.Status = domain.SandboxRunning
	sb.PID = proc.PID
	sb.StartedAt = &started
	if err := s.Store.UpdateSandbox(ctx, sb); err != nil {
		_ = s.Runtime.Stop(ctx, id)
		msg := "could not persist sandbox"
		sb.Status = domain.SandboxFailed
		sb.LastError = &msg
		sb.TokenHash = nil
		_ = s.Store.UpdateSandbox(context.Background(), sb)
		_ = s.Store.ReleaseSandboxLocks(context.Background(), id)
		return domain.Sandbox{}, Wrap(http.StatusInternalServerError, "internal", "could not persist sandbox", err)
	}
	if s.Terminal != nil {
		s.Terminal.Open(id, proc)
	}
	// Service Supervisor starts asynchronously: a slow service must not block
	// the terminal from becoming usable.
	go s.startServices(context.Background(), id, ws.RootPath)
	_ = s.Store.TouchWorkspace(ctx, ws.ID, id)
	_ = s.Store.TouchStateSandbox(ctx, state.ID, id)
	s.writeGeneratedConfig(state.Path, id, mode)
	unlock = false

	s.Audit.Record(ctx, "sandbox.started", &id, map[string]any{
		"harness_id":   req.HarnessID,
		"workspace_id": ws.ID,
		"network_mode": string(mode),
	})
	s.PublishEvent("sandbox.started", map[string]any{"sandbox_id": id, "status": "running"})
	return s.Store.GetSandbox(ctx, id)
}

func (s *Service) resolveState(ctx context.Context, stateID, harnessID string) (domain.State, error) {
	if stateID == "" {
		return s.CreateState(ctx, harnessID, "sandbox")
	}
	st, err := s.GetState(ctx, stateID)
	if err != nil {
		return domain.State{}, err
	}
	return st, nil
}

// writeGeneratedConfig records the non-secret runtime configuration. The token
// is referenced only by environment variable name.
func (s *Service) writeGeneratedConfig(statePath, sandboxID string, mode domain.NetworkMode) {
	dir := filepath.Join(statePath, "nullkey")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	cfg := map[string]any{
		"sandbox_id":     sandboxID,
		"network_mode":   string(mode),
		"gateway":        "http://" + s.GatewayAddr,
		"proxy":          "http://" + s.ProxyAddr,
		"token_env":      "NULLKEY_TOKEN",
		"generated_by":   "nullkeyd",
		"schema_version": 1,
	}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600)
}

// GetSandbox returns a sandbox.
func (s *Service) GetSandbox(ctx context.Context, id string) (domain.Sandbox, error) {
	sb, err := s.Store.GetSandbox(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return domain.Sandbox{}, NotFound("sandbox not found")
		}
		return domain.Sandbox{}, Wrap(http.StatusInternalServerError, "internal", "could not read sandbox", err)
	}
	return sb, nil
}

// ListSandboxes returns a page.
func (s *Service) ListSandboxes(ctx context.Context, limit int, cursor string) ([]domain.Sandbox, string, error) {
	items, next, err := s.Store.ListSandboxes(ctx, limit, cursor)
	if err != nil {
		return nil, "", Wrap(http.StatusInternalServerError, "internal", "could not list sandboxes", err)
	}
	return items, next, nil
}

// StopSandbox revokes the token, stops services, the PTY and the process group,
// then releases the workspace lease.
func (s *Service) StopSandbox(ctx context.Context, id string) error {
	sb, err := s.GetSandbox(ctx, id)
	if err != nil {
		return err
	}
	if sb.Status == domain.SandboxStopped {
		return nil
	}
	if err := s.Store.SetSandboxStatus(ctx, id, domain.SandboxStopping, nil); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not update sandbox", err)
	}
	// Revoke first: a concurrent gateway call must fail closed.
	_ = s.Store.SaveTokenHash(ctx, id, nil)
	if s.Terminal != nil {
		s.Terminal.Close(id)
	}
	if s.tunnel != nil {
		s.tunnel.closeSandbox(id)
	}
	s.stopServices(ctx, id)
	if s.Runtime != nil {
		if err := s.Runtime.Stop(ctx, id); err != nil && !errors.Is(err, native.ErrNotRunning) {
			s.Log.Warn("sandbox_stop_failed", "sandbox_id", id, "error", err)
		}
	}
	_ = s.Store.ReleaseSandboxLocks(ctx, id)
	if err := s.Store.SetSandboxStopped(ctx, id, s.now()); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not stop sandbox", err)
	}
	s.Audit.Record(ctx, "sandbox.stopped", &id, nil)
	s.PublishEvent("sandbox.stopped", map[string]any{"sandbox_id": id, "status": "stopped"})
	return nil
}

// DeleteSandbox stops the sandbox and removes its record. Workspace and state
// are always preserved.
func (s *Service) DeleteSandbox(ctx context.Context, id string) error {
	if _, err := s.GetSandbox(ctx, id); err != nil {
		return err
	}
	if err := s.StopSandbox(ctx, id); err != nil {
		return err
	}
	if err := s.Store.DeleteSandbox(ctx, id); err != nil {
		if isNotFound(err) {
			return NotFound("sandbox not found")
		}
		return Wrap(http.StatusInternalServerError, "internal", "could not delete sandbox", err)
	}
	s.Audit.Record(ctx, "sandbox.deleted", &id, nil)
	s.PublishEvent("sandbox.deleted", map[string]any{"sandbox_id": id, "status": "deleted"})
	return nil
}

// Stats samples sandbox resource usage.
func (s *Service) Stats(ctx context.Context, id string) (domain.Stats, error) {
	sb, err := s.GetSandbox(ctx, id)
	if err != nil {
		return domain.Stats{}, err
	}
	out := domain.Stats{PID: sb.PID}
	if sb.StartedAt != nil {
		out.UptimeMS = s.now().Sub(*sb.StartedAt).Milliseconds()
	}
	if sb.WorkspaceID != "" {
		if ws, err := s.Store.GetWorkspace(ctx, sb.WorkspaceID); err == nil {
			out.DiskBytes = dirSize(ws.RootPath)
		}
	}
	if s.Runtime != nil {
		if proc, ok := s.Runtime.Get(id); ok {
			stats, err := proc.Stats()
			if err != nil {
				return domain.Stats{}, Wrap(http.StatusInternalServerError, "internal", "could not sample sandbox", err)
			}
			out.MemoryBytes = stats.MemoryBytes
			out.CPUPercent = stats.CPUPercent
			out.PID = stats.PID
			out.UptimeMS = stats.Uptime.Milliseconds()
			out.StartupMS = stats.StartupMS
		}
	}
	return out, nil
}

// TerminalTicket mints a one-time terminal WebSocket ticket.
func (s *Service) TerminalTicket(ctx context.Context, id string) (domain.TerminalTicket, error) {
	sb, err := s.GetSandbox(ctx, id)
	if err != nil {
		return domain.TerminalTicket{}, err
	}
	if sb.Status != domain.SandboxRunning {
		return domain.TerminalTicket{}, Conflict("conflict", "sandbox is not running")
	}
	if s.Terminal == nil || !s.Terminal.Has(id) {
		return domain.TerminalTicket{}, Conflict("terminal_not_ready", "terminal is not ready")
	}
	raw, expires, err := s.Terminal.Mint(id)
	if err != nil {
		if errors.Is(err, terminal.ErrNotReady) {
			return domain.TerminalTicket{}, Conflict("terminal_not_ready", "terminal is not ready")
		}
		return domain.TerminalTicket{}, Wrap(http.StatusInternalServerError, "internal", "could not mint terminal ticket", err)
	}
	return domain.TerminalTicket{
		WSURL:     "ws://" + s.GatewayAddr + "/terminal",
		Ticket:    raw,
		ExpiresAt: expires,
	}, nil
}

// Scan performs a heuristic credential check of Nullkey-generated runtime
// files only. It never claims the user's workspace is secret-free.
func (s *Service) Scan(ctx context.Context, id string) (domain.ScanResult, error) {
	sb, err := s.GetSandbox(ctx, id)
	if err != nil {
		return domain.ScanResult{}, err
	}
	st, err := s.Store.GetState(ctx, sb.StateID)
	if err != nil {
		return domain.ScanResult{}, Wrap(http.StatusInternalServerError, "internal", "could not read sandbox state", err)
	}
	checks := []domain.ScanCheck{{Name: "generated_config", Hits: 0}, {Name: "state_files", Hits: 0}, {Name: "sandbox_env", Hits: 0}}
	hits := 0

	if _, err := os.Stat(st.Path); err != nil {
		return domain.ScanResult{}, Wrap(http.StatusInternalServerError, "internal", "could not read sandbox state", err)
	}
	configPath := filepath.Join(st.Path, "nullkey", "config.json")
	if raw, err := os.ReadFile(configPath); err == nil {
		n := redact.Count(string(raw))
		checks[0].Hits = n
		hits += n
	} else if !os.IsNotExist(err) {
		return domain.ScanResult{}, Wrap(http.StatusInternalServerError, "internal", "could not read generated config", err)
	}

	stateHits := 0
	generatedDir := filepath.Join(st.Path, "nullkey")
	walkErr := filepath.WalkDir(generatedDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() == "config.json" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1<<20 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		stateHits += redact.Count(string(raw))
		return nil
	})
	if walkErr != nil {
		return domain.ScanResult{}, Wrap(http.StatusInternalServerError, "internal", "could not scan generated runtime", walkErr)
	}
	checks[1].Hits = stateHits
	hits += stateHits

	return domain.ScanResult{
		OK:        true,
		Hits:      hits,
		Scope:     "generated_runtime",
		Excluded:  []string{"workspace", "user_imports", "user_input"},
		Checks:    checks,
		ScannedAt: s.now(),
	}, nil
}
