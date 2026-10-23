//go:build darwin && cgo

package secret

import (
	"log/slog"
)

// Open returns the platform secret Store.
//
// On darwin it uses the login Keychain via Security.framework unless
// NULLKEY_SECRET_BACKEND=file is set, in which case it uses the encrypted-file
// CI fallback under <home>/secrets/. Unknown backend values fail closed.
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
	logger.Debug("secret store selected", "backend", "keychain")
	return newKeychainStore(), nil
}
