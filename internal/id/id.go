// Package id generates prefixed, URL-safe identifiers and tokens.
package id

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync/atomic"
	"time"
)

// ID prefixes are part of the public API contract (docs/08-api-contract.md).
const (
	Sandbox    = "sbx_"
	Workspace  = "ws_"
	State      = "st_"
	Credential = "cred_"
	MCP        = "mcp_"
	Flow       = "flow_"
	Job        = "job_"
	Event      = "evt_"
	Request    = "req_"
	Terminal   = "term_"
)

var fallbackCounter atomic.Uint64

// New returns a prefixed random identifier, e.g. "sbx_...".
func New(prefix string) string {
	return prefix + Token(12)
}

// RequestID returns a request identifier suitable for X-Request-Id.
func RequestID() string {
	return Request + Token(12)
}

// Token returns a URL-safe random string. It is suitable for identifiers and
// request IDs. Security-sensitive tokens must use Random and handle the error:
// the fallback path is intentionally predictable to keep non-security call
// sites simple.
func Token(n int) string {
	s, err := Random(n)
	if err == nil {
		return s
	}
	return fmt.Sprintf("fallback-%d-%d", time.Now().UnixNano(), fallbackCounter.Add(1))
}

// Random returns n cryptographically random bytes encoded as base64url without
// padding. Use this for per-sandbox tokens and other secrets.
func Random(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("id: random byte count must be positive")
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("id: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
