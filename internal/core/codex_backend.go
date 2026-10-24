package core

import (
	"context"
	"net/http"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// CodexBackendGateway proxies the small set of ChatGPT backend endpoints Codex
// needs for account status and its built-in plugin MCP runtime. It is not a
// general-purpose ChatGPT proxy: only allowlisted paths are reachable, the
// sandbox token is replaced with the host OAuth token, and the account id is
// chosen by the host.
func (s *Service) CodexBackendGateway(ctx context.Context, sb domain.Sandbox, w http.ResponseWriter, r *http.Request) error {
	rel := strings.TrimPrefix(r.URL.Path, "/backend-api/")
	rel = strings.TrimPrefix(rel, "/")
	if !codexBackendAllowed(r.Method, rel) {
		return E(http.StatusForbidden, "network_denied", "ChatGPT backend path is not allowed")
	}
	cred, err := s.modelCredential(ctx, "codex")
	if err != nil {
		return err
	}
	if cred.AuthType != domain.AuthOAuth {
		return E(http.StatusUnauthorized, "reauth_required", "a ChatGPT login is required")
	}
	bundle, err := s.credentialBundleFor(ctx, cred)
	if err != nil {
		return err
	}
	target := chatgptBackendRoot(cred.BaseURL) + "/" + rel
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		return E(http.StatusBadRequest, "invalid_request", "invalid ChatGPT backend request")
	}
	// Preserve the original length instead of forcing chunked encoding; MCP
	// streamable HTTP servers may reject chunked POSTs.
	upstream.ContentLength = r.ContentLength
	sanitizeRequestHeaders(upstream.Header, r.Header)
	applyCredential(upstream, cred, bundle.AccessToken, cred.ProviderID)
	applyCodexIdentity(upstream, cred, bundle)
	if upstream.Header.Get("Content-Type") == "" && r.Body != nil {
		upstream.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.HTTP.Do(upstream)
	if err != nil {
		return E(http.StatusBadGateway, "gateway_unavailable", "ChatGPT backend is unreachable")
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return nil
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
	s.Audit.Record(ctx, "gateway.codex_backend", &sb.ID, map[string]any{"path": rel, "status": resp.StatusCode})
	return nil
}

func codexBackendAllowed(method, rel string) bool {
	clean := strings.Trim(rel, "/")
	switch {
	case clean == "ps/mcp" || strings.HasPrefix(clean, "ps/mcp/"):
		return true
	case method == http.MethodGet && (clean == "wham/accounts/check" ||
		clean == "wham/settings/user" ||
		clean == "wham/usage" ||
		clean == "wham/config/bundle"):
		return true
	case method == http.MethodGet && (clean == "codex/models" || strings.HasPrefix(clean, "codex/models/")):
		return true
	case method == http.MethodPost && clean == "codex/responses":
		return true
	default:
		return false
	}
}

func chatgptBackendRoot(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if index := strings.Index(base, "/backend-api"); index >= 0 {
		return base[:index+len("/backend-api")]
	}
	return "https://chatgpt.com/backend-api"
}
