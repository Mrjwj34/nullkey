//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestAC_RT_001_SandboxLifecycle(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "runtime")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	if sb.Status != "running" && sb.Status != "starting" {
		t.Fatalf("status = %q, want running/starting", sb.Status)
	}
	waitFor(t, 5*time.Second, "sandbox running", func() bool {
		return d.getSandbox(t, sb.ID).Status == "running"
	})
	current := d.getSandbox(t, sb.ID)
	if current.PID <= 0 {
		t.Fatalf("pid = %d, want > 0", current.PID)
	}
	if current.StateID == "" {
		t.Fatal("sandbox has no state_id")
	}

	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d; body=%s", resp.StatusCode, raw)
	}
	if got := d.getSandbox(t, sb.ID).Status; got != "stopped" {
		t.Fatalf("status after stop = %q, want stopped", got)
	}
	resp, raw = d.doAuthed(t, http.MethodDelete, "/sandboxes/"+sb.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d; body=%s", resp.StatusCode, raw)
	}
	if resp, _ := d.doAuthed(t, http.MethodGet, "/sandboxes/"+sb.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted sandbox get = %d, want 404", resp.StatusCode)
	}
	if resp, _ := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("workspace removed by sandbox delete: %d", resp.StatusCode)
	}
	var states struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/states", nil)
	if err := json.Unmarshal(raw, &states); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	found := false
	for _, st := range states.Items {
		if st.ID == current.StateID {
			found = true
		}
	}
	if !found {
		t.Fatalf("state %s removed by sandbox delete", current.StateID)
	}
}

// A read-only workspace must be enforced by Seatbelt, not just by the GUI
// label or the workspace lock. Keep state writable so the harness can run.
func TestAC_STATE_005_ReadOnlyWorkspace(t *testing.T) {
	requireDarwin(t)
	for _, mode := range []string{"ro", "rw"} {
		t.Run(mode, func(t *testing.T) {
			d := startDaemon(t)
			root := t.TempDir()
			original := filepath.Join(root, "original.txt")
			if err := os.WriteFile(original, []byte("original\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			resp, raw := d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{
				"name": "permissions", "root_path": root, "default_mode": mode,
			})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("create workspace: %d %s", resp.StatusCode, raw)
			}
			ws := decodeJSON[workspaceResponse](t, raw)
			sb := d.createSandbox(t, ws.ID, "shell", "")
			term := dialTerminal(t, d, sb.ID)
			defer term.close()
			term.input("cat original.txt; echo changed > original.txt; echo new > new.txt; mkdir new-dir; mv original.txt renamed.txt; rm -f original.txt; echo state > \"$HOME/state-write\"; printf 'PERMISSIONS_%s\\n' done\n")
			output := term.readUntil("PERMISSIONS_done", 5*time.Second)
			if !strings.Contains(output, "original\r\n") {
				t.Fatalf("workspace could not be read: %s", output)
			}
			stateFile := filepath.Join(d.home, "states", "shell", sb.StateID, "home", "state-write")
			if data, err := os.ReadFile(stateFile); err != nil || string(data) != "state\n" {
				t.Fatalf("state must remain writable: %q %v", data, err)
			}
			if mode == "ro" {
				if data, err := os.ReadFile(original); err != nil || string(data) != "original\n" {
					t.Fatalf("read-only workspace was modified: %q %v; terminal=%s", data, err, output)
				}
				for _, name := range []string{"new.txt", "new-dir", "renamed.txt"} {
					if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatalf("read-only workspace allowed creating %s: %v", name, err)
					}
				}
			} else if data, err := os.ReadFile(filepath.Join(root, "renamed.txt")); err != nil || string(data) != "changed\n" {
				t.Fatalf("read-write workspace must remain writable: %q %v", data, err)
			}
		})
	}
}

func TestAC_RT_002_TerminalWebSocket(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "terminal")
	sb := d.createSandbox(t, ws.ID, "shell", "")

	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/terminal", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket = %d; body=%s", resp.StatusCode, raw)
	}
	ticket := decodeJSON[struct {
		WSURL     string    `json:"ws_url"`
		Ticket    string    `json:"ticket"`
		ExpiresAt time.Time `json:"expires_at"`
	}](t, raw)
	if ticket.Ticket == "" || ticket.WSURL == "" {
		t.Fatalf("incomplete ticket: %+v", ticket)
	}
	now := time.Now()
	if ticket.ExpiresAt.Before(now.Add(20*time.Second)) || ticket.ExpiresAt.After(now.Add(40*time.Second)) {
		t.Fatalf("ticket expiry = %v, want ~30s", ticket.ExpiresAt)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, ticket.WSURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Authorization": {"Bearer " + ticket.Ticket}},
	})
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	var ready wsMessage
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read ready: %v", err)
		}
		_ = json.Unmarshal(payload, &ready)
		break
	}
	if ready.Type != "ready" || !strings.HasPrefix(ready.TerminalID, "term_") {
		t.Fatalf("first message = %+v, want ready with term_ id", ready)
	}
	writeWS(t, ctx, conn, map[string]any{"type": "input", "data": "echo READY_$((1+1))\n"})
	output := readWSUntil(t, ctx, conn, "READY_2", 5*time.Second)
	if !strings.Contains(output, "READY_2") {
		t.Fatalf("terminal output = %q", output)
	}
	writeWS(t, ctx, conn, map[string]any{"type": "resize", "cols": 90, "rows": 30})
	writeWS(t, ctx, conn, map[string]any{"type": "input", "data": "stty size\n"})
	sizeOut := readWSUntil(t, ctx, conn, "30 90", 5*time.Second)
	if !strings.Contains(sizeOut, "30 90") {
		t.Fatalf("resize not applied; output=%q", sizeOut)
	}

	// A wrong ticket is rejected before the upgrade.
	badCtx, badCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer badCancel()
	_, badResp, err := websocket.Dial(badCtx, ticket.WSURL+"?ticket=wrong", nil)
	if err == nil {
		t.Fatal("wrong ticket upgraded")
	}
	if badResp == nil || badResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong ticket status = %v, want 401", badResp)
	}
	// Tickets are one-time.
	_, reuseResp, err := websocket.Dial(badCtx, ticket.WSURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Authorization": {"Bearer " + ticket.Ticket}},
	})
	if err == nil {
		t.Fatal("reused ticket upgraded")
	}
	if reuseResp == nil || reuseResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket status = %v, want 401", reuseResp)
	}
}

func writeWS(t *testing.T, ctx context.Context, conn *websocket.Conn, payload any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write ws: %v", err)
	}
}

func readWSUntil(t *testing.T, ctx context.Context, conn *websocket.Conn, needle string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var out strings.Builder
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithDeadline(ctx, deadline)
		_, raw, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			break
		}
		var msg wsMessage
		_ = json.Unmarshal(raw, &msg)
		if msg.Type == "output" {
			out.WriteString(msg.Data)
			if strings.Contains(out.String(), needle) {
				return out.String()
			}
		}
	}
	t.Fatalf("timed out waiting for %q; output=%q", needle, out.String())
	return ""
}

func TestAC_RT_003_SeatbeltFailClosed(t *testing.T) {
	requireDarwin(t)
	d := startDaemonWithEnv(t, map[string]string{"NULLKEY_FORCE_RUNTIME_FAILURE": "1"})
	ws := d.createWorkspace(t, "seatbelt")
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes", map[string]any{"harness_id": "shell", "workspace_id": ws.ID})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "runtime_unavailable" {
		t.Fatalf("code = %q, want runtime_unavailable", env.Error.Code)
	}
	var list struct {
		Items []sandboxResponse `json:"items"`
	}
	resp, raw = d.doAuthed(t, http.MethodGet, "/sandboxes", nil)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode sandboxes: %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("failed sandbox record missing: %d %s", resp.StatusCode, raw)
	}
	if list.Items[0].Status != "failed" || list.Items[0].LastError == nil || *list.Items[0].LastError == "" {
		t.Fatalf("failed sandbox not recorded honestly: %+v", list.Items[0])
	}
}

func TestAC_RT_004_WorkspaceLock(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "lock")
	sb1 := d.createSandbox(t, ws.ID, "shell", "")
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes", map[string]any{"harness_id": "shell", "workspace_id": ws.ID})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second rw sandbox = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "state_locked" {
		t.Fatalf("code = %q, want state_locked", env.Error.Code)
	}
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb1.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop first sandbox = %d", resp.StatusCode)
	}
	sb2 := d.createSandbox(t, ws.ID, "shell", "")
	if sb2.ID == "" {
		t.Fatal("sandbox creation after lock release failed")
	}

	roWS := decodeJSON[workspaceResponse](t, func() []byte {
		resp, raw := d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{"name": "ro", "default_mode": "ro"})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create ro workspace = %d %s", resp.StatusCode, raw)
		}
		return raw
	}())
	ro1 := d.createSandbox(t, roWS.ID, "shell", "")
	ro2 := d.createSandbox(t, roWS.ID, "shell", "")
	if ro1.ID == ro2.ID {
		t.Fatal("ro sandboxes unexpectedly identical")
	}
	for _, id := range []string{sb2.ID, ro1.ID, ro2.ID} {
		_, _ = d.doAuthed(t, http.MethodPost, "/sandboxes/"+id+"/stop", nil)
	}
}

func TestAC_RT_005_PersistenceAndRotation(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "persist")
	sb1 := d.createSandbox(t, ws.ID, "shell", "")
	token1 := sandboxToken(t, d, sb1.ID)
	c1 := dialTerminal(t, d, sb1.ID)
	c1.input("echo persisted > data.txt; echo state > \"$HOME/state.txt\"; echo WRITE_DONE_$((7*7))\n")
	out1 := c1.readUntil("WRITE_DONE_49", 5*time.Second)
	c1.close()
	if _, err := os.Stat(filepath.Join(ws.RootPath, "data.txt")); err != nil {
		t.Fatalf("data.txt missing on host: %v\noutput=%q", err, out1)
	}

	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb1.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop first = %d", resp.StatusCode)
	}
	sb2 := d.createSandboxWithState(t, ws.ID, sb1.StateID)
	token2 := sandboxToken(t, d, sb2.ID)
	if token1 == token2 {
		t.Fatal("sandbox token was not rotated")
	}
	c2 := dialTerminal(t, d, sb2.ID)
	c2.input("cat data.txt; cat \"$HOME/state.txt\"; echo READ_DONE_$((7*7))\n")
	out := c2.readUntil("READ_DONE_49", 5*time.Second)
	c2.close()
	if !strings.Contains(out, "persisted") || !strings.Contains(out, "state") {
		t.Fatalf("workspace/state did not persist: %q", out)
	}

	var states struct {
		Items []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"items"`
	}
	_, raw := d.doAuthed(t, http.MethodGet, "/states", nil)
	if err := json.Unmarshal(raw, &states); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	var statePath string
	for _, st := range states.Items {
		if st.ID == sb1.StateID {
			statePath = st.Path
		}
	}
	if statePath == "" {
		t.Fatal("state path not found")
	}
	cfg, err := os.ReadFile(filepath.Join(statePath, "nullkey", "config.json"))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if !strings.Contains(string(cfg), "NULLKEY_TOKEN") {
		t.Fatalf("generated config does not reference the env token: %s", cfg)
	}
	for _, token := range []string{token1, token2} {
		if strings.Contains(string(cfg), token) {
			t.Fatal("generated config contains a sandbox token")
		}
	}
}

func TestAC_RT_006_Stats(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "stats")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	var first, second struct {
		MemoryBytes int64   `json:"memory_bytes"`
		CPUPercent  float64 `json:"cpu_percent"`
		PID         int     `json:"pid"`
		UptimeMS    int64   `json:"uptime_ms"`
		StartupMS   int64   `json:"startup_ms"`
	}
	resp, raw := d.doAuthed(t, http.MethodGet, "/sandboxes/"+sb.ID+"/stats", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats = %d; body=%s", resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if first.MemoryBytes <= 0 || first.PID <= 0 || first.UptimeMS < 0 || first.CPUPercent < 0 || first.StartupMS < 0 {
		t.Fatalf("invalid stats: %+v", first)
	}
	time.Sleep(150 * time.Millisecond)
	_, raw = d.doAuthed(t, http.MethodGet, "/sandboxes/"+sb.ID+"/stats", nil)
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatalf("decode stats 2: %v", err)
	}
	if second.PID != first.PID || second.UptimeMS < first.UptimeMS {
		t.Fatalf("stats did not track the live process: first=%+v second=%+v", first, second)
	}
}

func TestAC_RT_007_GracefulShutdown(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "shutdown")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	pid := d.getSandbox(t, sb.ID).PID
	home := d.home
	d.terminate()

	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("sandbox pid %d survived daemon shutdown", pid)
	}
	d2 := startDaemonWithHome(t, home, nil)
	current := d2.getSandbox(t, sb.ID)
	if current.Status != "stopped" {
		t.Fatalf("status after restart = %q, want stopped", current.Status)
	}
	resp, raw := d2.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old token status = %d, want 401; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "token_invalid" {
		t.Fatalf("code = %q, want token_invalid", env.Error.Code)
	}
}

func TestAC_RT_008_TerminalReconnect(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "reconnect")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	c1 := dialTerminal(t, d, sb.ID)
	c1.input("echo OLD_OUTPUT_$((7*7))\n")
	c1.readUntil("OLD_OUTPUT_49", 5*time.Second)

	c2 := dialTerminal(t, d, sb.ID)
	defer c2.close()
	if c1.terminalID != c2.terminalID {
		t.Fatalf("terminal id changed across attach: %s vs %s", c1.terminalID, c2.terminalID)
	}
	replaced := c1.waitType("error", 3*time.Second)
	if replaced.Error == nil || replaced.Error.Code != "terminal_replaced" {
		t.Fatalf("old client did not receive terminal_replaced: %+v", replaced)
	}
	c1.close()
	if strings.Contains(c2.Output(), "OLD_OUTPUT") {
		t.Fatalf("new client received scrollback: %q", c2.Output())
	}
	c2.input("echo NEW_OUTPUT_$((7*7))\n")
	c2.readUntil("NEW_OUTPUT_49", 5*time.Second)
	c2.resize(100, 30)
	c2.input("stty size\n")
	c2.readUntil("30 100", 5*time.Second)
}

func TestAC_RT_009_TerminalState(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "terminal-state")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop sandbox = %d", resp.StatusCode)
	}
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/terminal", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ticket after stop = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "conflict" && env.Error.Code != "terminal_not_ready" {
		t.Fatalf("code = %q, want conflict/terminal_not_ready", env.Error.Code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp2, err := websocket.Dial(ctx, "ws://"+d.gatewayAddr+"/terminal", nil)
	if err == nil {
		t.Fatal("terminal upgraded without a ticket")
	}
	if resp2 == nil || resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ticketless WS status = %v, want 401", resp2)
	}
}
