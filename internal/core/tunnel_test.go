package core

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func addTestService(t *testing.T, svc *Service, sandboxID, id string, port int) domain.Service {
	t.Helper()
	service := domain.Service{
		ID: id, SandboxID: sandboxID, Name: id, Protocol: "http",
		TargetHost: "127.0.0.1", TargetPort: port, Status: domain.ServiceReady,
	}
	if err := svc.Store.ReplaceServices(context.Background(), sandboxID, []domain.Service{service}); err != nil {
		t.Fatalf("replace services: %v", err)
	}
	return service
}

func runningServiceSandbox(t *testing.T, svc *Service, port int) (domain.Sandbox, domain.Service) {
	t.Helper()
	sb := insertSandbox(t, svc, domain.Sandbox{HarnessID: "shell", Status: domain.SandboxRunning})
	service := addTestService(t, svc, sb.ID, "svc_web", port)
	return sb, service
}

func TestServiceTicketValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.ServiceTicket(ctx, "sbx_missing", "svc_web")
	wantError(t, err, http.StatusNotFound, "not_found")

	stopped := insertSandbox(t, svc, domain.Sandbox{HarnessID: "shell", Status: domain.SandboxStopped})
	_, err = svc.ServiceTicket(ctx, stopped.ID, "svc_web")
	wantError(t, err, http.StatusConflict, "conflict")

	sb, _ := runningServiceSandbox(t, svc, 20001)
	ticket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	if ticket.Ticket == "" || ticket.ServiceID != "svc_web" || ticket.URL != "http://127.0.0.1:49153/tunnel" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if d := ticket.ExpiresAt.Sub(svc.now()); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("ticket ttl = %v", d)
	}
	// Services can also be addressed by name.
	if _, err := svc.ServiceTicket(ctx, sb.ID, "web"); err == nil {
		// The test service is stored as "svc_web" with name "svc_web"; only
		// exact ID/name matches are valid.
		t.Fatalf("unknown service id accepted")
	}
	_, err = svc.ServiceTicket(ctx, sb.ID, "svc_unknown")
	wantError(t, err, http.StatusNotFound, "service_not_found")
}

func TestTunnelTicketReuseExpiryAndCookieSession(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Target", "yes")
		w.Header().Add("Set-Cookie", "app=1; Path=/; HttpOnly")
		_, _ = w.Write([]byte("tunnel-body"))
	}))
	defer target.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	sb, _ := runningServiceSandbox(t, svc, port)
	_ = host
	ticket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tunnel/hello?q=1", nil)
	req.URL.RawQuery = "ticket=" + url.QueryEscape(ticket.Ticket) + "&q=1"
	rec := httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "tunnel-body" {
		t.Fatalf("first proxy = %d %q", rec.Code, rec.Body.String())
	}
	var sessionCookie *http.Cookie
	var appCookie string
	for _, raw := range rec.Result().Cookies() {
		if raw.Name == "nullkey_tunnel" {
			sessionCookie = raw
		}
		if raw.Name == "app" {
			appCookie = raw.Value
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("session cookie missing: %v", rec.Result().Cookies())
	}
	if appCookie != "1" {
		t.Fatalf("upstream cookie missing")
	}
	for _, raw := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(raw, "app=") && !strings.Contains(raw, "Path=/tunnel") {
			t.Fatalf("cookie path not rewritten: %q", raw)
		}
	}

	// Reusing the one-time ticket without a cookie fails.
	reuse := httptest.NewRequest(http.MethodGet, "/tunnel/again?ticket="+url.QueryEscape(ticket.Ticket), nil)
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, reuse)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ticket reuse = %d", rec.Code)
	}

	// The cookie session works without a ticket.
	sessionReq := httptest.NewRequest(http.MethodGet, "/tunnel/session", nil)
	sessionReq.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, sessionReq)
	if rec.Code != http.StatusOK || rec.Body.String() != "tunnel-body" {
		t.Fatalf("cookie proxy = %d %q", rec.Code, rec.Body.String())
	}

	// Expired tickets are rejected.
	expiredTicket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	svc.tunnel.mu.Lock()
	entry := svc.tunnel.tickets[expiredTicket.Ticket]
	entry.expires = time.Now().UTC().Add(-time.Second)
	svc.tunnel.tickets[expiredTicket.Ticket] = entry
	svc.tunnel.mu.Unlock()
	expiredReq := httptest.NewRequest(http.MethodGet, "/tunnel/x?ticket="+url.QueryEscape(expiredTicket.Ticket), nil)
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, expiredReq)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired ticket = %d", rec.Code)
	}

	// Expired session cookies are rejected too.
	svc.tunnel.mu.Lock()
	sess := svc.tunnel.sessions[sessionCookie.Value]
	sess.expires = time.Now().UTC().Add(-time.Minute)
	svc.tunnel.sessions[sessionCookie.Value] = sess
	svc.tunnel.mu.Unlock()
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, sessionReq)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session = %d", rec.Code)
	}

	// closeSandbox drops tickets and sessions for that sandbox only.
	otherTicket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	svc.tunnel.closeSandbox(sb.ID)
	svc.tunnel.mu.Lock()
	_, haveTicket := svc.tunnel.tickets[otherTicket.Ticket]
	_, haveSession := svc.tunnel.sessions[sessionCookie.Value]
	svc.tunnel.mu.Unlock()
	if haveTicket || haveSession {
		t.Fatalf("closeSandbox left state")
	}
}

func TestTunnelProxyPathHeadersAndOrigin(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	var gotPath, gotQuery, gotHost, gotAuth, gotTicketHeader string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHost = r.Host
		gotAuth = r.Header.Get("Authorization")
		gotTicketHeader = r.Header.Get("X-Nullkey-Ticket")
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)
	port, _ := strconv.Atoi(targetURL.Port())
	sb, _ := runningServiceSandbox(t, svc, port)
	ticket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tunnel/a/b", strings.NewReader("payload"))
	req.URL.RawQuery = "ticket=" + url.QueryEscape(ticket.Ticket) + "&k=v"
	req.Header.Set("Origin", "http://localhost:49152")
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	req.Header.Set("X-Forwarded-Host", "evil.test")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("proxy = %d %q", rec.Code, rec.Body.String())
	}
	if gotPath != "/a/b" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "k=v") {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotHost != targetURL.Host {
		t.Fatalf("host = %q, want %q", gotHost, targetURL.Host)
	}
	if gotAuth != "" || gotTicketHeader != "" {
		t.Fatalf("auth leaked upstream: %q %q", gotAuth, gotTicketHeader)
	}

	// A bearer-carried ticket is consumed and stripped from the upstream
	// request, as is the legacy X-Nullkey-Ticket header.
	authTicket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	authReq := httptest.NewRequest(http.MethodGet, "/tunnel/auth", nil)
	authReq.Header.Set("Authorization", "Bearer "+authTicket.Ticket)
	authReq.Header.Set("X-Nullkey-Ticket", "legacy")
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, authReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer proxy = %d", rec.Code)
	}
	if gotAuth != "" || gotTicketHeader != "" {
		t.Fatalf("bearer ticket leaked upstream: %q %q", gotAuth, gotTicketHeader)
	}

	// A bare /tunnel path maps to "/" upstream.
	rootTicket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	rootReq := httptest.NewRequest(http.MethodGet, "/tunnel?ticket="+url.QueryEscape(rootTicket.Ticket), nil)
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, rootReq)
	if rec.Code != http.StatusOK || gotPath != "/" {
		t.Fatalf("bare tunnel = %d path=%q", rec.Code, gotPath)
	}
	// Non-loopback origins are rejected before authentication.
	req = httptest.NewRequest(http.MethodGet, "/tunnel/x?ticket=whatever", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("origin = %d", rec.Code)
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("error body: %v", err)
	}
	if payload["error"]["code"] != "origin_not_allowed" {
		t.Fatalf("error = %+v", payload)
	}
}

func TestTunnelUnknownServiceAndBadGateway(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	sb, _ := runningServiceSandbox(t, svc, 20002)
	ticket, err := svc.ServiceTicket(ctx, sb.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	if err := svc.Store.ReplaceServices(ctx, sb.ID, nil); err != nil {
		t.Fatalf("clear services: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/tunnel/x?ticket="+url.QueryEscape(ticket.Ticket), nil)
	rec := httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown service = %d", rec.Code)
	}

	// A declared service whose port is closed yields 502.
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedPort := closedListener.Addr().(*net.TCPAddr).Port
	closedListener.Close()
	sb2, _ := runningServiceSandbox(t, svc, closedPort)
	ticket2, err := svc.ServiceTicket(ctx, sb2.ID, "svc_web")
	if err != nil {
		t.Fatalf("ticket: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/tunnel/x?ticket="+url.QueryEscape(ticket2.Ticket), nil)
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable service = %d", rec.Code)
	}

	// Without a ticket the request is unauthenticated.
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tunnel/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing ticket = %d", rec.Code)
	}

	// Bearer and X-Nullkey-Ticket are accepted as ticket carriers.
	sb3, _ := runningServiceSandbox(t, svc, 20003)
	bearerTicket, _ := svc.ServiceTicket(ctx, sb3.ID, "svc_web")
	req = httptest.NewRequest(http.MethodGet, "/tunnel/x", nil)
	req.Header.Set("Authorization", "Bearer "+bearerTicket.Ticket)
	rec = httptest.NewRecorder()
	svc.TunnelHandler().ServeHTTP(rec, req)
	// The target port is closed, so resolution succeeded but proxying failed.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("bearer ticket = %d", rec.Code)
	}
}

func TestRewriteCookiePath(t *testing.T) {
	cases := map[string]string{
		"a=b; Path=/":               "a=b; Path=/tunnel",
		"a=b; path=/deep; HttpOnly": "a=b; Path=/tunnel; HttpOnly",
		"a=b":                       "a=b; Path=/tunnel",
	}
	for in, want := range cases {
		if got := rewriteCookiePath(in); got != want {
			t.Fatalf("rewriteCookiePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 20001: "20001", -5: "-5", -20000: "-20000"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
	if got := itoa(1<<31 - 1); got != "2147483647" {
		t.Fatalf("itoa max = %q", got)
	}
}

func TestIsLoopbackOrigin(t *testing.T) {
	allowed := []string{
		"http://localhost:49152",
		"https://localhost",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
		"http://127.0.0.2",
	}
	for _, origin := range allowed {
		if !isLoopbackOrigin(origin) {
			t.Fatalf("%q should be loopback", origin)
		}
	}
	denied := []string{
		"", "not a url", "ftp://localhost", "http://example.com",
		"http://user:pass@localhost:8080", "http://10.0.0.1",
	}
	for _, origin := range denied {
		if isLoopbackOrigin(origin) {
			t.Fatalf("%q should be rejected", origin)
		}
	}
}

func TestWriteTunnelError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTunnelError(rec, http.StatusTeapot, "teapot", "short and stout")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if payload["error"]["code"] != "teapot" || payload["error"]["message"] != "short and stout" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestTerminalHandlers(t *testing.T) {
	svc, _ := newTestService(t, func(o *Options) { o.Terminal = nil })
	rec := httptest.NewRecorder()
	svc.TerminalHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/terminal", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("nil terminal handler = %d", rec.Code)
	}

	withHub, _ := newTestService(t)
	rec = httptest.NewRecorder()
	withHub.TerminalHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/terminal?ticket=bogus", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad ticket handler = %d", rec.Code)
	}
}
