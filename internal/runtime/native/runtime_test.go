package native

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestRuntime(t *testing.T, cfg Config) *Runtime {
	t.Helper()
	if cfg.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatalf("os.Executable: %v", err)
		}
		cfg.Executable = exe
	}
	if cfg.Home == "" {
		cfg.Home = t.TempDir()
	}
	if cfg.HostHome == "" {
		cfg.HostHome = "/Users/nullkey-test-host"
	}
	if cfg.GatewayAddr == "" {
		cfg.GatewayAddr = "127.0.0.1:49153"
	}
	if cfg.ProxyAddr == "" {
		cfg.ProxyAddr = "127.0.0.1:49154"
	}
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func newSpec(t *testing.T, id string) Spec {
	t.Helper()
	return Spec{
		SandboxID:     id,
		HarnessID:     "shell",
		WorkspacePath: t.TempDir(),
		StatePath:     t.TempDir(),
		CachePath:     t.TempDir(),
		Command:       []string{"/bin/sh"},
		Token:         "sk-test-sandbox-token",
		NetworkMode:   domain.NetworkFiltered,
		Cols:          80,
		Rows:          24,
		ExtraEnv:      []string{"NULLKEY_TEST_EXTRA=ok"},
	}
}

func writeFakeExecutable(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-nullkeyd")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatalf("write fake executable: %v", err)
	}
	return path
}

func waitForExit(t *testing.T, p *Process, timeout time.Duration) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(timeout):
		_ = p.Stop(context.Background())
		t.Fatalf("process %d did not exit within %s", p.PID, timeout)
	}
}

// readUntil reads PTY output until marker appears, returning everything read.
func readUntil(t *testing.T, p *Process, marker string, timeout time.Duration) string {
	t.Helper()
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 4096)
		var out strings.Builder
		for {
			n, err := p.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
				if strings.Contains(out.String(), marker) {
					ch <- result{out: out.String()}
					return
				}
			}
			if err != nil {
				ch <- result{out: out.String(), err: err}
				return
			}
		}
	}()
	select {
	case res := <-ch:
		if !strings.Contains(res.out, marker) {
			t.Fatalf("PTY output did not contain %q: %q (err=%v)", marker, res.out, res.err)
		}
		return res.out
	case <-time.After(timeout):
		_ = p.Close()
		t.Fatalf("timed out waiting for PTY output %q", marker)
		return ""
	}
}

// pollGone waits until pid is no longer signalable. Processes become zombies
// until Wait reaps them, so this polls rather than checking once.
func pollGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d is still alive after %s", pid, timeout)
}

func TestNewDefaultsAndOverrides(t *testing.T) {
	r, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.cfg.Executable == "" {
		t.Fatal("New did not resolve a default executable")
	}
	if r.log == nil {
		t.Fatal("New did not install a default logger")
	}
	if r.cfg.ReadyTimeout != 8*time.Second {
		t.Fatalf("ReadyTimeout = %s, want 8s", r.cfg.ReadyTimeout)
	}
	if r.procs == nil {
		t.Fatal("procs map is nil")
	}

	logger := discardLogger()
	r, err = New(Config{
		Executable:   "/bin/sh",
		Home:         "/tmp/nk-home",
		HostHome:     "/Users/alice",
		GatewayAddr:  "127.0.0.1:1",
		ProxyAddr:    "127.0.0.1:2",
		Logger:       logger,
		ReadyTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.cfg.Executable != "/bin/sh" || r.cfg.Home != "/tmp/nk-home" ||
		r.cfg.HostHome != "/Users/alice" || r.cfg.ReadyTimeout != 3*time.Second ||
		r.cfg.GatewayAddr != "127.0.0.1:1" || r.cfg.ProxyAddr != "127.0.0.1:2" {
		t.Fatalf("New did not preserve explicit config: %+v", r.cfg)
	}
	if r.log != logger {
		t.Fatal("New did not use the provided logger")
	}
}

func TestAvailableMatchesBuildTag(t *testing.T) {
	if got, want := Available(), sandboxAvailable(); got != want {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH != "" {
		// On the supported host with cgo this must be true; on a darwin build
		// without cgo the build-tag helper is false and this is skipped.
		if sandboxAvailable() && !Available() {
			t.Fatal("Available() must report Seatbelt support")
		}
	}
}

func TestGetStopUnknownReturnsErrNotRunning(t *testing.T) {
	r := newTestRuntime(t, Config{})
	if p, ok := r.Get("sbx_missing"); ok || p != nil {
		t.Fatalf("Get(unknown) = %v, %v", p, ok)
	}
	if err := r.Stop(context.Background(), "sbx_missing"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stop(unknown) = %v, want ErrNotRunning", err)
	}
}

func TestStartNonDarwinReturnsUnsupported(t *testing.T) {
	if runtime.GOOS == "darwin" && sandboxAvailable() {
		t.Skip("Seatbelt is available on this host")
	}
	r := newTestRuntime(t, Config{Executable: os.Args[0]})
	if _, err := r.Start(context.Background(), Spec{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Start on unsupported host = %v, want ErrUnsupported", err)
	}
}

func TestStartValidatesSpec(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	base := newSpec(t, "sbx_validate")

	cases := []struct {
		name   string
		mutate func(*Spec)
		want   string
	}{
		{"empty command", func(s *Spec) { s.Command = nil }, "empty harness command"},
		{"missing sandbox id", func(s *Spec) { s.SandboxID = "" }, "sandbox, workspace and state paths are required"},
		{"missing workspace", func(s *Spec) { s.WorkspacePath = "" }, "sandbox, workspace and state paths are required"},
		{"missing state", func(s *Spec) { s.StatePath = "" }, "sandbox, workspace and state paths are required"},
		{"missing token", func(s *Spec) { s.Token = "" }, "sandbox token is required"},
		{"empty cache path", func(s *Spec) { s.CachePath = "" }, "create cache dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			tc.mutate(&spec)
			p, err := r.Start(context.Background(), spec)
			if err == nil {
				_ = p.Stop(context.Background())
				t.Fatalf("Start accepted invalid spec %+v", spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}

	t.Run("state path is a file", func(t *testing.T) {
		spec := base
		file := filepath.Join(t.TempDir(), "state-file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		spec.StatePath = file
		if _, err := r.Start(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "create state dir") {
			t.Fatalf("Start error = %v, want create state dir", err)
		}
	})

	t.Run("cache path is a file", func(t *testing.T) {
		spec := base
		file := filepath.Join(t.TempDir(), "cache-file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		spec.CachePath = file
		if _, err := r.Start(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "create cache dir") {
			t.Fatalf("Start error = %v, want create cache dir", err)
		}
	})
}

func TestStartForcedFailureHook(t *testing.T) {
	requireNativeHost(t)
	t.Setenv("NULLKEY_FORCE_RUNTIME_FAILURE", "1")
	r := newTestRuntime(t, Config{})
	p, err := r.Start(context.Background(), newSpec(t, "sbx_forced"))
	if err == nil {
		_ = p.Stop(context.Background())
		t.Fatal("Start succeeded despite NULLKEY_FORCE_RUNTIME_FAILURE=1")
	}
	if !strings.Contains(err.Error(), "forced failure test hook") {
		t.Fatalf("error = %v, want forced failure hook", err)
	}
}

func TestStartFakeHelperFailures(t *testing.T) {
	requireNativeHost(t)

	t.Run("pty start error", func(t *testing.T) {
		r := newTestRuntime(t, Config{Executable: "/nonexistent-nullkeyd-binary"})
		_, err := r.Start(context.Background(), newSpec(t, "sbx_nopt"))
		if err == nil || !strings.Contains(err.Error(), "start pty") {
			t.Fatalf("error = %v, want start pty", err)
		}
	})

	t.Run("helper exits before readiness", func(t *testing.T) {
		exe := writeFakeExecutable(t, "exit 0")
		r := newTestRuntime(t, Config{Executable: exe, ReadyTimeout: 2 * time.Second})
		_, err := r.Start(context.Background(), newSpec(t, "sbx_early"))
		if err == nil || !strings.Contains(err.Error(), "exited before readiness") {
			t.Fatalf("error = %v, want exited before readiness", err)
		}
	})

	t.Run("helper error message", func(t *testing.T) {
		exe := writeFakeExecutable(t, `printf 'ERR:boom' >&3; exit 1`)
		r := newTestRuntime(t, Config{Executable: exe, ReadyTimeout: 2 * time.Second})
		_, err := r.Start(context.Background(), newSpec(t, "sbx_err"))
		if err == nil || !strings.Contains(err.Error(), "sandbox profile failed: boom") {
			t.Fatalf("error = %v, want propagated helper error", err)
		}
	})

	t.Run("readiness timeout", func(t *testing.T) {
		exe := writeFakeExecutable(t, "sleep 30")
		r := newTestRuntime(t, Config{Executable: exe, ReadyTimeout: 100 * time.Millisecond})
		start := time.Now()
		_, err := r.Start(context.Background(), newSpec(t, "sbx_timeout"))
		if err == nil || !strings.Contains(err.Error(), "did not become ready") {
			t.Fatalf("error = %v, want readiness timeout", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("timeout path took %s", elapsed)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		exe := writeFakeExecutable(t, "sleep 30")
		r := newTestRuntime(t, Config{Executable: exe, ReadyTimeout: 10 * time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.Start(ctx, newSpec(t, "sbx_ctx"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func TestStartRealCommandFailsWhenWorkspaceMissing(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_badcwd")
	spec.WorkspacePath = filepath.Join(t.TempDir(), "missing-workspace")
	_, err := r.Start(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "sandbox profile failed") {
		t.Fatalf("error = %v, want sandbox profile failure from chdir", err)
	}
	if !strings.Contains(err.Error(), "chdir") {
		t.Fatalf("error = %v, want chdir detail", err)
	}
}

func TestStartLifecyclePTY(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_lifecycle")
	spec.Cols = 0
	spec.Rows = 0

	p, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.ID != spec.SandboxID {
		t.Fatalf("ID = %q, want %q", p.ID, spec.SandboxID)
	}
	if p.PID <= 0 {
		t.Fatalf("PID = %d, want > 0", p.PID)
	}
	if !strings.HasPrefix(p.TerminalID, nkid.Terminal) {
		t.Fatalf("TerminalID = %q, want prefix %q", p.TerminalID, nkid.Terminal)
	}
	if p.StartedAt.IsZero() {
		t.Fatal("StartedAt is zero")
	}
	if p.StartupMS < 0 {
		t.Fatalf("StartupMS = %d", p.StartupMS)
	}
	if cols, rows := p.Size(); cols != 120 || rows != 40 {
		t.Fatalf("default Size = %d,%d, want 120,40", cols, rows)
	}
	if got := p.StatePath(); got != spec.StatePath {
		t.Fatalf("StatePath = %q, want %q", got, spec.StatePath)
	}
	if got, ok := r.Get(spec.SandboxID); !ok || got != p {
		t.Fatalf("Get(%q) = %v,%v", spec.SandboxID, got, ok)
	}

	if _, err := p.Write([]byte("printf 'NK_%s\\n' READY\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := readUntil(t, p, "NK_READY", 10*time.Second)
	if !strings.Contains(out, "NK_READY") {
		t.Fatalf("output = %q", out)
	}

	if err := p.Resize(100, 50); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if cols, rows := p.Size(); cols != 100 || rows != 50 {
		t.Fatalf("Size after Resize = %d,%d, want 100,50", cols, rows)
	}
	if err := p.Resize(0, 50); err == nil {
		t.Fatal("Resize accepted zero cols")
	}
	if err := p.Resize(100, -1); err == nil {
		t.Fatal("Resize accepted negative rows")
	}

	stats, err := p.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.PID != p.PID {
		t.Fatalf("Stats.PID = %d, want %d", stats.PID, p.PID)
	}
	if stats.MemoryBytes <= 0 {
		t.Fatalf("Stats.MemoryBytes = %d, want > 0", stats.MemoryBytes)
	}
	if stats.Uptime <= 0 {
		t.Fatalf("Stats.Uptime = %s, want > 0", stats.Uptime)
	}

	// Interactive shells ignore SIGTERM and, on macOS, the child's wait(2)
	// only completes once the PTY master is closed. Close the master first so
	// this test exercises the normal stop path instead of the 5s escalation.
	if err := p.Close(); err != nil {
		t.Fatalf("Close PTY: %v", err)
	}
	if err := r.Stop(context.Background(), spec.SandboxID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	pollGone(t, p.PID, 5*time.Second)
	if _, ok := r.Get(spec.SandboxID); ok {
		t.Fatal("process still registered after Stop")
	}
	if err := r.Stop(context.Background(), spec.SandboxID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second Stop = %v, want ErrNotRunning", err)
	}
}

func TestHarnessInstallationIsReadOnly(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_harness_readonly")
	spec.HarnessRoot = t.TempDir()
	installed := filepath.Join(spec.HarnessRoot, "agent.js")
	if err := os.WriteFile(installed, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	command := "cat '" + installed + "'; echo modified > '" + installed + "'; printf 'HARNESS_%s\\n' done\n"
	if _, err := p.Write([]byte(command)); err != nil {
		t.Fatal(err)
	}
	output := readUntil(t, p, "HARNESS_done", 5*time.Second)
	if !strings.Contains(output, "original\r\n") {
		t.Fatalf("installed harness must remain readable: %s", output)
	}
	if data, err := os.ReadFile(installed); err != nil || string(data) != "original\n" {
		t.Fatalf("sandbox modified shared harness installation: %q %v", data, err)
	}
}

func TestStartExitCodeAndStatsAfterExit(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_exit")
	spec.Command = []string{"/bin/sh", "-c", "printf 'NK_EXIT_MARKER\\n'; exit 7"}

	p, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	readUntil(t, p, "NK_EXIT_MARKER", 10*time.Second)
	waitForExit(t, p, 10*time.Second)
	if code := p.ExitCode(); code != 7 {
		t.Fatalf("ExitCode = %d, want 7", code)
	}
	stats, err := p.Stats()
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stats after exit = %v (stats=%+v), want ErrNotRunning", err, stats)
	}
	// Stopping an already-exited process is a no-op and must not hang.
	if err := r.Stop(context.Background(), spec.SandboxID); err != nil {
		t.Fatalf("Stop after natural exit: %v", err)
	}
}

func TestSandboxEnvironmentInjected(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{GatewayAddr: "127.0.0.1:49153", ProxyAddr: "127.0.0.1:49154"})
	spec := newSpec(t, "sbx_env")
	spec.Command = []string{"/bin/sh", "-c", `printf 'ENV:%s|%s|%s|%s|%s|%s\n' "$NULLKEY_TOKEN" "$NULLKEY_GATEWAY" "$NULLKEY_PROXY" "$NULLKEY_SANDBOX_ID" "$NULLKEY_TEST_EXTRA" "$HOME"`}

	p, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	out := readUntil(t, p, "ENV:", 10*time.Second)
	waitForExit(t, p, 10*time.Second)
	for _, want := range []string{
		"ENV:" + spec.Token,
		"http://127.0.0.1:49153",
		"http://127.0.0.1:49154",
		spec.SandboxID,
		"|ok|",
		filepath.Join(spec.StatePath, "home"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("PTY output missing %q: %q", want, out)
		}
	}
	_ = r.Stop(context.Background(), spec.SandboxID)
}

func TestStartTwiceAndStopAll(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})

	first := newSpec(t, "sbx_first")
	first.Command = []string{"/bin/sleep", "30"}
	p1, err := r.Start(context.Background(), first)
	if err != nil {
		t.Fatalf("Start first: %v", err)
	}
	second := newSpec(t, "sbx_second")
	second.Command = []string{"/bin/sleep", "30"}
	p2, err := r.Start(context.Background(), second)
	if err != nil {
		t.Fatalf("Start second: %v", err)
	}
	if _, ok := r.Get("sbx_first"); !ok {
		t.Fatal("first process not registered")
	}
	if _, ok := r.Get("sbx_second"); !ok {
		t.Fatal("second process not registered")
	}

	r.StopAll(context.Background())
	waitForExit(t, p1, 5*time.Second)
	waitForExit(t, p2, 5*time.Second)
	if _, ok := r.Get("sbx_first"); ok {
		t.Fatal("first process still registered after StopAll")
	}
	if _, ok := r.Get("sbx_second"); ok {
		t.Fatal("second process still registered after StopAll")
	}
	// StopAll on an empty runtime is a no-op.
	r.StopAll(context.Background())

	// Starting twice with the same sandbox id overwrites the map entry; the
	// older process is only reachable through the returned handle.
	dup := newSpec(t, "sbx_dup")
	dup.Command = []string{"/bin/sleep", "30"}
	older, err := r.Start(context.Background(), dup)
	if err != nil {
		t.Fatalf("Start older: %v", err)
	}
	newer, err := r.Start(context.Background(), dup)
	if err != nil {
		t.Fatalf("Start newer: %v", err)
	}
	if got, ok := r.Get("sbx_dup"); !ok || got != newer {
		t.Fatalf("Get after duplicate start = %v,%v, want newest", got, ok)
	}
	if err := older.Stop(context.Background()); err != nil {
		t.Fatalf("older Stop: %v", err)
	}
	waitForExit(t, older, 5*time.Second)
	r.StopAll(context.Background())
	waitForExit(t, newer, 5*time.Second)
}

func TestProcessStopZeroPIDAndCanceledContext(t *testing.T) {
	p := &Process{}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on zero PID = %v", err)
	}

	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_cancelstop")
	spec.Command = []string{"/bin/sleep", "30"}
	proc, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("Stop with canceled ctx: %v", err)
	}
	pollGone(t, proc.PID, 5*time.Second)
	_ = r.Stop(context.Background(), spec.SandboxID)
}

func TestKillPID(t *testing.T) {
	if err := KillPID(0); err != nil {
		t.Fatalf("KillPID(0) = %v", err)
	}
	if err := KillPID(-42); err != nil {
		t.Fatalf("KillPID(-42) = %v", err)
	}

	startSleep := func(t *testing.T) *exec.Cmd {
		t.Helper()
		cmd := exec.Command("/bin/sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		return cmd
	}

	t.Run("terminates child process group", func(t *testing.T) {
		cmd := startSleep(t)
		pid := cmd.Process.Pid
		if err := KillPID(pid); err != nil {
			t.Fatalf("KillPID(%d) = %v", pid, err)
		}
		// Reap first: an unreaped zombie still answers signal 0 on macOS.
		if err := cmd.Wait(); err == nil {
			t.Fatal("sleep exited cleanly, want signal termination")
		}
		pollGone(t, pid, 3*time.Second)
	})

	t.Run("already exited", func(t *testing.T) {
		cmd := startSleep(t)
		pid := cmd.Process.Pid
		if err := cmd.Process.Kill(); err != nil {
			t.Fatalf("kill child: %v", err)
		}
		_ = cmd.Wait()
		start := time.Now()
		if err := KillPID(pid); err != nil {
			t.Fatalf("KillPID(dead) = %v", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("KillPID(dead) took %s, want fast return", elapsed)
		}
	})

	t.Run("escalates to SIGKILL", func(t *testing.T) {
		cmd := exec.Command("/bin/sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start ignoring child: %v", err)
		}
		pid := cmd.Process.Pid
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		start := time.Now()
		if err := KillPID(pid); err != nil {
			t.Fatalf("KillPID(%d) = %v", pid, err)
		}
		if elapsed := time.Since(start); elapsed < 2*time.Second {
			t.Fatalf("KillPID returned in %s before SIGKILL escalation", elapsed)
		}
		_ = cmd.Wait()
		pollGone(t, pid, 3*time.Second)
	})
}
