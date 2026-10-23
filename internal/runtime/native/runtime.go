// Package native implements the macOS sandbox runtime: Seatbelt profile,
// process supervisor and PTY. On non-macOS hosts it fails closed.
package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/Mrjwj34/nullkey/internal/domain"
	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

// ErrUnsupported is returned when the host cannot enforce Seatbelt.
var ErrUnsupported = errors.New("native runtime is only available on macOS with Seatbelt support")

// ErrNotRunning is returned when a process is not running.
var ErrNotRunning = errors.New("sandbox process is not running")

// Config configures the native runtime.
type Config struct {
	// Executable is the nullkeyd binary used for the sandbox helper mode.
	Executable string
	// Home is the Nullkey home directory.
	Home string
	// HostHome is the real user home, denied inside the sandbox.
	HostHome string
	// GatewayAddr and ProxyAddr are loopback addresses injected into the child.
	GatewayAddr string
	ProxyAddr   string
	// Logger is used for redacted lifecycle logs.
	Logger *slog.Logger
	// ReadyTimeout bounds sandbox startup. Defaults to 8s.
	ReadyTimeout time.Duration
}

// Spec describes a sandbox process launch. It never contains credentials.
type Spec struct {
	SandboxID     string
	HarnessID     string
	WorkspacePath string
	StatePath     string
	CachePath     string
	Command       []string
	Token         string
	NetworkMode   domain.NetworkMode
	Cols          int
	Rows          int
	ExtraEnv      []string
}

// Process is a running sandbox process.
type Process struct {
	ID         string
	TerminalID string
	PID        int
	StartedAt  time.Time
	StartupMS  int64

	cmd     *exec.Cmd
	pty     *os.File
	done    chan struct{}
	once    sync.Once
	code    int
	mu      sync.Mutex
	cols    int
	rows    int
	state   string
	spec    Spec
	profile string
}

// Runtime supervises native sandbox processes.
type Runtime struct {
	cfg   Config
	log   *slog.Logger
	mu    sync.Mutex
	procs map[string]*Process
}

// New builds a native runtime.
func New(cfg Config) (*Runtime, error) {
	if cfg.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve executable: %w", err)
		}
		cfg.Executable = exe
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 8 * time.Second
	}
	if cfg.HostHome == "" {
		cfg.HostHome, _ = os.UserHomeDir()
	}
	return &Runtime{cfg: cfg, log: cfg.Logger, procs: make(map[string]*Process)}, nil
}

// Available reports whether Seatbelt enforcement is compiled in.
func Available() bool { return sandboxAvailable() }

// Start launches a sandbox process under Seatbelt and returns after the
// sandbox helper confirms that the profile is active and the command exec'd.
func (r *Runtime) Start(ctx context.Context, spec Spec) (*Process, error) {
	if !sandboxAvailable() {
		return nil, ErrUnsupported
	}
	if os.Getenv("NULLKEY_FORCE_RUNTIME_FAILURE") == "1" {
		return nil, errors.New("native: sandbox profile failed (forced failure test hook)")
	}
	if len(spec.Command) == 0 {
		return nil, errors.New("native: empty harness command")
	}
	if spec.SandboxID == "" || spec.WorkspacePath == "" || spec.StatePath == "" {
		return nil, errors.New("native: sandbox, workspace and state paths are required")
	}
	if spec.Token == "" {
		return nil, errors.New("native: sandbox token is required")
	}
	if spec.Cols <= 0 {
		spec.Cols = 120
	}
	if spec.Rows <= 0 {
		spec.Rows = 40
	}
	if err := os.MkdirAll(spec.StatePath, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(spec.CachePath, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	stateHome := filepath.Join(spec.StatePath, "home")
	tmpDir := filepath.Join(spec.CachePath, "tmp")
	for _, dir := range []string{stateHome, tmpDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create sandbox dir: %w", err)
		}
	}

	profile := BuildProfile(ProfileOptions{
		Workspace: spec.WorkspacePath,
		State:     spec.StatePath,
		Cache:     spec.CachePath,
		HostHome:  r.cfg.HostHome,
		Mode:      spec.NetworkMode,
	})

	args := []string{"__sandbox_exec", "--cwd", spec.WorkspacePath, "--"}
	args = append(args, spec.Command...)
	cmd := exec.CommandContext(context.Background(), r.cfg.Executable, args...)
	// pty.StartWithSize sets Setsid/Setctty; combining those with Setpgid makes
	// fork/exec fail with EPERM. Setsid already creates a new process group.

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create readiness pipe: %w", err)
	}
	profileR, profileW, err := os.Pipe()
	if err != nil {
		readyR.Close()
		readyW.Close()
		return nil, fmt.Errorf("create profile pipe: %w", err)
	}
	cmd.ExtraFiles = []*os.File{readyW, profileR}

	cmd.Env = r.sandboxEnv(spec, stateHome, tmpDir)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(spec.Cols), Rows: uint16(spec.Rows)})
	// Parent copies are no longer needed by the child.
	readyW.Close()
	profileR.Close()
	if err != nil {
		readyR.Close()
		profileW.Close()
		return nil, fmt.Errorf("start pty: %w", err)
	}

	go func() {
		_, _ = profileW.WriteString(profile)
		profileW.Close()
	}()

	readyCh := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(io.LimitReader(readyR, 4096))
		readyR.Close()
		readyCh <- string(raw)
	}()

	startupStart := time.Now()
	select {
	case msg := <-readyCh:
		if !strings.HasPrefix(msg, "1") {
			ptmx.Close()
			killProcessGroup(cmd)
			_ = cmd.Wait()
			if strings.HasPrefix(msg, "ERR:") {
				return nil, fmt.Errorf("sandbox profile failed: %s", strings.TrimSpace(strings.TrimPrefix(msg, "ERR:")))
			}
			return nil, fmt.Errorf("sandbox helper exited before readiness")
		}
	case <-time.After(r.cfg.ReadyTimeout):
		ptmx.Close()
		killProcessGroup(cmd)
		_ = cmd.Wait()
		return nil, fmt.Errorf("sandbox did not become ready within %s", r.cfg.ReadyTimeout)
	case <-ctx.Done():
		ptmx.Close()
		killProcessGroup(cmd)
		_ = cmd.Wait()
		return nil, ctx.Err()
	}

	p := &Process{
		ID:         spec.SandboxID,
		TerminalID: nkid.New(nkid.Terminal),
		PID:        cmd.Process.Pid,
		StartedAt:  startupStart,
		StartupMS:  time.Since(startupStart).Milliseconds(),
		cmd:        cmd,
		pty:        ptmx,
		done:       make(chan struct{}),
		cols:       spec.Cols,
		rows:       spec.Rows,
		state:      spec.StatePath,
		spec:       spec,
		profile:    profile,
	}
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.code = cmd.ProcessState.ExitCode()
		p.mu.Unlock()
		p.once.Do(func() { close(p.done) })
	}()

	r.mu.Lock()
	r.procs[spec.SandboxID] = p
	r.mu.Unlock()
	r.log.Info("sandbox_process_started",
		"sandbox_id", spec.SandboxID,
		"harness_id", spec.HarnessID,
		"pid", p.PID,
		"startup_ms", p.StartupMS,
		"network_mode", string(spec.NetworkMode),
	)
	return p, nil
}

func (r *Runtime) sandboxEnv(spec Spec, stateHome, tmpDir string) []string {
	env := []string{
		"HOME=" + stateHome,
		"TMPDIR=" + tmpDir,
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"SHELL=/bin/sh",
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"NULLKEY_TOKEN=" + spec.Token,
		"NULLKEY_GATEWAY=http://" + r.cfg.GatewayAddr,
		"NULLKEY_PROXY=http://" + r.cfg.ProxyAddr,
		"NULLKEY_SANDBOX_ID=" + spec.SandboxID,
		"HTTP_PROXY=http://" + r.cfg.ProxyAddr,
		"HTTPS_PROXY=http://" + r.cfg.ProxyAddr,
		"ALL_PROXY=http://" + r.cfg.ProxyAddr,
		"NO_PROXY=127.0.0.1,localhost",
		"DO_NOT_TRACK=1",
		"DISABLE_TELEMETRY=1",
		"NEXT_TELEMETRY_DISABLED=1",
		"CODEX_DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
	env = append(env, spec.ExtraEnv...)
	return env
}

// Get returns a running process.
func (r *Runtime) Get(id string) (*Process, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[id]
	return p, ok
}

// Stop terminates the sandbox process group and closes the PTY.
func (r *Runtime) Stop(ctx context.Context, id string) error {
	r.mu.Lock()
	p, ok := r.procs[id]
	if ok {
		delete(r.procs, id)
	}
	r.mu.Unlock()
	if !ok {
		return ErrNotRunning
	}
	return p.Stop(ctx)
}

// StopAll stops every process; used during daemon shutdown.
func (r *Runtime) StopAll(ctx context.Context) {
	r.mu.Lock()
	procs := make([]*Process, 0, len(r.procs))
	for _, p := range r.procs {
		procs = append(procs, p)
	}
	r.procs = make(map[string]*Process)
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func(p *Process) {
			defer wg.Done()
			_ = p.Stop(ctx)
		}(p)
	}
	wg.Wait()
}

// Stop terminates the process group, escalating to SIGKILL.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	pid := p.PID
	p.mu.Unlock()
	if pid <= 0 {
		return nil
	}
	killProcessGroupPID(pid, syscall.SIGTERM)
	// Closing the master delivers EIO/SIGHUP to the foreground process group and
	// unblocks cmd.Wait for interactive shells, which otherwise keep the PTY
	// open and make stop take the full SIGKILL timeout.
	_ = p.pty.Close()
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		killProcessGroupPID(pid, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	case <-ctx.Done():
		killProcessGroupPID(pid, syscall.SIGKILL)
	}
	p.pty.Close()
	return nil
}

// Read reads PTY output.
func (p *Process) Read(b []byte) (int, error) { return p.pty.Read(b) }

// Write writes PTY input.
func (p *Process) Write(b []byte) (int, error) { return p.pty.Write(b) }

// Close closes the PTY.
func (p *Process) Close() error { return p.pty.Close() }

// Resize resizes the PTY, which delivers SIGWINCH.
func (p *Process) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return errors.New("native: invalid terminal size")
	}
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	return pty.Setsize(p.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Done is closed when the process exits.
func (p *Process) Done() <-chan struct{} { return p.done }

// Size returns the current PTY size.
func (p *Process) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

// ExitCode returns the process exit code (valid after Done).
func (p *Process) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

// ProcessStats is a point-in-time resource sample.
type ProcessStats struct {
	MemoryBytes int64
	CPUPercent  float64
	PID         int
	Uptime      time.Duration
	StartupMS   int64
}

// Stats samples process resources using ps(1). The command has no user input.
func (p *Process) Stats() (ProcessStats, error) {
	out := ProcessStats{PID: p.PID, Uptime: time.Since(p.StartedAt), StartupMS: p.StartupMS}
	if err := syscall.Kill(p.PID, 0); err != nil {
		return out, ErrNotRunning
	}
	cmd := exec.Command("/bin/ps", "-o", "rss=,%cpu=", "-p", strconv.Itoa(p.PID))
	raw, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("ps: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) >= 2 {
		if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			out.MemoryBytes = kb * 1024
		}
		if cpu, err := strconv.ParseFloat(fields[1], 64); err == nil {
			out.CPUPercent = cpu
		}
	}
	return out, nil
}

// StatePath returns the sandbox state directory.
func (p *Process) StatePath() string { return p.state }

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		killProcessGroupPID(cmd.Process.Pid, syscall.SIGKILL)
	}
}

func killProcessGroupPID(pid int, sig syscall.Signal) {
	// Negative pid targets the process group created with Setpgid.
	_ = syscall.Kill(-pid, sig)
	_ = syscall.Kill(pid, sig)
}

// KillPID terminates a leftover process group after a daemon restart. It is a
// best-effort cleanup; callers still mark the sandbox stopped.
func KillPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return nil
}
