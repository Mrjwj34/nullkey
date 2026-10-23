//go:build acceptance

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

type daemon struct {
	baseURL     string
	gatewayURL  string
	proxyURL    string
	gatewayAddr string
	proxyAddr   string
	token       string
	home        string
	cmd         *exec.Cmd
	logs        *safeBuffer
	waitCh      chan error
	exited      chan struct{}
	client      *http.Client
}

func startDaemon(t *testing.T) *daemon {
	t.Helper()
	return startDaemonWithEnv(t, nil)
}

// startDaemonWithEnv starts a real daemon with deterministic test backends.
// extra is merged after the defaults and may override them (for OAuth/MCP
// stubs and egress allowlists).
func startDaemonWithEnv(t *testing.T, extra map[string]string) *daemon {
	t.Helper()
	return startDaemonWithHome(t, t.TempDir(), extra)
}

// startDaemonWithHome restarts a daemon on the same home directory. It is used
// for crash-recovery assertions; tokens from the previous process must fail.
func startDaemonWithHome(t *testing.T, home string, extra map[string]string) *daemon {
	t.Helper()
	if daemonBinary == "" {
		t.Fatal("acceptance: daemon binary was not built")
	}
	port := freePort(t)
	gatewayPort := freePort(t)
	proxyPort := freePort(t)
	hostImport := filepath.Join(home, "host-import")
	if err := os.MkdirAll(hostImport, 0o700); err != nil {
		t.Fatalf("create host import dir: %v", err)
	}
	token, err := nkid.Random(32)
	if err != nil {
		t.Fatalf("generate test token: %v", err)
	}

	env := map[string]string{
		"NULLKEY_HOME":                  home,
		"NULLKEY_LISTEN":                "127.0.0.1:" + strconv.Itoa(port),
		"NULLKEY_GATEWAY_LISTEN":        "127.0.0.1:" + strconv.Itoa(gatewayPort),
		"NULLKEY_PROXY_LISTEN":          "127.0.0.1:" + strconv.Itoa(proxyPort),
		"NULLKEY_DAEMON_TOKEN":          token,
		"NULLKEY_LOG_LEVEL":             "debug",
		"NULLKEY_SECRET_BACKEND":        "file",
		"NULLKEY_HOST_IMPORT_ROOT":      hostImport,
		"NULLKEY_DEFAULT_NETWORK_MODE":  "filtered",
		"NULLKEY_EGRESS_ALLOWLIST":      "localhost,127.0.0.1,example.com,api.openai.com,github.com",
		"NULLKEY_EGRESS_ALLOW_LOOPBACK": "1",
	}
	for key, value := range extra {
		env[key] = value
	}

	cmd := exec.Command(daemonBinary)
	logs := &safeBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.Env = cleanEnv(env)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	d := &daemon{
		baseURL:     "http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1",
		gatewayURL:  "http://127.0.0.1:" + strconv.Itoa(gatewayPort),
		proxyURL:    "http://127.0.0.1:" + strconv.Itoa(proxyPort),
		gatewayAddr: "127.0.0.1:" + strconv.Itoa(gatewayPort),
		proxyAddr:   "127.0.0.1:" + strconv.Itoa(proxyPort),
		token:       token,
		home:        home,
		cmd:         cmd,
		logs:        logs,
		waitCh:      make(chan error, 1),
		exited:      make(chan struct{}),
		client:      &http.Client{Timeout: 10 * time.Second},
	}
	go func() {
		d.waitCh <- cmd.Wait()
		close(d.exited)
	}()
	t.Cleanup(d.terminate)
	d.waitHealthy(t)
	return d
}

func (d *daemon) waitHealthy(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := d.client.Get(d.baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("health status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("daemon did not become healthy: %v\n%s", lastErr, d.logs.String())
}

func (d *daemon) terminate() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	select {
	case <-d.exited:
		return
	default:
	}
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.exited:
	case <-time.After(3 * time.Second):
		_ = d.cmd.Process.Kill()
		select {
		case <-d.exited:
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *daemon) crash(t *testing.T) {
	t.Helper()
	if d.cmd == nil || d.cmd.Process == nil {
		t.Fatal("daemon has no process")
	}
	_ = d.cmd.Process.Kill()
	select {
	case <-d.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit after SIGKILL")
	}
}

func (d *daemon) do(t *testing.T, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, d.baseURL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, raw
}

func (d *daemon) doAuthed(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	return d.do(t, method, path, body, map[string]string{
		"Authorization": "Bearer " + d.token,
	})
}

func decodeJSON[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode JSON: %v; body=%s", err, raw)
	}
	return value
}

type apiError struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Details   map[string]any `json:"details"`
		RequestID string         `json:"request_id"`
	} `json:"error"`
}

func pending(t *testing.T, id, reason string) {
	t.Helper()
	if os.Getenv("NULLKEY_ACCEPTANCE_STRICT") == "1" {
		t.Fatalf("%s: pending implementation: %s", id, reason)
	}
	t.Skipf("%s: pending implementation: %s (set NULLKEY_ACCEPTANCE_STRICT=1 to enforce)", id, reason)
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func cleanEnv(overrides map[string]string) []string {
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if _, overridden := overrides[key]; overridden {
			continue
		}
		if strings.HasPrefix(key, "NULLKEY_") {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
