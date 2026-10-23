//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestAC_AUD_001_AuditEvents(t *testing.T) {
	requireDarwin(t)
	stub := newOAuthStub(t, 3600)
	defer stub.Close()
	d := startDaemonWithEnv(t, stub.Env())
	ws := d.createWorkspace(t, "audit")
	port := freeServicePort(t)
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{"name": "web", "port": port, "health_path": "/", "command": []string{"/bin/sh", "-c", "exec python3 -m http.server " + itoaTest(port) + " --bind 127.0.0.1"}},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	waitServicesReady(t, d, sb.ID, 1)

	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "audit", "auth_type": "api_key", "value": "sk-test-audit-0123456789abcdef",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("credential = %d %s", resp.StatusCode, raw)
	}
	credID := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
	_, _ = d.doAuthed(t, http.MethodDelete, "/credentials/"+credID, nil)

	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/start", map[string]any{"provider_id": "codex"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("oauth start = %d %s", resp.StatusCode, raw)
	}
	flowID := decodeJSON[struct {
		FlowID string `json:"flow_id"`
	}](t, raw).FlowID
	_, _ = d.doAuthed(t, http.MethodPost, "/oauth/flows/"+flowID+"/cancel", nil)

	token := sandboxToken(t, d, sb.ID)
	conn, status := d.proxyConnect(t, "blocked.example:443", token)
	conn.Close()
	if status == "" {
		t.Fatal("proxy denial produced no status")
	}
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d", resp.StatusCode)
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/audit?limit=200", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit = %d; body=%s", resp.StatusCode, raw)
	}
	var list struct {
		Items []struct {
			ID        string         `json:"id"`
			TS        time.Time      `json:"ts"`
			Type      string         `json:"type"`
			SandboxID *string        `json:"sandbox_id"`
			Detail    map[string]any `json:"detail"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	seen := map[string]bool{}
	for _, event := range list.Items {
		seen[event.Type] = true
		if event.ID == "" || event.TS.IsZero() {
			t.Fatalf("audit event missing id/ts: %+v", event)
		}
		if event.Detail != nil {
			for key, value := range event.Detail {
				if s, ok := value.(string); ok {
					if s == "sk-test-audit-0123456789abcdef" {
						t.Fatalf("audit detail %s contains plaintext", key)
					}
				}
			}
		}
	}
	for _, want := range []string{
		"sandbox.started", "sandbox.stopped", "credential.created", "credential.deleted",
		"oauth.started", "oauth.cancelled", "network.blocked", "service.started", "service.stopped",
	} {
		if !seen[want] {
			t.Fatalf("missing audit type %q; saw %v\n%s", want, seen, raw)
		}
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
