//go:build darwin && !cgo

package secret

import "testing"

func TestOpenWithoutCgoFailsClosed(t *testing.T) {
	t.Setenv(BackendEnv, "")
	if _, err := Open(t.TempDir(), testLogger()); err == nil {
		t.Fatal("Open without cgo did not fail closed")
	}
}
