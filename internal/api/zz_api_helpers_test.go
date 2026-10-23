package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// testAPI bundles an in-process control-plane server with its real service,
// store and home directory so handlers can be exercised without a native
// runtime or any network access.
type testAPI struct {
	t       *testing.T
	handler http.Handler
	server  *Server
	svc     *core.Service
	store   *sqlite.Store
	home    string
	logger  *slog.Logger
}

// newTestAPI builds a handler backed by a real core.Service and a temporary
// SQLite store.
func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	return newTestAPIWith(t, nil)
}

// newTestAPIWith is newTestAPI with a hook to customize core.Options (MCP,
// HostImportRoot, ...).
func newTestAPIWith(t *testing.T, configure func(*core.Options)) *testAPI {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	home := t.TempDir()
	store, err := sqlite.Open(context.Background(), home)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opts := core.Options{
		Home:        home,
		Store:       store,
		Secrets:     secret.NewMemory(),
		Terminal:    terminal.NewHub(logger),
		Artifacts:   artifacts.New(),
		Events:      events.New(16),
		Audit:       audit.New(store, logger),
		Logger:      logger,
		GatewayAddr: "127.0.0.1:49153",
		ProxyAddr:   "127.0.0.1:49154",
		ControlAddr: "127.0.0.1:49152",
	}
	if configure != nil {
		configure(&opts)
	}
	svc, err := core.New(opts)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	srv := NewServer(Config{
		Token:        testToken,
		Version:      "0.1.0-test",
		StartedAt:    time.Now().Add(-time.Second),
		Capabilities: DefaultCapabilities(),
		Service:      svc,
	}, logger)
	return &testAPI{
		t:       t,
		handler: srv.Handler(),
		server:  srv,
		svc:     svc,
		store:   store,
		home:    home,
		logger:  logger,
	}
}

// newNilServiceHandler returns a fully routed handler without a kernel service
// so every handler's "kernel is not ready" path can be exercised.
func newNilServiceHandler(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(Config{
		Token:     testToken,
		Version:   "0.1.0-test",
		StartedAt: time.Now(),
	}, logger)
	return srv.Handler()
}

func (a *testAPI) request(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
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
	a.handler.ServeHTTP(rec, req)
	return rec
}

func (a *testAPI) auth(method, path, body string) *httptest.ResponseRecorder {
	return a.request(method, path, body, authHeader())
}

func (a *testAPI) authWith(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	merged := map[string]string{"Authorization": "Bearer " + testToken}
	for k, v := range headers {
		merged[k] = v
	}
	return a.request(method, path, body, merged)
}

func (a *testAPI) postJSON(path string, payload any) *httptest.ResponseRecorder {
	raw, err := json.Marshal(payload)
	if err != nil {
		a.t.Fatalf("marshal payload: %v", err)
	}
	return a.auth(http.MethodPost, path, string(raw))
}

func (a *testAPI) createWorkspace(t *testing.T, body map[string]any) domain.Workspace {
	t.Helper()
	rec := a.postJSON("/api/v1/workspaces", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create workspace status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	return mustDecode[domain.Workspace](t, rec)
}

func (a *testAPI) createState(t *testing.T, harnessID, name string) domain.State {
	t.Helper()
	rec := a.postJSON("/api/v1/states", map[string]string{"harness_id": harnessID, "name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create state status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	return mustDecode[domain.State](t, rec)
}

func (a *testAPI) createCredential(t *testing.T, body map[string]any) domain.Credential {
	t.Helper()
	rec := a.postJSON("/api/v1/credentials", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create credential status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	return mustDecode[domain.Credential](t, rec)
}

func (a *testAPI) insertSandbox(t *testing.T, sb domain.Sandbox) domain.Sandbox {
	t.Helper()
	if sb.CreatedAt.IsZero() {
		sb.CreatedAt = time.Now().UTC()
	}
	if sb.Runtime == "" {
		sb.Runtime = domain.RuntimeNative
	}
	if sb.NetworkMode == "" {
		sb.NetworkMode = domain.NetworkFiltered
	}
	if sb.Status == "" {
		sb.Status = domain.SandboxRunning
	}
	if err := a.store.CreateSandbox(context.Background(), sb); err != nil {
		t.Fatalf("insert sandbox: %v", err)
	}
	return sb
}

func mustDecode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v; body=%q", err, rec.Body.String())
	}
	return out
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, want, rec.Body.String())
	}
}

func wantErrorCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code ErrorCode) errorEnvelope {
	t.Helper()
	wantStatus(t, rec, status)
	env := decodeError(t, rec)
	if env.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body=%s", env.Error.Code, code, rec.Body.String())
	}
	if env.Error.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request_id mismatch: body=%q header=%q", env.Error.RequestID, rec.Header().Get("X-Request-Id"))
	}
	return env
}

// ---------------------------------------------------------------- fake PTY

// fakeTerminalSource is a blocking terminal.Source with no real PTY.
type fakeTerminalSource struct {
	done     chan struct{}
	closeOne sync.Once
}

func newFakeTerminalSource() *fakeTerminalSource {
	return &fakeTerminalSource{done: make(chan struct{})}
}

func (f *fakeTerminalSource) Read([]byte) (int, error) {
	<-f.done
	return 0, io.EOF
}

func (f *fakeTerminalSource) Write(p []byte) (int, error) { return len(p), nil }

func (f *fakeTerminalSource) Close() error {
	f.closeOne.Do(func() { close(f.done) })
	return nil
}

func (f *fakeTerminalSource) Resize(int, int) error { return nil }
func (f *fakeTerminalSource) Size() (int, int)      { return 80, 24 }
func (f *fakeTerminalSource) Done() <-chan struct{} { return f.done }
func (f *fakeTerminalSource) ExitCode() int         { return 0 }

// --------------------------------------------------------------- fake MCP

// fakeMCP is an in-process core.MCPProxy.
type fakeMCP struct {
	testTools []string
	testErr   error
	proxyErr  error
	proxied   bool
}

func (f *fakeMCP) Test(context.Context, core.MCPConn) ([]string, error) {
	return f.testTools, f.testErr
}

func (f *fakeMCP) Proxy(_ context.Context, _ core.MCPConn, w http.ResponseWriter, _ *http.Request) error {
	f.proxied = true
	if f.proxyErr != nil {
		return f.proxyErr
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
	return nil
}

// --------------------------------------------------------- helper coverage

func TestPageParams(t *testing.T) {
	cases := []struct {
		query      string
		wantLimit  int
		wantCursor string
	}{
		{"", 0, ""},
		{"?limit=7", 7, ""},
		{"?limit=abc", 0, ""},
		{"?limit=-3", -3, ""},
		{"?cursor=abc", 0, "abc"},
		{"?limit=2&cursor=xyz", 2, "xyz"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/things"+tc.query, nil)
		limit, cursor := pageParams(req)
		if limit != tc.wantLimit || cursor != tc.wantCursor {
			t.Fatalf("pageParams(%q) = (%d,%q), want (%d,%q)", tc.query, limit, cursor, tc.wantLimit, tc.wantCursor)
		}
	}
}

func TestQueryBool(t *testing.T) {
	cases := []struct {
		query   string
		want    bool
		wantErr bool
	}{
		{"", false, false},
		{"?confirm=true", true, false},
		{"?confirm=false", false, false},
		{"?confirm=1", true, false},
		{"?confirm=0", false, false},
		{"?confirm=notabool", false, true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/things"+tc.query, nil)
		got, err := queryBool(req, "confirm")
		if tc.wantErr {
			if err == nil {
				t.Fatalf("queryBool(%q) succeeded, want error", tc.query)
			}
			ce, ok := core.AsError(err)
			if !ok || ce.Status != http.StatusBadRequest || ce.Code != "invalid_request" {
				t.Fatalf("queryBool(%q) error = %#v, want typed 400 invalid_request", tc.query, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("queryBool(%q): %v", tc.query, err)
		}
		if got != tc.want {
			t.Fatalf("queryBool(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestDecodeBody(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.Body = nil
		rec := httptest.NewRecorder()
		got, ok := decodeBody[struct {
			Name string `json:"name"`
		}](rec, req)
		if !ok || got.Name != "" {
			t.Fatalf("decodeBody(nil) = (%+v,%v), want zero,true", got, ok)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("nil body wrote status %d", rec.Code)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
		rec := httptest.NewRecorder()
		got, ok := decodeBody[struct {
			Name string `json:"name"`
		}](rec, req)
		if !ok || got.Name != "" {
			t.Fatalf("decodeBody(empty) = (%+v,%v), want zero,true", got, ok)
		}
	})

	t.Run("valid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"abc"}`))
		rec := httptest.NewRecorder()
		got, ok := decodeBody[struct {
			Name string `json:"name"`
		}](rec, req)
		if !ok || got.Name != "abc" {
			t.Fatalf("decodeBody(valid) = (%+v,%v), want abc,true", got, ok)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{`))
		req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_test"))
		rec := httptest.NewRecorder()
		rec.Header().Set("X-Request-Id", "req_test")
		_, ok := decodeBody[struct {
			Name string `json:"name"`
		}](rec, req)
		if ok {
			t.Fatal("decodeBody(invalid) returned ok")
		}
		wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
		if !strings.Contains(rec.Body.String(), `"request_id":"req_test"`) {
			t.Fatalf("missing request id in body: %s", rec.Body.String())
		}
	})
}

func TestWriteAPIError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_api_error"))
	rec := httptest.NewRecorder()
	writeAPIError(rec, req, http.StatusTeapot, CodeConflict, "conflict", map[string]any{"x": 1})
	wantStatus(t, rec, http.StatusTeapot)
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	env := decodeError(t, rec)
	if env.Error.Code != CodeConflict || env.Error.RequestID != "req_api_error" || env.Error.Details["x"] != float64(1) {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestWriteList(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	rec := httptest.NewRecorder()
	srv.writeList(rec, req, http.StatusOK, []string{"a"}, "")
	body := mustDecode[struct {
		Items      []string `json:"items"`
		NextCursor *string  `json:"next_cursor"`
	}](t, rec)
	if len(body.Items) != 1 || body.NextCursor != nil {
		t.Fatalf("unexpected list body: %+v", body)
	}

	rec = httptest.NewRecorder()
	srv.writeList(rec, req, http.StatusOK, []string{"a"}, "next")
	body = mustDecode[struct {
		Items      []string `json:"items"`
		NextCursor *string  `json:"next_cursor"`
	}](t, rec)
	if body.NextCursor == nil || *body.NextCursor != "next" {
		t.Fatalf("next_cursor = %v, want next", body.NextCursor)
	}
}

func TestServerFail(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &Server{log: logger}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_fail"))

	// nil error is a no-op.
	rec := httptest.NewRecorder()
	srv.fail(rec, req, nil)
	if rec.Body.Len() != 0 {
		t.Fatalf("fail(nil) wrote body %q", rec.Body.String())
	}

	// Typed core error keeps status/code/message/details.
	rec = httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", "req_fail")
	srv.fail(rec, req, core.Invalid("bad input").WithDetails(map[string]any{"field": "name"}))
	env := wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	if env.Error.Message != "bad input" || env.Error.Details["field"] != "name" {
		t.Fatalf("unexpected typed error: %+v", env)
	}

	// Untyped error is hidden behind a generic 500.
	rec = httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", "req_fail")
	srv.fail(rec, req, errors.New("boom: sk-test-must-not-leak"))
	env = wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)
	if strings.Contains(rec.Body.String(), "sk-test-must-not-leak") || strings.Contains(env.Error.Message, "boom") {
		t.Fatalf("internal error leaked: %s", rec.Body.String())
	}
}

func TestWriteJSONNilPayload(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	srv.writeJSON(rec, req, http.StatusNoContent, nil)
	wantStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 {
		t.Fatalf("nil payload wrote body %q", rec.Body.String())
	}
}

func TestWriteJSONEncodeFailure(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	srv.writeJSON(rec, req, http.StatusOK, math.Inf(1))
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.Len() != 0 {
		t.Fatalf("encode failure wrote body %q", rec.Body.String())
	}
}

func TestRecorderAndCopyHeaders(t *testing.T) {
	rec := newRecorder()
	rec.Header().Set("X-Recorder", "yes")
	if got := rec.Header().Get("X-Recorder"); got != "yes" {
		t.Fatalf("Header().Get = %q", got)
	}
	rec.WriteHeader(http.StatusCreated)
	rec.WriteHeader(http.StatusInternalServerError)
	if rec.status != http.StatusCreated {
		t.Fatalf("first status lost: %d", rec.status)
	}
	if _, err := rec.Write([]byte("body")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := rec.body.String(); got != "body" {
		t.Fatalf("body = %q", got)
	}
	rec.Flush()

	second := newRecorder()
	second.Write(nil)
	if second.status != http.StatusOK {
		t.Fatalf("implicit status = %d, want 200", second.status)
	}

	src := http.Header{}
	src.Set("Content-Length", "10")
	src.Add("X-Test", "one")
	src.Add("X-Test", "two")
	dst := http.Header{}
	copyRecorderHeaders(dst, src)
	if dst.Get("Content-Length") != "" {
		t.Fatalf("Content-Length was copied: %q", dst.Get("Content-Length"))
	}
	if got := dst.Values("X-Test"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("X-Test = %v", got)
	}
}

type failingBodyReader struct{}

func (failingBodyReader) Read([]byte) (int, error) { return 0, errors.New("body read failed") }

func TestIdempotencyBodyReadFailure(t *testing.T) {
	api := newTestAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", nil)
	req.Body = io.NopCloser(failingBodyReader{})
	req.Header.Set("Idempotency-Key", "read-failure")
	rec := httptest.NewRecorder()
	api.server.idempotency(func(_ *Server, w http.ResponseWriter, r *http.Request) {
		api.server.handleCreateWorkspace(w, r)
	}, rec, req)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
}

func TestIdempotencyEmptyResponse(t *testing.T) {
	api := newTestAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", strings.NewReader(""))
	req.Header.Set("Idempotency-Key", "empty-response")
	rec := httptest.NewRecorder()
	api.server.idempotency(func(*Server, http.ResponseWriter, *http.Request) {}, rec, req)
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

func TestIdempotencyContract(t *testing.T) {
	api := newTestAPI(t)
	body := `{"name":"idempotent"}`

	first := api.authWith(http.MethodPost, "/api/v1/workspaces", body, map[string]string{
		"Idempotency-Key": "idem-success",
	})
	wantStatus(t, first, http.StatusCreated)
	if got := first.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("first Content-Type = %q", got)
	}

	replay := api.authWith(http.MethodPost, "/api/v1/workspaces", body, map[string]string{
		"Idempotency-Key": "idem-success",
	})
	wantStatus(t, replay, http.StatusCreated)
	if got := replay.Header().Get("Idempotent-Replayed"); got != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", got)
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replayed body differs:\nfirst=%s\nreplay=%s", first.Body.String(), replay.Body.String())
	}
	if got := replay.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("replayed Content-Type = %q", got)
	}

	// Reusing a key with a different body is rejected.
	conflict := api.authWith(http.MethodPost, "/api/v1/workspaces", `{"name":"different"}`, map[string]string{
		"Idempotency-Key": "idem-success",
	})
	wantErrorCode(t, conflict, http.StatusBadRequest, CodeInvalidRequest)

	// Error responses are replayed too.
	badFirst := api.authWith(http.MethodPost, "/api/v1/workspaces", `{}`, map[string]string{
		"Idempotency-Key": "idem-failure",
	})
	wantErrorCode(t, badFirst, http.StatusBadRequest, CodeInvalidRequest)
	badReplay := api.authWith(http.MethodPost, "/api/v1/workspaces", `{}`, map[string]string{
		"Idempotency-Key": "idem-failure",
	})
	wantStatus(t, badReplay, http.StatusBadRequest)
	if env := decodeError(t, badReplay); env.Error.Code != CodeInvalidRequest {
		t.Fatalf("replayed error code = %q", env.Error.Code)
	}
	if got := badReplay.Header().Get("Idempotent-Replayed"); got != "true" {
		t.Fatalf("error replay header = %q", got)
	}
	firstEnv := decodeError(t, badFirst)
	replayEnv := decodeError(t, badReplay)
	if replayEnv.Error.Code != firstEnv.Error.Code || replayEnv.Error.Message != firstEnv.Error.Message {
		t.Fatalf("error replay body differs:\nfirst=%s\nreplay=%s", badFirst.Body.String(), badReplay.Body.String())
	}
	if replayEnv.Error.RequestID != badReplay.Header().Get("X-Request-Id") {
		t.Fatalf("replayed request_id %q does not match header %q", replayEnv.Error.RequestID, badReplay.Header().Get("X-Request-Id"))
	}

	// Non-POST requests are unaffected by Idempotency-Key.
	get := api.authWith(http.MethodGet, "/api/v1/capabilities", "", map[string]string{
		"Idempotency-Key": "idem-success",
	})
	wantStatus(t, get, http.StatusOK)
	if got := get.Header().Get("Idempotent-Replayed"); got != "" {
		t.Fatalf("GET was replayed: %q", got)
	}
}

func TestIdempotencyKeyTooLong(t *testing.T) {
	api := newTestAPI(t)
	rec := api.authWith(http.MethodPost, "/api/v1/workspaces", `{"name":"x"}`, map[string]string{
		"Idempotency-Key": strings.Repeat("k", 201),
	})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
}

// decodeBase64String decodes the API's binary workspace-file payload.
func decodeBase64String(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return raw
}
