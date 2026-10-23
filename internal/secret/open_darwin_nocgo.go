//go:build darwin && !cgo

package secret

import (
	"errors"
	"log/slog"
)

// Open returns the platform secret Store.
//
// On darwin the default backend is the login Keychain, which requires cgo. If
// this binary was built with CGO_ENABLED=0, Open fails closed unless
// NULLKEY_SECRET_BACKEND=file selects the encrypted-file CI fallback.
func Open(home string, logger *slog.Logger) (Store, error) {
	logger = normalizeLogger(logger)
	override, err := backendOverride()
	if err != nil {
		return nil, err
	}
	if override == "file" {
		logger.Debug("secret store selected", "backend", "file")
		return openFileStore(home)
	}
	return nil, errors.New("secret: macOS Keychain backend requires cgo; rebuild with cgo or set NULLKEY_SECRET_BACKEND=file")
}
