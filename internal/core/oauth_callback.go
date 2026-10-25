package core

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

const oauthCallbackPath = "/auth/callback"

// oauthCallbackServer receives provider redirects that cannot target the
// control API. Codex is the first such provider: OpenAI only allows its public
// client to redirect to localhost:1455. The listener is bound only while a
// pending flow needs it, never at daemon startup, so Codex CLI's own login
// server keeps working when Nullkey is idle.
type oauthCallbackServer struct {
	svc    *Service
	addr   string
	server *http.Server
	flows  map[string]struct{}
	timer  *time.Timer
}

func newOAuthCallbackServer(s *Service, addr string) (*oauthCallbackServer, error) {
	listeners, err := listenOAuthCallback(addr)
	if err != nil {
		return nil, err
	}
	cb := &oauthCallbackServer{
		svc:   s,
		addr:  addr,
		flows: make(map[string]struct{}),
	}
	cb.server = &http.Server{
		Handler:           http.HandlerFunc(cb.handle),
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	for _, listener := range listeners {
		go func(ln net.Listener) {
			_ = cb.server.Serve(ln)
		}(listener)
	}
	return cb, nil
}

func (cb *oauthCallbackServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet || r.URL.Path != oauthCallbackPath {
		http.NotFound(w, r)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		writeOAuthPage(w, http.StatusBadRequest, false, "登录未完成，请回到 Nullkey 重试。")
		return
	}
	if _, err := cb.svc.CompleteOAuth(r.Context(), code, state); err != nil {
		writeOAuthPage(w, http.StatusBadRequest, false, "登录失败或已取消，请回到 Nullkey 重试。")
		return
	}
	writeOAuthPage(w, http.StatusOK, true, "登录完成，可以关闭此页面。")
}

func writeOAuthPage(w http.ResponseWriter, status int, ok bool, message string) {
	title := "登录失败"
	if ok {
		title = "登录完成"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s · Nullkey</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;background:#f6f6f4;color:#20201e;display:grid;place-items:center;min-height:100vh;margin:0}main{max-width:26rem;padding:2rem;text-align:center}h1{font-size:1.25rem;font-weight:600;margin:0 0 .5rem}p{color:#666;margin:0}</style>
</head><body><main><h1>%s</h1><p>%s</p></main></body></html>
`, title, title, message)
}

// listenOAuthCallback binds only loopback addresses. "localhost" is expanded to
// both families so a browser that resolves ::1 still reaches the listener.
func listenOAuthCallback(addr string) ([]net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return nil, fmt.Errorf("oauth callback listen address %q is invalid", addr)
	}
	var targets []string
	if host == "localhost" {
		targets = []string{
			net.JoinHostPort("127.0.0.1", port),
			net.JoinHostPort("::1", port),
		}
	} else {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("oauth callback listener must be loopback")
		}
		targets = []string{addr}
	}
	var listeners []net.Listener
	var lastErr error
	for _, target := range targets {
		ln, err := net.Listen("tcp", target)
		if err != nil {
			lastErr = err
			continue
		}
		listeners = append(listeners, ln)
	}
	if len(listeners) == 0 {
		return nil, lastErr
	}
	return listeners, nil
}

func (s *Service) startOAuthCallback(flowID, addr string) error {
	if addr == "" {
		return nil
	}
	s.oauth.callbackMu.Lock()
	defer s.oauth.callbackMu.Unlock()
	cb := s.oauth.callback
	if cb != nil && cb.addr != addr {
		return E(http.StatusConflict, "oauth_unavailable", "another OAuth callback listener is already active")
	}
	if cb == nil {
		var err error
		cb, err = newOAuthCallbackServer(s, addr)
		if err != nil {
			return E(http.StatusConflict, "oauth_unavailable", "OAuth callback port is already in use")
		}
		s.oauth.callback = cb
	}
	cb.flows[flowID] = struct{}{}
	if cb.timer == nil {
		cb.timer = time.AfterFunc(11*time.Minute, s.expireOAuthCallback)
	} else {
		cb.timer.Reset(11 * time.Minute)
	}
	return nil
}

// releaseOAuthCallback removes one flow. The server is closed shortly after the
// last flow so the HTTP response can still reach the browser.
func (s *Service) releaseOAuthCallback(flowID string) {
	s.oauth.callbackMu.Lock()
	defer s.oauth.callbackMu.Unlock()
	cb := s.oauth.callback
	if cb == nil {
		return
	}
	delete(cb.flows, flowID)
	if len(cb.flows) != 0 {
		return
	}
	if cb.timer != nil {
		cb.timer.Stop()
	}
	cb.timer = time.AfterFunc(2*time.Second, s.expireOAuthCallback)
}

// expireOAuthCallback frees the fixed callback port if a flow was abandoned.
func (s *Service) expireOAuthCallback() {
	s.oauth.callbackMu.Lock()
	defer s.oauth.callbackMu.Unlock()
	s.closeOAuthCallbackLocked()
}

func (s *Service) closeOAuthCallbacks() {
	s.oauth.callbackMu.Lock()
	defer s.oauth.callbackMu.Unlock()
	s.closeOAuthCallbackLocked()
}

func (s *Service) closeOAuthCallbackLocked() {
	cb := s.oauth.callback
	if cb == nil {
		return
	}
	s.oauth.callback = nil
	if cb.timer != nil {
		cb.timer.Stop()
	}
	_ = cb.server.Close()
}
