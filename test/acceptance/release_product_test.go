//go:build acceptance && release

package acceptance

import (
	"net/http"
	"strings"
	"testing"
)

// Product capability assertions run only on release (or manual release
// workflow_dispatch), never on pull requests.

func TestProduct_HealthAndCapabilities(t *testing.T) {
	d := startDaemon(t)

	resp, raw := d.do(t, http.MethodGet, "/health", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	health := decodeJSON[struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}](t, raw)
	if !health.OK || health.Version == "" {
		t.Fatalf("unexpected health: %+v", health)
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/capabilities", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	caps := decodeJSON[struct {
		Runtimes         []string `json:"runtimes"`
		DesktopAvailable bool     `json:"desktop_available"`
		NetworkModes     []string `json:"network_modes"`
		Tunnel           bool     `json:"tunnel"`
		Artifacts        bool     `json:"artifacts"`
		OAuthProviders   []string `json:"oauth_providers"`
		MCPServers       []string `json:"mcp_servers"`
	}](t, raw)
	if len(caps.Runtimes) == 0 || caps.Runtimes[0] != "native" {
		t.Fatalf("runtimes = %v, want native first", caps.Runtimes)
	}
	wantModes := []string{"services_only", "filtered", "open"}
	if strings.Join(caps.NetworkModes, ",") != strings.Join(wantModes, ",") {
		t.Fatalf("network_modes = %v, want %v", caps.NetworkModes, wantModes)
	}
	for _, field := range []string{"tunnel", "artifacts", "desktop_available"} {
		if !strings.Contains(strings.ToLower(string(raw)), `"`+field+`"`) {
			t.Fatalf("capabilities missing %q", field)
		}
	}

	// A public capability response must never contain a credential-shaped
	// value or the daemon token.
	body := string(raw)
	if strings.Contains(body, d.token) {
		t.Fatal("capabilities leaked the daemon token")
	}
	for _, prefix := range []string{"sk-", "ghp_", "xoxb-"} {
		if strings.Contains(body, prefix) {
			t.Fatalf("capabilities contains credential prefix %q", prefix)
		}
	}
}

func TestProduct_AuthAndErrorEnvelope(t *testing.T) {
	d := startDaemon(t)

	resp, raw := d.do(t, http.MethodGet, "/capabilities", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "unauthorized" || env.Error.RequestID == "" {
		t.Fatalf("unexpected auth error: %+v", env)
	}
	if env.Error.RequestID != resp.Header.Get("X-Request-Id") {
		t.Fatal("request id mismatch in auth error")
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/definitely-not-a-route", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404; body=%s", resp.StatusCode, raw)
	}
	notFound := decodeJSON[apiError](t, raw)
	if notFound.Error.Code != "not_found" {
		t.Fatalf("unknown route code = %q, want not_found", notFound.Error.Code)
	}
}

func TestProduct_CORSAndLoopbackPolicy(t *testing.T) {
	d := startDaemon(t)
	origin := "http://127.0.0.1:5173"

	resp, raw := d.do(t, http.MethodOptions, "/capabilities", nil, map[string]string{"Origin": origin})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204; body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("Allow-Origin = %q, want %q", got, origin)
	}

	resp, _ = d.do(t, http.MethodOptions, "/capabilities", nil, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback origin status = %d, want 403", resp.StatusCode)
	}
}
