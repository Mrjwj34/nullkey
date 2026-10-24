package core

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// TunnelHandler exposes the declared-service tunnel.
func (s *Service) TunnelHandler() http.Handler { return s.tunnel }

// TerminalHandler exposes the terminal WebSocket.
func (s *Service) TerminalHandler() http.Handler {
	if s.Terminal == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"code":"terminal_not_ready","message":"terminal is not available"}}`, http.StatusConflict)
		})
	}
	return http.HandlerFunc(s.Terminal.ServeWS)
}

func sanitizeRequestHeaders(dst, src http.Header) {
	hop := map[string]bool{
		"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
		"Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
		"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
		"Authorization": true, "Cookie": true, "X-Api-Key": true,
		"Forwarded": true, "X-Forwarded-For": true, "X-Forwarded-Host": true,
		"X-Forwarded-Proto": true, "X-Real-Ip": true, "X-Original-Url": true,
	}
	for key, values := range src {
		if hop[http.CanonicalHeaderKey(key)] {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	hop := map[string]bool{
		"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
		"Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
		"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	}
	for key, values := range src {
		if hop[http.CanonicalHeaderKey(key)] {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// ModelGateway proxies a model request to the provider's upstream, injecting
// the real credential only in the host process. Responses are streamed.
//
// Codex ChatGPT logins are not OpenAI API keys: their responses traffic lives
// under https://chatgpt.com/backend-api/codex. The sandbox still sees a normal
// OpenAI-compatible provider whose bearer token is the per-sandbox token.
func (s *Service) ModelGateway(ctx context.Context, sb domain.Sandbox, providerID string, w http.ResponseWriter, r *http.Request) error {
	provider, ok := providerByID(providerID)
	if !ok {
		return E(http.StatusForbidden, "network_denied", "unknown model provider")
	}
	cred, err := s.modelCredential(ctx, providerID)
	if err != nil {
		return err
	}
	bundle, err := s.credentialBundleFor(ctx, cred)
	if err != nil {
		return err
	}
	base := modelUpstreamBase(cred, provider, providerID)
	if base == "" {
		return E(http.StatusBadGateway, "gateway_unavailable", "provider has no upstream configured")
	}
	rel := strings.TrimPrefix(r.URL.Path, "/"+providerID)
	if rel == r.URL.Path && providerID == "codex" {
		// The historical route name for Codex was /openai.
		rel = strings.TrimPrefix(r.URL.Path, "/openai")
	}
	if rel == "" {
		rel = "/"
	}
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	target := strings.TrimRight(base, "/") + rel
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		return E(http.StatusBadRequest, "invalid_request", "invalid upstream request")
	}
	upstream.ContentLength = r.ContentLength
	sanitizeRequestHeaders(upstream.Header, r.Header)
	applyCredential(upstream, cred, bundle.AccessToken, cred.ProviderID)
	applyCodexIdentity(upstream, cred, bundle)
	if upstream.Header.Get("Content-Type") == "" {
		upstream.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.HTTP.Do(upstream)
	if err != nil {
		return E(http.StatusBadGateway, "gateway_unavailable", "model provider is unreachable")
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
	s.Audit.Record(ctx, "gateway.model", &sb.ID, map[string]any{"provider": providerID, "upstream_provider": cred.ProviderID, "status": resp.StatusCode})
	return nil
}

// modelCredential picks the best host credential for a model route. An OpenAI
// route may fall back to an imported Codex ChatGPT login.
func (s *Service) modelCredential(ctx context.Context, providerID string) (domain.Credential, error) {
	candidates := []string{providerID}
	if providerID == "openai" {
		candidates = append(candidates, "codex")
	}
	for _, id := range candidates {
		if cred, err := s.Store.CredentialForProvider(ctx, id); err == nil {
			_ = s.Store.TouchCredential(ctx, cred.ID)
			return cred, nil
		}
		if cred, err := s.Store.RefreshableCredentialForProvider(ctx, id); err == nil {
			_ = s.Store.TouchCredential(ctx, cred.ID)
			return cred, nil
		}
	}
	return domain.Credential{}, E(http.StatusUnauthorized, "reauth_required", "no credential is connected for this provider")
}

// modelUpstreamBase returns the inference root for the selected credential.
// A Codex credential always targets the ChatGPT Codex backend, regardless of
// whether the sandbox called /openai or /codex.
func modelUpstreamBase(cred domain.Credential, provider Provider, route string) string {
	if cred.ProviderID == "codex" || route == "codex" {
		if cred.AuthType == domain.AuthOAuth {
			if cred.BaseURL != "" {
				return strings.TrimRight(cred.BaseURL, "/")
			}
			return "https://chatgpt.com/backend-api/codex"
		}
		// A Codex API key is an OpenAI API key, not a ChatGPT session.
		return "https://api.openai.com/v1"
	}
	if cred.BaseURL != "" {
		return strings.TrimRight(cred.BaseURL, "/")
	}
	if provider.BaseURL != nil {
		return strings.TrimRight(*provider.BaseURL, "/")
	}
	return ""
}

// applyCodexIdentity makes account selection a host decision: any account id
// supplied by the sandbox is discarded and the imported account is injected.
func applyCodexIdentity(req *http.Request, cred domain.Credential, bundle credentialBundle) {
	req.Header.Del("ChatGPT-Account-Id")
	if cred.ProviderID != "codex" || cred.AuthType != domain.AuthOAuth {
		return
	}
	if bundle.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", bundle.AccountID)
	}
}

func applyCredential(req *http.Request, cred domain.Credential, secret, providerID string) {
	switch cred.AuthType {
	case domain.AuthOAuth, domain.AuthAPIKey, domain.AuthPAT, domain.AuthBearer:
		if providerID == "anthropic" {
			req.Header.Set("x-api-key", secret)
			if req.Header.Get("anthropic-version") == "" {
				req.Header.Set("anthropic-version", "2023-06-01")
			}
			return
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	case domain.AuthHeader:
		name := cred.HeaderName
		if name == "" {
			name = "x-api-key"
		}
		req.Header.Set(name, secret)
		if providerID == "anthropic" && req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case domain.AuthBasic:
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(secret)))
	}
}

// GitGateway proxies GitHub smart HTTP with an injected token. The sandbox
// remote never contains the real token.
func (s *Service) GitGateway(ctx context.Context, sb domain.Sandbox, owner, repo string, w http.ResponseWriter, r *http.Request) error {
	if owner == "" || repo == "" {
		return E(http.StatusNotFound, "not_found", "repository not found")
	}
	cred, err := s.credentialForProvider(ctx, "github")
	if err != nil {
		return err
	}
	secret, err := s.CredentialSecret(ctx, cred)
	if err != nil {
		return err
	}
	rest := strings.TrimPrefix(r.URL.Path, "/git/github/"+owner+"/"+repo+".git")
	if rest == "" {
		rest = "/"
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	base := strings.TrimRight(cred.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	} else if parsed, err := url.Parse(base); err == nil && strings.EqualFold(parsed.Host, "api.github.com") {
		// The provider registry's API base is not a git smart-HTTP endpoint.
		base = "https://github.com"
	}
	target := base + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + ".git" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		return E(http.StatusBadRequest, "invalid_request", "invalid git request")
	}
	sanitizeRequestHeaders(upstream.Header, r.Header)
	// git http-backend (CGI) relies on CONTENT_LENGTH; preserve the incoming
	// length instead of switching the upstream request to chunked encoding.
	upstream.ContentLength = r.ContentLength
	upstream.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret)))
	upstream.Header.Set("User-Agent", "nullkey-git-proxy")
	resp, err := s.HTTP.Do(upstream)
	if err != nil {
		return E(http.StatusBadGateway, "gateway_unavailable", "git upstream is unreachable")
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	s.Audit.Record(ctx, "gateway.git", &sb.ID, map[string]any{"owner": owner, "repo": repo, "status": resp.StatusCode})
	return nil
}
