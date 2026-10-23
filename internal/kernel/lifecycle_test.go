package kernel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func testKernelConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Listen:        "127.0.0.1:" + strconv.Itoa(freeTCPPort(t)),
		GatewayListen: "127.0.0.1:" + strconv.Itoa(freeTCPPort(t)),
		ProxyListen:   "127.0.0.1:" + strconv.Itoa(freeTCPPort(t)),
		Home:          t.TempDir(),
		HostHome:      t.TempDir(),
		DaemonToken:   "kernel-test-token-0123456789abcdef",
		LogLevel:      "error",
	}
}

func TestKernelNewHandlerAndRun(t *testing.T) {
	t.Setenv("NULLKEY_SECRET_BACKEND", "file")
	cfg := testKernelConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := k.Config(); got.Listen != cfg.Listen {
		t.Fatalf("Config listen = %q", got.Listen)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	k.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	gw := httptest.NewRecorder()
	gwReq := httptest.NewRequest(http.MethodGet, "/nope", nil)
	k.GatewayHandler().ServeHTTP(gw, gwReq)
	if gw.Code != http.StatusNotFound {
		t.Fatalf("gateway unknown status = %d", gw.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + cfg.Listen + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestKernelNewRejectsBadSecretBackend(t *testing.T) {
	t.Setenv("NULLKEY_SECRET_BACKEND", "bogus")
	cfg := testKernelConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(cfg, logger); err == nil {
		t.Fatal("New accepted an unsupported secret backend")
	}
}

func TestKernelRunListenConflict(t *testing.T) {
	t.Setenv("NULLKEY_SECRET_BACKEND", "file")
	cfg := testKernelConfig(t)
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("occupy control port: %v", err)
	}
	defer listener.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := k.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded with an occupied control port")
	}
}

func TestKernelHandlerCapabilities(t *testing.T) {
	t.Setenv("NULLKEY_SECRET_BACKEND", "file")
	cfg := testKernelConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.DaemonToken)
	k.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["tunnel"] != true || body["artifacts"] != true {
		t.Fatalf("capabilities not reflecting implemented features: %v", body)
	}
}
