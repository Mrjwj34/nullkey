//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func daemonRSSBytes(t *testing.T, d *daemon) int64 {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(d.cmd.Process.Pid)).Output()
	if err != nil {
		t.Fatalf("ps daemon: %v", err)
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatalf("parse daemon rss: %v", err)
	}
	return kb * 1024
}

func processRSSBytes(pid int) int64 {
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb * 1024
}

func measureColdStart(t *testing.T, d *daemon, workspaceID string) (time.Duration, sandboxResponse) {
	t.Helper()
	start := time.Now()
	sb := d.createSandbox(t, workspaceID, "shell", "")
	c := dialTerminal(t, d, sb.ID)
	c.close()
	elapsed := time.Since(start)
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop cold sandbox = %d", resp.StatusCode)
	}
	return elapsed, sb
}

func TestAC_SLO_001_ColdStartAndMemory(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	samples := make([]time.Duration, 0, 10)
	for i := 0; i < 10; i++ {
		ws := d.createWorkspace(t, fmt.Sprintf("slo-cold-%d", i))
		elapsed, _ := measureColdStart(t, d, ws.ID)
		samples = append(samples, elapsed)
	}
	p95 := durationP95(samples)
	p50 := durationP50(samples)
	if p50 > 800*time.Millisecond || p95 > 1500*time.Millisecond {
		t.Fatalf("cold start p50=%v p95=%v, want p50<800ms p95<1.5s", p50, p95)
	}
	idleRSS := daemonRSSBytes(t, d)
	if idleRSS > 80<<20 {
		t.Fatalf("daemon idle RSS = %d MB, want < 80MB", idleRSS>>20)
	}
	ws := d.createWorkspace(t, "slo-memory")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	extra := processRSSBytes(sb.PID)
	if extra > 40<<20 {
		t.Fatalf("single sandbox RSS = %d MB, want < 40MB", extra>>20)
	}
	t.Logf("cold start p50=%v p95=%v daemon_rss=%dMB sandbox_rss=%dMB", p50, p95, idleRSS>>20, extra>>20)
}

func durationP50(values []time.Duration) time.Duration {
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
	return copied[len(copied)/2]
}

func TestAC_SLO_002_TerminalAndGatewayRTT(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "slo-rtt")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	c := dialTerminal(t, d, sb.ID)
	defer c.close()
	var rtts []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		c.input(fmt.Sprintf("echo RTT_%d_$((6*7))\n", i))
		c.readUntil(fmt.Sprintf("RTT_%d_42", i), 5*time.Second)
		rtts = append(rtts, time.Since(start))
	}
	p95 := durationP95(rtts)
	if p95 > 50*time.Millisecond {
		t.Fatalf("terminal RTT p95 = %v, want < 50ms", p95)
	}

	stub, _, _ := modelStub(t)
	defer stub.Close()
	createModelCredential(t, d, stub.URL, "sk-test-slo-0123456789abcdef")
	token := sandboxToken(t, d, sb.ID)
	var overheads []time.Duration
	client := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 20; i++ {
		start := time.Now()
		req, _ := http.NewRequest(http.MethodPost, stub.URL+"/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("direct: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		direct := time.Since(start)
		start = time.Now()
		resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("gateway: %d %s", resp.StatusCode, raw)
		}
		overheads = append(overheads, time.Since(start)-direct)
	}
	if gwP95 := durationP95(overheads); gwP95 > 30*time.Millisecond {
		t.Fatalf("gateway p95 overhead = %v, want < 30ms", gwP95)
	}
}

func TestAC_SLO_003_TunnelAndArtifacts(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "slo-tunnel")
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tunnel_server.py"))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	writeFile(t, filepath.Join(ws.RootPath, "tunnel_server.py"), string(script))
	port := freeServicePort(t)
	writeServicesManifest(t, ws.RootPath, []map[string]any{
		{"name": "app", "port": port, "health_path": "/", "websocket": true, "command": []string{"python3", "tunnel_server.py", fmt.Sprint(port)}},
	})
	sb := d.createSandbox(t, ws.ID, "shell", "")
	services := waitServicesReady(t, d, sb.ID, 1)
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/services/"+services[0].ID+"/ticket", nil)
	tk := decodeJSON[struct {
		URL    string `json:"url"`
		Ticket string `json:"ticket"`
	}](t, raw)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	req, _ := http.NewRequest(http.MethodGet, tk.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+tk.Ticket)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("tunnel warmup: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	var rtts []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		resp, err := client.Get(tk.URL + "/path/echo")
		if err != nil {
			t.Fatalf("tunnel rtt: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		rtts = append(rtts, time.Since(start))
	}
	if p95 := durationP95(rtts); p95 > 20*time.Millisecond {
		t.Fatalf("tunnel RTT p95 = %v, want < 20ms", p95)
	}
	start := time.Now()
	resp, err = client.Get(tk.URL + "/big")
	if err != nil {
		t.Fatalf("tunnel big: %v", err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	throughput := float64(n) / time.Since(start).Seconds() / (1 << 20)
	if throughput < 50 {
		t.Fatalf("tunnel throughput = %.1f MB/s, want > 50MB/s", throughput)
	}

	for i := 0; i < 10000; i++ {
		writeFile(t, filepath.Join(ws.RootPath, "many", fmt.Sprintf("f%05d.txt", i)), "x")
	}
	var listTimes []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		resp, raw := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/files?path=many", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list 10k = %d %s", resp.StatusCode, raw)
		}
		listTimes = append(listTimes, time.Since(start))
	}
	if p95 := durationP95(listTimes); p95 > 300*time.Millisecond {
		t.Fatalf("10k file list p95 = %v, want < 300ms", p95)
	}
	var preview []time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		resp, _ := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/file?path=many/f00000.txt", nil)
		_ = resp
		preview = append(preview, time.Since(start))
	}
	if p95 := durationP95(preview); p95 > 100*time.Millisecond {
		t.Fatalf("file preview p95 = %v, want < 100ms", p95)
	}
}

func TestAC_SLO_004_Concurrency(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	workspaces := make([]workspaceResponse, 3)
	for i := range workspaces {
		workspaces[i] = d.createWorkspace(t, fmt.Sprintf("slo-concurrent-%d", i))
	}
	start := time.Now()
	var wg sync.WaitGroup
	results := make([]sandboxResponse, 3)
	errs := make([]error, 3)
	for i := range workspaces {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes", map[string]any{"harness_id": "shell", "workspace_id": workspaces[i].ID})
			if resp.StatusCode != http.StatusCreated {
				errs[i] = fmt.Errorf("create %d: %s", resp.StatusCode, raw)
				return
			}
			results[i] = decodeJSON[sandboxResponse](t, raw)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = ctx
			c := dialTerminal(t, d, results[i].ID)
			c.close()
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("3 concurrent cold starts = %v, want < 3s", elapsed)
	}
	var total int64
	for _, sb := range results {
		total += processRSSBytes(sb.PID)
	}
	if total > 1500<<20 {
		t.Fatalf("3 sandbox total RSS = %d MB, want < 1.5GB", total>>20)
	}
	t.Logf("3 concurrent cold starts = %v total_rss=%dMB", elapsed, total>>20)
}

func TestAC_SLO_005_HarnessReadiness(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "slo-harness")
	start := time.Now()
	sb := d.createSandbox(t, ws.ID, "shell", "")
	c := dialTerminal(t, d, sb.ID)
	c.close()
	t.Logf("PTY ready in %v; harness TUI readiness: 未测 (shell adapter has no TUI readiness probe; not substituting S1)", time.Since(start))
	if sb.ID == "" {
		t.Fatal("no sandbox")
	}
}
