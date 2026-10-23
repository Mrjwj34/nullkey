//go:build acceptance

package acceptance

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("native sandbox runtime requires macOS")
	}
}

func modelStub(t *testing.T) (*httptest.Server, *int, *string) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n", "data: [DONE]\n\n"} {
			_, _ = w.Write([]byte(chunk))
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	return srv, &calls, &auth
}

func createModelCredential(t *testing.T, d *daemon, baseURL, secret string) string {
	t.Helper()
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "model", "auth_type": "api_key",
		"value": secret, "base_url": baseURL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create model credential = %d; body=%s", resp.StatusCode, raw)
	}
	return decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw).ID
}

func TestAC_NET_001_ModelGateway(t *testing.T) {
	requireDarwin(t)
	stub, calls, auth := modelStub(t)
	defer stub.Close()
	d := startDaemon(t)
	secret := "sk-test-model-0123456789abcdef"
	createModelCredential(t, d, stub.URL, secret)
	ws := d.createWorkspace(t, "model")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)

	resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{"model":"x"}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gateway = %d; body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "data: one") || !strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("streaming body incomplete: %s", raw)
	}
	if *auth != "Bearer "+secret {
		t.Fatalf("upstream authorization = %q, want injected real credential", *auth)
	}
	if *calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", *calls)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(d.logs.String(), secret) {
		t.Fatal("model gateway leaked the real credential")
	}
	if strings.Contains(d.logs.String(), token) {
		t.Fatal("gateway log contains the sandbox token")
	}
}

func TestAC_NET_002_InvalidToken(t *testing.T) {
	requireDarwin(t)
	stub, calls, _ := modelStub(t)
	defer stub.Close()
	d := startDaemon(t)
	createModelCredential(t, d, stub.URL, "sk-test-invalid-0123456789abcdef")
	resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", "not-a-real-token", []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid token = %d; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "token_invalid" {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if *calls != 0 {
		t.Fatalf("invalid token reached upstream %d times", *calls)
	}

	ws := d.createWorkspace(t, "invalid")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d", resp.StatusCode)
	}
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stopped token = %d; body=%s", resp.StatusCode, raw)
	}
	if env := decodeJSON[apiError](t, raw); env.Error.Code != "token_invalid" {
		t.Fatalf("stopped token code = %q", env.Error.Code)
	}
	if *calls != 0 {
		t.Fatalf("stopped token reached upstream")
	}
}

func TestAC_NET_003_EgressAllowlist(t *testing.T) {
	requireDarwin(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("allowed"))
	}))
	defer upstream.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	d := startDaemon(t)

	filtered := d.createSandbox(t, d.createWorkspace(t, "filtered").ID, "shell", "filtered")
	ft := sandboxToken(t, d, filtered.ID)
	conn, status := d.proxyConnect(t, net.JoinHostPort("localhost", port), ft)
	if !strings.Contains(status, "200") {
		conn.Close()
		t.Fatalf("filtered allowlisted CONNECT status = %q", status)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"))
	body, _ := io.ReadAll(conn)
	conn.Close()
	if !strings.Contains(string(body), "allowed") {
		t.Fatalf("allowlisted tunnel body = %q", body)
	}
	conn, status = d.proxyConnect(t, "denied.example:443", ft)
	conn.Close()
	if !strings.Contains(status, "403") {
		t.Fatalf("filtered denied CONNECT status = %q", status)
	}

	open := d.createSandbox(t, d.createWorkspace(t, "open").ID, "shell", "open")
	ot := sandboxToken(t, d, open.ID)
	conn, status = d.proxyConnect(t, "denied.example:443", ot)
	conn.Close()
	if strings.Contains(status, "403") {
		t.Fatalf("open mode denied egress: %q", status)
	}

	servicesOnly := d.createSandbox(t, d.createWorkspace(t, "services").ID, "shell", "services_only")
	st := sandboxToken(t, d, servicesOnly.ID)
	conn, status = d.proxyConnect(t, "example.com:443", st)
	conn.Close()
	if !strings.Contains(status, "403") {
		t.Fatalf("services_only allowed generic egress: %q", status)
	}

	// Plain HTTP proxy path (absolute-form) also enforces the allowlist.
	conn, err := net.DialTimeout("tcp", d.proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	req := fmt.Sprintf("GET %s/plain HTTP/1.1\r\nHost: localhost\r\nProxy-Authorization: Bearer %s\r\nConnection: close\r\n\r\n", upstream.URL, ft)
	_, _ = conn.Write([]byte(req))
	raw, _ := io.ReadAll(conn)
	conn.Close()
	if !strings.Contains(string(raw), "200") || !strings.Contains(string(raw), "allowed") {
		t.Fatalf("plain HTTP proxy response = %q", raw)
	}
}

func TestAC_NET_004_DirectOutboundBlocked(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "outbound")
	sb := d.createSandbox(t, ws.ID, "shell", "filtered")
	c := dialTerminal(t, d, sb.ID)
	defer c.close()
	cmd := "curl -s --noproxy '*' -o /dev/null -w 'DIRECT=%{http_code}' --max-time 2 http://example.com; echo DIRECT_EXIT=$?; " +
		"curl -s --noproxy '*' -o /dev/null -w 'GW=%{http_code}' --max-time 2 -H \"Authorization: Bearer $NULLKEY_TOKEN\" http://127.0.0.1:" + d.gatewayPort() + "/openai/v1/models; echo GW_EXIT=$?; " +
		"curl -s -o /dev/null -w 'PROXY=%{http_code}' --max-time 2 -x http://127.0.0.1:" + d.proxyPort() + " http://denied.example/; echo PROXY_EXIT=$?; echo NET_DONE_$((7*7))\n"
	c.input(cmd)
	out := c.readUntil("NET_DONE_49", 10*time.Second)
	if strings.Contains(out, "DIRECT=200") || strings.Contains(out, "DIRECT_EXIT=0") {
		t.Fatalf("direct outbound was not blocked: %q", out)
	}
	if !strings.Contains(out, "GW=401") {
		t.Fatalf("loopback gateway was not reachable: %q", out)
	}
	if !strings.Contains(out, "PROXY=403") {
		t.Fatalf("loopback proxy was not enforcing policy: %q", out)
	}
}

func TestAC_NET_005_GatewayLatency(t *testing.T) {
	requireDarwin(t)
	stub, _, _ := modelStub(t)
	defer stub.Close()
	d := startDaemon(t)
	createModelCredential(t, d, stub.URL, "sk-test-latency-0123456789abcdef")
	ws := d.createWorkspace(t, "latency")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	client := &http.Client{Timeout: 5 * time.Second}
	var overheads []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		req, _ := http.NewRequest(http.MethodPost, stub.URL+"/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("direct: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		direct := time.Since(start)
		start = time.Now()
		resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("gateway: %d %s", resp.StatusCode, raw)
		}
		gw := time.Since(start)
		overheads = append(overheads, gw-direct)
	}
	p95 := durationP95(overheads)
	if p95 > 30*time.Millisecond {
		t.Fatalf("gateway p95 overhead = %v, want < 30ms", p95)
	}
}

func durationP95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	copied := append([]time.Duration(nil), values...)
	for i := 0; i < len(copied); i++ {
		for j := i + 1; j < len(copied); j++ {
			if copied[j] < copied[i] {
				copied[i], copied[j] = copied[j], copied[i]
			}
		}
	}
	idx := int(float64(len(copied)) * 0.95)
	if idx >= len(copied) {
		idx = len(copied) - 1
	}
	return copied[idx]
}

func TestAC_NET_006_GitProxy(t *testing.T) {
	requireDarwin(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "o", "r.git")
	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "init", "--bare", bare)
	runGit(t, "", "init", work)
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "http.receivepack", "true")
	writeFile(t, filepath.Join(work, "README.md"), "hello\n")
	runGit(t, work, "add", "README.md")
	runGit(t, work, "commit", "-m", "initial")
	runGit(t, work, "remote", "add", "origin", bare)
	runGit(t, work, "push", "origin", "HEAD:main")
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, bare, "config", "http.receivepack", "true")

	var mu sync.Mutex
	var seenAuth string
	cgiHandler := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()
		cgiHandler.ServeHTTP(w, r)
	}))
	defer stub.Close()
	debugReq, _ := http.NewRequest(http.MethodGet, stub.URL+"/o/r.git/info/refs?service=git-upload-pack", nil)
	if debugResp, derr := http.DefaultClient.Do(debugReq); derr == nil {
		body, _ := io.ReadAll(debugResp.Body)
		debugResp.Body.Close()
		t.Logf("git backend direct status=%d body=%s", debugResp.StatusCode, body)
	}

	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "github", "label": "git", "auth_type": "pat",
		"value": "ghp_testgit0123456789abcdef", "base_url": stub.URL,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create github credential = %d; body=%s", resp.StatusCode, raw)
	}
	ws := d.createWorkspace(t, "git")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)

	gwReq, _ := http.NewRequest(http.MethodGet, d.gatewayURL+"/git/github/o/r.git/info/refs?service=git-upload-pack", nil)
	gwReq.Header.Set("Authorization", "Bearer "+token)
	if gwResp, gerr := http.DefaultClient.Do(gwReq); gerr == nil {
		body, _ := io.ReadAll(gwResp.Body)
		gwResp.Body.Close()
		t.Logf("gateway git status=%d body=%q", gwResp.StatusCode, body)
	}
	dest := filepath.Join(t.TempDir(), "clone")
	url := d.gatewayURL + "/git/github/o/r.git"
	cmd := exec.Command(gitPath, "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", url, dest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone via gateway: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("clone missing file: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if strings.Contains(string(cfg), "ghp_") || strings.Contains(string(cfg), token) {
		t.Fatal("git config contains a real or sandbox token")
	}
	writeFile(t, filepath.Join(dest, "README.md"), "hello world\n")
	runGit(t, dest, "config", "user.email", "test@example.com")
	runGit(t, dest, "config", "user.name", "Test")
	runGit(t, dest, "add", "README.md")
	runGit(t, dest, "commit", "-m", "update")
	if remoteOut, rerr := exec.Command(gitPath, "-C", dest, "remote", "-v").CombinedOutput(); rerr == nil {
		t.Logf("clone remotes: %s", remoteOut)
	}
	push := exec.Command(gitPath, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:main")
	push.Dir = dest
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("git push via gateway: %v\n%s", err, out)
	}
	mu.Lock()
	auth := seenAuth
	mu.Unlock()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_testgit0123456789abcdef"))
	if auth != want {
		t.Fatalf("upstream git auth = %q, want injected Basic", auth)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestAC_NET_007_FailClosed(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "failclosed")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential = %d; body=%s", resp.StatusCode, raw)
	}
	if env := decodeJSON[apiError](t, raw); env.Error.Code != "reauth_required" {
		t.Fatalf("no credential code = %q", env.Error.Code)
	}
	createModelCredential(t, d, "http://127.0.0.1:1", "sk-test-fail-0123456789abcdef")
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unreachable upstream = %d; body=%s", resp.StatusCode, raw)
	}
	if env := decodeJSON[apiError](t, raw); env.Error.Code != "gateway_unavailable" {
		t.Fatalf("code = %q, want gateway_unavailable", env.Error.Code)
	}
	conn, status := d.proxyConnect(t, "localhost:1", token)
	conn.Close()
	if !strings.Contains(status, "502") {
		t.Fatalf("unreachable egress = %q, want 502", status)
	}
}

func TestAC_NET_008_ModeSemantics(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	resp, raw := d.doAuthed(t, http.MethodGet, "/capabilities", nil)
	var caps struct {
		NetworkModes []string `json:"network_modes"`
	}
	if err := json.Unmarshal(raw, &caps); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities: %d %s", resp.StatusCode, raw)
	}
	if len(caps.NetworkModes) != 3 {
		t.Fatalf("network_modes = %v", caps.NetworkModes)
	}
	for _, mode := range []string{"services_only", "filtered", "open"} {
		ws := d.createWorkspace(t, "mode-"+mode)
		sb := d.createSandbox(t, ws.ID, "shell", mode)
		token := sandboxToken(t, d, sb.ID)
		resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("mode %s gateway = %d; body=%s", mode, resp.StatusCode, raw)
		}
		if env := decodeJSON[apiError](t, raw); env.Error.Code != "reauth_required" {
			t.Fatalf("mode %s gateway reachability code = %q, want reauth_required", mode, env.Error.Code)
		}
	}
}

func TestAC_NET_005_StreamingFirstByte(t *testing.T) {
	requireDarwin(t)
	stub, _, _ := modelStub(t)
	defer stub.Close()
	d := startDaemon(t)
	createModelCredential(t, d, stub.URL, "sk-test-stream-0123456789abcdef")
	ws := d.createWorkspace(t, "stream")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	req, _ := http.NewRequest(http.MethodPost, d.gatewayURL+"/openai/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("first byte: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("first byte latency = %v, want < 50ms", elapsed)
	}
}

var _ = context.Background
var _ = bufio.NewReader
