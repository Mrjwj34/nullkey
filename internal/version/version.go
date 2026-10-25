// Package version exposes build metadata injected at link time.
package version

import "strings"

// These values are overridden with -ldflags in the Makefile and release builds.
var (
	Version = "0.1.3-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a human-readable version, including the commit when known.
func String() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return Version + " (" + Commit + ")"
}

// IsDev reports whether this is an unlinked development build.
func IsDev() bool {
	return strings.Contains(Version, "dev") || Commit == "unknown"
}
