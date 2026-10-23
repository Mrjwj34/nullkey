// Package secret persists real credentials inside the host trust boundary.
//
// Values are never written to argv, logs, audit records, SQLite, or the sandbox
// environment. On macOS the production backend is the login Keychain through
// Security.framework (service "com.nullkey.credentials", account supplied by
// the caller). On CI/Linux an encrypted-file fallback is used.
//
// Backend selection:
//
//   - macOS (darwin): Keychain by default.
//   - every other OS: encrypted file under <home>/secrets/.
//   - NULLKEY_SECRET_BACKEND=file selects the encrypted file backend on any OS.
//     This is intended for deterministic tests and for CI on Linux; it is not
//     the production backend on macOS.
//
// Unknown non-empty values of NULLKEY_SECRET_BACKEND fail closed rather than
// silently selecting a different backend.
//
// The file fallback stores one file per account (SHA-256 of the account is the
// filename), encrypted with AES-256-GCM. A random 32-byte master key lives at
// <home>/secrets/master.key with mode 0600. Writes are atomic and no plaintext
// is written to disk. The fallback exists only because Linux is CI-only; real
// macOS installs must use the Keychain.
package secret

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"
)

// BackendEnv is the optional environment variable used to force a backend.
// Only the value "file" is accepted; an empty value means the platform default.
const BackendEnv = "NULLKEY_SECRET_BACKEND"

// ErrNotFound is returned by Get when the account has no stored secret.
var ErrNotFound = errors.New("secret: not found")

// ErrInvalidAccount is returned when an account name is empty, contains a NUL
// byte, or is not valid UTF-8. Accounts are identifiers, never secret values.
var ErrInvalidAccount = errors.New("secret: invalid account")

// Store persists opaque credential bytes by account.
//
// Implementations must copy values in and out of the backing store; callers
// may reuse or mutate their slices after the call returns. Implementations must
// be safe for concurrent use.
type Store interface {
	Put(ctx context.Context, account string, value []byte) error
	Get(ctx context.Context, account string) ([]byte, error)
	Delete(ctx context.Context, account string) error
}

// Fingerprint returns a stable, non-reversible identifier for value. It is
// suitable for API responses and audit records: the full value can never be
// recovered from it and it never contains the plaintext.
func Fingerprint(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// validateAccount rejects account names that cannot be represented safely in
// both the Keychain (C strings) and hashed file names.
func validateAccount(account string) error {
	if account == "" {
		return fmt.Errorf("%w: empty", ErrInvalidAccount)
	}
	if strings.IndexByte(account, 0) >= 0 {
		return fmt.Errorf("%w: contains NUL", ErrInvalidAccount)
	}
	if !utf8.ValidString(account) {
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidAccount)
	}
	return nil
}

// backendOverride returns the backend explicitly requested by the environment,
// or "" when the platform default should be used.
func backendOverride() (string, error) {
	raw := strings.TrimSpace(os.Getenv(BackendEnv))
	if raw == "" {
		return "", nil
	}
	if strings.EqualFold(raw, "file") {
		return "file", nil
	}
	// Do not echo the value: environment variables are occasionally misused to
	// pass credentials, and this error may end up in a log.
	return "", fmt.Errorf("secret: unsupported %s (only \"file\" is supported)", BackendEnv)
}

// normalizeLogger makes a nil logger safe to use without exposing secrets.
func normalizeLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
