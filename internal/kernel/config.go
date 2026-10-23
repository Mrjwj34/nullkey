package kernel

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

const (
	// DefaultListen is the control-plane address. It must always be loopback.
	DefaultListen = "127.0.0.1:49152"
	// DefaultGatewayListen is the sandbox data-plane address.
	DefaultGatewayListen = "127.0.0.1:49153"
	// DefaultProxyListen is the egress proxy address.
	DefaultProxyListen = "127.0.0.1:49154"

	defaultLogLevel = "info"
	minTokenChars   = 16
)

// Config is the daemon configuration. Secrets are only read from the
// environment or a 0600 token file; they are never accepted as flags.
type Config struct {
	Listen        string
	GatewayListen string
	ProxyListen   string
	Home          string
	HostHome      string
	TokenFile     string
	DaemonToken   string
	LogLevel      string
}

// DefaultConfig reads environment overrides. Call Validate after applying flags.
func DefaultConfig() (Config, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve user config dir: %w", err)
	}
	hostHome, _ := os.UserHomeDir()
	return Config{
		Listen:        envOrDefault("NULLKEY_LISTEN", DefaultListen),
		GatewayListen: envOrDefault("NULLKEY_GATEWAY_LISTEN", DefaultGatewayListen),
		ProxyListen:   envOrDefault("NULLKEY_PROXY_LISTEN", DefaultProxyListen),
		Home:          envOrDefault("NULLKEY_HOME", filepath.Join(configDir, "Nullkey")),
		HostHome:      hostHome,
		TokenFile:     os.Getenv("NULLKEY_TOKEN_FILE"),
		DaemonToken:   os.Getenv("NULLKEY_DAEMON_TOKEN"),
		LogLevel:      strings.ToLower(envOrDefault("NULLKEY_LOG_LEVEL", defaultLogLevel)),
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Validate normalizes paths and rejects unsafe or unsupported configuration.
func (c *Config) Validate() error {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.GatewayListen == "" {
		c.GatewayListen = DefaultGatewayListen
	}
	if c.ProxyListen == "" {
		c.ProxyListen = DefaultProxyListen
	}
	if err := validateListen(c.Listen); err != nil {
		return err
	}
	if err := validateListen(c.GatewayListen); err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	if err := validateListen(c.ProxyListen); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	if c.Listen == c.GatewayListen || c.Listen == c.ProxyListen || c.GatewayListen == c.ProxyListen {
		return errors.New("control, gateway, and proxy listeners must use distinct addresses")
	}
	if c.HostHome == "" {
		c.HostHome, _ = os.UserHomeDir()
	}
	home, err := expandPath(c.Home)
	if err != nil {
		return fmt.Errorf("home: %w", err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("home: %w", err)
	}
	if home == string(filepath.Separator) {
		return errors.New("home must not be the filesystem root")
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" && home == userHome {
		return errors.New("home must not be the user's home directory; use a dedicated Nullkey directory")
	}
	c.Home = home

	if c.TokenFile != "" {
		tokenFile, err := expandPath(c.TokenFile)
		if err != nil {
			return fmt.Errorf("token file: %w", err)
		}
		tokenFile, err = filepath.Abs(tokenFile)
		if err != nil {
			return fmt.Errorf("token file: %w", err)
		}
		c.TokenFile = tokenFile
	}
	if c.DaemonToken != "" {
		if err := validateToken(c.DaemonToken); err != nil {
			return fmt.Errorf("daemon token: %w", err)
		}
	}
	if _, err := parseLogLevel(c.LogLevel); err != nil {
		return err
	}
	return nil
}

func validateListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", listen, err)
	}
	if host == "" || !isLoopbackHost(host) {
		return fmt.Errorf("listen %q: host must be loopback (127.0.0.1, ::1, or localhost)", listen)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("listen %q: port must be between 1 and 65535", listen)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func expandPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	if path == "~" {
		return os.UserHomeDir()
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func validateToken(token string) error {
	if len(token) < minTokenChars {
		return fmt.Errorf("must be at least %d characters", minTokenChars)
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("must not contain whitespace or control characters")
		}
	}
	return nil
}

func parseLogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (debug, info, warn, error)", level)
	}
}

// NewLogger returns a structured JSON logger writing to stderr.
func NewLogger(level string) (*slog.Logger, error) {
	parsed, err := parseLogLevel(level)
	if err != nil {
		return nil, err
	}
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: parsed})
	return slog.New(handler), nil
}

func (c Config) tokenPath() string {
	if c.TokenFile != "" {
		return c.TokenFile
	}
	return filepath.Join(c.Home, "daemon-token")
}

// LoadDaemonToken returns the control-plane bearer token. It prefers
// NULLKEY_DAEMON_TOKEN; otherwise it reads or creates <home>/daemon-token with
// mode 0600. The token is never logged.
func (c Config) LoadDaemonToken() (token string, generated bool, err error) {
	if c.DaemonToken != "" {
		if err := validateToken(c.DaemonToken); err != nil {
			return "", false, fmt.Errorf("daemon token from environment: %w", err)
		}
		return c.DaemonToken, false, nil
	}

	path := c.tokenPath()
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		token = strings.TrimSpace(string(content))
		if err := validateToken(token); err != nil {
			return "", false, fmt.Errorf("daemon token file %s: %w", path, err)
		}
		// Harden an existing file, but still fail closed if we cannot.
		if err := os.Chmod(path, 0o600); err != nil {
			return "", false, fmt.Errorf("harden daemon token file %s: %w", path, err)
		}
		return token, false, nil
	case errors.Is(err, os.ErrNotExist):
		// Generate below.
	default:
		return "", false, fmt.Errorf("read daemon token file %s: %w", path, err)
	}

	token, err = nkid.Random(32)
	if err != nil {
		return "", false, fmt.Errorf("generate daemon token: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("create token directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".daemon-token-*")
	if err != nil {
		return "", false, fmt.Errorf("create daemon token file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(token + "\n"); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("write daemon token: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("chmod daemon token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", false, fmt.Errorf("close daemon token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, fmt.Errorf("install daemon token file: %w", err)
	}
	return token, true, nil
}
