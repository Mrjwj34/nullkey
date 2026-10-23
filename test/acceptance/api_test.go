//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAC_API_001_Health(t *testing.T) {
	d := startDaemon(t)
	resp, raw := d.do(t, http.MethodGet, "/health", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	body := decodeJSON[struct {
		OK       bool   `json:"ok"`
		Version  string `json:"version"`
		UptimeMS int64  `json:"uptime_ms"`
	}](t, raw)
	if !body.OK || body.Version == "" {
		t.Fatalf("unexpected health: %+v", body)
	}
	if body.UptimeMS < 0 {
		t.Fatalf("uptime_ms = %d, want >= 0", body.UptimeMS)
	}
	if !strings.HasPrefix(resp.Header.Get("X-Request-Id"), "req_") {
		t.Fatalf("X-Request-Id = %q, want req_ prefix", resp.Header.Get("X-Request-Id"))
	}
}

func TestAC_API_002_Auth(t *testing.T) {
	d := startDaemon(t)

	resp, raw := d.do(t, http.MethodGet, "/capabilities", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d, want 401; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", env.Error.Code)
	}
	if env.Error.RequestID != resp.Header.Get("X-Request-Id") {
		t.Fatalf("request_id mismatch: body=%q header=%q", env.Error.RequestID, resp.Header.Get("X-Request-Id"))
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
	}

	resp, raw = d.do(t, http.MethodGet, "/capabilities", nil, map[string]string{
		"Authorization": "Bearer wrong-token-0123456789",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401; body=%s", resp.StatusCode, raw)
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/capabilities", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid token status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	// Health is public even without a token.
	resp, raw = d.do(t, http.MethodGet, "/health", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public health status = %d, want 200; body=%s", resp.StatusCode, raw)
	}

	// OAuth callback is public but not implemented in P0; it must not be 401.
	resp, raw = d.do(t, http.MethodGet, "/oauth/callback?code=x&state=y", nil, nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("public callback status = %d, want 501; body=%s", resp.StatusCode, raw)
	}
}

func TestAC_API_003_ErrorEnvelope(t *testing.T) {
	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodGet, "/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", env.Error.Code)
	}
	if env.Error.Message == "" || env.Error.RequestID == "" {
		t.Fatalf("incomplete error envelope: %+v", env)
	}
	if env.Error.RequestID != resp.Header.Get("X-Request-Id") {
		t.Fatalf("request_id mismatch: body=%q header=%q", env.Error.RequestID, resp.Header.Get("X-Request-Id"))
	}
}

func TestAC_API_004_Capabilities(t *testing.T) {
	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodGet, "/capabilities", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, raw)
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
		t.Fatalf("runtimes = %v, want native", caps.Runtimes)
	}
	if len(caps.NetworkModes) != 3 || caps.NetworkModes[0] != "services_only" {
		t.Fatalf("network_modes = %v, want services_only/filtered/open", caps.NetworkModes)
	}
	if caps.OAuthProviders == nil || caps.MCPServers == nil {
		t.Fatalf("capability arrays must not be nil: %+v", caps)
	}

	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err != nil {
		t.Fatalf("decode capability fields: %v", err)
	}
	for _, field := range []string{"runtimes", "network_modes", "desktop_available", "tunnel", "artifacts", "oauth_providers", "mcp_servers"} {
		if _, ok := rawFields[field]; !ok {
			t.Fatalf("capabilities missing field %q", field)
		}
	}
}

func TestAC_API_006_LoopbackCORS(t *testing.T) {
	d := startDaemon(t)
	allowed := "http://127.0.0.1:5173"

	resp, raw := d.do(t, http.MethodOptions, "/capabilities", nil, map[string]string{"Origin": allowed})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204; body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != allowed {
		t.Fatalf("Allow-Origin = %q, want %q", got, allowed)
	}

	resp, raw = d.do(t, http.MethodOptions, "/capabilities", nil, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin status = %d, want 403; body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("evil origin reflected: %q", got)
	}

	resp, raw = d.do(t, http.MethodGet, "/capabilities", nil, map[string]string{
		"Origin":        allowed,
		"Authorization": "Bearer " + d.token,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET with origin status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != allowed {
		t.Fatalf("GET Allow-Origin = %q, want %q", got, allowed)
	}
}

func TestAC_API_007_UnimplementedExplicit(t *testing.T) {
	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodGet, "/sandboxes", nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "not_implemented" {
		t.Fatalf("code = %q, want not_implemented", env.Error.Code)
	}
}
