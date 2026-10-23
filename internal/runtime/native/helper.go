package native

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// RunSandboxHelper is the hidden `nullkeyd __sandbox_exec` mode. It reads a
// Seatbelt profile from fd 4, applies it, signals readiness on fd 3, then
// execs the harness process. Applying the sandbox in a dedicated child avoids
// restricting the daemon itself.
//
// It returns a process exit code.
func RunSandboxHelper(args []string) int {
	cwd, command, err := parseHelperArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nullkeyd sandbox helper:", err)
		return 125
	}
	profileFile := os.NewFile(4, "sandbox-profile")
	if profileFile == nil {
		fmt.Fprintln(os.Stderr, "nullkeyd sandbox helper: missing profile fd")
		return 125
	}
	profile, err := io.ReadAll(io.LimitReader(profileFile, 1<<20))
	profileFile.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nullkeyd sandbox helper: read profile:", err)
		return 125
	}

	ready := os.NewFile(3, "sandbox-ready")

	// Resolve the harness before applying the profile so a missing binary gives
	// a clear error rather than a Seatbelt denial.
	resolved, err := exec.LookPath(command[0])
	if err != nil {
		writeReady(ready, helperError(fmt.Errorf("harness %q not found: %w", command[0], err)))
		return 127
	}
	if err := applySandbox(string(profile)); err != nil {
		writeReady(ready, helperError(err))
		return 126
	}
	if err := os.Chdir(cwd); err != nil {
		writeReady(ready, helperError(fmt.Errorf("chdir %s: %w", cwd, err)))
		return 126
	}
	writeReady(ready, "1")
	argv := append([]string{resolved}, command[1:]...)
	if err := syscall.Exec(resolved, argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "nullkeyd sandbox helper: exec:", err)
		return 126
	}
	return 0
}

func writeReady(f *os.File, msg string) {
	if f == nil {
		return
	}
	_, _ = f.WriteString(msg)
	_ = f.Close()
}

func parseHelperArgs(args []string) (cwd string, command []string, err error) {
	if len(args) < 3 || args[0] != "--cwd" {
		return "", nil, errors.New("usage: __sandbox_exec --cwd <dir> -- <command...>")
	}
	cwd = args[1]
	if cwd == "" {
		return "", nil, errors.New("empty working directory")
	}
	if args[2] != "--" {
		return "", nil, errors.New("missing -- separator")
	}
	command = args[3:]
	if len(command) == 0 {
		return "", nil, errors.New("empty command")
	}
	return cwd, command, nil
}
