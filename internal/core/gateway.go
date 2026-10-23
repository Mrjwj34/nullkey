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
func (s *Service) ModelGateway(ctx context.Context, sb domain.Sandbox, providerID string, w http.ResponseWriter, r *http.Request) error {
	if providerID == "codex" {
		providerID = "openai"
	}
	provider, ok := providerByID(providerID)
	if !ok {
		return E(http.StatusForbidden, "network_denied", "unknown model provider")
	}
	cred, err := s.credentialForProvider(ctx, providerID)
	if err != nil {
		// A codex OAuth credential is a valid fallback for the OpenAI API.
		if providerID == "openai" {
			if fallback, ferr := s.credentialForProvider(ctx, "codex"); ferr == nil {
				cred = fallback
				err = nil
			}
		}
		if err != nil {
			return err
		}
	}
	secret, err := s.CredentialSecret(ctx, cred)
	if err != nil {
		return err
	}
	base := strings.TrimRight(cred.BaseURL, "/")
	if base == "" && provider.BaseURL != nil {
		base = strings.TrimRight(*provider.BaseURL, "/")
	}
	if base == "" {
		return E(http.StatusBadGateway, "gateway_unavailable", "provider has no upstream configured")
	}
	rel := strings.TrimPrefix(r.URL.Path, "/"+providerID)
	if rel == "" {
		rel = "/"
	}
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	target := base + rel
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		return E(http.StatusBadRequest, "invalid_request", "invalid upstream request")
	}
	sanitizeRequestHeaders(upstream.Header, r.Header)
	applyCredential(upstream, cred, secret, providerID)
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
	s.Audit.Record(ctx, "gateway.model", &sb.ID, map[string]any{"provider": providerID, "status": resp.StatusCode})
	return nil
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
