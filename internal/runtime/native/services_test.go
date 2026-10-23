package native

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newInjectedRuntime(t *testing.T, exe string, readyTimeout time.Duration) (*Runtime, string) {
	t.Helper()
	r := newTestRuntime(t, Config{Executable: exe, ReadyTimeout: readyTimeout})
	spec := newSpec(t, "sbx_parent_injected")
	parent := &Process{
		ID:      spec.SandboxID,
		PID:     -1, // never signalled by these tests
		spec:    spec,
		profile: BuildProfile(ProfileOptions{Workspace: spec.WorkspacePath, State: spec.StatePath, Cache: spec.CachePath}),
		state:   spec.StatePath,
	}
	r.procs[spec.SandboxID] = parent
	return r, spec.SandboxID
}

func waitForLogs(t *testing.T, svc *ServiceProcess, marker string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if logs := svc.Logs(); strings.Contains(logs, marker) {
			return logs
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service logs never contained %q: %q", marker, svc.Logs())
	return ""
}

func TestBoundedBuffer(t *testing.T) {
	b := newBoundedBuffer(8)
	if n, err := b.Write([]byte("0123456789")); err != nil || n != 10 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := b.String(); got != "23456789" {
		t.Fatalf("String = %q, want suffix \"23456789\"", got)
	}
	if n, err := b.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := b.String(); got != "56789abc" {
		t.Fatalf("String = %q, want \"56789abc\"", got)
	}
}

func TestStartServiceInvalidRequest(t *testing.T) {
	requireNativeHost(t)

	r := newTestRuntime(t, Config{})
	if _, err := r.StartService(context.Background(), "sbx_missing_parent", ServiceSpec{Command: []string{"/bin/sh"}}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("StartService(unknown parent) = %v, want ErrNotRunning", err)
	}

	spec := newSpec(t, "sbx_svc_parent")
	spec.Command = []string{"/bin/sleep", "30"}
	parent, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	defer func() {
		_ = r.Stop(context.Background(), spec.SandboxID)
		waitForExit(t, parent, 5*time.Second)
	}()

	if _, err := r.StartService(context.Background(), spec.SandboxID, ServiceSpec{Name: "empty"}); err == nil || !strings.Contains(err.Error(), "empty service command") {
		t.Fatalf("StartService(empty command) = %v", err)
	}

	if _, err := r.StartService(context.Background(), spec.SandboxID, ServiceSpec{
		Name:    "missing-binary",
		Command: []string{"nullkey-service-missing-binary"},
	}); err == nil || !strings.Contains(err.Error(), "service sandbox failed") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("StartService(missing binary) = %v, want service sandbox failure", err)
	}
}

func TestStartServiceLifecycle(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_svc_life")
	spec.Command = []string{"/bin/sleep", "30"}
	parent, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	defer func() {
		_ = r.Stop(context.Background(), spec.SandboxID)
		waitForExit(t, parent, 5*time.Second)
	}()

	svc, err := r.StartService(context.Background(), spec.SandboxID, ServiceSpec{
		Name:    "web",
		Command: []string{"/bin/sh", "-c", `printf 'SVC:%s|%s\n' "$NULLKEY_SERVICE_NAME" "$PORT"; exec sleep 30`},
		Env:     []string{"NULLKEY_EXTRA=svc-extra"},
		Port:    12345,
	})
	if err != nil {
		t.Fatalf("StartService: %v", err)
	}
	if svc.Name != "web" {
		t.Fatalf("Name = %q", svc.Name)
	}
	if svc.PID <= 0 {
		t.Fatalf("PID = %d", svc.PID)
	}
	logs := waitForLogs(t, svc, "SVC:web|12345", 10*time.Second)
	if !strings.Contains(logs, "SVC:web|12345") {
		t.Fatalf("logs = %q", logs)
	}

	svc.Stop(context.Background())
	select {
	case <-svc.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("service did not stop")
	}
	pollGone(t, svc.PID, 5*time.Second)
	// ExitCode is valid once Done is closed; the exact signal code is platform
	// dependent, so only require that it was observed.
	_ = svc.ExitCode()
	// Stopping an already-dead service is safe.
	svc.Stop(context.Background())
}

func TestStartServiceNaturalExit(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_svc_exit")
	spec.Command = []string{"/bin/sleep", "30"}
	parent, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	defer func() {
		_ = r.Stop(context.Background(), spec.SandboxID)
		waitForExit(t, parent, 5*time.Second)
	}()

	svc, err := r.StartService(context.Background(), spec.SandboxID, ServiceSpec{
		Name:    "once",
		Command: []string{"/bin/sh", "-c", `printf 'SVC_DONE\n'`},
	})
	if err != nil {
		t.Fatalf("StartService: %v", err)
	}
	waitForLogs(t, svc, "SVC_DONE", 10*time.Second)
	select {
	case <-svc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("service did not exit")
	}
	if code := svc.ExitCode(); code != 0 {
		t.Fatalf("ExitCode = %d, want 0", code)
	}
	if logs := svc.Logs(); !strings.Contains(logs, "SVC_DONE") {
		t.Fatalf("logs = %q", logs)
	}
	svc.Stop(context.Background())
}

func TestStartServiceFakeHelperFailures(t *testing.T) {
	requireNativeHost(t)

	t.Run("readiness timeout", func(t *testing.T) {
		exe := writeFakeExecutable(t, "sleep 30")
		r, sandboxID := newInjectedRuntime(t, exe, 100*time.Millisecond)
		_, err := r.StartService(context.Background(), sandboxID, ServiceSpec{Name: "slow", Command: []string{"/bin/sh"}})
		if err == nil || !strings.Contains(err.Error(), "did not become ready") {
			t.Fatalf("StartService = %v, want readiness timeout", err)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		exe := writeFakeExecutable(t, "sleep 30")
		r, sandboxID := newInjectedRuntime(t, exe, 10*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.StartService(ctx, sandboxID, ServiceSpec{Name: "ctx", Command: []string{"/bin/sh"}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartService = %v, want context.Canceled", err)
		}
	})

	t.Run("exited before readiness", func(t *testing.T) {
		exe := writeFakeExecutable(t, "exit 0")
		r, sandboxID := newInjectedRuntime(t, exe, 2*time.Second)
		_, err := r.StartService(context.Background(), sandboxID, ServiceSpec{Name: "early", Command: []string{"/bin/sh"}})
		if err == nil || !strings.Contains(err.Error(), "exited before readiness") {
			t.Fatalf("StartService = %v, want exited before readiness", err)
		}
	})

	t.Run("helper error", func(t *testing.T) {
		exe := writeFakeExecutable(t, `printf 'ERR:service-boom' >&3; exit 1`)
		r, sandboxID := newInjectedRuntime(t, exe, 2*time.Second)
		_, err := r.StartService(context.Background(), sandboxID, ServiceSpec{Name: "err", Command: []string{"/bin/sh"}})
		if err == nil || !strings.Contains(err.Error(), "service sandbox failed: service-boom") {
			t.Fatalf("StartService = %v, want propagated helper error", err)
		}
	})

	t.Run("start error", func(t *testing.T) {
		r, sandboxID := newInjectedRuntime(t, "/nonexistent-service-executable", 2*time.Second)
		if _, err := r.StartService(context.Background(), sandboxID, ServiceSpec{Name: "null", Command: []string{"/bin/sh"}}); err == nil {
			t.Fatal("StartService succeeded with a missing executable")
		}
	})
}

func TestServiceStopCanceledContext(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_svc_cancel")
	spec.Command = []string{"/bin/sleep", "30"}
	parent, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	defer func() {
		_ = r.Stop(context.Background(), spec.SandboxID)
		waitForExit(t, parent, 5*time.Second)
	}()

	svc, err := r.StartService(context.Background(), spec.SandboxID, ServiceSpec{
		Name:    "stubborn",
		Command: []string{"/bin/sh", "-c", `trap "" TERM; while :; do sleep 1; done`},
	})
	if err != nil {
		t.Fatalf("StartService: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	svc.Stop(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop(canceled) took %s, want prompt SIGKILL", elapsed)
	}
	pollGone(t, svc.PID, 5*time.Second)
}

func TestServiceStopEscalatesToKill(t *testing.T) {
	requireNativeHost(t)
	r := newTestRuntime(t, Config{})
	spec := newSpec(t, "sbx_svc_escalate")
	spec.Command = []string{"/bin/sleep", "30"}
	parent, err := r.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}
	defer func() {
		_ = r.Stop(context.Background(), spec.SandboxID)
		waitForExit(t, parent, 5*time.Second)
	}()

	svc, err := r.StartService(context.Background(), spec.SandboxID, ServiceSpec{
		Name:    "stubborn",
		Command: []string{"/bin/sh", "-c", `trap "" TERM; printf 'TRAP_READY\n'; while :; do sleep 1; done`},
	})
	if err != nil {
		t.Fatalf("StartService: %v", err)
	}
	waitForLogs(t, svc, "TRAP_READY", 10*time.Second)
	start := time.Now()
	svc.Stop(context.Background())
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("Stop returned in %s before SIGKILL escalation", elapsed)
	}
	pollGone(t, svc.PID, 5*time.Second)
}
