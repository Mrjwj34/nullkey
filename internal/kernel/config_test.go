package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Listen:   DefaultListen,
		Home:     t.TempDir(),
		LogLevel: "info",
	}
}

func TestValidateListen(t *testing.T) {
	cases := []struct {
		listen string
		ok     bool
	}{
		{"127.0.0.1:49152", true},
		{"localhost:49152", true},
		{"[::1]:49152", true},
		{"0.0.0.0:49152", false},
		{"192.168.1.2:49152", false},
		{":49152", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1:70000", false},
		{"not-an-address", false},
	}
	for _, tc := range cases {
		err := validateListen(tc.listen)
		if tc.ok && err != nil {
			t.Fatalf("validateListen(%q) = %v, want nil", tc.listen, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("validateListen(%q) = nil, want error", tc.listen)
		}
	}
}

func TestValidateRejectsUnsafeHome(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err == nil && userHome != "" {
		cfg := validConfig(t)
		cfg.Home = userHome
		if err := cfg.Validate(); err == nil {
			t.Fatal("Validate accepted the user's home directory")
		}
	}

	cfg := validConfig(t)
	cfg.Home = string(filepath.Separator)
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted the filesystem root")
	}
}

func TestLoadDaemonTokenFromEnv(t *testing.T) {
	cfg := Config{DaemonToken: "env-token-0123456789abcdef"}
	token, generated, err := cfg.LoadDaemonToken()
	if err != nil {
		t.Fatalf("LoadDaemonToken: %v", err)
	}
	if token != cfg.DaemonToken || generated {
		t.Fatalf("token=%q generated=%v", token, generated)
	}
}

func TestLoadDaemonTokenRejectsShort(t *testing.T) {
	cfg := Config{DaemonToken: "short"}
	if _, _, err := cfg.LoadDaemonToken(); err == nil {
		t.Fatal("LoadDaemonToken accepted a short token")
	}
}

func TestLoadDaemonTokenFile(t *testing.T) {
	cfg := validConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	token, generated, err := cfg.LoadDaemonToken()
	if err != nil {
		t.Fatalf("LoadDaemonToken: %v", err)
	}
	if !generated || len(token) < minTokenChars {
		t.Fatalf("token=%q generated=%v", token, generated)
	}
	path := filepath.Join(cfg.Home, "daemon-token")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat token file: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("token file mode = %o, want 600", mode)
		}
	}

	again, generatedAgain, err := cfg.LoadDaemonToken()
	if err != nil {
		t.Fatalf("LoadDaemonToken second call: %v", err)
	}
	if generatedAgain || again != token {
		t.Fatalf("token not reused: first=%q second=%q generated=%v", token, again, generatedAgain)
	}
}

func TestNewLogger(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "warning", "error", ""} {
		if _, err := NewLogger(level); err != nil {
			t.Fatalf("NewLogger(%q): %v", level, err)
		}
	}
	if _, err := NewLogger("verbose"); err == nil {
		t.Fatal("NewLogger accepted an unknown level")
	}
}

func TestExpandPath(t *testing.T) {
	if _, err := expandPath(""); err == nil {
		t.Fatal("expandPath accepted an empty path")
	}
	got, err := expandPath("~/nullkey-test")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	if strings.HasPrefix(got, "~") || !filepath.IsAbs(got) {
		t.Fatalf("expandPath did not expand: %q", got)
	}
}
