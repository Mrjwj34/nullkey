//go:build !darwin

package secret

import (
	"log/slog"
)

// Open returns the platform secret Store.
//
// Outside darwin the default is the encrypted-file CI fallback under
// <home>/secrets/. NULLKEY_SECRET_BACKEND=file is accepted for explicitness;
// any other non-empty value fails closed.
func Open(home string, logger *slog.Logger) (Store, error) {
	logger = normalizeLogger(logger)
	if _, err := backendOverride(); err != nil {
		return nil, err
	}
	logger.Debug("secret store selected", "backend", "file")
	return openFileStore(home)
}
