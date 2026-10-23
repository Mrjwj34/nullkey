// Package egress implements Nullkey's loopback-only HTTP/HTTPS egress proxy.
//
// The proxy accepts CONNECT tunnels and absolute-form plain HTTP requests. It
// never terminates TLS, installs a CA, or inspects request bodies. The network
// mode of the requesting sandbox is resolved per request from the
// Proxy-Authorization bearer token; a missing or unknown token falls back to
// Config.DefaultMode.
//
// Mode semantics:
//
//   - services_only denies all generic egress.
//   - filtered allows only allowlisted hosts.
//   - open allows any public host.
//
// None of the modes is offline: model, MCP, and git gateway traffic never
// traverses this proxy.
//
// Allowlist entries match a hostname exactly and all of its subdomains. A
// leading dot or "*." is accepted and ignored, so ".example.com",
// "*.example.com", and "example.com" all match example.com and
// api.example.com, but never badexample.com. For the loopback exception only,
// "localhost", "127.0.0.1", and "::1" are treated as aliases of one another.
//
// Loopback, private, link-local, unspecified, and multicast targets are
// rejected unless Config.AllowLoopback is true and the target host is
// explicitly allowlisted. This prevents the proxy from becoming an SSRF pivot
// into the host or its local network.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/id"
)

// Mode is the sandbox network mode enforced by the proxy.
type Mode = domain.NetworkMode

const (
	// dialTimeout bounds target resolution and connection establishment.
	dialTimeout = 10 * time.Second
	// resolveTimeout bounds DNS resolution for the SSRF check.
	resolveTimeout = 5 * time.Second
	// upstreamTimeout bounds a plain HTTP proxy exchange, including reading the
	// upstream response body.
	upstreamTimeout = 30 * time.Second

	codeNetworkDenied = "network_denied"
	codeInternal      = "internal"

	errMessageDenied       = "egress denied by network policy"
	errMessageServicesOnly = "generic egress is disabled in services_only mode"
	errMessageInvalid      = "egress proxy supports CONNECT or absolute-form http requests"
	errMessageBadTarget    = "invalid proxy target"
	errMessageDial         = "upstream connection failed"
	errMessageHijack       = "connection hijacking is unavailable"
	errMessageForward      = "upstream request failed"
)

var errTargetDenied = errors.New("egress: target denied")

// Resolver resolves a per-sandbox proxy token to the owning sandbox and its
// network mode. Tokens are the per-sandbox tokens present in the sandbox
// environment; they are not host credentials. An ok=false result means the
// token is unknown or invalid and the proxy falls back to Config.DefaultMode.
type Resolver interface {
	ResolveProxyToken(token string) (sandboxID string, mode domain.NetworkMode, ok bool)
}

// Config configures a Proxy.
type Config struct {
	// DefaultMode applies when no proxy token is presented or the resolver does
	// not recognize it. An empty value means domain.NetworkFiltered.
	DefaultMode domain.NetworkMode
	// Allowlist contains hostnames allowed in filtered mode and names
	// explicitly permitted by the AllowLoopback exception. Entries match the
	// hostname and its subdomains; see the package documentation.
	Allowlist []string
	// Resolver binds a sandbox token to its own network mode. It may be nil.
	Resolver Resolver
	// Logger receives request metadata only: host, status, bytes, and duration.
	// A nil logger uses slog.Default.
	Logger *slog.Logger
	// AllowLoopback permits targets that resolve to loopback, private,
	// link-local, unspecified, or multicast addresses, but only when the target
	// host is also explicitly allowlisted. Use only in tests and controlled
	// local deployments.
	AllowLoopback bool
	// OnDenied is called after a 403 policy decision, with the bound sandbox id
	// (empty when the token is unknown) and the target host. It must not block.
	OnDenied func(sandboxID, host string)
}

// Proxy is a loopback-only HTTP/HTTPS CONNECT proxy. It is safe for concurrent
// use after New returns.
type Proxy struct {
	defaultMode   domain.NetworkMode
	allowlist     map[string]struct{}
	resolver      Resolver
	logger        *slog.Logger
	allowLoopback bool
	onDenied      func(sandboxID, host string)
	dialer        net.Dialer
	lookupIP      func(ctx context.Context, host string) ([]net.IPAddr, error)
	transport     *http.Transport
}

// New validates cfg and returns a ready-to-serve Proxy.
func New(cfg Config) (*Proxy, error) {
	mode := cfg.DefaultMode
	switch mode {
	case "":
		mode = domain.NetworkFiltered
	case domain.NetworkServicesOnly, domain.NetworkFiltered, domain.NetworkOpen:
	default:
		return nil, fmt.Errorf("egress: invalid default network mode %q", mode)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	p := &Proxy{
		defaultMode:   mode,
		allowlist:     buildAllowlist(cfg.Allowlist),
		resolver:      cfg.Resolver,
		logger:        logger,
		allowLoopback: cfg.AllowLoopback,
		onDenied:      cfg.OnDenied,
		lookupIP:      net.DefaultResolver.LookupIPAddr,
	}
	p.dialer = net.Dialer{Timeout: dialTimeout}
	p.transport = &http.Transport{
		// Never honor HTTP_PROXY/HTTPS_PROXY from the host environment: this
		// process is the proxy, and chaining would bypass the allowlist.
		Proxy:               nil,
		DialContext:         p.dialContext,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: dialTimeout,
	}
	return p, nil
}

// Mode reports the proxy's default network mode. Per-request modes may differ
// when a resolver binds a sandbox token to a different mode.
func (p *Proxy) Mode() domain.NetworkMode {
	return p.defaultMode
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	host := requestHost(r)
	status := http.StatusOK
	var written int64
	defer func() {
		p.logRequest(host, status, written, start)
		if status == http.StatusForbidden && p.onDenied != nil {
			sandboxID := ""
			if p.resolver != nil {
				if token, ok := proxyToken(r.Header.Get("Proxy-Authorization")); ok {
					sandboxID, _, _ = p.resolver.ResolveProxyToken(token)
				}
			}
			p.onDenied(sandboxID, host)
		}
	}()

	if r.Method == http.MethodConnect {
		status, written = p.serveConnect(w, r)
		return
	}
	status, written = p.servePlainHTTP(w, r)
}

// serveConnect handles CONNECT host:port by resolving policy before dialing and
// then copying bytes in both directions without inspecting them.
func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request) (int, int64) {
	mode := p.resolveMode(r)
	if mode == domain.NetworkServicesOnly {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageServicesOnly)
		return http.StatusForbidden, 0
	}

	target := r.Host
	if target == "" && r.URL != nil {
		target = r.URL.Host
	}
	host, port, err := splitTarget(target)
	if err != nil && r.URL != nil && r.URL.Host != "" && r.URL.Host != target {
		// Direct handler invocations (and some intermediaries) may only set
		// URL.Host for CONNECT. Prefer the Host field, but accept the URL
		// authority when it is the only well-formed one.
		host, port, err = splitTarget(r.URL.Host)
	}
	if err != nil {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageBadTarget)
		return http.StatusForbidden, 0
	}

	if mode == domain.NetworkFiltered && !p.targetAllowed(host) {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageDenied)
		return http.StatusForbidden, 0
	}
	if err := p.checkTarget(r.Context(), host); err != nil {
		if errors.Is(err, errTargetDenied) {
			p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageDenied)
			return http.StatusForbidden, 0
		}
		p.writeError(w, http.StatusBadGateway, codeInternal, errMessageDial)
		return http.StatusBadGateway, 0
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.writeError(w, http.StatusBadGateway, codeInternal, errMessageHijack)
		return http.StatusBadGateway, 0
	}

	dialCtx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	upstream, err := p.dialContext(dialCtx, "tcp", net.JoinHostPort(host, port))
	cancel()
	if err != nil {
		p.writeError(w, http.StatusBadGateway, codeInternal, errMessageDial)
		return http.StatusBadGateway, 0
	}

	client, brw, err := hijacker.Hijack()
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		_ = upstream.Close()
		p.writeError(w, http.StatusBadGateway, codeInternal, errMessageHijack)
		return http.StatusBadGateway, 0
	}

	const established = "HTTP/1.1 200 Connection Established\r\n\r\n"
	if brw != nil {
		if _, err := brw.WriteString(established); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return http.StatusBadGateway, 0
		}
		if err := brw.Flush(); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return http.StatusBadGateway, 0
		}
	} else if _, err := io.WriteString(client, established); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return http.StatusBadGateway, 0
	}

	var clientReader io.Reader = client
	if brw != nil && brw.Reader != nil {
		clientReader = brw.Reader
	}
	// Establishment is bounded by dialTimeout. The tunnel itself is bounded by
	// the inbound request context (client disconnect or server cancellation)
	// rather than the shorter plain-HTTP exchange timeout, because long-lived
	// TLS connections are legitimate.
	n := tunnel(r.Context(), client, clientReader, upstream)
	return http.StatusOK, n
}

// servePlainHTTP handles absolute-form HTTP proxy requests.
func (p *Proxy) servePlainHTTP(w http.ResponseWriter, r *http.Request) (int, int64) {
	if r.URL == nil || r.URL.Opaque != "" || !r.URL.IsAbs() || r.URL.Host == "" || !strings.EqualFold(r.URL.Scheme, "http") {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageInvalid)
		return http.StatusForbidden, 0
	}

	mode := p.resolveMode(r)
	if mode == domain.NetworkServicesOnly {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageServicesOnly)
		return http.StatusForbidden, 0
	}

	host := normalizeHost(r.URL.Hostname())
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	if _, err := parsePort(port); err != nil {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageBadTarget)
		return http.StatusForbidden, 0
	}

	if mode == domain.NetworkFiltered && !p.targetAllowed(host) {
		p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageDenied)
		return http.StatusForbidden, 0
	}
	if err := p.checkTarget(r.Context(), host); err != nil {
		if errors.Is(err, errTargetDenied) {
			p.writeError(w, http.StatusForbidden, codeNetworkDenied, errMessageDenied)
			return http.StatusForbidden, 0
		}
		p.writeError(w, http.StatusBadGateway, codeInternal, errMessageDial)
		return http.StatusBadGateway, 0
	}

	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	out := r.Clone(ctx)
	out.RequestURI = ""
	out.URL.Scheme = "http"
	if out.Host == "" {
		out.Host = out.URL.Host
	}
	out.Close = false
	out.TransferEncoding = nil
	out.Trailer = nil
	out.Header = r.Header.Clone()
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	stripHopByHop(out.Header)
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Authenticate")
	if _, ok := out.Header["User-Agent"]; !ok {
		// net/http adds its own User-Agent when none is present; a proxy must
		// forward the client's headers rather than injecting a new identity.
		out.Header["User-Agent"] = []string{""}
	}

	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.writeError(w, http.StatusBadGateway, codeInternal, errMessageForward)
		return http.StatusBadGateway, 0
	}
	defer func() { _ = resp.Body.Close() }()

	stripHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return resp.StatusCode, n
}

// resolveMode returns the mode for this request. Unknown, malformed, or
// unresolved tokens fall back to the default mode; they never inject a mode
// from a different request or a more permissive default.
func (p *Proxy) resolveMode(r *http.Request) domain.NetworkMode {
	if p.resolver == nil {
		return p.defaultMode
	}
	token, ok := proxyToken(r.Header.Get("Proxy-Authorization"))
	if !ok {
		return p.defaultMode
	}
	_, mode, ok := p.resolver.ResolveProxyToken(token)
	if !ok || !validMode(mode) {
		return p.defaultMode
	}
	return mode
}

func validMode(mode domain.NetworkMode) bool {
	switch mode {
	case domain.NetworkServicesOnly, domain.NetworkFiltered, domain.NetworkOpen:
		return true
	default:
		return false
	}
}

// proxyToken parses a single Proxy-Authorization: Bearer <token> value.
func proxyToken(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

// checkTarget resolves host and enforces the SSRF guard. It returns
// errTargetDenied for policy denials and a wrapped error for resolution
// failures.
func (p *Proxy) checkTarget(ctx context.Context, host string) error {
	ips, err := p.resolveHost(ctx, host)
	if err != nil {
		return err
	}
	return p.authorizeAddresses(host, ips)
}

// authorizeAddresses rejects prohibited destination addresses unless the
// loopback exception is enabled and the host is explicitly allowlisted.
func (p *Proxy) authorizeAddresses(host string, ips []net.IPAddr) error {
	if len(ips) == 0 {
		return fmt.Errorf("egress: no addresses for %s", host)
	}
	prohibited := false
	for _, addr := range ips {
		if isProhibited(addr.IP) {
			prohibited = true
			break
		}
	}
	if !prohibited {
		return nil
	}
	if !p.allowLoopback {
		return errTargetDenied
	}
	if !p.targetAllowed(host) {
		return errTargetDenied
	}
	return nil
}

// dialContext resolves and authorizes the destination and then dials the
// checked IPs directly. Dialing the resolved address avoids a second,
// unchecked DNS lookup (DNS rebinding) between policy and connect time.
func (p *Proxy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("egress: split dial address: %w", err)
	}
	host = normalizeHost(host)
	ips, err := p.resolveHost(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := p.authorizeAddresses(host, ips); err != nil {
		return nil, err
	}

	var lastErr error
	for _, addr := range ips {
		conn, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no usable address")
	}
	return nil, fmt.Errorf("egress: dial %s: %w", host, lastErr)
}

// resolveHost returns the IPs for host, accepting literals without a DNS
// lookup.
func (p *Proxy) resolveHost(ctx context.Context, host string) ([]net.IPAddr, error) {
	host = normalizeHost(host)
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	lookup := p.lookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	resolveCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := lookup(resolveCtx, host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("egress: resolve %s: no addresses", host)
	}
	return ips, nil
}

func isProhibited(ip net.IP) bool {
	return ip == nil ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() ||
		ip.Equal(net.IPv4bcast)
}

// targetAllowed applies allowlist rules, including loopback aliases for the
// controlled local case.
func (p *Proxy) targetAllowed(host string) bool {
	if p.hostAllowed(host) {
		return true
	}
	// localhost, 127.0.0.1, and ::1 are the same destination for operators;
	// accept any of them as the explicit loopback allowlist entry.
	if !isLoopbackName(host) {
		return false
	}
	for _, alias := range [...]string{"localhost", "127.0.0.1", "::1"} {
		if _, ok := p.allowlist[alias]; ok {
			return true
		}
	}
	return false
}

// hostAllowed reports whether host is an allowlist entry or a subdomain of one.
func (p *Proxy) hostAllowed(host string) bool {
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	if _, ok := p.allowlist[h]; ok {
		return true
	}
	for {
		i := strings.IndexByte(h, '.')
		if i < 0 {
			return false
		}
		h = h[i+1:]
		if _, ok := p.allowlist[h]; ok {
			return true
		}
	}
}

func buildAllowlist(entries []string) map[string]struct{} {
	set := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if host := normalizeAllowlistEntry(entry); host != "" {
			set[host] = struct{}{}
		}
	}
	return set
}

func normalizeAllowlistEntry(entry string) string {
	e := strings.TrimSpace(entry)
	e = strings.TrimPrefix(e, "*.")
	e = strings.TrimPrefix(e, ".")
	if host, _, err := net.SplitHostPort(e); err == nil {
		e = host
	}
	return normalizeHost(e)
}

func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	h = strings.ToLower(h)
	h = strings.TrimRight(h, ".")
	return h
}

func isLoopbackName(host string) bool {
	h := normalizeHost(host)
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func splitTarget(target string) (host, port string, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", fmt.Errorf("egress: empty target")
	}
	host, port, err = net.SplitHostPort(target)
	if err != nil {
		return "", "", fmt.Errorf("egress: invalid target: %w", err)
	}
	host = normalizeHost(host)
	if host == "" {
		return "", "", fmt.Errorf("egress: empty host")
	}
	if _, err := parsePort(port); err != nil {
		return "", "", err
	}
	return host, port, nil
}

func parsePort(port string) (int, error) {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("egress: invalid port %q", port)
	}
	return n, nil
}

func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.Method == http.MethodConnect {
		if host, _, err := net.SplitHostPort(r.Host); err == nil {
			return normalizeHost(host)
		}
		if r.URL != nil {
			return normalizeHost(r.URL.Hostname())
		}
		return normalizeHost(r.Host)
	}
	if r.URL != nil && r.URL.Host != "" {
		return normalizeHost(r.URL.Hostname())
	}
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		return normalizeHost(host)
	}
	return normalizeHost(r.Host)
}

// tunnel copies bytes in both directions until either side closes or ctx is
// canceled. It returns the total number of bytes copied.
func tunnel(ctx context.Context, client net.Conn, clientReader io.Reader, upstream net.Conn) int64 {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
			_ = upstream.Close()
		case <-done:
		}
	}()

	type copyResult struct {
		n   int64
		err error
	}
	results := make(chan copyResult, 2)
	go func() {
		n, err := io.Copy(upstream, clientReader)
		closeWrite(upstream)
		results <- copyResult{n: n, err: err}
	}()
	go func() {
		n, err := io.Copy(client, upstream)
		closeWrite(client)
		results <- copyResult{n: n, err: err}
	}()

	first := <-results
	second := <-results
	_ = client.Close()
	_ = upstream.Close()
	return first.n + second.n
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = conn.Close()
}

func stripHopByHop(header http.Header) {
	connectionValues := append([]string(nil), header.Values("Connection")...)
	connectionValues = append(connectionValues, header.Values("Proxy-Connection")...)
	for _, connection := range connectionValues {
		for _, field := range strings.Split(connection, ",") {
			if name := strings.TrimSpace(field); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range [...]string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(name)
	}
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (p *Proxy) writeError(w http.ResponseWriter, status int, code, message string) {
	requestID := id.RequestID()
	body, err := json.Marshal(errorEnvelope{Error: errorBody{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	}})
	if err != nil {
		body = []byte(`{"error":{"code":"internal","message":"internal error","request_id":""}}`)
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (p *Proxy) logRequest(host string, status int, written int64, start time.Time) {
	p.logger.Info("egress_request",
		"host", safeLogHost(host),
		"status", status,
		"bytes", written,
		"duration", time.Since(start),
	)
}

// safeLogHost keeps pathological Host values from smuggling path, query, or
// fragment text into logs. Valid hostnames/IPs pass through unchanged.
func safeLogHost(host string) string {
	h := normalizeHost(host)
	if strings.ContainsAny(h, "/?#@\\ \t\r\n") {
		return ""
	}
	return h
}
