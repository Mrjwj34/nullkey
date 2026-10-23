//go:build acceptance && release

package acceptance

import (
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
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

func TestSLO_UnimplementedReleaseGates(t *testing.T) {
	cases := []struct {
		id     string
		reason string
	}{
		{"AC-SLO-001", "cold start to WS ready and daemon/sandbox RSS require the native runtime"},
		{"AC-SLO-002", "terminal RTT and gateway overhead require PTY and model gateway"},
		{"AC-SLO-003", "tunnel RTT/throughput and artifact latency require tunnel/artifacts"},
		{"AC-SLO-004", "multi-sandbox concurrency requires the native runtime"},
		{"AC-SLO-005", "harness readiness requires a harness adapter"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			releasePending(t, tc.id, tc.reason)
		})
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

func releasePending(t *testing.T, id, reason string) {
	t.Helper()
	if os.Getenv("NULLKEY_RELEASE_STRICT") == "1" {
		t.Fatalf("%s: release gate failed: %s", id, reason)
	}
	t.Skipf("%s: not implemented yet: %s", id, reason)
}
