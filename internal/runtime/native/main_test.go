package native

import (
	"os"
	"testing"
)

// TestMain turns the test binary into the real nullkeyd sandbox helper when it
// is re-executed with the hidden __sandbox_exec argument. Production Start
// spawns Nullkeyd's own executable with exactly this argument list, so using
// the test binary keeps the end-to-end PTY/Seatbelt path under test without
// building or installing the daemon.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__sandbox_exec" {
		os.Exit(RunSandboxHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}
