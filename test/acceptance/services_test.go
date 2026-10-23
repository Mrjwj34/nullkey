//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type serviceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	WebSocket bool   `json:"websocket"`
}

func (d *daemon) listServices(t *testing.T, sandboxID string) []serviceInfo {
	t.Helper()
	resp, raw := d.doAuthed(t, http.MethodGet, "/sandboxes/"+sandboxID+"/services", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list services = %d; body=%s", resp.StatusCode, raw)
	}
	var list struct {
		Items []serviceInfo `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode services: %v", err)
	}
	return list.Items
}

func waitServicesReady(t *testing.T, d *daemon, sandboxID string, count int) []serviceInfo {
	t.Helper()
	var last []serviceInfo
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		last = d.listServices(t, sandboxID)
		if len(last) == count {
			allReady := true
			for _, svc := range last {
				if svc.Status != "ready" {
					allReady = false
				}
			}
			if allReady {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("services not ready: %+v\nlogs=%s", last, d.logs.String())
	return nil
}

func writeServicesManifest(t *testing.T, workspaceRoot string, services []map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 1, "services": services})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(workspaceRoot, ".nullkey", "services.json"), string(raw))
}

func TestAC_SVC_001_Lifecycle(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "services")
	portA := freeServicePort(t)
	portB := freeServicePort(t)
	for portB == portA {
		portB = freeServicePort(t)
	}
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{
			"name": "db", "port": portA, "health_path": "/",
			"command": []string{"/bin/sh", "-c", fmt.Sprintf("echo $NULLKEY_SERVICE_TOKEN > db-token.txt; exec python3 -m http.server %d --bind 127.0.0.1", portA)},
		},
		{
			"name": "web", "port": portB, "health_path": "/", "depends_on": []string{"db"}, "websocket": true,
			"command": []string{"/bin/sh", "-c", fmt.Sprintf("exec python3 -m http.server %d --bind 127.0.0.1", portB)},
		},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	services := waitServicesReady(t, d, sb.ID, 2)
	if services[0].Name != "db" || services[1].Name != "web" {
		t.Fatalf("dependency order not respected: %+v", services)
	}
	if !services[1].WebSocket {
		t.Fatalf("websocket flag not preserved: %+v", services[1])
	}
	token, err := os.ReadFile(filepath.Join(ws.RootPath, "db-token.txt"))
	if err != nil {
		t.Fatalf("read generated service token: %v", err)
	}
	if len(strings.TrimSpace(string(token))) < 20 {
		t.Fatalf("generated service token too short: %q", token)
	}
	if out, err := exec.Command("lsof", "-nP", "-iTCP:"+fmt.Sprint(portA), "-sTCP:LISTEN").Output(); err == nil {
		if !strings.Contains(string(out), "127.0.0.1:"+fmt.Sprint(portA)) {
			t.Fatalf("service did not bind loopback only: %s", out)
		}
	}
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d", resp.StatusCode)
	}
	waitFor(t, 5*time.Second, "services removed after stop", func() bool {
		return len(d.listServices(t, sb.ID)) == 0
	})

	// A new sandbox gets a fresh generated token; the old one is not reused.
	sb2 := d.createSandbox(t, ws.ID, "shell", "")
	waitServicesReady(t, d, sb2.ID, 2)
	token2, err := os.ReadFile(filepath.Join(ws.RootPath, "db-token.txt"))
	if err != nil {
		t.Fatalf("read second service token: %v", err)
	}
	if strings.TrimSpace(string(token)) == strings.TrimSpace(string(token2)) {
		t.Fatal("service auth token was reused across sandboxes")
	}
}

func TestAC_SVC_002_LoopbackAuth(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "services-invalid")
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{"name": "bad", "port": 80, "command": []string{"/bin/sh", "-c", "python3 -m http.server 80 --bind 127.0.0.1"}},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	time.Sleep(500 * time.Millisecond)
	if current := d.getSandbox(t, sb.ID); current.Status != "running" {
		t.Fatalf("invalid service manifest blocked the terminal: %q", current.Status)
	}
	if services := d.listServices(t, sb.ID); len(services) != 0 {
		t.Fatalf("out-of-range service was registered: %+v", services)
	}
}

func TestAC_SVC_003_RestartLogs(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "services-restart")
	port := freeServicePort(t)
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{
			"name": "flaky", "port": port, "health_path": "/", "restart": "on-failure",
			"command": []string{"/bin/sh", "-c", fmt.Sprintf("echo sk-test-service-secret-0123456789 >&2; if [ ! -f restarted.marker ]; then touch restarted.marker; sleep 0.2; exit 1; fi; exec python3 -m http.server %d --bind 127.0.0.1", port)},
		},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	restarted := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, svc := range d.listServices(t, sb.ID) {
			if svc.Name == "flaky" && svc.Status == "ready" {
				restarted = true
			}
		}
		if restarted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !restarted {
		t.Fatalf("service was not restarted: %+v\nlogs=%s", d.listServices(t, sb.ID), d.logs.String())
	}
	if _, err := os.Stat(filepath.Join(ws.RootPath, "restarted.marker")); err != nil {
		t.Fatalf("service was not restarted: %v", err)
	}
	if strings.Contains(d.logs.String(), "sk-test-service-secret") {
		t.Fatal("service stderr leaked into the daemon log")
	}
}

func TestAC_TUN_001_ServiceTicket(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "tunnel-ticket")
	port := freeServicePort(t)
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{"name": "web", "port": port, "health_path": "/", "command": []string{"/bin/sh", "-c", fmt.Sprintf("exec python3 -m http.server %d --bind 127.0.0.1", port)}},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	services := waitServicesReady(t, d, sb.ID, 1)
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/services/"+services[0].ID+"/ticket", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket = %d; body=%s", resp.StatusCode, raw)
	}
	ticket := decodeJSON[struct {
		URL       string    `json:"url"`
		Ticket    string    `json:"ticket"`
		ServiceID string    `json:"service_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}](t, raw)
	if ticket.Ticket == "" || ticket.URL == "" || ticket.ServiceID != services[0].ID {
		t.Fatalf("incomplete ticket: %+v", ticket)
	}
	if time.Until(ticket.ExpiresAt) > 61*time.Second || time.Until(ticket.ExpiresAt) < 50*time.Second {
		t.Fatalf("ticket expiry = %v, want ~60s", ticket.ExpiresAt)
	}
	req, _ := http.NewRequest(http.MethodGet, ticket.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+ticket.Ticket)
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("tunnel request: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("tunnel status = %d", resp2.StatusCode)
	}
	// Reusing the ticket without the session cookie is rejected.
	req2, _ := http.NewRequest(http.MethodGet, ticket.URL+"/", nil)
	req2.Header.Set("Authorization", "Bearer "+ticket.Ticket)
	resp3, err := (&http.Client{Timeout: 5 * time.Second}).Do(req2)
	if err != nil {
		t.Fatalf("reuse request: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket status = %d, want 401", resp3.StatusCode)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/services/missing/ticket", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("undeclared service ticket = %d; body=%s", resp.StatusCode, raw)
	}
	if env := decodeJSON[apiError](t, raw); env.Error.Code != "service_not_found" {
		t.Fatalf("code = %q, want service_not_found", env.Error.Code)
	}
}

func TestAC_TUN_002_HttpWsSse(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "tunnel")
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tunnel_server.py"))
	if err != nil {
		t.Fatalf("read tunnel server script: %v", err)
	}
	writeFile(t, filepath.Join(ws.RootPath, "tunnel_server.py"), string(script))
	port := freeServicePort(t)
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{"name": "app", "port": port, "health_path": "/", "websocket": true,
			"command": []string{"python3", "tunnel_server.py", fmt.Sprint(port)}},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	services := waitServicesReady(t, d, sb.ID, 1)
	ticket := func() struct {
		URL       string `json:"url"`
		Ticket    string `json:"ticket"`
		ServiceID string `json:"service_id"`
	} {
		resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/services/"+services[0].ID+"/ticket", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("ticket = %d; body=%s", resp.StatusCode, raw)
		}
		return decodeJSON[struct {
			URL       string `json:"url"`
			Ticket    string `json:"ticket"`
			ServiceID string `json:"service_id"`
		}](t, raw)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	tk := ticket()
	req, _ := http.NewRequest(http.MethodGet, tk.URL+"/path/echo", nil)
	req.Header.Set("Authorization", "Bearer "+tk.Ticket)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("tunnel HTTP: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "path=/path/echo") {
		t.Fatalf("path rewrite failed: %d %s", resp.StatusCode, body)
	}
	foundCookie := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "sid" {
			foundCookie = true
			if cookie.Path != "/tunnel" {
				t.Fatalf("cookie path = %q, want /tunnel", cookie.Path)
			}
		}
	}
	if !foundCookie {
		t.Fatal("upstream cookie was not forwarded")
	}

	// The session cookie authorizes subsequent requests without the ticket.
	resp, err = client.Get(tk.URL + "/sse")
	if err != nil {
		t.Fatalf("tunnel SSE: %v", err)
	}
	sse, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(sse), "data: hello") || !strings.Contains(string(sse), "data: bye") {
		t.Fatalf("SSE failed: %d %q", resp.StatusCode, sse)
	}

	// Non-loopback Origin is rejected.
	badReq, _ := http.NewRequest(http.MethodGet, tk.URL+"/", nil)
	badReq.Header.Set("Origin", "https://evil.example")
	badReq.Header.Set("Authorization", "Bearer "+tk.Ticket)
	badResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(badReq)
	if err != nil {
		t.Fatalf("origin request: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin status = %d, want 403", badResp.StatusCode)
	}

	// WebSocket upgrade through the tunnel, authenticated by a fresh ticket.
	wsTicket := ticket()
	wsURL := strings.Replace(wsTicket.URL, "http://", "ws://", 1) + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"X-Nullkey-Ticket": {wsTicket.Ticket}},
	})
	if err != nil {
		t.Fatalf("tunnel WS dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := conn.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("WS write: %v", err)
	}
	_, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("WS read: %v", err)
	}
	if string(msg) != "ws:ping" {
		t.Fatalf("WS echo = %q", msg)
	}
}

func TestAC_TUN_003_UndeclaredService(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "tunnel-undeclared")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	if services := d.listServices(t, sb.ID); len(services) != 0 {
		t.Fatalf("undeclared services appeared: %+v", services)
	}
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/services/svc_nope/ticket", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ticket for undeclared service = %d; body=%s", resp.StatusCode, raw)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+d.gatewayAddr+"/tunnel/", nil)
	req.Header.Set("Authorization", "Bearer not-a-ticket")
	resp2, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("tunnel request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid tunnel ticket = %d, want 401", resp2.StatusCode)
	}
}
