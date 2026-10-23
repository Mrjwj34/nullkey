package api

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNilServiceRoutes(t *testing.T) {
	h := newNilServiceHandler(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_ = logger

	for _, route := range routeTable {
		path := strings.NewReplacer(
			"{id}", "x",
			"{job_id}", "x",
			"{app_id}", "x",
			"{flow_id}", "x",
			"{credential_id}", "x",
			"{service_id}", "x",
		).Replace(route.Path)

		want := http.StatusServiceUnavailable
		switch route.Path {
		case "/api/v1/health", "/api/v1/capabilities":
			want = http.StatusOK
		case "/api/v1/apps/{id}/install",
			"/api/v1/sandboxes/{id}/apps/{app_id}/launch",
			"/api/v1/sandboxes/{id}/desktop":
			want = http.StatusNotImplemented
		}

		var rec *httptest.ResponseRecorder
		if route.Path == "/api/v1/health" {
			rec = doRequest(t, h, route.Method, path, "", nil)
		} else {
			rec = doRequest(t, h, route.Method, path, "", authHeader())
		}
		if rec.Code != want {
			t.Fatalf("%s %s status = %d, want %d; body=%s", route.Method, route.Path, rec.Code, want, rec.Body.String())
		}
	}
}

func TestNewServerDefaults(t *testing.T) {
	srv := NewServer(Config{}, nil)
	if srv.cfg.Version != "0.0.0" {
		t.Fatalf("version = %q, want 0.0.0", srv.cfg.Version)
	}
	if srv.cfg.StartedAt.IsZero() || srv.started.IsZero() {
		t.Fatal("StartedAt was not defaulted")
	}
	if srv.log == nil {
		t.Fatal("logger was not defaulted")
	}
	caps := srv.cfg.Capabilities
	if caps.Runtimes == nil || caps.NetworkModes == nil || caps.OAuthProviders == nil || caps.MCPServers == nil {
		t.Fatalf("capabilities contain nil slices: %+v", caps)
	}
	if srv.Handler() == nil {
		t.Fatal("Handler() is nil")
	}
}

func TestHandleNotImplemented(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/future", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_ni"))
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", "req_ni")
	srv.handleNotImplemented(rec, req)
	env := wantErrorCode(t, rec, http.StatusNotImplemented, CodeNotImplemented)
	if env.Error.Details["method"] != http.MethodGet || env.Error.Details["path"] != "/api/v1/future" {
		t.Fatalf("unexpected details: %+v", env.Error.Details)
	}
}

func TestRecoverPanic(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_panic"))

	t.Run("writes 500 before status", func(t *testing.T) {
		h := srv.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("kaboom")
		}))
		rec := httptest.NewRecorder()
		rec.Header().Set("X-Request-Id", "req_panic")
		h.ServeHTTP(rec, req)
		wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)
	})

	t.Run("keeps already written status", func(t *testing.T) {
		h := srv.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("after headers")
		}))
		rec := httptest.NewRecorder()
		sw := &statusWriter{ResponseWriter: rec}
		sw.WriteHeader(http.StatusAccepted)
		h.ServeHTTP(sw, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty", rec.Body.String())
		}
	})

	t.Run("repanics ErrAbortHandler", func(t *testing.T) {
		h := srv.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))
		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Fatalf("recover = %v, want http.ErrAbortHandler", got)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), req)
	})
}

func TestRequestIDAndSecurityHeaders(t *testing.T) {
	h := newNilServiceHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/api/v1/health", "", nil)
	if !strings.HasPrefix(rec.Header().Get("X-Request-Id"), "req_") {
		t.Fatalf("X-Request-Id = %q", rec.Header().Get("X-Request-Id"))
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}
}

func TestCORSExtra(t *testing.T) {
	h := newNilServiceHandler(t)

	rec := doRequest(t, h, http.MethodOptions, "/api/v1/capabilities", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS without origin status = %d, want 204", rec.Code)
	}

	rec = doRequest(t, h, http.MethodGet, "/api/v1/health", "", map[string]string{"Origin": "https://evil.example"})
	wantErrorCode(t, rec, http.StatusForbidden, CodeInvalidRequest)

	rec = doRequest(t, h, http.MethodOptions, "/api/v1/capabilities", "", map[string]string{"Origin": "http://127.0.0.1:5173"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Request-Id") {
		t.Fatalf("Expose-Headers = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q", got)
	}
}

func TestIsPublicPath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/health":         true,
		"/api/v1/oauth/callback": true,
		"/api/v1/capabilities":   false,
		"/api/v1/workspaces":     false,
		"":                       false,
	}
	for path, want := range cases {
		if got := isPublicPath(path); got != want {
			t.Fatalf("isPublicPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestStatusWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.WriteHeader(http.StatusCreated)
	sw.WriteHeader(http.StatusInternalServerError)
	if sw.status != http.StatusCreated || rec.Code != http.StatusCreated {
		t.Fatalf("status = %d/%d, want 201", sw.status, rec.Code)
	}
	n, err := sw.Write([]byte("abc"))
	if err != nil || n != 3 || sw.bytes != 3 {
		t.Fatalf("Write = (%d,%v), bytes=%d", n, err, sw.bytes)
	}
	if sw.Unwrap() != http.ResponseWriter(rec) {
		t.Fatalf("Unwrap did not return the wrapped writer")
	}
	sw.Flush()
	if !rec.Flushed {
		t.Fatal("Flush was not forwarded")
	}

	implicit := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	implicit.Write(nil)
	if implicit.status != http.StatusOK {
		t.Fatalf("implicit status = %d, want 200", implicit.status)
	}

	plain := &plainWriter{header: http.Header{}}
	noFlush := &statusWriter{ResponseWriter: plain}
	noFlush.Flush() // must not panic when the underlying writer has no Flush

	if _, _, err := (&statusWriter{ResponseWriter: plain}).Hijack(); err == nil {
		t.Fatal("Hijack without Hijacker succeeded")
	}

	hijack := &hijackWriter{header: http.Header{}}
	hw := &statusWriter{ResponseWriter: hijack}
	if _, _, err := hw.Hijack(); err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	if hw.status != http.StatusSwitchingProtocols {
		t.Fatalf("hijack status = %d, want 101", hw.status)
	}
}

type plainWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *plainWriter) Header() http.Header { return w.header }
func (w *plainWriter) WriteHeader(status int) {
	w.status = status
}
func (w *plainWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

type hijackWriter struct {
	header http.Header
}

func (w *hijackWriter) Header() http.Header         { return w.header }
func (w *hijackWriter) WriteHeader(int)             {}
func (w *hijackWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

func TestAccessLogZeroStatus(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	srv.accessLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
}

func TestEventsWithoutFlusher(t *testing.T) {
	api := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req_sse"))
	rec := &plainWriter{header: http.Header{}}
	rec.header.Set("X-Request-Id", "req_sse")
	api.server.handleEvents(rec, req)
	if rec.status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.status)
	}
	if !strings.Contains(rec.body.String(), `"code":"internal"`) {
		t.Fatalf("unexpected body: %s", rec.body.String())
	}
}
