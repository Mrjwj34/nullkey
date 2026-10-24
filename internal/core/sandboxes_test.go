package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
)

func TestCreateSandboxValidation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateSandbox(ctx, CreateSandboxRequest{})
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell"})
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: "ws_x", Runtime: "desktop"})
	wantError(t, err, http.StatusNotImplemented, "desktop_unavailable")

	ws := mustWorkspace(t, svc, "ws", "", "")
	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID})
	wantError(t, err, http.StatusInternalServerError, "runtime_unavailable")
}

func TestCreateSandboxValidationWithRuntime(t *testing.T) {
	if !native.Available() {
		t.Skip("native runtime is not available on this host")
	}
	t.Setenv("PATH", t.TempDir())
	// Validation failures happen before Start, so a zero-value runtime is
	// enough to pass the availability gate.
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = &native.Runtime{} })
	ctx := context.Background()

	ws := mustWorkspace(t, svc, "ws", "", "")
	if err := svc.Store.LockWorkspace(ctx, ws.ID, "sbx_holder", "rw"); err != nil {
		t.Fatalf("lock workspace: %v", err)
	}
	_, err := svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID})
	wantError(t, err, http.StatusConflict, "state_locked")
	if err := svc.Store.ReleaseSandboxLocks(ctx, "sbx_holder"); err != nil {
		t.Fatalf("release: %v", err)
	}

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID, NetworkMode: "bogus"})
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: "ws_missing"})
	wantError(t, err, http.StatusNotFound, "not_found")

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID, StateID: "st_missing"})
	wantError(t, err, http.StatusNotFound, "not_found")

	other := mustState(t, svc, "codex", "default")
	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID, StateID: other.ID})
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "claude", WorkspaceID: ws.ID})
	wantError(t, err, http.StatusConflict, "harness_not_installed")
}

func TestCreateSandboxForcedRuntimeFailure(t *testing.T) {
	if runtime.GOOS != "darwin" || !native.Available() {
		t.Skip("native Seatbelt runtime is only available on darwin")
	}
	t.Setenv("NULLKEY_FORCE_RUNTIME_FAILURE", "1")
	home := t.TempDir()
	rt, err := native.New(native.Config{
		Home:        home,
		GatewayAddr: "127.0.0.1:49153",
		ProxyAddr:   "127.0.0.1:49154",
		Logger:      testLogger(),
	})
	if err != nil {
		t.Fatalf("native runtime: %v", err)
	}
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = rt })
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")

	ch, cancel, err := svc.Events.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID})
	ce := wantError(t, err, http.StatusInternalServerError, "runtime_unavailable")
	if ce.Err == nil || !strings.Contains(ce.Err.Error(), "forced failure") {
		t.Fatalf("internal cause missing: %v", ce.Err)
	}

	sandboxes, _, err := svc.ListSandboxes(ctx, 50, "")
	if err != nil || len(sandboxes) != 1 {
		t.Fatalf("sandboxes = %+v, %v", sandboxes, err)
	}
	sb := sandboxes[0]
	if sb.Status != domain.SandboxFailed {
		t.Fatalf("status = %s", sb.Status)
	}
	if sb.LastError == nil || sb.TokenHash != nil {
		t.Fatalf("failed sandbox leaked token or lost error: %+v", sb)
	}
	wsAfter, err := svc.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if wsAfter.LockedBy != nil {
		t.Fatalf("workspace lock leaked: %v", *wsAfter.LockedBy)
	}

	deadline := time.After(2 * time.Second)
	seenFailed := false
	for !seenFailed {
		select {
		case ev := <-ch:
			if ev.Type == "sandbox.failed" {
				seenFailed = true
			}
		case <-deadline:
			t.Fatalf("sandbox.failed event not published")
		}
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "sandbox.failed")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit events = %d, %v", len(evs), err)
	}

	// The lock was released, so a second attempt reaches the runtime again.
	_, err = svc.CreateSandbox(ctx, CreateSandboxRequest{HarnessID: "shell", WorkspaceID: ws.ID})
	wantError(t, err, http.StatusInternalServerError, "runtime_unavailable")
}

func TestSandboxGetListStats(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb, ws, _ := runningSandbox(t, svc, "shell")
	if err := os.WriteFile(filepath.Join(ws.RootPath, "data.bin"), make([]byte, 128), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := svc.GetSandbox(ctx, sb.ID)
	if err != nil || got.ID != sb.ID {
		t.Fatalf("get = %+v, %v", got, err)
	}
	_, err = svc.GetSandbox(ctx, "sbx_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	items, next, err := svc.ListSandboxes(ctx, 10, "")
	if err != nil || next != "" || len(items) != 1 {
		t.Fatalf("list = %d, %q, %v", len(items), next, err)
	}
	if _, _, err := svc.ListSandboxes(ctx, 10, "%%%"); err == nil {
		t.Fatalf("bad cursor accepted")
	}

	stats, err := svc.Stats(ctx, sb.ID)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.PID != sb.PID || stats.DiskBytes < 128 || stats.UptimeMS < 0 {
		t.Fatalf("stats = %+v", stats)
	}
	// Uptime is derived from StartedAt.
	earlier := svc.now().Add(-2 * time.Second)
	if err := svc.Store.UpdateSandbox(ctx, domain.Sandbox{
		ID: sb.ID, Status: domain.SandboxRunning, StartedAt: &earlier,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	stats, err = svc.Stats(ctx, sb.ID)
	if err != nil || stats.UptimeMS < 1500 {
		t.Fatalf("uptime stats = %+v, %v", stats, err)
	}
	_, err = svc.Stats(ctx, "sbx_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestSandboxStopAndDelete(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb, ws, _ := runningSandbox(t, svc, "shell")
	sb.TokenHash = hashToken("sandbox-token")
	if err := svc.Store.UpdateSandbox(ctx, sb); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := svc.Store.LockWorkspace(ctx, ws.ID, sb.ID, "rw"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	svc.Terminal.Open(sb.ID, newFakeSource())
	if err := svc.Store.ReplaceServices(ctx, sb.ID, []domain.Service{{
		ID: "svc_web", SandboxID: sb.ID, Name: "web", Protocol: "http",
		TargetHost: "127.0.0.1", TargetPort: 20123, Status: domain.ServiceReady,
	}}); err != nil {
		t.Fatalf("services: %v", err)
	}
	if _, err := svc.ServiceTicket(ctx, sb.ID, "svc_web"); err != nil {
		t.Fatalf("ticket: %v", err)
	}

	if err := svc.StopSandbox(ctx, sb.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	got, err := svc.Store.GetSandbox(ctx, sb.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.SandboxStopped || got.TokenHash != nil {
		t.Fatalf("not stopped: %+v", got)
	}
	if svc.Terminal.Has(sb.ID) {
		t.Fatalf("terminal still open")
	}
	if wsAfter, err := svc.Store.GetWorkspace(ctx, ws.ID); err != nil || wsAfter.LockedBy != nil {
		t.Fatalf("lock not released: %+v %v", wsAfter, err)
	}
	svc.tunnel.mu.Lock()
	tickets := len(svc.tunnel.tickets)
	sessions := len(svc.tunnel.sessions)
	svc.tunnel.mu.Unlock()
	if tickets != 0 || sessions != 0 {
		t.Fatalf("tunnel state survived stop: %d/%d", tickets, sessions)
	}
	// Idempotent stop.
	if err := svc.StopSandbox(ctx, sb.ID); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if err := svc.StopSandbox(ctx, "sbx_missing"); err == nil {
		t.Fatalf("missing stop succeeded")
	} else {
		wantError(t, err, http.StatusNotFound, "not_found")
	}

	if err := svc.DeleteSandbox(ctx, sb.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.GetSandbox(ctx, sb.ID); err == nil {
		t.Fatalf("deleted sandbox still visible")
	}
	err = svc.DeleteSandbox(ctx, sb.ID)
	wantError(t, err, http.StatusNotFound, "not_found")
	// Workspace and state always survive.
	if _, err := svc.GetWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("workspace removed: %v", err)
	}
}

func TestStopSandboxMissingRuntimeAndServices(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb, _, _ := runningSandbox(t, svc, "shell")

	// A supervisor with no procs still gets removed and clears declarations.
	svc.servicesMu.Lock()
	svc.services[sb.ID] = &serviceSupervisor{sandboxID: sb.ID, procs: map[string]*runningService{}}
	svc.servicesMu.Unlock()
	if err := svc.Store.ReplaceServices(ctx, sb.ID, []domain.Service{{ID: "svc_x", SandboxID: sb.ID, Name: "x"}}); err != nil {
		t.Fatalf("services: %v", err)
	}

	if err := svc.StopSandbox(ctx, sb.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if sup := svc.servicesFor(sb.ID); sup != nil {
		t.Fatalf("supervisor not removed")
	}
	services, err := svc.Store.ListServices(ctx, sb.ID)
	if err != nil || len(services) != 0 {
		t.Fatalf("services not cleared: %+v %v", services, err)
	}
}

func TestTerminalTicket(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "shell", Status: domain.SandboxStopped,
	})
	_, err := svc.TerminalTicket(ctx, sb.ID)
	wantError(t, err, http.StatusConflict, "conflict")

	if err := svc.Store.SetSandboxStatus(ctx, sb.ID, domain.SandboxRunning, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	_, err = svc.TerminalTicket(ctx, sb.ID)
	wantError(t, err, http.StatusConflict, "terminal_not_ready")

	source := newFakeSource()
	svc.Terminal.Open(sb.ID, source)
	ticket, err := svc.TerminalTicket(ctx, sb.ID)
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	if ticket.Ticket == "" || !strings.HasPrefix(ticket.WSURL, "ws://127.0.0.1:49153/") {
		t.Fatalf("ticket = %+v", ticket)
	}
	if !ticket.ExpiresAt.After(svc.now()) {
		t.Fatalf("ticket already expired: %v", ticket.ExpiresAt)
	}
	svc.Terminal.Close(sb.ID)
	_, err = svc.TerminalTicket(ctx, sb.ID)
	wantError(t, err, http.StatusConflict, "terminal_not_ready")

	_, err = svc.TerminalTicket(ctx, "sbx_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestScanGeneratedRuntime(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb, _, st := runningSandbox(t, svc, "shell")
	_, _ = svc.writeGeneratedConfig(st.Path, sb.ID, domain.NetworkFiltered, "", "")

	res, err := svc.Scan(ctx, sb.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !res.OK || res.Hits != 0 || res.Scope != "generated_runtime" || len(res.Checks) != 3 || len(res.Excluded) != 3 {
		t.Fatalf("scan = %+v", res)
	}
	if res.ScannedAt.IsZero() {
		t.Fatalf("scanned_at missing")
	}
	for _, check := range res.Checks {
		if check.Hits != 0 {
			t.Fatalf("unexpected hit: %+v", check)
		}
	}

	// Plant credential-shaped material in the generated config and state files.
	secretFile := filepath.Join(st.Path, "nullkey", "session.env")
	if err := os.WriteFile(secretFile, []byte("GITHUB_TOKEN=ghp_0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	configPath := filepath.Join(st.Path, "nullkey", "config.json")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg = append(cfg, []byte("\nkey=sk-test-planted-abcdefghijklmnop\n")...)
	if err := os.WriteFile(configPath, cfg, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	res, err = svc.Scan(ctx, sb.ID)
	if err != nil {
		t.Fatalf("scan planted: %v", err)
	}
	if res.Hits < 2 {
		t.Fatalf("planted secret not detected: %+v", res)
	}
	if res.Checks[0].Hits == 0 || res.Checks[1].Hits == 0 {
		t.Fatalf("check breakdown = %+v", res.Checks)
	}

	_, err = svc.Scan(ctx, "sbx_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestScanErrorPaths(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")
	missingState := insertState(t, svc, "shell", filepath.Join(t.TempDir(), "does-not-exist"))
	sb := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "shell", WorkspaceID: ws.ID, StateID: missingState.ID, Status: domain.SandboxRunning,
	})
	_, err := svc.Scan(ctx, sb.ID)
	wantError(t, err, http.StatusInternalServerError, "internal")

	// A sandbox that references a state row that does not exist.
	sb2 := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "shell", WorkspaceID: ws.ID, StateID: "st_missing", Status: domain.SandboxRunning,
	})
	_, err = svc.Scan(ctx, sb2.ID)
	wantError(t, err, http.StatusInternalServerError, "internal")

	// A config path that is a directory is not treated as "absent".
	st := mustState(t, svc, "claude", "default")
	if err := os.MkdirAll(filepath.Join(st.Path, "nullkey", "config.json"), 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	sb3 := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "claude", WorkspaceID: ws.ID, StateID: st.ID, Status: domain.SandboxRunning,
	})
	_, err = svc.Scan(ctx, sb3.ID)
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestWriteGeneratedConfig(t *testing.T) {
	svc, _ := newTestService(t)
	statePath := t.TempDir()
	_, _ = svc.writeGeneratedConfig(statePath, "sbx_test", domain.NetworkOpen, "", "")
	raw, err := os.ReadFile(filepath.Join(statePath, "nullkey", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg["sandbox_id"] != "sbx_test" || cfg["network_mode"] != "open" || cfg["token_env"] != "NULLKEY_TOKEN" {
		t.Fatalf("config = %+v", cfg)
	}
	if strings.Contains(string(raw), "token") && strings.Contains(string(raw), "secret") {
		t.Fatalf("unexpected secret material")
	}
	if cfg["gateway"] != "http://127.0.0.1:49153" || cfg["proxy"] != "http://127.0.0.1:49154" {
		t.Fatalf("addresses = %+v", cfg)
	}

	// Failure to create the directory is swallowed: config is best effort.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	_, _ = svc.writeGeneratedConfig(blocker, "sbx_test", domain.NetworkFiltered, "", "")
}

func TestResolveState(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st, err := svc.resolveState(ctx, "", "shell")
	if err != nil {
		t.Fatalf("auto state: %v", err)
	}
	if st.HarnessID != "shell" || st.Name != "sandbox" {
		t.Fatalf("auto state = %+v", st)
	}
	explicit := mustState(t, svc, "shell", "explicit")
	got, err := svc.resolveState(ctx, explicit.ID, "shell")
	if err != nil || got.ID != explicit.ID {
		t.Fatalf("explicit state = %+v, %v", got, err)
	}
	_, err = svc.resolveState(ctx, "st_missing", "shell")
	wantError(t, err, http.StatusNotFound, "not_found")
}
