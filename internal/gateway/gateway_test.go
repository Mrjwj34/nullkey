package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/artifacts"
	"github.com/Mrjwj34/nullkey/internal/audit"
	"github.com/Mrjwj34/nullkey/internal/core"
	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/events"
	"github.com/Mrjwj34/nullkey/internal/secret"
	"github.com/Mrjwj34/nullkey/internal/store/sqlite"
	"github.com/Mrjwj34/nullkey/internal/terminal"
)

const testSandboxToken = "sbx-token-0123456789abcdef"

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

type gatewayFixture struct {
	srv     *Server
	handler http.Handler
	store   *sqlite.Store
	logs    *bytes.Buffer
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	store, err := sqlite.Open(ctx, home)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := core.New(core.Options{
		Home:      home,
		Store:     store,
		Secrets:   secret.NewMemory(),
		Terminal:  terminal.NewHub(logger),
		Events:    events.New(128),
		Audit:     audit.New(store, logger),
		Artifacts: artifacts.New(),
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}

	hash := sha256.Sum256([]byte(testSandboxToken))
	sandbox := domain.Sandbox{
		ID:          "sbx_gateway_test",
		HarnessID:   "shell",
		WorkspaceID: "ws_gateway_test",
		Runtime:     domain.RuntimeNative,
		NetworkMode: domain.NetworkFiltered,
		Status:      domain.SandboxRunning,
		TokenHash:   hash[:],
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.CreateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}

	srv := New(svc, logger)
	return &gatewayFixture{srv: srv, handler: srv.Handler(), store: store, logs: logs}
}

func (f *gatewayFixture) do(t *testing.T, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope from %q: %v", rec.Body.String(), err)
	}
	return env
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorEnvelope {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	env := decodeEnvelope(t, rec)
	if env.Error.Code != code {
		t.Fatalf("error code = %q, want %q", env.Error.Code, code)
	}
	return env
}

func TestNewNilLoggerUsesDefault(t *testing.T) {
	srv := New(nil, nil)
	if srv == nil || srv.log == nil {
		t.Fatal("New(nil, nil) did not install a logger")
	}
	f := newGatewayFixture(t)
	if f.srv.log == nil {
		t.Fatal("fixture gateway has no logger")
	}
}

func TestDataPlaneNotFound(t *testing.T) {
	f := newGatewayFixture(t)
	for _, target := range []string{"/", "/not-a-route", "/terminal/extra", "/tunnelx"} {
		t.Run(target, func(t *testing.T) {
			rec := f.do(t, http.MethodGet, target, "")
			env := assertError(t, rec, http.StatusNotFound, "not_found")
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if !strings.HasPrefix(rec.Header().Get("X-Request-Id"), "req_") {
				t.Fatalf("X-Request-Id = %q", rec.Header().Get("X-Request-Id"))
			}
			if env.Error.RequestID == "" || env.Error.RequestID != rec.Header().Get("X-Request-Id") {
				t.Fatalf("envelope request_id %q does not match header %q", env.Error.RequestID, rec.Header().Get("X-Request-Id"))
			}
		})
	}
}

func TestAuthenticateRejectsBadTokens(t *testing.T) {
	f := newGatewayFixture(t)
	cases := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"scheme only", "Bearer"},
		{"wrong scheme", "Basic " + testSandboxToken},
		{"empty token", "Bearer "},
		{"extra fields", "Bearer one two"},
		{"unknown token", "Bearer sbx-unknown-token"},
		{"daemon token style", "Bearer daemon-token-0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			env := assertError(t, rec, http.StatusUnauthorized, "token_invalid")
			if env.Error.Message == "" {
				t.Fatal("token_invalid envelope has an empty message")
			}
		})
	}

	// A stopped sandbox's token is no longer accepted: token hashes are
	// invalidated on stop.
	if err := f.store.SetSandboxStopped(context.Background(), "sbx_gateway_test", time.Now().UTC()); err != nil {
		t.Fatalf("SetSandboxStopped: %v", err)
	}
	rec := f.do(t, http.MethodGet, "/openai/v1/models", testSandboxToken)
	assertError(t, rec, http.StatusUnauthorized, "token_invalid")
}

func TestModelRoutesWithoutCredential(t *testing.T) {
	f := newGatewayFixture(t)
	for _, tc := range []struct {
		method string
		target string
	}{
		{http.MethodGet, "/openai/v1/models"},
		{http.MethodPost, "/openai/v1/chat/completions"},
		{http.MethodPost, "/anthropic/v1/messages"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			rec := f.do(t, tc.method, tc.target, testSandboxToken)
			env := assertError(t, rec, http.StatusUnauthorized, "reauth_required")
			if !strings.Contains(strings.ToLower(env.Error.Message), "credential") {
				t.Fatalf("message = %q, want credential guidance", env.Error.Message)
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Fatal("missing X-Request-Id on gateway service error")
			}
		})
	}
}

func TestMCPGatewayMissingConnection(t *testing.T) {
	f := newGatewayFixture(t)

	// Empty connection id is rejected by the gateway itself.
	rec := f.do(t, http.MethodPost, "/mcp/", testSandboxToken)
	assertError(t, rec, http.StatusNotFound, "not_found")

	// Unknown connection id fails closed in the core MCP gateway.
	rec = f.do(t, http.MethodPost, "/mcp/mcp_unknown", testSandboxToken)
	env := assertError(t, rec, http.StatusNotFound, "not_found")
	if !strings.Contains(env.Error.Message, "MCP connection") {
		t.Fatalf("message = %q", env.Error.Message)
	}

	// Authentication still wins over routing.
	rec = f.do(t, http.MethodPost, "/mcp/", "")
	assertError(t, rec, http.StatusUnauthorized, "token_invalid")
}

func TestGitGatewayRoutingAndCredentialFailClosed(t *testing.T) {
	f := newGatewayFixture(t)

	rec := f.do(t, http.MethodGet, "/git/github/", testSandboxToken)
	assertError(t, rec, http.StatusNotFound, "not_found")

	rec = f.do(t, http.MethodGet, "/git/github/owner/repo.git/info/refs?service=git-upload-pack", testSandboxToken)
	assertError(t, rec, http.StatusUnauthorized, "reauth_required")

	rec = f.do(t, http.MethodGet, "/git/github/owner/repo.git/info/refs", "")
	assertError(t, rec, http.StatusUnauthorized, "token_invalid")
}

func TestTerminalRequiresValidTicket(t *testing.T) {
	f := newGatewayFixture(t)

	for _, target := range []string{"/terminal", "/terminal?ticket=not-a-real-ticket"} {
		t.Run(target, func(t *testing.T) {
			rec := f.do(t, http.MethodGet, target, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "ticket_invalid") {
				t.Fatalf("body = %q, want ticket_invalid", rec.Body.String())
			}
		})
	}
}

func TestTunnelRequiresValidTicket(t *testing.T) {
	f := newGatewayFixture(t)

	for _, target := range []string{"/tunnel/", "/tunnel/?ticket=not-a-real-ticket"} {
		t.Run(target, func(t *testing.T) {
			rec := f.do(t, http.MethodGet, target, "daemon-token-not-a-ticket")
			env := assertError(t, rec, http.StatusUnauthorized, "ticket_invalid")
			_ = env
		})
	}

	// Non-loopback Origins are rejected before ticket handling.
	req := httptest.NewRequest(http.MethodGet, "/tunnel/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	assertError(t, rec, http.StatusForbidden, "origin_not_allowed")
}

func TestStatusWriter(t *testing.T) {
	t.Run("Write sets implicit 200 and counts bytes", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &statusWriter{ResponseWriter: rec}
		n, err := w.Write([]byte("hello"))
		if err != nil || n != 5 {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if w.status != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.status)
		}
		if w.bytes != 5 {
			t.Fatalf("bytes = %d, want 5", w.bytes)
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
			t.Fatalf("underlying recorder = %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("first WriteHeader wins", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &statusWriter{ResponseWriter: rec}
		w.WriteHeader(http.StatusCreated)
		w.WriteHeader(http.StatusTeapot)
		if w.status != http.StatusCreated || rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, recorder = %d, want 201", w.status, rec.Code)
		}
	})

	t.Run("WriteHeader after Write keeps implicit 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &statusWriter{ResponseWriter: rec}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusInternalServerError)
		if w.status != http.StatusOK || rec.Code != http.StatusOK {
			t.Fatalf("status = %d, recorder = %d, want 200", w.status, rec.Code)
		}
	})

	t.Run("Flush forwards to flusher", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &statusWriter{ResponseWriter: rec}
		w.WriteHeader(http.StatusAccepted)
		w.Flush()
		if !rec.Flushed {
			t.Fatal("Flush did not reach the underlying http.Flusher")
		}
	})

	t.Run("Flush without flusher is a no-op", func(t *testing.T) {
		w := &statusWriter{ResponseWriter: plainResponseWriter{header: make(http.Header)}}
		w.Flush()
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatalf("Write after Flush: %v", err)
		}
	})

	t.Run("Unwrap exposes the wrapped writer", func(t *testing.T) {
		rec := httptest.NewRecorder()
		w := &statusWriter{ResponseWriter: rec}
		if got := w.Unwrap(); got != rec {
			t.Fatalf("Unwrap = %v, want recorder", got)
		}
	})
}

type plainResponseWriter struct {
	header http.Header
}

func (w plainResponseWriter) Header() http.Header         { return w.header }
func (w plainResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w plainResponseWriter) WriteHeader(int)             {}

func TestAccessLog(t *testing.T) {
	f := newGatewayFixture(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := New(f.srv.svc, logger)
	handler := srv.accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("teapot"))
	}))
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions?api_key=super-secret", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d", rec.Code)
	}
	logged := buf.String()
	for _, want := range []string{
		"gateway_request",
		"method=POST",
		"path=/openai/v1/chat/completions",
		"status=418",
		"bytes=6",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log missing %q: %s", want, logged)
		}
	}
	if strings.Contains(logged, "super-secret") || strings.Contains(logged, "api_key=") {
		t.Fatalf("access log leaked a query string: %s", logged)
	}

	// A handler that writes nothing is logged as an implicit 200.
	buf.Reset()
	empty := srv.accessLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	empty.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/empty", nil))
	if !strings.Contains(buf.String(), "status=200") {
		t.Fatalf("empty handler was not logged as 200: %s", buf.String())
	}
}

func TestWriteErrorFallback(t *testing.T) {
	f := newGatewayFixture(t)

	rec := httptest.NewRecorder()
	f.srv.writeError(rec, errors.New("boom: secret-value-that-must-not-leak"))
	assertError(t, rec, http.StatusInternalServerError, "internal")
	if strings.Contains(rec.Body.String(), "secret-value") {
		t.Fatalf("internal error detail leaked to client: %s", rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing request id on internal error")
	}

	rec = httptest.NewRecorder()
	f.srv.writeError(rec, core.E(http.StatusForbidden, "network_denied", "denied"))
	assertError(t, rec, http.StatusForbidden, "network_denied")

	rec = httptest.NewRecorder()
	f.srv.writeJSONError(rec, http.StatusBadRequest, "bad", "bad request", "req_fixed")
	if got := rec.Header().Get("X-Request-Id"); got != "req_fixed" {
		t.Fatalf("X-Request-Id = %q, want req_fixed", got)
	}
	env := decodeEnvelope(t, rec)
	if env.Error.RequestID != "req_fixed" || env.Error.Code != "bad" {
		t.Fatalf("envelope = %+v", env)
	}

	rec = httptest.NewRecorder()
	f.srv.writeJSONError(rec, http.StatusConflict, "conflict", "conflict", "")
	if got := rec.Header().Get("X-Request-Id"); !strings.HasPrefix(got, "req_") {
		t.Fatalf("generated X-Request-Id = %q", got)
	}
}

func TestBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"BEARER abc", "abc", true},
		{"  Bearer   abc  ", "abc", true},
		{"", "", false},
		{"Bearer", "", false},
		{"Basic abc", "", false},
		{"Bearer a b", "", false},
		{"Bearer ", "", false},
	}
	for _, tc := range cases {
		got, ok := bearer(tc.header)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("bearer(%q) = %q,%v; want %q,%v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}
