package kernel

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewServesHealth(t *testing.T) {
	cfg := validConfig(t)
	cfg.DaemonToken = "kernel-token-0123456789abcdef"

	k, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !body.OK || body.Version == "" {
		t.Fatalf("unexpected health: %+v", body)
	}
}

func TestNewRejectsNonLoopback(t *testing.T) {
	cfg := validConfig(t)
	cfg.Listen = "0.0.0.0:49152"
	cfg.DaemonToken = "kernel-token-0123456789abcdef"
	if _, err := New(cfg, testLogger()); err == nil {
		t.Fatal("New accepted a non-loopback listener")
	}
}

func TestNewCreatesHome(t *testing.T) {
	cfg := validConfig(t)
	cfg.Home = cfg.Home + "/nested/nullkey"
	cfg.DaemonToken = "kernel-token-0123456789abcdef"
	if _, err := New(cfg, testLogger()); err != nil {
		t.Fatalf("New: %v", err)
	}
	if info, err := os.Stat(cfg.Home); err != nil || !info.IsDir() {
		t.Fatalf("home was not created: info=%v err=%v", info, err)
	}
}
