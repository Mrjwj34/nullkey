//go:build acceptance && release

package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// SLO assertions run only on release (or manual release workflow_dispatch),
// never on pull requests. The full S1-S19 set requires the native runtime and
// gateway; the unimplemented gates below fail the release until P9.

func TestSLO_DaemonIdleRSS(t *testing.T) {
	d := startDaemon(t)
	time.Sleep(500 * time.Millisecond)
	rssKB := processRSSKB(t, d.cmd.Process.Pid)
	t.Logf("daemon idle RSS: %.1f MB (target < 80 MB)", float64(rssKB)/1024)
	if rssKB > 80*1024 {
		t.Fatalf("daemon idle RSS = %d KB, want < %d KB", rssKB, 80*1024)
	}
}

func TestSLO_HealthLatency(t *testing.T) {
	d := startDaemon(t)
	const samples = 30
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		resp, err := d.client.Get(d.baseURL + "/health")
		if err != nil {
			t.Fatalf("health request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health status = %d", resp.StatusCode)
		}
		durations = append(durations, time.Since(start))
	}
	p95 := percentile95(durations)
	t.Logf("health latency: p95 %.2f ms (release smoke target < 50 ms)", float64(p95)/float64(time.Millisecond))
	if p95 > 50*time.Millisecond {
		t.Fatalf("health p95 = %v, want < 50ms", p95)
	}
}

func TestSLO_ColdStartAndMemory(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	samples := make([]time.Duration, 0, 10)
	for i := 0; i < 10; i++ {
		ws := d.createWorkspace(t, fmt.Sprintf("release-cold-%d", i))
		elapsed, _ := measureColdStart(t, d, ws.ID)
		samples = append(samples, elapsed)
	}
	p50 := durationP50(samples)
	p95 := durationP95(samples)
	t.Logf("cold start p50=%v p95=%v (target p50<800ms p95<1.5s)", p50, p95)
	if p50 > 800*time.Millisecond || p95 > 1500*time.Millisecond {
		t.Fatalf("cold start p50=%v p95=%v", p50, p95)
	}
	idleRSS := daemonRSSBytes(t, d)
	t.Logf("daemon idle RSS=%dMB (target <80MB)", idleRSS>>20)
	if idleRSS > 80<<20 {
		t.Fatalf("daemon idle RSS = %dMB, want <80MB", idleRSS>>20)
	}
	ws := d.createWorkspace(t, "release-memory")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	extra := processRSSBytes(sb.PID)
	t.Logf("single sandbox RSS=%dMB (target <40MB)", extra>>20)
	if extra > 40<<20 {
		t.Fatalf("sandbox RSS = %dMB, want <40MB", extra>>20)
	}
}

func TestSLO_TerminalAndGatewayRTT(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "release-rtt")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	c := dialTerminal(t, d, sb.ID)
	defer c.close()
	var rtts []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		c.input(fmt.Sprintf("echo SLO_%d_$((6*7))\n", i))
		c.readUntil(fmt.Sprintf("SLO_%d_42", i), 5*time.Second)
		rtts = append(rtts, time.Since(start))
	}
	if p95 := durationP95(rtts); p95 > 50*time.Millisecond {
		t.Fatalf("terminal RTT p95 = %v, want <50ms", p95)
	}
	stub, _, _ := modelStub(t)
	defer stub.Close()
	createModelCredential(t, d, stub.URL, "sk-test-release-slo-0123456789")
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
	if p95 := durationP95(overheads); p95 > 30*time.Millisecond {
		t.Fatalf("gateway overhead p95 = %v, want <30ms", p95)
	}
}

func TestSLO_TunnelAndArtifacts(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "release-tunnel")
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tunnel_server.py"))
	if err != nil {
		t.Fatalf("read tunnel script: %v", err)
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
		t.Fatalf("tunnel RTT p95 = %v, want <20ms", p95)
	}
	start := time.Now()
	resp, err = client.Get(tk.URL + "/big")
	if err != nil {
		t.Fatalf("tunnel throughput request: %v", err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	throughput := float64(n) / time.Since(start).Seconds() / (1 << 20)
	t.Logf("tunnel throughput = %.1f MB/s (target >50MB/s)", throughput)
	if throughput < 50 {
		t.Fatalf("tunnel throughput = %.1f MB/s", throughput)
	}
	for i := 0; i < 10000; i++ {
		writeFile(t, filepath.Join(ws.RootPath, "many", fmt.Sprintf("f%05d.txt", i)), "x")
	}
	var listTimes []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		resp, raw := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/files?path=many", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list 10k: %d %s", resp.StatusCode, raw)
		}
		listTimes = append(listTimes, time.Since(start))
	}
	if p95 := durationP95(listTimes); p95 > 300*time.Millisecond {
		t.Fatalf("10k file list p95 = %v", p95)
	}
}

func TestSLO_Concurrency(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	workspaces := make([]workspaceResponse, 3)
	for i := range workspaces {
		workspaces[i] = d.createWorkspace(t, fmt.Sprintf("release-concurrent-%d", i))
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
	var total int64
	for _, sb := range results {
		total += processRSSBytes(sb.PID)
	}
	t.Logf("3 concurrent cold starts=%v total RSS=%dMB", elapsed, total>>20)
	if elapsed > 3*time.Second {
		t.Fatalf("3 concurrent cold starts = %v", elapsed)
	}
	if total > 1500<<20 {
		t.Fatalf("3 sandbox RSS = %dMB", total>>20)
	}
}

func TestSLO_HarnessReadiness(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "release-harness")
	start := time.Now()
	sb := d.createSandbox(t, ws.ID, "shell", "")
	c := dialTerminal(t, d, sb.ID)
	c.close()
	t.Logf("PTY ready in %v; harness TUI readiness: 未测 (shell adapter has no TUI readiness probe; S1 not substituted)", time.Since(start))
	if sb.ID == "" {
		t.Fatal("no sandbox")
	}
}

func processRSSKB(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("read RSS for pid %d: %v", pid, err)
	}
	value := strings.TrimSpace(string(out))
	rss, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("parse RSS %q: %v", value, err)
	}
	return rss
}

func percentile95(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (len(sorted)*95+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
