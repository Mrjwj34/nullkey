//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type workspaceResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	RootPath    string  `json:"root_path"`
	StorageKind string  `json:"storage_kind"`
	LockedBy    *string `json:"locked_by"`
}

type sandboxResponse struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	HarnessID   string  `json:"harness_id"`
	WorkspaceID string  `json:"workspace_id"`
	StateID     string  `json:"state_id"`
	NetworkMode string  `json:"network_mode"`
	PID         int     `json:"pid"`
	LastError   *string `json:"last_error"`
}

func (d *daemon) createWorkspace(t *testing.T, name string) workspaceResponse {
	t.Helper()
	resp, raw := d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace: status=%d body=%s", resp.StatusCode, raw)
	}
	return decodeJSON[workspaceResponse](t, raw)
}

func (d *daemon) createSandbox(t *testing.T, workspaceID, harnessID, mode string) sandboxResponse {
	t.Helper()
	body := map[string]any{"harness_id": harnessID, "workspace_id": workspaceID}
	if mode != "" {
		body["network_mode"] = mode
	}
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sandbox: status=%d body=%s\nlogs=%s", resp.StatusCode, raw, d.logs.String())
	}
	return decodeJSON[sandboxResponse](t, raw)
}

func (d *daemon) createSandboxWithState(t *testing.T, workspaceID, stateID string) sandboxResponse {
	t.Helper()
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes", map[string]any{
		"harness_id": "shell", "workspace_id": workspaceID, "state_id": stateID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sandbox with state: status=%d body=%s", resp.StatusCode, raw)
	}
	return decodeJSON[sandboxResponse](t, raw)
}

func (d *daemon) getSandbox(t *testing.T, id string) sandboxResponse {
	t.Helper()
	resp, raw := d.doAuthed(t, http.MethodGet, "/sandboxes/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get sandbox: status=%d body=%s", resp.StatusCode, raw)
	}
	return decodeJSON[sandboxResponse](t, raw)
}

func waitFor(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type wsMessage struct {
	Type       string `json:"type"`
	Data       string `json:"data"`
	TerminalID string `json:"terminal_id"`
	Code       *int   `json:"code"`
	Error      *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type terminalClient struct {
	t          *testing.T
	conn       *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	terminalID string
	mu         sync.Mutex
	output     strings.Builder
	messages   chan wsMessage
}

func dialTerminal(t *testing.T, d *daemon, sandboxID string) *terminalClient {
	t.Helper()
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sandboxID+"/terminal", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("terminal ticket: status=%d body=%s", resp.StatusCode, raw)
	}
	ticket := decodeJSON[struct {
		WSURL     string `json:"ws_url"`
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expires_at"`
	}](t, raw)
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, ticket.WSURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Authorization": {"Bearer " + ticket.Ticket}},
	})
	if err != nil {
		cancel()
		t.Fatalf("dial terminal: %v", err)
	}
	c := &terminalClient{t: t, conn: conn, ctx: ctx, cancel: cancel, messages: make(chan wsMessage, 512)}
	go c.readLoop()
	ready := c.waitType("ready", 3*time.Second)
	if ready.TerminalID == "" {
		t.Fatal("ready message has no terminal_id")
	}
	c.terminalID = ready.TerminalID
	return c
}

func (c *terminalClient) readLoop() {
	for {
		_, raw, err := c.conn.Read(c.ctx)
		if err != nil {
			close(c.messages)
			return
		}
		var msg wsMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if msg.Type == "output" {
			c.mu.Lock()
			c.output.WriteString(msg.Data)
			c.mu.Unlock()
		}
		select {
		case c.messages <- msg:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *terminalClient) waitType(typ string, timeout time.Duration) wsMessage {
	c.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-c.messages:
			if !ok {
				c.t.Fatalf("terminal closed while waiting for %q; output=%q", typ, c.Output())
			}
			if msg.Type == typ {
				return msg
			}
		case <-deadline:
			c.t.Fatalf("timed out waiting for terminal message %q; output=%q", typ, c.Output())
		}
	}
}

func (c *terminalClient) send(msg any) {
	c.t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatalf("marshal terminal message: %v", err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageText, raw); err != nil {
		c.t.Fatalf("write terminal message: %v", err)
	}
}

func (c *terminalClient) resize(cols, rows int) {
	c.send(map[string]any{"type": "resize", "cols": cols, "rows": rows})
}

func (c *terminalClient) input(text string) {
	c.send(map[string]any{"type": "input", "data": text})
}

func (c *terminalClient) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

func (c *terminalClient) readUntil(substr string, timeout time.Duration) string {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := c.Output()
		if strings.Contains(out, substr) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for terminal output %q; output=%q", substr, c.Output())
	return ""
}

func (c *terminalClient) close() {
	c.cancel()
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

func (d *daemon) gatewayPort() string {
	_, port, _ := net.SplitHostPort(d.gatewayAddr)
	return port
}

func (d *daemon) proxyPort() string {
	_, port, _ := net.SplitHostPort(d.proxyAddr)
	return port
}

func (d *daemon) gatewayRequest(t *testing.T, method, path, token string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, d.gatewayURL+path, reader)
	if err != nil {
		t.Fatalf("build gateway request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func (d *daemon) proxyConnect(t *testing.T, target, token string) (net.Conn, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", d.proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if token != "" {
		req += "Proxy-Authorization: Bearer " + token + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		t.Fatalf("write CONNECT: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		t.Fatalf("read CONNECT response: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	status := strings.SplitN(string(buf[:n]), "\r\n", 2)[0]
	return conn, status
}

// sandboxToken reads the per-sandbox token through the PTY. It is the only
// way the black-box test can obtain the token; the API never returns it.
func sandboxToken(t *testing.T, d *daemon, sandboxID string) string {
	t.Helper()
	c := dialTerminal(t, d, sandboxID)
	defer c.close()
	c.input("printf '\\n__TOKEN__%s__END__\\n' \"$NULLKEY_TOKEN\"\n")
	out := c.readUntil("__END__", 5*time.Second)
	re := regexp.MustCompile(`__TOKEN__([A-Za-z0-9_\-]+)__END__`)
	matches := re.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		t.Fatalf("could not parse sandbox token from %q", out)
	}
	return matches[len(matches)-1][1]
}

var servicePortCounter atomic.Int32

// freeServicePort hands out deterministic ports in the supervisor's allocated
// range without pre-binding them. Binding a specific low port on macOS can
// trigger a local-network permission prompt, so tests avoid probing one.
func freeServicePort(t *testing.T) int {
	t.Helper()
	port := int(24000 + servicePortCounter.Add(1))
	if port > 29999 {
		t.Fatal("service port counter exhausted")
	}
	return port
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func mustJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON: %v; body=%s", err, raw)
	}
	return out
}

func statusLine(resp *http.Response) string {
	return fmt.Sprintf("%d", resp.StatusCode)
}
