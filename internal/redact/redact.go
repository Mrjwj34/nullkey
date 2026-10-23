// Package redact removes credential-shaped values from logs, audit details and
// terminal output. It is defence in depth: code must still avoid logging
// secrets in the first place.
package redact

import (
	"fmt"
	"regexp"
	"strings"
)

const replacement = "[REDACTED]"

var patterns = []*regexp.Regexp{
	// OpenAI/Anthropic-style keys and generic sk- keys.
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{8,}`),
	// GitHub tokens.
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	// Slack tokens.
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{8,}`),
	// AWS access key ids.
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// Google API keys.
	regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}`),
	// Bearer credentials in text/headers.
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=\-]{8,}`),
	// Basic credentials in text/headers.
	regexp.MustCompile(`(?i)basic\s+[A-Za-z0-9+/=]{8,}`),
	// Private key blocks.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	// NULLKEY token assignments.
	regexp.MustCompile(`NULLKEY_TOKEN=[^\s"']+`),
	// Daemon token assignments.
	regexp.MustCompile(`(?i)nullkey[_-]?(daemon[_-]?)?token["'\s:=]+[A-Za-z0-9._~+/=\-]{8,}`),
}

// String returns s with credential-shaped substrings replaced.
func String(s string) string {
	for _, re := range patterns {
		s = re.ReplaceAllString(s, replacement)
	}
	return s
}

// Map redacts every string value recursively. Keys are preserved.
func Map(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = Value(v)
	}
	return out
}

// Value redacts a nested JSON-like value.
func Value(v any) any {
	switch typed := v.(type) {
	case string:
		return String(typed)
	case []string:
		out := make([]string, len(typed))
		for i, s := range typed {
			out[i] = String(s)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = Value(item)
		}
		return out
	case map[string]any:
		return Map(typed)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for k, item := range typed {
			out[k] = String(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i, item := range typed {
			out[i] = Map(item)
		}
		return out
	default:
		return v
	}
}

// Bytes redacts a byte slice.
func Bytes(b []byte) []byte {
	return []byte(String(string(b)))
}

// Contains reports whether text still contains a credential-shaped value.
func Contains(text string) bool {
	return String(text) != text
}

// Count returns the number of credential-shaped matches across known patterns.
func Count(text string) int {
	total := 0
	for _, re := range patterns {
		total += len(re.FindAllStringIndex(text, -1))
	}
	return total
}

// Describe is a safe, non-secret summary for audit details.
func Describe(prefix string, n int) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(prefix), n)
}
