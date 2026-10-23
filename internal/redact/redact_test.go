package redact

import (
	"strings"
	"testing"
)

func TestStringRedactsCredentialShapes(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer sk-test-abcdefghijklmnop":                               "sk-test-abcdefghijklmnop",
		"token ghp_1234567890abcdefghijklmnop":                                         "ghp_1234567890abcdefghijklmnop",
		"slack xoxb-1234567890-abcdef":                                                 "xoxb-1234567890",
		"aws AKIAIOSFODNN7EXAMPLE":                                                     "AKIAIOSFODNN7EXAMPLE",
		"google AIzaSyA1234567890abcdefghijklmnopqrst":                                 "AIzaSyA1234567890",
		"basic dXNlcjpwYXNzd29yZA==":                                                   "dXNlcjpwYXNzd29yZA",
		"NULLKEY_TOKEN=abcdef0123456789":                                               "abcdef0123456789",
		"nullkey_daemon_token=abcdef0123456789":                                        "abcdef0123456789",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----": "AAAA",
	}
	for in, secret := range cases {
		out := String(in)
		if strings.Contains(out, secret) {
			t.Fatalf("String(%q) = %q still contains %q", in, out, secret)
		}
		if !strings.Contains(out, replacement) {
			t.Fatalf("String(%q) = %q has no redaction marker", in, out)
		}
	}
	if got := String("plain text"); got != "plain text" {
		t.Fatalf("String over-redacted %q", got)
	}
}

func TestMapValueBytesContainsCount(t *testing.T) {
	in := map[string]any{
		"key":  "sk-test-abcdefghijklmnop",
		"list": []any{"ghp_1234567890abcdefghijklmnop", 42},
		"strs": []string{"AKIAIOSFODNN7EXAMPLE"},
		"nested": map[string]any{
			"inner": "Bearer abcdefghijklmnop",
		},
	}
	out := Map(in)
	flat := strings.Join([]string{
		out["key"].(string),
		out["list"].([]any)[0].(string),
		out["strs"].([]string)[0],
		out["nested"].(map[string]any)["inner"].(string),
	}, " ")
	if strings.Contains(flat, "sk-test") || strings.Contains(flat, "ghp_") || strings.Contains(flat, "AKIA") {
		t.Fatalf("nested map not redacted: %v", out)
	}
	if Map(nil) != nil {
		t.Fatal("Map(nil) should be nil")
	}
	if value := Value(42); value != 42 {
		t.Fatalf("Value(42) = %v", value)
	}
	if string(Bytes([]byte("sk-test-abcdefghijklmnop"))) == "sk-test-abcdefghijklmnop" {
		t.Fatal("Bytes did not redact")
	}
	if Contains("plain") {
		t.Fatal("Contains false positive")
	}
	if !Contains("sk-test-abcdefghijklmnop") {
		t.Fatal("Contains false negative")
	}
	if Count("sk-test-abcdefghijklmnop ghp_1234567890abcdefghijklmnop") < 2 {
		t.Fatal("Count missed credentials")
	}
	if got := Describe("ghp", 2); got != "ghp:2" {
		t.Fatalf("Describe = %q", got)
	}
}
