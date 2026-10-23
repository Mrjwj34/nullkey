package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ServiceSpec describes a sandbox-local service. Command runs under the same
// Seatbelt profile as the harness; it never receives real credentials.
type ServiceSpec struct {
	Name    string
	Command []string
	Env     []string
	Port    int
}

// ServiceProcess is a supervised sandbox service process.
type ServiceProcess struct {
	Name string
	PID  int

	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	code int
	logs *boundedBuffer
	stop bool
}

type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newBoundedBuffer(max int) *boundedBuffer { return &boundedBuffer{max: max} }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// StartService launches a service process under the sandbox profile.
func (r *Runtime) StartService(ctx context.Context, sandboxID string, spec ServiceSpec) (*ServiceProcess, error) {
	parent, ok := r.Get(sandboxID)
	if !ok {
		return nil, ErrNotRunning
	}
	if len(spec.Command) == 0 {
		return nil, errors.New("native: empty service command")
	}
	runSpec := parent.spec
	runSpec.Command = spec.Command
	runSpec.ExtraEnv = append([]string{}, spec.Env...)
	args := []string{"__sandbox_exec", "--cwd", parent.spec.WorkspacePath, "--"}
	args = append(args, spec.Command...)
	cmd := exec.CommandContext(context.Background(), r.cfg.Executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	profileR, profileW, err := os.Pipe()
	if err != nil {
		readyR.Close()
		readyW.Close()
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{readyW, profileR}
	stateHome := parent.spec.StatePath + "/home"
	tmpDir := parent.spec.CachePath + "/tmp"
	env := r.sandboxEnv(runSpec, stateHome, tmpDir)
	env = append(env, "NULLKEY_SERVICE_NAME="+spec.Name)
	if spec.Port > 0 {
		env = append(env, fmt.Sprintf("PORT=%d", spec.Port))
	}
	cmd.Env = env
	logs := newBoundedBuffer(64 * 1024)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		readyR.Close()
		readyW.Close()
		profileR.Close()
		profileW.Close()
		return nil, err
	}
	readyW.Close()
	profileR.Close()
	go func() {
		_, _ = profileW.WriteString(parent.profile)
		profileW.Close()
	}()
	ready := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(io.LimitReader(readyR, 4096))
		readyR.Close()
		ready <- string(raw)
	}()
	select {
	case msg := <-ready:
		if !strings.HasPrefix(msg, "1") {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			if strings.HasPrefix(msg, "ERR:") {
				return nil, fmt.Errorf("service sandbox failed: %s", strings.TrimPrefix(msg, "ERR:"))
			}
			return nil, errors.New("service exited before readiness")
		}
	case <-time.After(r.cfg.ReadyTimeout):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, errors.New("service did not become ready")
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
	svc := &ServiceProcess{Name: spec.Name, PID: cmd.Process.Pid, cmd: cmd, done: make(chan struct{}), logs: logs}
	go func() {
		_ = cmd.Wait()
		svc.mu.Lock()
		svc.code = cmd.ProcessState.ExitCode()
		svc.mu.Unlock()
		close(svc.done)
	}()
	return svc, nil
}

// Done is closed when the service exits.
func (s *ServiceProcess) Done() <-chan struct{} { return s.done }

// ExitCode returns the exit code.
func (s *ServiceProcess) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Logs returns the redacted-friendly captured output (redaction is applied by
// the caller before persisting or displaying).
func (s *ServiceProcess) Logs() string { return s.logs.String() }

// Stop terminates the service process group.
func (s *ServiceProcess) Stop(ctx context.Context) {
	s.mu.Lock()
	s.stop = true
	pid := s.PID
	s.mu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-s.done:
		case <-time.After(time.Second):
		}
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
