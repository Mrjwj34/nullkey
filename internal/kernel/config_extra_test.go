package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigFromEnv(t *testing.T) {
	t.Setenv("NULLKEY_LISTEN", "127.0.0.1:50001")
	t.Setenv("NULLKEY_GATEWAY_LISTEN", "127.0.0.1:50002")
	t.Setenv("NULLKEY_PROXY_LISTEN", "127.0.0.1:50003")
	t.Setenv("NULLKEY_HOME", "/tmp/nullkey-config-test")
	t.Setenv("NULLKEY_DAEMON_TOKEN", "env-token-0123456789abcdef")
	t.Setenv("NULLKEY_LOG_LEVEL", "WARN")
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if cfg.Listen != "127.0.0.1:50001" || cfg.GatewayListen != "127.0.0.1:50002" || cfg.ProxyListen != "127.0.0.1:50003" {
		t.Fatalf("listeners not read from env: %+v", cfg)
	}
	if cfg.LogLevel != "warn" {
		t.Fatalf("log level = %q", cfg.LogLevel)
	}
	if cfg.HostHome == "" {
		t.Fatal("HostHome is empty")
	}
}

func TestValidateListenerDefaultsAndConflicts(t *testing.T) {
	cfg := Config{Home: t.TempDir(), LogLevel: "info"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate defaults: %v", err)
	}
	if cfg.Listen != DefaultListen || cfg.GatewayListen != DefaultGatewayListen || cfg.ProxyListen != DefaultProxyListen {
		t.Fatalf("defaults not applied: %+v", cfg)
	}

	cfg = Config{Listen: "127.0.0.1:50001", GatewayListen: "127.0.0.1:50001", ProxyListen: "127.0.0.1:50003", Home: t.TempDir(), LogLevel: "info"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("duplicate listener accepted: %v", err)
	}

	cfg = Config{Listen: "127.0.0.1:50001", GatewayListen: "0.0.0.0:50002", ProxyListen: "127.0.0.1:50003", Home: t.TempDir(), LogLevel: "info"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("non-loopback gateway accepted")
	}
}

func TestValidateTokenCharacters(t *testing.T) {
	for _, token := range []string{"has space 0123456789", "has\ttab0123456789", "has\nnewline0123456789"} {
		cfg := Config{DaemonToken: token}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("token %q accepted", token)
		}
	}
}

func TestTokenPathAndCustomFile(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom-token")
	cfg := Config{Home: dir, TokenFile: custom, LogLevel: "info"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.tokenPath() != custom {
		t.Fatalf("tokenPath = %q", cfg.tokenPath())
	}
	cfg2 := Config{Home: dir, LogLevel: "info"}
	if cfg2.tokenPath() != filepath.Join(dir, "daemon-token") {
		t.Fatalf("default tokenPath = %q", cfg2.tokenPath())
	}
}

func TestExpandPathVariants(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := expandPath("~")
	if err != nil || got != home {
		t.Fatalf("expandPath(~) = %q, %v", got, err)
	}
	got, err = expandPath("~/x")
	if err != nil || got != filepath.Join(home, "x") {
		t.Fatalf("expandPath(~/x) = %q, %v", got, err)
	}
	got, err = expandPath("/absolute")
	if err != nil || got != "/absolute" {
		t.Fatalf("expandPath absolute = %q, %v", got, err)
	}
}
