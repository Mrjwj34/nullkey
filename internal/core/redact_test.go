package core

import (
	"strings"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/redact"
)

func TestRedactCredentialShapes(t *testing.T) {
	cases := []string{
		"Authorization: Bearer sk-test-abcdefghijklmnop",
		"key sk-proj-1234567890abcdef",
		"ghp_1234567890abcdefghijklmnop",
		"AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----",
		"NULLKEY_TOKEN=abc123def456",
	}
	for _, in := range cases {
		out := redact.String(in)
		if out == in {
			t.Fatalf("not redacted: %q", in)
		}
		if strings.Contains(out, "sk-test-abcdefghijklmnop") || strings.Contains(out, "ghp_1234567890") {
			t.Fatalf("secret survived: %q", out)
		}
	}
	if got := redact.String("ordinary text"); got != "ordinary text" {
		t.Fatalf("over-redacted: %q", got)
	}
}
