package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeResolver struct {
	mu     sync.Mutex
	modes  map[string]domain.NetworkMode
	tokens []string
}

func (f *fakeResolver) ResolveProxyToken(token string) (string, domain.NetworkMode, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	mode, ok := f.modes[token]
	if !ok {
		return "", "", false
	}
	return "sbx_test", mode, true
}

func (f *fakeResolver) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...)
}

func mustProxy(t *testing.T, cfg Config) *Proxy {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func startHTTPServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Port()
}

func proxyClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(u)}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

type captured struct {
	mu     sync.Mutex
	method string
	path   string
	query  string
	body   string
	header http.Header
}

func (c *captured) snapshot() (method, path, query, body string, header http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.method, c.path, c.query, c.body, c.header.Clone()
}

func startCaptureUpstream(t *testing.T) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.body = string(body)
		cap.header = r.Header.Clone()
		cap.mu.Unlock()

		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("X-Resp-Hop", "secret")
		w.Header().Set("Connection", "X-Resp-Hop")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

type proxyResponse struct {
	status int
	header http.Header
	body   []byte
}

func readProxyHead(t *testing.T, br *bufio.Reader) (int, http.Header) {
	t.Helper()
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	fields := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(fields) < 2 {
		t.Fatalf("malformed status line %q", line)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse status %q: %v", fields[1], err)
	}
	header := make(http.Header)
	for {
		line, err = br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		header.Add(strings.TrimSpace(key), strings.TrimSpace(value))
	}
	return status, header
}

func readProxyResponse(t *testing.T, br *bufio.Reader) proxyResponse {
	t.Helper()
	status, header := readProxyHead(t, br)
	var body []byte
	if raw := header.Get("Content-Length"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("parse content-length %q: %v", raw, err)
		}
		body = make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			t.Fatalf("read body: %v", err)
		}
	}
	return proxyResponse{status: status, header: header, body: body}
}

func rawConnect(t *testing.T, proxyAddr, target, auth string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if auth != "" {
		fmt.Fprintf(&request, "Proxy-Authorization: %s\r\n", auth)
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

func decodeProxyEnvelope(t *testing.T, data []byte) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", data, err)
	}
	return env
}

func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) errorEnvelope {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, wantStatus, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	env := decodeProxyEnvelope(t, rec.Body.Bytes())
	if env.Error.Code != wantCode {
		t.Fatalf("code = %q, want %q", env.Error.Code, wantCode)
	}
	if env.Error.Message == "" {
		t.Fatal("empty error message")
	}
	if env.Error.RequestID == "" {
		t.Fatal("empty request_id")
	}
	if got := rec.Header().Get("X-Request-Id"); got != env.Error.RequestID {
		t.Fatalf("X-Request-Id = %q, envelope request_id = %q", got, env.Error.RequestID)
	}
	return env
}

func assertNoSecrets(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%s leaked %q: %s", what, secret, text)
		}
	}
}

// ---------------------------------------------------------------------------
// constructor / mode tests
// ---------------------------------------------------------------------------

func TestNewDefaultsAndMode(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.logger == nil {
		t.Fatal("New did not install a default logger")
	}
	if got := p.Mode(); got != domain.NetworkFiltered {
		t.Fatalf("Mode() = %q, want filtered", got)
	}
	if len(p.allowlist) != 0 {
		t.Fatalf("allowlist = %v, want empty", p.allowlist)
	}

	for _, mode := range []domain.NetworkMode{
		domain.NetworkServicesOnly,
		domain.NetworkFiltered,
		domain.NetworkOpen,
	} {
		p := mustProxy(t, Config{DefaultMode: mode})
		if got := p.Mode(); got != mode {
			t.Fatalf("Mode() = %q, want %q", got, mode)
		}
	}
}

func TestNewRejectsInvalidMode(t *testing.T) {
	if _, err := New(Config{DefaultMode: domain.NetworkMode("bogus")}); err == nil {
		t.Fatal("New accepted an invalid mode")
	}
}

func TestNewCopiesAllowlist(t *testing.T) {
	entries := []string{"Example.COM", ".sub.example.org", "*.wild.example", "", "  "}
	p := mustProxy(t, Config{Allowlist: entries})
	for _, host := range []string{"example.com", "api.example.com", "sub.example.org", "x.sub.example.org", "wild.example", "a.wild.example"} {
		if !p.hostAllowed(host) {
			t.Fatalf("hostAllowed(%q) = false", host)
		}
	}
	for _, host := range []string{"badexample.com", "example.com.evil", "example.org"} {
		if p.hostAllowed(host) {
			t.Fatalf("hostAllowed(%q) = true", host)
		}
	}

	// Mutating the caller's slice after New must not change policy.
	entries[0] = "attacker.example"
	if p.hostAllowed("attacker.example") {
		t.Fatal("allowlist was not copied")
	}
}

func TestModeIsDefaultOnly(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkOpen})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", "Bearer anything")
	if got := p.Mode(); got != domain.NetworkOpen {
		t.Fatalf("Mode() = %q, want open", got)
	}
	// Mode is the default; per-request resolution requires a resolver.
	if got := p.resolveMode(req); got != domain.NetworkOpen {
		t.Fatalf("resolveMode() = %q, want open", got)
	}
}

// ---------------------------------------------------------------------------
// plain HTTP proxy behavior
// ---------------------------------------------------------------------------

func TestPlainHTTPForwarding(t *testing.T) {
	upstream, cap := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		AllowLoopback: true,
	})

	body := strings.NewReader("request-body")
	req := httptest.NewRequest(http.MethodPost, upstream.URL+"/some/path?q=42", body)
	req.Header.Set("Proxy-Authorization", "Bearer sandbox-token")
	req.Header.Set("Connection", "X-Hop")
	req.Header.Set("X-Hop", "remove-me")
	req.Header.Set("X-Keep", "keep-me")
	req.Header.Set("User-Agent", "client-agent")
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "upstream-ok" {
		t.Fatalf("body = %q, want upstream-ok", got)
	}
	if got := rec.Header().Get("X-Upstream"); got != "yes" {
		t.Fatalf("X-Upstream = %q", got)
	}
	if got := rec.Header().Get("X-Resp-Hop"); got != "" {
		t.Fatalf("hop-by-hop response header leaked: %q", got)
	}
	if got := rec.Header().Get("Connection"); got != "" {
		t.Fatalf("Connection response header leaked: %q", got)
	}

	method, path, query, gotBody, header := cap.snapshot()
	if method != http.MethodPost || path != "/some/path" || query != "q=42" || gotBody != "request-body" {
		t.Fatalf("upstream saw %s %s?%s body=%q", method, path, query, gotBody)
	}
	if got := header.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("Proxy-Authorization leaked upstream: %q", got)
	}
	if got := header.Get("X-Hop"); got != "" {
		t.Fatalf("Connection-listed header leaked upstream: %q", got)
	}
	if got := header.Get("Connection"); got != "" {
		t.Fatalf("Connection leaked upstream: %q", got)
	}
	if got := header.Get("X-Keep"); got != "keep-me" {
		t.Fatalf("X-Keep = %q, want keep-me", got)
	}
	if got := header.Get("User-Agent"); got != "client-agent" {
		t.Fatalf("User-Agent = %q, want client-agent", got)
	}

	// A request without a User-Agent must not gain net/http's default one.
	req = httptest.NewRequest(http.MethodGet, upstream.URL+"/no-user-agent", nil)
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("no-UA status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, path, _, _, header = cap.snapshot()
	if path != "/no-user-agent" {
		t.Fatalf("upstream path = %q", path)
	}
	if got := header.Get("User-Agent"); got != "" {
		t.Fatalf("proxy injected User-Agent %q", got)
	}
}

func TestPlainHTTPNilHeader(t *testing.T) {
	upstream, _ := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		AllowLoopback: true,
	})
	u, err := url.Parse(upstream.URL + "/nil-header")
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestPlainHTTPThroughProxyClient(t *testing.T) {
	upstream, cap := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	resolver := &fakeResolver{modes: map[string]domain.NetworkMode{
		"open-token":     domain.NetworkOpen,
		"filtered-token": domain.NetworkFiltered,
	}}
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		Resolver:      resolver,
		AllowLoopback: true,
	})
	proxySrv := startHTTPServer(t, p)
	client := proxyClient(t, proxySrv.URL)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/through-proxy?x=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", "Bearer open-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream-ok" {
		t.Fatalf("body = %q", body)
	}
	method, path, query, _, header := cap.snapshot()
	if method != http.MethodGet || path != "/through-proxy" || query != "x=1" {
		t.Fatalf("upstream saw %s %s?%s", method, path, query)
	}
	if got := header.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("Proxy-Authorization leaked upstream: %q", got)
	}
}

func TestPlainHTTPModeMatrix(t *testing.T) {
	upstream, _ := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	resolver := &fakeResolver{modes: map[string]domain.NetworkMode{
		"open-token":     domain.NetworkOpen,
		"filtered-token": domain.NetworkFiltered,
		"services-token": domain.NetworkServicesOnly,
		"bogus-token":    domain.NetworkMode("bogus"),
	}}
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		Resolver:      resolver,
		AllowLoopback: true,
	})
	// Unknown hosts must not consult DNS in filtered mode; force resolution
	// failure to make open-mode behavior deterministic and offline.
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("test resolver disabled")
	}
	proxySrv := startHTTPServer(t, p)
	client := proxyClient(t, proxySrv.URL)

	type testCase struct {
		name       string
		target     string
		auth       string
		wantStatus int
		wantCode   string
	}
	cases := []testCase{
		{name: "filtered allowlisted", target: upstream.URL, auth: "Bearer filtered-token", wantStatus: http.StatusCreated},
		{name: "open allowlisted", target: upstream.URL, auth: "Bearer open-token", wantStatus: http.StatusCreated},
		{name: "default filtered allowlisted", target: upstream.URL, wantStatus: http.StatusCreated},
		{name: "unknown token falls back to filtered", target: upstream.URL, auth: "Bearer unknown-token", wantStatus: http.StatusCreated},
		{name: "invalid mode falls back to filtered", target: upstream.URL, auth: "Bearer bogus-token", wantStatus: http.StatusCreated},
		{name: "filtered denied", target: "http://denied.invalid/secret", auth: "Bearer filtered-token", wantStatus: http.StatusForbidden, wantCode: codeNetworkDenied},
		{name: "default filtered denied", target: "http://denied.invalid/secret", wantStatus: http.StatusForbidden, wantCode: codeNetworkDenied},
		{name: "services only denies allowlisted", target: upstream.URL, auth: "Bearer services-token", wantStatus: http.StatusForbidden, wantCode: codeNetworkDenied},
		{name: "open passes filtered policy", target: "http://denied.invalid/secret", auth: "Bearer open-token", wantStatus: http.StatusBadGateway, wantCode: codeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth != "" {
				req.Header.Set("Proxy-Authorization", tc.auth)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.wantCode != "" {
				var env errorEnvelope
				if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
					t.Fatalf("decode envelope: %v", err)
				}
				if env.Error.Code != tc.wantCode {
					t.Fatalf("code = %q, want %q", env.Error.Code, tc.wantCode)
				}
			}
		})
	}
}

func TestPlainHTTPLoopbackDeniedByDefault(t *testing.T) {
	upstream, _ := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	p := mustProxy(t, Config{
		DefaultMode: domain.NetworkFiltered,
		Allowlist:   []string{host},
		// AllowLoopback intentionally false.
	})
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/local", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)
}

func TestPlainHTTPProhibitedAddressesDenied(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkOpen, AllowLoopback: true})
	for _, target := range []string{
		"http://10.1.2.3/",
		"http://169.254.10.1/",
		"http://0.0.0.0/",
		"http://239.1.1.1/",
		"http://[::1]/",
		"http://[fe80::1]/",
	} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)
		})
	}
}

func TestPlainHTTPRejectsNonProxyRequests(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkOpen, AllowLoopback: true})

	cases := []struct {
		name string
		req  *http.Request
	}{
		{
			name: "origin form",
			req:  httptest.NewRequest(http.MethodGet, "/relative/path", nil),
		},
		{
			name: "https absolute form",
			req:  httptest.NewRequest(http.MethodGet, "https://example.com/secret", nil),
		},
		{
			name: "nil URL",
			req:  &http.Request{Method: http.MethodGet},
		},
		{
			name: "opaque URL",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Scheme: "http", Opaque: "example.com/path", Host: "example.com"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, tc.req)
			assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)
		})
	}
}

func TestPlainHTTPRejectsBadPort(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkOpen, AllowLoopback: true})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.URL = &url.URL{Scheme: "http", Host: "example.com:99999", Path: "/"}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)
}

func TestPlainHTTPDialFailureEnvelope(t *testing.T) {
	upstream, _ := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		AllowLoopback: true,
	})
	p.transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("test dial failure")
	}

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/very-secret-path?access_token=super-secret-query-value", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	env := assertErrorEnvelope(t, rec, http.StatusBadGateway, codeInternal)
	assertNoSecrets(t, "error message", env.Error.Message, "very-secret-path", "access_token", "super-secret-query-value")
}

func TestPlainHTTPUpstreamRedirectNotFollowed(t *testing.T) {
	upstream := startHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/", http.StatusFound)
	}))
	host := hostOf(t, upstream.URL)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		AllowLoopback: true,
	})
	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/redirect", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "http://elsewhere.invalid/" {
		t.Fatalf("Location = %q", loc)
	}
}

// ---------------------------------------------------------------------------
// CONNECT behavior
// ---------------------------------------------------------------------------

func TestConnectTunnelToEchoServer(t *testing.T) {
	echoAddr := startEchoServer(t)
	echoHost, _, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{echoHost},
		AllowLoopback: true,
	})
	proxySrv := startHTTPServer(t, p)

	conn, br := rawConnect(t, proxySrv.Listener.Addr().String(), echoAddr, "")
	status, _ := readProxyHead(t, br)
	if status != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", status)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write tunnel: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read tunnel: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want ping", got)
	}
}

func TestConnectPipelinedData(t *testing.T) {
	echoAddr := startEchoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkOpen,
		Allowlist:     []string{echoHost},
		AllowLoopback: true,
	})
	proxySrv := startHTTPServer(t, p)

	conn, err := net.Dial("tcp", proxySrv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nearly-data", echoAddr, echoAddr)
	br := bufio.NewReader(conn)
	status, _ := readProxyHead(t, br)
	if status != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", status)
	}
	got := make([]byte, len("early-data"))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read pipelined echo: %v", err)
	}
	if string(got) != "early-data" {
		t.Fatalf("echo = %q", got)
	}
}

func TestConnectModeMatrixAndPolicy(t *testing.T) {
	echoAddr := startEchoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	resolver := &fakeResolver{modes: map[string]domain.NetworkMode{
		"open-token":     domain.NetworkOpen,
		"filtered-token": domain.NetworkFiltered,
		"services-token": domain.NetworkServicesOnly,
	}}

	baseCfg := Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{echoHost},
		Resolver:      resolver,
		AllowLoopback: true,
	}
	p := mustProxy(t, baseCfg)
	proxySrv := startHTTPServer(t, p)

	cases := []struct {
		name       string
		target     string
		auth       string
		wantStatus int
		wantCode   string
	}{
		{name: "default filtered allowlisted", target: echoAddr, wantStatus: http.StatusOK},
		{name: "filtered allowlisted", target: echoAddr, auth: "Bearer filtered-token", wantStatus: http.StatusOK},
		{name: "open allowlisted", target: echoAddr, auth: "Bearer open-token", wantStatus: http.StatusOK},
		{name: "lowercase bearer", target: echoAddr, auth: "bearer filtered-token", wantStatus: http.StatusOK},
		{name: "services only denies", target: echoAddr, auth: "Bearer services-token", wantStatus: http.StatusForbidden, wantCode: codeNetworkDenied},
		{name: "filtered host denied", target: "denied.invalid:443", auth: "Bearer filtered-token", wantStatus: http.StatusForbidden, wantCode: codeNetworkDenied},
		{name: "default filtered host denied", target: "denied.invalid:443", wantStatus: http.StatusForbidden, wantCode: codeNetworkDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, br := rawConnect(t, proxySrv.Listener.Addr().String(), tc.target, tc.auth)
			if tc.wantStatus == http.StatusOK {
				status, _ := readProxyHead(t, br)
				if status != http.StatusOK {
					t.Fatalf("status = %d, want 200", status)
				}
				if _, err := conn.Write([]byte("x")); err != nil {
					t.Fatalf("write: %v", err)
				}
				one := make([]byte, 1)
				if _, err := io.ReadFull(br, one); err != nil {
					t.Fatalf("read: %v", err)
				}
				if one[0] != 'x' {
					t.Fatalf("echo = %q", one)
				}
				return
			}
			resp := readProxyResponse(t, br)
			if resp.status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", resp.status, tc.wantStatus, resp.body)
			}
			env := decodeProxyEnvelope(t, resp.body)
			if env.Error.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", env.Error.Code, tc.wantCode)
			}
			if got := resp.header.Get("X-Request-Id"); got == "" || got != env.Error.RequestID {
				t.Fatalf("X-Request-Id = %q, request_id = %q", got, env.Error.RequestID)
			}
		})
	}
}

func TestConnectLoopbackDeniedByDefault(t *testing.T) {
	echoAddr := startEchoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	p := mustProxy(t, Config{
		DefaultMode: domain.NetworkFiltered,
		Allowlist:   []string{echoHost},
		// AllowLoopback intentionally false.
	})
	proxySrv := startHTTPServer(t, p)

	_, br := rawConnect(t, proxySrv.Listener.Addr().String(), echoAddr, "")
	resp := readProxyResponse(t, br)
	if resp.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.status)
	}
	if env := decodeProxyEnvelope(t, resp.body); env.Error.Code != codeNetworkDenied {
		t.Fatalf("code = %q, want %q", env.Error.Code, codeNetworkDenied)
	}
}

func TestConnectAllowLoopbackRequiresAllowlist(t *testing.T) {
	echoAddr := startEchoServer(t)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkOpen,
		Allowlist:     []string{"example.com"},
		AllowLoopback: true,
	})
	proxySrv := startHTTPServer(t, p)
	_, br := rawConnect(t, proxySrv.Listener.Addr().String(), echoAddr, "")
	resp := readProxyResponse(t, br)
	if resp.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.status)
	}
	if env := decodeProxyEnvelope(t, resp.body); env.Error.Code != codeNetworkDenied {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestConnectLoopbackAliases(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, echoPort, _ := net.SplitHostPort(echoAddr)

	t.Run("127.0.0.1 target with localhost allowlist", func(t *testing.T) {
		p := mustProxy(t, Config{
			DefaultMode:   domain.NetworkFiltered,
			Allowlist:     []string{"localhost"},
			AllowLoopback: true,
		})
		proxySrv := startHTTPServer(t, p)
		conn, br := rawConnect(t, proxySrv.Listener.Addr().String(), echoAddr, "")
		status, _ := readProxyHead(t, br)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		_, _ = conn.Write([]byte("a"))
		one := make([]byte, 1)
		if _, err := io.ReadFull(br, one); err != nil {
			t.Fatalf("read: %v", err)
		}
	})

	t.Run("localhost target with 127.0.0.1 allowlist", func(t *testing.T) {
		p := mustProxy(t, Config{
			DefaultMode:   domain.NetworkFiltered,
			Allowlist:     []string{"127.0.0.1"},
			AllowLoopback: true,
		})
		proxySrv := startHTTPServer(t, p)
		conn, br := rawConnect(t, proxySrv.Listener.Addr().String(), net.JoinHostPort("localhost", echoPort), "")
		status, _ := readProxyHead(t, br)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		_, _ = conn.Write([]byte("b"))
		one := make([]byte, 1)
		if _, err := io.ReadFull(br, one); err != nil {
			t.Fatalf("read: %v", err)
		}
	})
}

func TestConnectInvalidTargets(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkOpen, AllowLoopback: true})
	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "empty", target: ""},
		{name: "missing port", target: "example.com"},
		{name: "empty host", target: ":443"},
		{name: "zero port", target: "example.com:0"},
		{name: "port too large", target: "example.com:65536"},
		{name: "non numeric port", target: "example.com:not-a-port"},
		{name: "ipv6 missing port", target: "[::1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{
				Method: http.MethodConnect,
				Host:   tc.target,
				Header: make(http.Header),
				URL:    &url.URL{},
			}
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)
		})
	}
}

func TestConnectHijackUnavailable(t *testing.T) {
	echoAddr := startEchoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{echoHost},
		AllowLoopback: true,
	})
	req := &http.Request{
		Method: http.MethodConnect,
		Host:   echoAddr,
		Header: make(http.Header),
		URL:    &url.URL{},
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assertErrorEnvelope(t, rec, http.StatusBadGateway, codeInternal)
}

func TestConnectURLAuthorityFallback(t *testing.T) {
	echoAddr := startEchoServer(t)
	echoHost, _, _ := net.SplitHostPort(echoAddr)
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{echoHost},
		AllowLoopback: true,
	})
	req := &http.Request{
		Method: http.MethodConnect,
		Header: make(http.Header),
		URL:    &url.URL{Host: echoAddr},
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	// The request got past target parsing and policy; the recorder cannot
	// hijack, so the proxy returns the internal envelope.
	assertErrorEnvelope(t, rec, http.StatusBadGateway, codeInternal)
}

func TestConnectResolveFailure(t *testing.T) {
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkOpen,
		AllowLoopback: true,
	})
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("test DNS failure")
	}
	proxySrv := startHTTPServer(t, p)
	_, br := rawConnect(t, proxySrv.Listener.Addr().String(), "unresolvable.invalid:443", "")
	resp := readProxyResponse(t, br)
	if resp.status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.status)
	}
	env := decodeProxyEnvelope(t, resp.body)
	if env.Error.Code != codeInternal {
		t.Fatalf("code = %q, want internal", env.Error.Code)
	}
	assertNoSecrets(t, "error message", env.Error.Message, "unresolvable.invalid", "/")
}

func TestConnectDialFailure(t *testing.T) {
	// Reserve a loopback port, then close it so the dial is refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()
	host, _, _ := net.SplitHostPort(closedAddr)

	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{host},
		AllowLoopback: true,
	})
	p.dialer = net.Dialer{Timeout: 200 * time.Millisecond}
	proxySrv := startHTTPServer(t, p)
	_, br := rawConnect(t, proxySrv.Listener.Addr().String(), closedAddr, "")
	resp := readProxyResponse(t, br)
	if resp.status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.status)
	}
	env := decodeProxyEnvelope(t, resp.body)
	if env.Error.Code != codeInternal {
		t.Fatalf("code = %q, want internal", env.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// resolver binding
// ---------------------------------------------------------------------------

func TestResolverBindsModePerRequest(t *testing.T) {
	resolver := &fakeResolver{modes: map[string]domain.NetworkMode{
		"token-a": domain.NetworkOpen,
		"token-b": domain.NetworkFiltered,
		"token-c": domain.NetworkServicesOnly,
	}}
	p := mustProxy(t, Config{
		DefaultMode: domain.NetworkFiltered,
		Resolver:    resolver,
		Allowlist:   []string{"allowed.example"},
	})

	request := func(auth string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "http://allowed.example/", nil)
		if auth != "" {
			req.Header.Set("Proxy-Authorization", auth)
		}
		return req
	}

	cases := []struct {
		name string
		req  *http.Request
		want domain.NetworkMode
	}{
		{name: "token a", req: request("Bearer token-a"), want: domain.NetworkOpen},
		{name: "token b", req: request("Bearer token-b"), want: domain.NetworkFiltered},
		{name: "token c", req: request("Bearer token-c"), want: domain.NetworkServicesOnly},
		{name: "unknown token", req: request("Bearer unknown"), want: domain.NetworkFiltered},
		{name: "missing header", req: request(""), want: domain.NetworkFiltered},
		{name: "non bearer", req: request("Basic abc"), want: domain.NetworkFiltered},
		{name: "empty bearer", req: request("Bearer "), want: domain.NetworkFiltered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.resolveMode(tc.req); got != tc.want {
				t.Fatalf("resolveMode = %q, want %q", got, tc.want)
			}
		})
	}

	// A previous open-token request must not leak its mode into a later
	// filtered-token request. The filtered request must be denied before any
	// DNS lookup.
	reqA := request("Bearer token-a")
	reqB := request("Bearer token-b")
	if got := p.resolveMode(reqA); got != domain.NetworkOpen {
		t.Fatalf("token A mode = %q, want open", got)
	}
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		t.Fatal("filtered denial must not resolve DNS")
		return nil, errors.New("unreachable")
	}
	reqB.URL, _ = url.Parse("http://denied.example/secret")
	reqB.Host = "denied.example"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, reqB)
	assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)

	seen := resolver.seen()
	wantTokens := []string{"token-a", "token-b", "token-c", "unknown"}
	for _, token := range wantTokens {
		found := false
		for _, got := range seen {
			if got == token {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resolver never saw token %q (saw %v)", token, seen)
		}
	}
}

// ---------------------------------------------------------------------------
// logging and envelope secrecy
// ---------------------------------------------------------------------------

func TestLoggingOmitsSecretsAndURL(t *testing.T) {
	upstream, _ := startCaptureUpstream(t)
	host := hostOf(t, upstream.URL)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	p := mustProxy(t, Config{
		DefaultMode: domain.NetworkFiltered,
		Allowlist:   []string{host},
		Resolver: &fakeResolver{modes: map[string]domain.NetworkMode{
			"open-token": domain.NetworkOpen,
		}},
		Logger:        logger,
		AllowLoopback: true,
	})

	// Denied request carries a secret path, query, and proxy token.
	req := httptest.NewRequest(http.MethodGet, "http://denied.invalid/very-secret-path?access_token=super-secret-query-value", nil)
	req.Header.Set("Proxy-Authorization", "Bearer super-secret-proxy-token")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d", rec.Code)
	}
	body := rec.Body.String()
	assertNoSecrets(t, "error body", body, "very-secret-path", "access_token", "super-secret-query-value", "super-secret-proxy-token")

	// Allowed request carries a secret path/query/body.
	req = httptest.NewRequest(http.MethodPost, upstream.URL+"/visible-body-path?token=visible-body-query", strings.NewReader("visible-body-secret"))
	req.Header.Set("Proxy-Authorization", "Bearer open-token")
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("allowed status = %d (body %s)", rec.Code, rec.Body.String())
	}

	// A malformed origin-form request with path/query smuggled into Host must
	// not put that text into logs either.
	badHost := &http.Request{
		Method: http.MethodGet,
		Host:   "evil.example/secret-path?token=log-host-secret",
		Header: make(http.Header),
		URL:    &url.URL{},
	}
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, badHost)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad host status = %d, want 403", rec.Code)
	}

	text := logs.String()
	assertNoSecrets(t, "logs", text,
		"very-secret-path", "access_token", "super-secret-query-value", "super-secret-proxy-token",
		"visible-body-path", "visible-body-query", "visible-body-secret",
		"secret-path", "log-host-secret", "evil.example/secret",
	)
	for _, want := range []string{"host=", "status=", "bytes=", "duration="} {
		if !strings.Contains(text, want) {
			t.Fatalf("logs missing %q: %s", want, text)
		}
	}
	if !strings.Contains(text, "denied.invalid") {
		t.Fatalf("logs missing target host: %s", text)
	}
}

func TestErrorEnvelopeShapeAndSecrecy(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkFiltered, Allowlist: []string{"allowed.example"}})
	req := httptest.NewRequest(http.MethodGet, "http://denied.example/secret-path?api_key=secret-value", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	env := assertErrorEnvelope(t, rec, http.StatusForbidden, codeNetworkDenied)
	assertNoSecrets(t, "error message", env.Error.Message, "secret-path", "api_key", "secret-value", "denied.example")
	if !strings.HasPrefix(env.Error.RequestID, "req_") {
		t.Fatalf("request_id = %q, want req_ prefix", env.Error.RequestID)
	}
}

// ---------------------------------------------------------------------------
// SSRF guard
// ---------------------------------------------------------------------------

func TestAuthorizeAddresses(t *testing.T) {
	public := net.ParseIP("93.184.216.34")
	loopback := net.ParseIP("127.0.0.1")
	private := net.ParseIP("10.1.2.3")
	linkLocal := net.ParseIP("169.254.10.1")
	unspecified := net.ParseIP("0.0.0.0")
	multicast := net.ParseIP("239.1.1.1")

	tests := []struct {
		name          string
		allowLoopback bool
		allowlist     []string
		host          string
		ips           []net.IPAddr
		wantDenied    bool
		wantErr       bool
	}{
		{name: "public allowed", host: "example.com", ips: []net.IPAddr{{IP: public}}},
		{name: "empty resolution", host: "example.com", wantErr: true},
		{name: "loopback denied by default", host: "127.0.0.1", ips: []net.IPAddr{{IP: loopback}}, wantDenied: true},
		{name: "loopback allowed with flag and allowlist", allowLoopback: true, allowlist: []string{"127.0.0.1"}, host: "127.0.0.1", ips: []net.IPAddr{{IP: loopback}}},
		{name: "loopback flag without allowlist", allowLoopback: true, allowlist: []string{"example.com"}, host: "127.0.0.1", ips: []net.IPAddr{{IP: loopback}}, wantDenied: true},
		{name: "private denied without allowlist", allowLoopback: true, allowlist: []string{"127.0.0.1"}, host: "10.1.2.3", ips: []net.IPAddr{{IP: private}}, wantDenied: true},
		{name: "private allowed with flag and allowlist", allowLoopback: true, allowlist: []string{"10.1.2.3"}, host: "10.1.2.3", ips: []net.IPAddr{{IP: private}}},
		{name: "link local denied", allowLoopback: true, allowlist: []string{"169.254.10.1"}, host: "169.254.10.1", ips: []net.IPAddr{{IP: linkLocal}}},
		{name: "unspecified denied", allowLoopback: true, allowlist: []string{"0.0.0.0"}, host: "0.0.0.0", ips: []net.IPAddr{{IP: unspecified}}},
		{name: "multicast denied", allowLoopback: true, allowlist: []string{"239.1.1.1"}, host: "239.1.1.1", ips: []net.IPAddr{{IP: multicast}}},
		{name: "mixed public and loopback", allowLoopback: false, host: "mixed.example", ips: []net.IPAddr{{IP: public}, {IP: loopback}}, wantDenied: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := mustProxy(t, Config{
				DefaultMode:   domain.NetworkOpen,
				Allowlist:     tc.allowlist,
				AllowLoopback: tc.allowLoopback,
			})
			err := p.authorizeAddresses(tc.host, tc.ips)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if errors.Is(err, errTargetDenied) {
					t.Fatalf("error = %v, want resolution/internal error", err)
				}
				return
			}
			if tc.wantDenied {
				if !errors.Is(err, errTargetDenied) {
					t.Fatalf("error = %v, want errTargetDenied", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCheckTargetResolvesAndClassifies(t *testing.T) {
	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkOpen,
		Allowlist:     []string{"loop.test", "127.0.0.1"},
		AllowLoopback: true,
	})
	p.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "loop.test":
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		case "private.test":
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.1")}}, nil
		default:
			return nil, errors.New("unknown host")
		}
	}

	if err := p.checkTarget(context.Background(), "loop.test"); err != nil {
		t.Fatalf("allowlisted loopback: %v", err)
	}
	if err := p.checkTarget(context.Background(), "127.0.0.1"); err != nil {
		t.Fatalf("literal loopback: %v", err)
	}
	if err := p.checkTarget(context.Background(), "private.test"); !errors.Is(err, errTargetDenied) {
		t.Fatalf("private.test error = %v, want denied", err)
	}
	if err := p.checkTarget(context.Background(), "missing.test"); err == nil || errors.Is(err, errTargetDenied) {
		t.Fatalf("missing.test error = %v, want resolution error", err)
	}
}

func TestIsProhibited(t *testing.T) {
	prohibited := []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.1.1", "fe80::1", "0.0.0.0", "::", "224.0.0.1",
		"239.1.1.1", "ff02::1", "255.255.255.255",
	}
	for _, raw := range prohibited {
		if ip := net.ParseIP(raw); !isProhibited(ip) {
			t.Fatalf("isProhibited(%s) = false, want true", raw)
		}
	}
	if !isProhibited(nil) {
		t.Fatal("isProhibited(nil) = false, want true")
	}
	for _, raw := range []string{"93.184.216.34", "192.0.2.1", "2001:4860:4860::8888"} {
		if ip := net.ParseIP(raw); isProhibited(ip) {
			t.Fatalf("isProhibited(%s) = true, want false", raw)
		}
	}
}

// ---------------------------------------------------------------------------
// dialing
// ---------------------------------------------------------------------------

func TestDialContext(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, echoPort, _ := net.SplitHostPort(echoAddr)
	echoHost, _, _ := net.SplitHostPort(echoAddr)

	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkFiltered,
		Allowlist:     []string{echoHost, "multi.test"},
		AllowLoopback: true,
	})
	p.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "multi.test":
			return []net.IPAddr{
				{IP: net.ParseIP("::1")},
				{IP: net.ParseIP("127.0.0.1")},
			}, nil
		default:
			return nil, errors.New("unknown host")
		}
	}

	t.Run("literal address", func(t *testing.T) {
		conn, err := p.dialContext(context.Background(), "tcp", echoAddr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("z"))
		one := make([]byte, 1)
		if _, err := io.ReadFull(conn, one); err != nil {
			t.Fatalf("read: %v", err)
		}
	})

	t.Run("tries every resolved address", func(t *testing.T) {
		// ::1 has no listener; the dialer must fall through to 127.0.0.1.
		p.dialer = net.Dialer{Timeout: 500 * time.Millisecond}
		conn, err := p.dialContext(context.Background(), "tcp", net.JoinHostPort("multi.test", echoPort))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("y"))
		one := make([]byte, 1)
		if _, err := io.ReadFull(conn, one); err != nil {
			t.Fatalf("read: %v", err)
		}
	})

	t.Run("invalid address", func(t *testing.T) {
		if _, err := p.dialContext(context.Background(), "tcp", "no-port"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("resolution failure", func(t *testing.T) {
		if _, err := p.dialContext(context.Background(), "tcp", "missing.test:80"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("policy denial", func(t *testing.T) {
		denied := mustProxy(t, Config{
			DefaultMode: domain.NetworkOpen,
			Allowlist:   []string{echoHost},
		})
		if _, err := denied.dialContext(context.Background(), "tcp", net.JoinHostPort(echoHost, echoPort)); !errors.Is(err, errTargetDenied) {
			t.Fatalf("error = %v, want errTargetDenied", err)
		}
	})

	t.Run("all addresses fail", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closedAddr := ln.Addr().String()
		_ = ln.Close()
		p.dialer = net.Dialer{Timeout: 200 * time.Millisecond}
		if _, err := p.dialContext(context.Background(), "tcp", closedAddr); err == nil {
			t.Fatal("expected dial error")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.dialContext(ctx, "tcp", echoAddr); err == nil {
			t.Fatal("expected context error")
		}
	})
}

// ---------------------------------------------------------------------------
// helpers in isolation
// ---------------------------------------------------------------------------

func TestProxyToken(t *testing.T) {
	cases := []struct {
		header string
		token  string
		ok     bool
	}{
		{header: "Bearer abc", token: "abc", ok: true},
		{header: "bearer abc", token: "abc", ok: true},
		{header: "  Bearer   abc  ", token: "abc", ok: true},
		{header: "BEARER abc", token: "abc", ok: true},
		{header: "", ok: false},
		{header: "Bearer", ok: false},
		{header: "Bearer ", ok: false},
		{header: "Basic abc", ok: false},
		{header: "Bearer a b", ok: false},
		{header: "Token abc", ok: false},
	}
	for _, tc := range cases {
		token, ok := proxyToken(tc.header)
		if ok != tc.ok || token != tc.token {
			t.Fatalf("proxyToken(%q) = (%q, %v), want (%q, %v)", tc.header, token, ok, tc.token, tc.ok)
		}
	}
}

func TestValidMode(t *testing.T) {
	for _, mode := range []domain.NetworkMode{domain.NetworkServicesOnly, domain.NetworkFiltered, domain.NetworkOpen} {
		if !validMode(mode) {
			t.Fatalf("validMode(%q) = false", mode)
		}
	}
	for _, mode := range []domain.NetworkMode{"", "bogus", "OPEN"} {
		if validMode(mode) {
			t.Fatalf("validMode(%q) = true", mode)
		}
	}
}

func TestSplitTarget(t *testing.T) {
	cases := []struct {
		target string
		host   string
		port   string
		ok     bool
	}{
		{target: "example.com:443", host: "example.com", port: "443", ok: true},
		{target: "Example.COM.:443", host: "example.com", port: "443", ok: true},
		{target: "[::1]:80", host: "::1", port: "80", ok: true},
		{target: " localhost:8080 ", host: "localhost", port: "8080", ok: true},
		{target: "", ok: false},
		{target: "example.com", ok: false},
		{target: ":443", ok: false},
		{target: "example.com:0", ok: false},
		{target: "example.com:65536", ok: false},
		{target: "example.com:abc", ok: false},
		{target: "[::1]", ok: false},
	}
	for _, tc := range cases {
		host, port, err := splitTarget(tc.target)
		if tc.ok {
			if err != nil || host != tc.host || port != tc.port {
				t.Fatalf("splitTarget(%q) = (%q, %q, %v), want (%q, %q, nil)", tc.target, host, port, err, tc.host, tc.port)
			}
			continue
		}
		if err == nil {
			t.Fatalf("splitTarget(%q) succeeded", tc.target)
		}
	}
}

func TestParsePort(t *testing.T) {
	for _, port := range []string{"1", "80", "443", "65535"} {
		if n, err := parsePort(port); err != nil || n < 1 || n > 65535 {
			t.Fatalf("parsePort(%q) = (%d, %v)", port, n, err)
		}
	}
	for _, port := range []string{"", "0", "-1", "65536", "abc", " 80"} {
		if _, err := parsePort(port); err == nil {
			t.Fatalf("parsePort(%q) succeeded", port)
		}
	}
}

func TestNormalizeHostAndAllowlistEntry(t *testing.T) {
	hostCases := map[string]string{
		"[::1]":            "::1",
		"API.Example.COM.": "api.example.com",
		"  localhost  ":    "localhost",
		"example.com...":   "example.com",
		"":                 "",
	}
	for in, want := range hostCases {
		if got := normalizeHost(in); got != want {
			t.Fatalf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
	entryCases := map[string]string{
		"*.example.com":   "example.com",
		".example.com":    "example.com",
		" Example.COM. ":  "example.com",
		"example.com:443": "example.com",
		"[::1]:443":       "::1",
		"[::1]":           "::1",
		"":                "",
	}
	for in, want := range entryCases {
		if got := normalizeAllowlistEntry(in); got != want {
			t.Fatalf("normalizeAllowlistEntry(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTargetAllowed(t *testing.T) {
	p := mustProxy(t, Config{Allowlist: []string{"localhost", ".example.com"}})
	allowed := []string{"localhost", "127.0.0.1", "::1", "example.com", "api.example.com"}
	for _, host := range allowed {
		if !p.targetAllowed(host) {
			t.Fatalf("targetAllowed(%q) = false", host)
		}
	}
	for _, host := range []string{"", "badexample.com", "example.org", "10.0.0.1"} {
		if p.targetAllowed(host) {
			t.Fatalf("targetAllowed(%q) = true", host)
		}
	}

	noAlias := mustProxy(t, Config{Allowlist: []string{"example.com"}})
	if noAlias.targetAllowed("127.0.0.1") {
		t.Fatal("loopback alias allowed without a loopback allowlist entry")
	}
}

func TestRequestHost(t *testing.T) {
	if got := requestHost(nil); got != "" {
		t.Fatalf("requestHost(nil) = %q", got)
	}

	connect := &http.Request{
		Method: http.MethodConnect,
		Host:   "API.Example.COM.:443",
	}
	if got := requestHost(connect); got != "api.example.com" {
		t.Fatalf("CONNECT host = %q", got)
	}

	connect = &http.Request{
		Method: http.MethodConnect,
		Host:   "not-an-authority",
		URL:    &url.URL{Host: "url.example:443"},
	}
	if got := requestHost(connect); got != "url.example" {
		t.Fatalf("CONNECT fallback = %q", got)
	}

	connect = &http.Request{Method: http.MethodConnect, Host: "bare-host"}
	if got := requestHost(connect); got != "bare-host" {
		t.Fatalf("CONNECT bare host = %q", got)
	}

	plain := &http.Request{Method: http.MethodGet, URL: &url.URL{Host: "API.Example.COM:80"}}
	if got := requestHost(plain); got != "api.example.com" {
		t.Fatalf("plain absolute host = %q", got)
	}

	plain = &http.Request{Method: http.MethodGet, URL: &url.URL{}, Host: "Origin.Example.:80"}
	if got := requestHost(plain); got != "origin.example" {
		t.Fatalf("plain origin host = %q", got)
	}
}

func TestStripHopByHop(t *testing.T) {
	h := http.Header{
		"Connection":          {"X-Hop, keep-me", "X-Hop-2"},
		"X-Hop":               {"remove"},
		"X-Hop-2":             {"remove-too"},
		"Keep-Alive":          {"timeout=5"},
		"Proxy-Connection":    {"keep-alive, X-Proxy-Hop"},
		"X-Proxy-Hop":         {"remove"},
		"Proxy-Authenticate":  {"Basic"},
		"Proxy-Authorization": {"Bearer secret"},
		"Te":                  {"trailers"},
		"Trailer":             {"X-Trailer"},
		"Transfer-Encoding":   {"chunked"},
		"Upgrade":             {"websocket"},
		"X-Keep":              {"keep"},
	}
	stripHopByHop(h)
	for _, name := range []string{
		"Connection", "X-Hop", "X-Hop-2", "Keep-Alive", "Proxy-Connection",
		"X-Proxy-Hop", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade",
	} {
		if got := h.Get(name); got != "" {
			t.Fatalf("%s = %q, want removed", name, got)
		}
	}
	if got := h.Get("X-Keep"); got != "keep" {
		t.Fatalf("X-Keep = %q", got)
	}
}

func TestCopyHeader(t *testing.T) {
	dst := make(http.Header)
	src := http.Header{"X-Multi": {"a", "b"}, "X-One": {"c"}}
	copyHeader(dst, src)
	if got := dst.Values("X-Multi"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("X-Multi = %v", got)
	}
	if got := dst.Get("X-One"); got != "c" {
		t.Fatalf("X-One = %q", got)
	}
}

// ---------------------------------------------------------------------------
// tunnel
// ---------------------------------------------------------------------------

func TestTunnelCopiesBothDirections(t *testing.T) {
	clientApp, clientProxy := net.Pipe()
	upstreamProxy, upstreamApp := net.Pipe()
	t.Cleanup(func() {
		_ = clientApp.Close()
		_ = upstreamApp.Close()
	})

	done := make(chan int64, 1)
	go func() {
		done <- tunnel(context.Background(), clientProxy, clientProxy, upstreamProxy)
	}()

	writeDone := make(chan struct{})
	go func() {
		_, _ = clientApp.Write([]byte("hello"))
		close(writeDone)
	}()
	fromClient := make([]byte, len("hello"))
	if _, err := io.ReadFull(upstreamApp, fromClient); err != nil {
		t.Fatalf("read client->upstream: %v", err)
	}
	<-writeDone

	readDone := make(chan struct{})
	go func() {
		_, _ = upstreamApp.Write([]byte("world"))
		close(readDone)
	}()
	toClient := make([]byte, len("world"))
	if _, err := io.ReadFull(clientApp, toClient); err != nil {
		t.Fatalf("read upstream->client: %v", err)
	}
	<-readDone

	_ = clientApp.Close()
	_ = upstreamApp.Close()

	select {
	case n := <-done:
		if n != int64(len("hello")+len("world")) {
			t.Fatalf("tunnel bytes = %d, want %d", n, len("hello")+len("world"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel did not finish")
	}
}

func TestTunnelCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	clientProxy, clientApp := net.Pipe()
	upstreamProxy, upstreamApp := net.Pipe()
	t.Cleanup(func() {
		_ = clientApp.Close()
		_ = upstreamApp.Close()
	})

	done := make(chan int64, 1)
	go func() {
		done <- tunnel(ctx, clientProxy, clientProxy, upstreamProxy)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel ignored canceled context")
	}
}

func TestResolveHost(t *testing.T) {
	p := mustProxy(t, Config{DefaultMode: domain.NetworkOpen})
	ips, err := p.resolveHost(context.Background(), "127.0.0.1")
	if err != nil || len(ips) != 1 || !ips[0].IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("literal resolution = (%v, %v)", ips, err)
	}

	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("lookup failed")
	}
	if _, err := p.resolveHost(context.Background(), "example.test"); err == nil {
		t.Fatal("expected lookup error")
	}

	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, nil
	}
	if _, err := p.resolveHost(context.Background(), "empty.test"); err == nil {
		t.Fatal("expected empty resolution error")
	}

	p.lookupIP = nil
	if _, err := p.resolveHost(context.Background(), "127.0.0.1"); err != nil {
		t.Fatalf("literal resolution with nil lookup: %v", err)
	}
}

func TestDialContextAllAddressesFail(t *testing.T) {
	// Covered in TestDialContext subtests; this test exists to exercise the
	// loop's final error path with a synthetic resolver.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	p := mustProxy(t, Config{
		DefaultMode:   domain.NetworkOpen,
		Allowlist:     []string{"fail.test"},
		AllowLoopback: true,
	})
	p.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	p.dialer = net.Dialer{Timeout: 200 * time.Millisecond}
	if _, err := p.dialContext(context.Background(), "tcp", "fail.test:1"); err == nil {
		t.Fatalf("expected dial failure to %s", addr)
	}
}
