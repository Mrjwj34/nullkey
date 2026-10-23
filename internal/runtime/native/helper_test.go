package native

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func helperCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exec.Command(exe, append([]string{"__sandbox_exec"}, args...)...)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func TestParseHelperArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantCWD string
		wantCmd []string
		wantErr string
	}{
		{name: "no args", args: nil, wantErr: "usage:"},
		{name: "two args", args: []string{"--cwd", "/tmp"}, wantErr: "usage:"},
		{name: "wrong flag", args: []string{"--dir", "/tmp", "--", "/bin/sh"}, wantErr: "usage:"},
		{name: "empty cwd", args: []string{"--cwd", "", "--", "/bin/sh"}, wantErr: "empty working directory"},
		{name: "missing separator", args: []string{"--cwd", "/tmp", "/bin/sh"}, wantErr: "missing -- separator"},
		{name: "empty command", args: []string{"--cwd", "/tmp", "--"}, wantErr: "empty command"},
		{
			name:    "success",
			args:    []string{"--cwd", "/tmp/nk", "--", "/bin/sh", "-c", "echo hi"},
			wantCWD: "/tmp/nk",
			wantCmd: []string{"/bin/sh", "-c", "echo hi"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd, command, err := parseHelperArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cwd != tc.wantCWD {
				t.Fatalf("cwd = %q, want %q", cwd, tc.wantCWD)
			}
			if fmt.Sprint(command) != fmt.Sprint(tc.wantCmd) {
				t.Fatalf("command = %v, want %v", command, tc.wantCmd)
			}
		})
	}
}

func TestRunSandboxHelperInvalidArgsReturns125(t *testing.T) {
	badArgs := [][]string{
		nil,
		{"--cwd"},
		{"--bogus", "/tmp", "--", "/bin/sh"},
		{"--cwd", "", "--", "/bin/sh"},
		{"--cwd", "/tmp", "nope", "/bin/sh"},
		{"--cwd", "/tmp", "--"},
	}
	for _, args := range badArgs {
		t.Run(fmt.Sprintf("%v", args), func(t *testing.T) {
			// The direct call must fail before it ever touches fd 3/4.
			restore := silenceStderr(t)
			got := RunSandboxHelper(args)
			restore()
			if got != 125 {
				t.Fatalf("RunSandboxHelper(%v) = %d, want 125", args, got)
			}

			cmd := helperCommand(t, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			if code := exitCode(err); code != 125 {
				t.Fatalf("subprocess exit = %d (%v), want 125; stderr=%s", code, err, stderr.String())
			}
			if !strings.Contains(stderr.String(), "sandbox helper") {
				t.Fatalf("stderr missing helper usage message: %q", stderr.String())
			}
		})
	}
}

func TestRunSandboxHelperMissingProfileFDReturns125(t *testing.T) {
	// Valid argv but no inherited profile fd: reading fd 4 fails and the helper
	// must fail closed without exec'ing the requested command.
	cmd := helperCommand(t, "--cwd", t.TempDir(), "--", "/bin/echo", "must-not-run")
	cmd.ExtraFiles = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if code := exitCode(err); code != 125 {
		t.Fatalf("exit = %d (%v), want 125", code, err)
	}
	if strings.Contains(stdout.String(), "must-not-run") {
		t.Fatalf("helper exec'd the command despite missing profile fd: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "read profile") {
		t.Fatalf("stderr = %q, want read profile error", stderr.String())
	}
}

func TestRunSandboxHelperCommandNotFound(t *testing.T) {
	requireNativeHost(t)

	echo := []byte("")
	readyR, readyW := newPipe(t)
	profileR, profileW := newPipe(t)
	defer readyR.Close()
	defer profileW.Close()

	cmd := helperCommand(t, "--cwd", t.TempDir(), "--", "nullkey-helper-missing-binary")
	cmd.ExtraFiles = []*os.File{readyW, profileR}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	readyW.Close()
	profileR.Close()
	if _, err := profileW.Write(echo); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	profileW.Close()

	ready, err := io.ReadAll(readyR)
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if code := exitCode(cmd.Wait()); code != 127 {
		t.Fatalf("exit = %d, want 127; stderr=%s", code, stderr.String())
	}
	if !strings.HasPrefix(string(ready), "ERR:") || !strings.Contains(string(ready), "not found") {
		t.Fatalf("readiness = %q, want ERR:...not found", ready)
	}
}

func TestRunSandboxHelperAppliesSandboxAndExecs(t *testing.T) {
	requireNativeHost(t)

	workspace := t.TempDir()
	state := t.TempDir()
	cache := t.TempDir()
	profile := BuildProfile(ProfileOptions{
		Workspace: workspace,
		State:     state,
		Cache:     cache,
		HostHome:  "/Users/nobody-nullkey-test",
		Mode:      domain.NetworkServicesOnly,
	})

	readyR, readyW := newPipe(t)
	profileR, profileW := newPipe(t)
	defer readyR.Close()
	defer profileW.Close()

	cmd := helperCommand(t, "--cwd", workspace, "--", "/bin/echo", "helper-ok")
	cmd.ExtraFiles = []*os.File{readyW, profileR}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	readyW.Close()
	profileR.Close()
	go func() {
		_, _ = profileW.WriteString(profile)
		_ = profileW.Close()
	}()

	ready, err := io.ReadAll(readyR)
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper failed: %v; stderr=%s", err, stderr.String())
	}
	if string(ready) != "1" {
		t.Fatalf("readiness = %q, want \"1\"; stderr=%s", ready, stderr.String())
	}
	if !strings.Contains(stdout.String(), "helper-ok") {
		t.Fatalf("helper did not exec the command: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunSandboxHelperInvalidProfileReturns126(t *testing.T) {
	requireNativeHost(t)

	readyR, readyW := newPipe(t)
	profileR, profileW := newPipe(t)
	defer readyR.Close()
	defer profileW.Close()

	cmd := helperCommand(t, "--cwd", t.TempDir(), "--", "/bin/echo", "must-not-run")
	cmd.ExtraFiles = []*os.File{readyW, profileR}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	readyW.Close()
	profileR.Close()
	if _, err := profileW.WriteString("(this is not a valid seatbelt profile"); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	profileW.Close()

	ready, err := io.ReadAll(readyR)
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if code := exitCode(cmd.Wait()); code != 126 {
		t.Fatalf("exit = %d, want 126; stderr=%s", code, stderr.String())
	}
	if !strings.HasPrefix(string(ready), "ERR:") {
		t.Fatalf("readiness = %q, want ERR: prefix", ready)
	}
	if strings.Contains(stdout.String(), "must-not-run") {
		t.Fatalf("helper exec'd command after sandbox failure: %q", stdout.String())
	}
}

func TestRunSandboxHelperExecFailureReturns126(t *testing.T) {
	requireNativeHost(t)

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad-interpreter")
	if err := os.WriteFile(bad, []byte("#!/nonexistent-nullkey-interpreter\n"), 0o700); err != nil {
		t.Fatalf("write bad executable: %v", err)
	}
	profile := BuildProfile(ProfileOptions{Workspace: dir, State: dir, Cache: dir})

	readyR, readyW := newPipe(t)
	profileR, profileW := newPipe(t)
	defer readyR.Close()
	defer profileW.Close()

	cmd := helperCommand(t, "--cwd", dir, "--", bad)
	cmd.ExtraFiles = []*os.File{readyW, profileR}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	readyW.Close()
	profileR.Close()
	if _, err := profileW.WriteString(profile); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	profileW.Close()

	ready, err := io.ReadAll(readyR)
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if code := exitCode(cmd.Wait()); code != 126 {
		t.Fatalf("exit = %d, want 126; stderr=%s", code, stderr.String())
	}
	if string(ready) != "1" {
		t.Fatalf("readiness = %q, want \"1\"", ready)
	}
	if !strings.Contains(stderr.String(), "exec:") {
		t.Fatalf("stderr = %q, want exec failure", stderr.String())
	}
}

func TestWriteReadyNil(t *testing.T) {
	// The nil guard exists for the (rare) case where fd 3 cannot be constructed.
	writeReady(nil, "ignored")
}

func silenceStderr(t *testing.T) func() {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	old := os.Stderr
	os.Stderr = devnull
	return func() {
		os.Stderr = old
		_ = devnull.Close()
	}
}

func newPipe(t *testing.T) (r, w *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	return r, w
}

func requireNativeHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" || !sandboxAvailable() {
		t.Skip("Seatbelt helper tests require darwin with cgo")
	}
	if testing.Short() {
		t.Skip("skipping real sandbox helper test in -short mode")
	}
}
