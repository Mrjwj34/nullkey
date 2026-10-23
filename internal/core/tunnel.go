package core

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

type tunnelTicket struct {
	sandboxID string
	serviceID string
	expires   time.Time
}

type tunnelSession struct {
	sandboxID string
	serviceID string
	expires   time.Time
}

type tunnelServer struct {
	svc      *Service
	mu       sync.Mutex
	tickets  map[string]tunnelTicket
	sessions map[string]tunnelSession
}

func newTunnelServer(s *Service) *tunnelServer {
	return &tunnelServer{svc: s, tickets: make(map[string]tunnelTicket), sessions: make(map[string]tunnelSession)}
}

// ServiceTicket mints a one-time 60s tunnel ticket for a declared service.
func (s *Service) ServiceTicket(ctx context.Context, sandboxID, serviceID string) (domain.ServiceTicket, error) {
	sb, err := s.GetSandbox(ctx, sandboxID)
	if err != nil {
		return domain.ServiceTicket{}, err
	}
	if sb.Status != domain.SandboxRunning {
		return domain.ServiceTicket{}, Conflict("conflict", "sandbox is not running")
	}
	svc, err := s.Store.GetService(ctx, sandboxID, serviceID)
	if err != nil {
		return domain.ServiceTicket{}, E(http.StatusNotFound, "service_not_found", "service is not declared")
	}
	raw, err := randomURL(32)
	if err != nil {
		return domain.ServiceTicket{}, Wrap(http.StatusInternalServerError, "internal", "could not mint service ticket", err)
	}
	expires := s.now().Add(60 * time.Second)
	s.tunnel.mu.Lock()
	s.tunnel.pruneLocked()
	s.tunnel.tickets[raw] = tunnelTicket{sandboxID: sandboxID, serviceID: svc.ID, expires: expires}
	s.tunnel.mu.Unlock()
	return domain.ServiceTicket{
		URL:       "http://" + s.GatewayAddr + "/tunnel",
		Ticket:    raw,
		ServiceID: svc.ID,
		ExpiresAt: expires,
	}, nil
}

func (t *tunnelServer) closeSandbox(sandboxID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.tickets {
		if v.sandboxID == sandboxID {
			delete(t.tickets, k)
		}
	}
	for k, v := range t.sessions {
		if v.sandboxID == sandboxID {
			delete(t.sessions, k)
		}
	}
}

func (t *tunnelServer) pruneLocked() {
	now := time.Now().UTC()
	for key, ticket := range t.tickets {
		if now.After(ticket.expires) {
			delete(t.tickets, key)
		}
	}
	for key, session := range t.sessions {
		if now.After(session.expires) {
			delete(t.sessions, key)
		}
	}
}

func (t *tunnelServer) resolveSession(w http.ResponseWriter, r *http.Request) (tunnelSession, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	if cookie, err := r.Cookie("nullkey_tunnel"); err == nil {
		if sess, ok := t.sessions[cookie.Value]; ok && time.Now().UTC().Before(sess.expires) {
			return sess, true
		}
	}
	raw := ""
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.Fields(h)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			raw = parts[1]
		}
	}
	if raw == "" {
		raw = r.Header.Get("X-Nullkey-Ticket")
	}
	if raw == "" {
		raw = r.URL.Query().Get("ticket")
	}
	if raw == "" {
		return tunnelSession{}, false
	}
	ticket, ok := t.tickets[raw]
	delete(t.tickets, raw)
	if !ok || time.Now().UTC().After(ticket.expires) {
		return tunnelSession{}, false
	}
	token, err := randomURL(24)
	if err != nil {
		return tunnelSession{}, false
	}
	sess := tunnelSession{sandboxID: ticket.sandboxID, serviceID: ticket.serviceID, expires: time.Now().UTC().Add(10 * time.Minute)}
	t.sessions[token] = sess
	http.SetCookie(w, &http.Cookie{
		Name:     "nullkey_tunnel",
		Value:    token,
		Path:     "/tunnel",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return sess, true
}

// ServeHTTP proxies a declared service. It rejects unknown services, arbitrary
// ports and non-loopback Origins.
func (t *tunnelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		writeTunnelError(w, http.StatusForbidden, "origin_not_allowed", "origin is not allowed")
		return
	}
	sess, ok := t.resolveSession(w, r)
	if !ok {
		writeTunnelError(w, http.StatusUnauthorized, "ticket_invalid", "service ticket is invalid or expired")
		return
	}
	svc, err := t.svc.Store.GetService(r.Context(), sess.sandboxID, sess.serviceID)
	if err != nil {
		writeTunnelError(w, http.StatusNotFound, "service_not_found", "service is not declared")
		return
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(svc.TargetHost, itoa(svc.TargetPort))}
	rawQuery := r.URL.RawQuery
	if q := r.URL.Query(); q.Has("ticket") {
		q.Del("ticket")
		rawQuery = q.Encode()
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/tunnel")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.URL.RawQuery = rawQuery
		req.Host = target.Host
		// Never trust client-supplied forwarding headers.
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-Proto")
		req.Header.Del("X-Nullkey-Ticket")
		req.Header.Del("Authorization")
	}
	proxy.FlushInterval = -1
	proxy.ModifyResponse = func(resp *http.Response) error {
		for _, cookie := range resp.Header.Values("Set-Cookie") {
			resp.Header.Del("Set-Cookie")
			resp.Header.Add("Set-Cookie", rewriteCookiePath(cookie))
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeTunnelError(w, http.StatusBadGateway, "service_unavailable", "service did not answer")
	}
	proxy.ServeHTTP(w, r)
}

func rewriteCookiePath(cookie string) string {
	parts := strings.Split(cookie, ";")
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(trimmed), "path=") {
			parts[i] = " Path=/tunnel"
			return strings.Join(parts, ";")
		}
	}
	return cookie + "; Path=/tunnel"
}

func writeTunnelError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message, "request_id": ""},
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
