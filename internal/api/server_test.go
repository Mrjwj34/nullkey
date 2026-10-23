package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "test-token-0123456789abcdef"

func newTestHandler() http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(Config{
		Token:        testToken,
		Version:      "0.1.0-test",
		StartedAt:    time.Now().Add(-time.Second),
		Capabilities: DefaultCapabilities(),
	}, logger).Handler()
}

func doRequest(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func authHeader() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testToken}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v; body=%q", err, rec.Body.String())
	}
	return env
}

func TestHealthPublic(t *testing.T) {
	h := newTestHandler()
	rec := doRequest(t, h, http.MethodGet, "/api/v1/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !got.OK || got.Version == "" {
		t.Fatalf("unexpected health body: %+v", got)
	}
	if got.UptimeMS < 0 {
		t.Fatalf("uptime_ms = %d, want >= 0", got.UptimeMS)
	}
	if !strings.HasPrefix(rec.Header().Get("X-Request-Id"), "req_") {
		t.Fatalf("X-Request-Id = %q, want req_ prefix", rec.Header().Get("X-Request-Id"))
	}
}

func TestAuthRequired(t *testing.T) {
	h := newTestHandler()

	rec := doRequest(t, h, http.MethodGet, "/api/v1/capabilities", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d, want 401", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Code != CodeUnauthorized {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeUnauthorized)
	}
	if env.Error.RequestID == "" || env.Error.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request_id mismatch: body=%q header=%q", env.Error.RequestID, rec.Header().Get("X-Request-Id"))
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header")
	}

	rec = doRequest(t, h, http.MethodGet, "/api/v1/capabilities", "", map[string]string{
		"Authorization": "Bearer wrong-token-0123456789",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", rec.Code)
	}

	rec = doRequest(t, h, http.MethodGet, "/api/v1/capabilities", "", authHeader())
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCapabilities(t *testing.T) {
	h := newTestHandler()
	rec := doRequest(t, h, http.MethodGet, "/api/v1/capabilities", "", authHeader())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var got Capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if len(got.Runtimes) == 0 || got.Runtimes[0] != "native" {
		t.Fatalf("runtimes = %v, want native", got.Runtimes)
	}
	if len(got.NetworkModes) != 3 {
		t.Fatalf("network_modes = %v, want 3", got.NetworkModes)
	}
	if got.NetworkModes[0] != "services_only" {
		t.Fatalf("network_modes[0] = %q, want services_only", got.NetworkModes[0])
	}
	if got.OAuthProviders == nil || got.MCPServers == nil {
		t.Fatalf("capability arrays must not be nil: %+v", got)
	}
}

func TestUnknownRoute(t *testing.T) {
	h := newTestHandler()
	rec := doRequest(t, h, http.MethodGet, "/api/v1/does-not-exist", "", authHeader())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
	if env.Error.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request_id mismatch")
	}
}

func TestStubRoute(t *testing.T) {
	h := newTestHandler()
	rec := doRequest(t, h, http.MethodGet, "/api/v1/sandboxes", "", authHeader())
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	env := decodeError(t, rec)
	if env.Error.Code != CodeNotImplemented {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotImplemented)
	}
}

func TestOAuthCallbackIsPublic(t *testing.T) {
	h := newTestHandler()
	rec := doRequest(t, h, http.MethodGet, "/api/v1/oauth/callback?code=x&state=y", "", nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (public but unimplemented)", rec.Code)
	}
}

func TestCORS(t *testing.T) {
	h := newTestHandler()
	allowed := "http://127.0.0.1:5173"

	rec := doRequest(t, h, http.MethodOptions, "/api/v1/capabilities", "", map[string]string{"Origin": allowed})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowed {
		t.Fatalf("Allow-Origin = %q, want %q", got, allowed)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Fatalf("Allow-Headers = %q, want Authorization", got)
	}

	rec = doRequest(t, h, http.MethodOptions, "/api/v1/capabilities", "", map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("evil origin status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("evil origin reflected: %q", got)
	}

	rec = doRequest(t, h, http.MethodGet, "/api/v1/capabilities", "", map[string]string{
		"Origin":        allowed,
		"Authorization": "Bearer " + testToken,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with allowed origin status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != allowed {
		t.Fatalf("GET Allow-Origin = %q, want %q", got, allowed)
	}
}

func TestRouteSpecs(t *testing.T) {
	specs := RouteSpecs()
	if len(specs) != 53 {
		t.Fatalf("route count = %d, want 53 (OpenAPI contract)", len(specs))
	}
	seen := make(map[string]bool, len(specs))
	implemented := 0
	for _, spec := range specs {
		key := spec.Method + " " + spec.Path
		if seen[key] {
			t.Fatalf("duplicate route %q", key)
		}
		seen[key] = true
		if !strings.HasPrefix(spec.Path, "/api/v1/") {
			t.Fatalf("route %q is outside /api/v1", spec.Path)
		}
		if spec.Method != strings.ToUpper(spec.Method) {
			t.Fatalf("route %q method is not uppercase", key)
		}
		if spec.Implemented {
			implemented++
		}
	}
	if !seen["GET /api/v1/health"] || !seen["GET /api/v1/capabilities"] {
		t.Fatal("system routes missing")
	}
	if implemented != 2 {
		t.Fatalf("implemented routes = %d, want 2 at P0", implemented)
	}
}

func TestParseBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"Bearer  abc", "abc", true},
		{"", "", false},
		{"Bearer", "", false},
		{"Basic abc", "", false},
		{"Bearer a b", "", false},
	}
	for _, tc := range cases {
		got, ok := parseBearer(tc.header)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseBearer(%q) = (%q,%v), want (%q,%v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTokenEqual(t *testing.T) {
	if !tokenEqual("abc", "abc") {
		t.Fatal("equal tokens should match")
	}
	if tokenEqual("abc", "abd") {
		t.Fatal("different tokens should not match")
	}
	if tokenEqual("", "") {
		t.Fatal("empty tokens must not match")
	}
}

func TestLoopbackOrigin(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:5173":         true,
		"http://localhost:3000":         true,
		"http://[::1]:8080":             true,
		"https://127.0.0.1":             true,
		"https://evil.example":          false,
		"file:///tmp/x":                 false,
		"http://127.0.0.1@evil.example": false,
		"":                              false,
	}
	for origin, want := range cases {
		if got := isLoopbackOrigin(origin); got != want {
			t.Fatalf("isLoopbackOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}
