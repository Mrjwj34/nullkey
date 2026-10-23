//go:build !darwin || !cgo

package native

func sandboxAvailable() bool { return false }

func applySandbox(string) error { return ErrUnsupported }
