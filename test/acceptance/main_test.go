//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var daemonBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nullkey-acceptance-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "acceptance: temp dir: %v\n", err)
		os.Exit(1)
	}
	daemonBinary = filepath.Join(dir, "nullkeyd")

	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "acceptance: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-trimpath", "-o", daemonBinary, "./cmd/nullkeyd")
	build.Dir = root
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "acceptance: build nullkeyd: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
