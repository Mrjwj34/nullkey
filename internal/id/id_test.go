package id

import (
	"strings"
	"testing"
)

func TestNewPrefix(t *testing.T) {
	got := New(Sandbox)
	if !strings.HasPrefix(got, Sandbox) {
		t.Fatalf("New(%q) = %q", Sandbox, got)
	}
	if len(got) <= len(Sandbox) {
		t.Fatalf("id is too short: %q", got)
	}
}

func TestRequestID(t *testing.T) {
	got := RequestID()
	if !strings.HasPrefix(got, Request) {
		t.Fatalf("RequestID() = %q", got)
	}
}

func TestTerminalPrefix(t *testing.T) {
	got := New(Terminal)
	if !strings.HasPrefix(got, Terminal) {
		t.Fatalf("New(Terminal) = %q", got)
	}
}

func TestRandomAndUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		value, err := Random(32)
		if err != nil {
			t.Fatalf("Random: %v", err)
		}
		if len(value) != 43 {
			t.Fatalf("Random(32) length = %d, want 43", len(value))
		}
		if seen[value] {
			t.Fatal("Random returned a duplicate")
		}
		seen[value] = true
	}
	if _, err := Random(0); err == nil {
		t.Fatal("Random(0) should fail")
	}
}
