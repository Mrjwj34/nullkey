package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/artifacts"
	"github.com/Mrjwj34/nullkey/internal/audit"
	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/events"
	"github.com/Mrjwj34/nullkey/internal/secret"
	"github.com/Mrjwj34/nullkey/internal/store/sqlite"
	"github.com/Mrjwj34/nullkey/internal/terminal"
)

// ------------------------------------------------------------------ helpers

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestService builds a fully wired Service over a real SQLite store in a
// temporary home. Mutators run before New so tests can replace adapters.
func newTestService(t *testing.T, mutators ...func(*Options)) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	store, err := sqlite.Open(context.Background(), home)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	log := testLogger()
	opts := Options{
		Home:        home,
		Store:       store,
		Secrets:     secret.NewMemory(),
		Terminal:    terminal.NewHub(log),
		Events:      events.New(64),
		Audit:       audit.New(store, log),
		Artifacts:   artifacts.New(),
		Logger:      log,
		GatewayAddr: "127.0.0.1:49153",
		ProxyAddr:   "127.0.0.1:49154",
		ControlAddr: "127.0.0.1:49152",
		// Keep harness discovery hermetic; tests that need extra paths set this.
		HarnessPathExtra: []string{},
	}
	for _, mutate := range mutators {
		mutate(&opts)
	}
	svc, err := New(opts)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return svc, home
}

func mustWorkspace(t *testing.T, s *Service, name, root, mode string) domain.Workspace {
	t.Helper()
	ws, err := s.CreateWorkspace(context.Background(), name, root, mode)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return ws
}

func mustState(t *testing.T, s *Service, harness, name string) domain.State {
	t.Helper()
	st, err := s.CreateState(context.Background(), harness, name)
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	return st
}

func insertSandbox(t *testing.T, s *Service, sb domain.Sandbox) domain.Sandbox {
	t.Helper()
	if sb.ID == "" {
		sb.ID = newID("sbx_")
	}
	if sb.Status == "" {
		sb.Status = domain.SandboxRunning
	}
	if sb.Runtime == "" {
		sb.Runtime = domain.RuntimeNative
	}
	if sb.NetworkMode == "" {
		sb.NetworkMode = domain.NetworkFiltered
	}
	if sb.CreatedAt.IsZero() {
		sb.CreatedAt = s.now()
	}
	if err := s.Store.CreateSandbox(context.Background(), sb); err != nil {
		t.Fatalf("insert sandbox: %v", err)
	}
	return sb
}

// runningSandbox inserts a workspace, state and running sandbox row without
// launching a runtime. Most lifecycle tests only need a consistent DB row.
func runningSandbox(t *testing.T, s *Service, harness string) (domain.Sandbox, domain.Workspace, domain.State) {
	t.Helper()
	ws := mustWorkspace(t, s, "ws-"+newID(""), "", "")
	st := mustState(t, s, harness, "default")
	started := s.now()
	sb := insertSandbox(t, s, domain.Sandbox{
		HarnessID:   harness,
		WorkspaceID: ws.ID,
		StateID:     st.ID,
		Status:      domain.SandboxRunning,
		StartedAt:   &started,
	})
	return sb, ws, st
}

func wantError(t *testing.T, err error, status int, code string) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	ce, ok := AsError(err)
	if !ok {
		t.Fatalf("expected *core.Error, got %T: %v", err, err)
	}
	if ce.Status != status || ce.Code != code {
		t.Fatalf("expected (%d, %s), got (%d, %s): %v", status, code, ce.Status, ce.Code, err)
	}
	return ce
}

// fakeSource is a terminal.Source that blocks on Read until closed.
type fakeSource struct {
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	cols   int
	rows   int
	output bytes.Buffer
}

func newFakeSource() *fakeSource {
	return &fakeSource{closed: make(chan struct{}), cols: 120, rows: 40}
}

func (f *fakeSource) Read(p []byte) (int, error) {
	<-f.closed
	return 0, io.EOF
}

func (f *fakeSource) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output.Write(p)
}

func (f *fakeSource) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeSource) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols, f.rows = cols, rows
	return nil
}

func (f *fakeSource) Size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows
}

func (f *fakeSource) Done() <-chan struct{} { return f.closed }
func (f *fakeSource) ExitCode() int         { return 0 }

// ------------------------------------------------------------------- New

func TestNewValidation(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if _, err := New(Options{Secrets: secret.NewMemory()}); err == nil || !strings.Contains(err.Error(), "store is required") {
		t.Fatalf("expected store requirement, got %v", err)
	}
	if _, err := New(Options{Store: store}); err == nil || !strings.Contains(err.Error(), "secret store is required") {
		t.Fatalf("expected secret requirement, got %v", err)
	}
}

func TestNewDefaults(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	store, err := sqlite.Open(ctx, home)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc, err := New(Options{Home: home, Store: store, Secrets: secret.NewMemory()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if svc.Log == nil || svc.Events == nil || svc.Audit == nil || svc.HTTP == nil {
		t.Fatalf("defaults not applied: %+v", svc)
	}
	if svc.now == nil {
		t.Fatalf("now not installed")
	}
	if got := svc.now(); got.Location() != time.UTC {
		t.Fatalf("default now is not UTC: %v", got.Location())
	}
	hostHome, _ := os.UserHomeDir()
	if svc.HostImportDir != hostHome {
		t.Fatalf("host import dir = %q, want %q", svc.HostImportDir, hostHome)
	}
	if svc.jobs == nil || svc.services == nil {
		t.Fatalf("maps not initialized")
	}
	if svc.oauth == nil || svc.tunnel == nil {
		t.Fatalf("brokers not initialized")
	}
	transport, ok := svc.HTTP.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || !transport.DisableCompression {
		t.Fatalf("unexpected default gateway transport: %#v", svc.HTTP.Transport)
	}

	fixed := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	custom, err := New(Options{
		Home:           home + "-custom",
		Store:          store,
		Secrets:        secret.NewMemory(),
		Now:            func() time.Time { return fixed },
		HostImportRoot: home,
	})
	if err != nil {
		t.Fatalf("new custom: %v", err)
	}
	if !custom.now().Equal(fixed) {
		t.Fatalf("now not honored: %v", custom.now())
	}
	if custom.HostImportDir != home {
		t.Fatalf("host import override ignored: %q", custom.HostImportDir)
	}
}

// --------------------------------------------------------------- Reconcile

func TestReconcileStopsActiveSandboxes(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb, ws, _ := runningSandbox(t, svc, "shell")
	sb.TokenHash = hashToken("tok-reconcile")
	started := svc.now()
	sb.StartedAt = &started
	if err := svc.Store.UpdateSandbox(ctx, sb); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	if err := svc.Store.LockWorkspace(ctx, ws.ID, sb.ID, "rw"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	svc.Terminal.Open(sb.ID, newFakeSource())

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, err := svc.Store.GetSandbox(ctx, sb.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != domain.SandboxStopped || got.TokenHash != nil || got.PID != 0 {
		t.Fatalf("sandbox not reconciled: %+v", got)
	}
	if svc.Terminal.Has(sb.ID) {
		t.Fatalf("terminal not closed")
	}
	locked, err := svc.Store.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if locked.LockedBy != nil {
		t.Fatalf("workspace lock not released: %v", *locked.LockedBy)
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "sandbox.stopped")
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(evs) == 0 || evs[0].Detail["reason"] != "daemon_restart" {
		t.Fatalf("missing reconcile audit: %+v", evs)
	}
}

func TestReconcileStoreError(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := svc.Reconcile(context.Background()); err == nil {
		t.Fatalf("expected store error")
	}
}

func TestShutdownStopsRunningSandboxes(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	running, _, _ := runningSandbox(t, svc, "shell")
	stopped := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "shell", WorkspaceID: running.WorkspaceID, StateID: running.StateID,
		Status: domain.SandboxStopped,
	})
	svc.Terminal.Open(running.ID, newFakeSource())

	svc.Shutdown(ctx)

	got, err := svc.Store.GetSandbox(ctx, running.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != domain.SandboxStopped {
		t.Fatalf("running sandbox not stopped: %s", got.Status)
	}
	if svc.Terminal.Has(running.ID) {
		t.Fatalf("terminal not closed on shutdown")
	}
	still, err := svc.Store.GetSandbox(ctx, stopped.ID)
	if err != nil || still.Status != domain.SandboxStopped {
		t.Fatalf("stopped sandbox disturbed: %+v %v", still, err)
	}
}

// ------------------------------------------------------- token resolution

func TestResolveProxyTokenAndSandboxByToken(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")
	st := mustState(t, svc, "shell", "default")
	token := "sandbox-token-abcdef"
	sb := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "shell", WorkspaceID: ws.ID, StateID: st.ID,
		NetworkMode: domain.NetworkServicesOnly, TokenHash: hashToken(token),
	})

	id, mode, ok := svc.ResolveProxyToken(token)
	if !ok || id != sb.ID || mode != domain.NetworkServicesOnly {
		t.Fatalf("resolve = (%q,%q,%v), want (%q,%q,true)", id, mode, ok, sb.ID, domain.NetworkServicesOnly)
	}
	if _, _, ok := svc.ResolveProxyToken(""); ok {
		t.Fatalf("empty token resolved")
	}
	if _, _, ok := svc.ResolveProxyToken("nope"); ok {
		t.Fatalf("unknown token resolved")
	}

	got, ok := svc.SandboxByToken(ctx, token)
	if !ok || got.ID != sb.ID {
		t.Fatalf("SandboxByToken = %+v,%v", got, ok)
	}
	if _, ok := svc.SandboxByToken(ctx, ""); ok {
		t.Fatalf("empty token accepted")
	}
	if _, ok := svc.SandboxByToken(ctx, "nope"); ok {
		t.Fatalf("unknown token accepted")
	}

	if err := svc.Store.SetSandboxStatus(ctx, sb.ID, domain.SandboxStopped, nil); err != nil {
		t.Fatalf("stop row: %v", err)
	}
	if _, _, ok := svc.ResolveProxyToken(token); ok {
		t.Fatalf("stopped sandbox token resolved")
	}
}

// ------------------------------------------------------------- event/audit

func TestPublishEvent(t *testing.T) {
	svc, _ := newTestService(t)
	ch, cancel, err := svc.Events.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()
	svc.PublishEvent("sandbox.progress", map[string]any{"sandbox_id": "sbx_x"})
	select {
	case ev := <-ch:
		if ev.Type != "sandbox.progress" || ev.Data["sandbox_id"] != "sbx_x" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatalf("event not published")
	}
}

func TestRecordNetworkBlocked(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.RecordNetworkBlocked("sbx_abc", "example.test")
	svc.RecordNetworkBlocked("", "other.test")
	evs, _, err := svc.Audit.List(ctx, 50, "", "network.blocked")
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(evs))
	}
	foundWithID := false
	for _, ev := range evs {
		if ev.SandboxID != nil && *ev.SandboxID == "sbx_abc" {
			foundWithID = true
		}
		if ev.Detail["host"] == "" {
			t.Fatalf("host detail missing: %+v", ev)
		}
	}
	if !foundWithID {
		t.Fatalf("sandbox id not recorded: %+v", evs)
	}
}

// ------------------------------------------------------------ idempotency

func TestIdempotencyLookupStoreMismatch(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, ok, err := svc.LookupIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", []byte(`{"name":"a"}`)); err != nil || ok {
		t.Fatalf("unexpected initial lookup: ok=%v err=%v", ok, err)
	}
	svc.StoreIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", []byte(`{"name":"a"}`), 201, []byte(`{"id":"ws_1"}`))
	resp, ok, err := svc.LookupIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", []byte(`{"name":"a"}`))
	if err != nil || !ok {
		t.Fatalf("lookup after store: ok=%v err=%v", ok, err)
	}
	if resp.Status != 201 || string(resp.Body) != `{"id":"ws_1"}` {
		t.Fatalf("unexpected response: %+v", resp)
	}
	_, ok, err = svc.LookupIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", []byte(`{"name":"b"}`))
	if ok {
		t.Fatalf("mismatched body accepted")
	}
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	// A store failure must not be reported as a miss.
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, _, err := svc.LookupIdempotent(ctx, "key-1", "POST", "/api/v1/workspaces", []byte(`{"name":"a"}`)); err == nil {
		t.Fatalf("expected error for closed store")
	}
	svc.StoreIdempotent(ctx, "key-2", "POST", "/x", nil, 200, nil) // must not panic
}

// ---------------------------------------------------------------- helpers

func TestPtrAndHashToken(t *testing.T) {
	if got := ptr("abc"); got == nil || *got != "abc" {
		t.Fatalf("ptr = %v", got)
	}
	sum := sha256.Sum256([]byte("token"))
	if got := hashToken("token"); !bytes.Equal(got, sum[:]) {
		t.Fatalf("hashToken mismatch")
	}
}

func TestNewIDAndReadRandom(t *testing.T) {
	if id := newID("sbx_"); !strings.HasPrefix(id, "sbx_") || len(id) <= len("sbx_") {
		t.Fatalf("newID = %q", id)
	}
	buf := make([]byte, 16)
	n, err := readRandom(buf)
	if err != nil || n != len(buf) {
		t.Fatalf("readRandom = %d, %v", n, err)
	}
	if bytes.Equal(buf, make([]byte, 16)) {
		t.Fatalf("random bytes look zeroed")
	}
}

func TestErrorHelpers(t *testing.T) {
	base := E(http.StatusConflict, "conflict", "boom")
	if base.Status != http.StatusConflict || base.Error() != "conflict: boom" {
		t.Fatalf("unexpected error: %+v", base)
	}
	cause := errors.New("cause")
	wrapped := Wrap(http.StatusInternalServerError, "internal", "wrapped", cause)
	if !strings.Contains(wrapped.Error(), "cause") || !errors.Is(wrapped, cause) {
		t.Fatalf("wrap lost cause: %v", wrapped)
	}
	detail := wrapped.WithDetails(map[string]any{"field": "x"})
	if detail.Details["field"] != "x" {
		t.Fatalf("details not attached")
	}
	if got := Invalid("bad"); got.Status != http.StatusBadRequest || got.Code != "invalid_request" {
		t.Fatalf("invalid = %+v", got)
	}
	if got := NotFound("gone"); got.Status != http.StatusNotFound || got.Code != "not_found" {
		t.Fatalf("notfound = %+v", got)
	}
	if got := Conflict("state_locked", "locked"); got.Status != http.StatusConflict || got.Code != "state_locked" {
		t.Fatalf("conflict = %+v", got)
	}
	if ce, ok := AsError(wrapped); !ok || ce != wrapped {
		t.Fatalf("AsError failed")
	}
	if _, ok := AsError(cause); ok {
		t.Fatalf("plain error reported as core error")
	}
}

// ---------------------------------------------------------------- catalogs

func TestListHarnessesAndInstalled(t *testing.T) {
	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	svc, home := newTestService(t)

	items := svc.ListHarnesses()
	if len(items) != len(harnessCatalog) {
		t.Fatalf("catalog size = %d", len(items))
	}
	for _, item := range items {
		if item.ID == "shell" && !item.Installed {
			t.Fatalf("shell must always be installed")
		}
		if item.ID != "shell" && item.Installed {
			t.Fatalf("%s unexpectedly installed", item.ID)
		}
	}

	markerDir := filepath.Join(home, "harnesses", "codex")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "installed.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	for _, item := range svc.ListHarnesses() {
		if item.ID == "codex" && item.Installed {
			t.Fatalf("marker alone must not report a missing executable as installed")
		}
	}
	if !svc.harnessInstalled("shell") {
		t.Fatalf("shell not installed")
	}
	if svc.harnessInstalled("codex") {
		t.Fatalf("codex marker must not substitute for an executable")
	}

	// claude is discovered via PATH.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	t.Setenv("PATH", binDir)
	if !svc.harnessInstalled("claude") {
		t.Fatalf("PATH probe failed")
	}
	t.Setenv("PATH", emptyPath)
	if svc.harnessInstalled("claude") {
		t.Fatalf("claude should not be installed with empty PATH")
	}
}

func TestHarnessCommand(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	svc, home := newTestService(t)

	cmd, _, err := svc.harnessCommand("")
	if err != nil || len(cmd) != 1 || cmd[0] != "/bin/sh" {
		t.Fatalf("empty harness = %v, %v", cmd, err)
	}
	if cmd, _, err = svc.harnessCommand("shell"); err != nil || cmd[0] != "/bin/sh" {
		t.Fatalf("shell = %v, %v", cmd, err)
	}

	_, _, err = svc.harnessCommand("codex")
	wantError(t, err, http.StatusConflict, "harness_not_installed")

	// Marker present but the binary is missing: distinct error path.
	if err := os.MkdirAll(filepath.Join(home, "harnesses", "codex"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "harnesses", "codex", "installed.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	_, _, err = svc.harnessCommand("codex")
	wantError(t, err, http.StatusConflict, "harness_not_installed")

	bin := filepath.Join(binDir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	cmd, _, err = svc.harnessCommand("codex")
	resolved, _ := filepath.EvalSymlinks(bin)
	if err != nil || len(cmd) != 2 || cmd[0] != resolved || cmd[1] != "--no-daemon" {
		t.Fatalf("codex command = %v, %v", cmd, err)
	}
}

func TestHarnessPathExtraFindsPerUserInstall(t *testing.T) {
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	t.Setenv("PATH", t.TempDir()) // GUI/Finder PATH: no per-user bin
	svc, _ := newTestService(t, func(o *Options) {
		o.HarnessPathExtra = []string{binDir}
	})
	if !svc.harnessInstalled("codex") {
		t.Fatal("codex in an extra harness path was not found")
	}
	cmd, _, err := svc.harnessCommand("codex")
	resolved, _ := filepath.EvalSymlinks(bin)
	if err != nil || len(cmd) != 2 || cmd[0] != resolved || cmd[1] != "--no-daemon" {
		t.Fatalf("codex command = %v, %v", cmd, err)
	}
}

func TestDefaultHarnessPathExtraIncludesUserBins(t *testing.T) {
	dirs := defaultHarnessPathExtra("/Users/tester")
	joined := strings.Join(dirs, string(os.PathListSeparator))
	for _, want := range []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/Users/tester/.local/bin",
		"/Users/tester/.npm-global/bin",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("default harness path missing %q: %v", want, dirs)
		}
	}
}

func TestListAppsAndProviders(t *testing.T) {
	svc, _ := newTestService(t)
	if apps := svc.ListApps(); apps == nil || len(apps) != 0 {
		t.Fatalf("apps = %#v", apps)
	}
	providers := svc.Providers()
	if len(providers) != len(providerRegistry) {
		t.Fatalf("providers = %d", len(providers))
	}
	providers[0].Name = "mutated"
	if providerRegistry[0].Name == "mutated" {
		t.Fatalf("Providers did not copy")
	}
	if p, ok := providerByID("anthropic"); !ok || p.ID != "anthropic" {
		t.Fatalf("providerByID failed")
	}
	if _, ok := providerByID("missing"); ok {
		t.Fatalf("unknown provider resolved")
	}
}

func TestOAuthConfigAndProviders(t *testing.T) {
	svc, _ := newTestService(t)
	for _, key := range []string{
		"NULLKEY_OAUTH_CODEX_AUTH_URL", "NULLKEY_OAUTH_CODEX_TOKEN_URL",
		"NULLKEY_OAUTH_CODEX_CLIENT_ID", "NULLKEY_OAUTH_CODEX_CLIENT_SECRET",
		"NULLKEY_OAUTH_CODEX_SCOPES",
		"NULLKEY_OAUTH_GITHUB_CLIENT_ID",
	} {
		t.Setenv(key, "")
	}

	if got := svc.oauthConfig("codex"); got.authURL == "" || got.tokenURL == "" || len(got.scopes) == 0 {
		t.Fatalf("codex defaults missing: %+v", got)
	}
	if got := svc.oauthConfig("github"); got.authURL == "" || got.tokenURL == "" || len(got.scopes) == 0 {
		t.Fatalf("github defaults missing: %+v", got)
	}
	if got := svc.oauthConfig("unknown"); got.authURL != "" || got.tokenURL != "" || len(got.scopes) != 0 {
		t.Fatalf("unknown provider defaults: %+v", got)
	}
	if svc.oauthConfig("codex").available() {
		t.Fatalf("codex should be unavailable without client id")
	}

	providers := svc.OAuthProviders()
	if len(providers) != 2 || providers[0].Status != "unavailable" || providers[1].Status != "unavailable" {
		t.Fatalf("unexpected providers: %+v", providers)
	}

	t.Setenv("NULLKEY_OAUTH_CODEX_CLIENT_ID", "client-1")
	t.Setenv("NULLKEY_OAUTH_CODEX_AUTH_URL", "https://auth.example.test/authorize")
	t.Setenv("NULLKEY_OAUTH_CODEX_TOKEN_URL", "https://auth.example.test/token")
	t.Setenv("NULLKEY_OAUTH_CODEX_CLIENT_SECRET", "shh")
	t.Setenv("NULLKEY_OAUTH_CODEX_SCOPES", "openid profile")
	cfg := svc.oauthConfig("codex")
	if !cfg.available() || cfg.clientID != "client-1" || cfg.clientSecret != "shh" {
		t.Fatalf("env override ignored: %+v", cfg)
	}
	if len(cfg.scopes) != 2 {
		t.Fatalf("scopes = %v", cfg.scopes)
	}
	providers = svc.OAuthProviders()
	if providers[0].Status != "available" || len(providers[0].Scopes) != 2 {
		t.Fatalf("codex not available: %+v", providers[0])
	}
	if providers[1].Status != "unavailable" {
		t.Fatalf("github should remain unavailable: %+v", providers[1])
	}
}

func TestMCPCatalog(t *testing.T) {
	svc, _ := newTestService(t)
	catalog := svc.MCPCatalog()
	if len(catalog) != 2 {
		t.Fatalf("mcp catalog = %+v", catalog)
	}
	for _, item := range catalog {
		if item.ID == "github" && !item.RequiresOAuthApp {
			t.Fatalf("github should require an oauth app")
		}
	}
	if item, ok := mcpServerByID("filesystem"); !ok || item.Name != "Filesystem" {
		t.Fatalf("mcpServerByID filesystem failed")
	}
	if _, ok := mcpServerByID("nope"); ok {
		t.Fatalf("unknown MCP server resolved")
	}
}

func TestDirSizeAndSortedUnique(t *testing.T) {
	dir := t.TempDir()
	if got := dirSize(dir); got != 0 {
		t.Fatalf("empty dir size = %d", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.txt"), []byte("1234567890"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := dirSize(dir); got != 15 {
		t.Fatalf("dir size = %d, want 15", got)
	}
	if got := dirSize(filepath.Join(dir, "does-not-exist")); got != 0 {
		t.Fatalf("missing dir size = %d", got)
	}

	got := sortedUnique([]string{"b", "a", "b", "", "c", "a"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("sortedUnique = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedUnique = %v", got)
		}
	}
	if empty := sortedUnique(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("sortedUnique(nil) = %#v", empty)
	}
}

func TestDecodeEncodeOAuthSecret(t *testing.T) {
	if access, refresh, expires := decodeSecret([]byte("sk-test-raw")); access != "sk-test-raw" || refresh != "" || !expires.IsZero() {
		t.Fatalf("raw decode = %q %q %v", access, refresh, expires)
	}
	bundle, err := json.Marshal(map[string]any{
		"access_token":  "access",
		"refresh_token": "refresh",
		"expires_at":    "2025-01-02T03:04:05Z",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	access, refresh, expires := decodeSecret(bundle)
	if access != "access" || refresh != "refresh" || expires.Format(time.RFC3339) != "2025-01-02T03:04:05Z" {
		t.Fatalf("bundle decode = %q %q %v", access, refresh, expires)
	}
	refreshOnly, _ := json.Marshal(map[string]any{"refresh_token": "r"})
	if a, r, _ := decodeSecret(refreshOnly); a != "" || r != "r" {
		t.Fatalf("refresh-only decode = %q %q", a, r)
	}
	if access, _, _ := decodeSecret([]byte("not-json")); access != "not-json" {
		t.Fatalf("invalid json should fall back to raw")
	}

	when := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	encoded := encodeOAuthSecret("a", "r", when)
	access, refresh, expires = decodeSecret(encoded)
	if access != "a" || refresh != "r" || !expires.Equal(when) {
		t.Fatalf("roundtrip = %q %q %v", access, refresh, expires)
	}
}

func TestFingerprint(t *testing.T) {
	if got := fingerprint(""); got != "" {
		t.Fatalf("empty fingerprint = %q", got)
	}
	if got := fingerprint("abc"); !strings.HasPrefix(got, "sha256:") || strings.Contains(got, "…") {
		t.Fatalf("short fingerprint = %q", got)
	}
	if got := fingerprint("sk-test-abcd"); !strings.HasSuffix(got, "…abcd") {
		t.Fatalf("long fingerprint = %q", got)
	}
}
