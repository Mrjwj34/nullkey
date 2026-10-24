package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
)

func writeManifest(t *testing.T, workspaceRoot string, manifest serviceManifest) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".nullkey"), 0o700); err != nil {
		t.Fatalf("mkdir manifest: %v", err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".nullkey", "services.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestOrderServices(t *testing.T) {
	valid := []serviceDef{
		{Name: "web", Command: []string{"/bin/sh"}, Port: 20001, DependsOn: []string{"db"}},
		{Name: "db", Command: []string{"/bin/sh"}, Port: 20002},
		{Name: "worker", Command: []string{"/bin/sh"}, Port: 20003, DependsOn: []string{"db", "web"}},
	}
	ordered, err := orderServices(valid)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	positions := map[string]int{}
	for i, def := range ordered {
		positions[def.Name] = i
	}
	if len(ordered) != 3 || positions["db"] > positions["web"] || positions["web"] > positions["worker"] || positions["db"] > positions["worker"] {
		t.Fatalf("order = %+v", ordered)
	}

	if got, err := orderServices(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty order = %+v, %v", got, err)
	}
	if _, err := orderServices([]serviceDef{{Name: "a", Command: []string{"/bin/sh"}, Port: 20000}}); err != nil {
		t.Fatalf("in-range port rejected: %v", err)
	}

	base := serviceDef{Name: "a", Command: []string{"/bin/sh"}, Port: 20001}
	cases := map[string][]serviceDef{
		"missing-name":    {{Command: []string{"/bin/sh"}, Port: 20001}},
		"duplicate-name":  {base, {Name: "a", Command: []string{"/bin/sh"}, Port: 20002}},
		"missing-command": {{Name: "a", Port: 20001}},
		"low-port":        {{Name: "a", Command: []string{"/bin/sh"}, Port: 80}},
		"high-port":       {{Name: "a", Command: []string{"/bin/sh"}, Port: 70000}},
		"duplicate-port":  {base, {Name: "b", Command: []string{"/bin/sh"}, Port: 20001}},
		"cycle": {
			{Name: "a", Command: []string{"/bin/sh"}, Port: 20001, DependsOn: []string{"b"}},
			{Name: "b", Command: []string{"/bin/sh"}, Port: 20002, DependsOn: []string{"a"}},
		},
		"unknown-dependency": {{Name: "a", Command: []string{"/bin/sh"}, Port: 20001, DependsOn: []string{"ghost"}}},
		"self-cycle":         {{Name: "a", Command: []string{"/bin/sh"}, Port: 20001, DependsOn: []string{"a"}}},
	}
	for name, defs := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := orderServices(defs); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestStartServicesNoManifest(t *testing.T) {
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = &native.Runtime{} })
	root := t.TempDir()
	svc.startServices(context.Background(), "sbx_none", root)
	if svc.servicesFor("sbx_none") != nil {
		t.Fatalf("supervisor created without manifest")
	}
}

func TestStartServicesInvalidManifest(t *testing.T) {
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = &native.Runtime{} })
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nullkey"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nullkey", "services.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc.startServices(ctx, "sbx_bad", root)
	evs, _, err := svc.Audit.List(ctx, 50, "", "service.manifest_invalid")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}

	// A manifest that parses but fails validation is also rejected.
	writeManifest(t, root, serviceManifest{Services: []serviceDef{{Name: "a", Port: 20001}}})
	svc.startServices(ctx, "sbx_bad2", root)
	evs, _, err = svc.Audit.List(ctx, 50, "", "service.manifest_invalid")
	if err != nil || len(evs) != 2 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestStartServicesSpawnFailure(t *testing.T) {
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = &native.Runtime{} })
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")
	writeManifest(t, ws.RootPath, serviceManifest{Version: 1, Services: []serviceDef{{
		Name: "web", Command: []string{"/bin/sh", "-c", "true"}, Port: 20011, Health: "/",
	}}})
	sb := insertSandbox(t, svc, domain.Sandbox{HarnessID: "shell", WorkspaceID: ws.ID, Status: domain.SandboxRunning})

	svc.startServices(ctx, sb.ID, ws.RootPath)
	if sup := svc.servicesFor(sb.ID); sup == nil || len(sup.procs) != 0 {
		t.Fatalf("supervisor = %+v", sup)
	}
	services, err := svc.ListSandboxServices(ctx, sb.ID)
	if err != nil || len(services) != 1 {
		t.Fatalf("services = %+v, %v", services, err)
	}
	if services[0].Status != domain.ServiceError || services[0].TargetPort != 20011 || services[0].ID != "svc_web" {
		t.Fatalf("service = %+v", services[0])
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "service.error")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}

	// stopServices with an empty supervisor still clears declarations.
	svc.stopServices(ctx, sb.ID)
	if svc.servicesFor(sb.ID) != nil {
		t.Fatalf("supervisor not removed")
	}
	services, err = svc.ListSandboxServices(ctx, sb.ID)
	if err != nil || len(services) != 0 {
		t.Fatalf("services after stop = %+v, %v", services, err)
	}
	svc.stopServices(ctx, sb.ID) // nil supervisor is a no-op
}

func TestStartServicesRetryBackoff(t *testing.T) {
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = &native.Runtime{} })
	ws := mustWorkspace(t, svc, "ws", "", "")
	writeManifest(t, ws.RootPath, serviceManifest{Services: []serviceDef{{
		Name: "web", Command: []string{"/bin/sh"}, Port: 20012, Restart: "always",
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel() // cancel before the first retry backoff so the test stays fast
	svc.startServices(ctx, "sbx_retry", ws.RootPath)
	if sup := svc.servicesFor("sbx_retry"); sup == nil {
		t.Fatalf("supervisor missing")
	}
}

func TestListSandboxServicesErrors(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.ListSandboxServices(ctx, "sbx_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	sb := insertSandbox(t, svc, domain.Sandbox{HarnessID: "shell", Status: domain.SandboxRunning})
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = svc.ListSandboxServices(ctx, sb.ID)
	wantError(t, err, http.StatusInternalServerError, "internal")
}

// -------------------------------------------------- real native runtime

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("cannot locate test source")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// buildSandboxHelper compiles a minimal binary that only implements the
// hidden __sandbox_exec helper mode. It deliberately avoids building the full
// daemon so this package's tests stay independent of other packages.
func buildSandboxHelper(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || !native.Available() {
		t.Skip("native Seatbelt runtime is only available on darwin")
	}
	root := repoRoot(t)
	dir, err := os.MkdirTemp(root, ".core-native-helper-")
	if err != nil {
		t.Fatalf("create helper dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	source := `package main

import (
	"os"

	"github.com/Mrjwj34/nullkey/internal/runtime/native"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__sandbox_exec" {
		os.Exit(native.RunSandboxHelper(os.Args[2:]))
	}
	os.Exit(2)
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("relative helper dir: %v", err)
	}
	exe := filepath.Join(t.TempDir(), "nullkeyd-helper")
	cmd := exec.Command("go", "build", "-o", exe, "./"+filepath.ToSlash(rel))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sandbox helper: %v\n%s", err, out)
	}
	return exe
}

var (
	freePortMu   sync.Mutex
	freePortUsed = map[int]bool{}
)

// freePort returns an unused unprivileged TCP port that has not been handed
// out before in this test binary.
func freePort(t *testing.T) int {
	t.Helper()
	freePortMu.Lock()
	defer freePortMu.Unlock()
	start := 20000 + int(time.Now().UnixNano()%9000)
	for i := 0; i < 10000; i++ {
		port := 20000 + (start-20000+i)%10000
		if freePortUsed[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		freePortUsed[port] = true
		return port
	}
	t.Fatalf("no free port in 20000-29999")
	return 0
}

// TestSandboxServicesNative starts a real Seatbelt sandbox and a real
// service command so the success path (spawn, health check, store, stop) is
// exercised end to end.
func TestSandboxServicesNative(t *testing.T) {
	exe := buildSandboxHelper(t)
	home := t.TempDir()
	rt, err := native.New(native.Config{
		Executable:   exe,
		Home:         home,
		HostHome:     os.Getenv("HOME"),
		GatewayAddr:  "127.0.0.1:49153",
		ProxyAddr:    "127.0.0.1:49154",
		Logger:       testLogger(),
		ReadyTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("native runtime: %v", err)
	}
	svc, _ := newTestService(t, func(o *Options) { o.Runtime = rt })
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "services", "", "")
	port := freePort(t)
	oncePort := freePort(t)
	// Tiny HTTP responders made from /bin/sh and nc, both reachable inside the
	// Seatbelt profile. PORT is injected by the service supervisor.
	script := `while true; do printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok' | /usr/bin/nc -l 127.0.0.1 "$PORT"; done`
	onceScript := `printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok' | /usr/bin/nc -l 127.0.0.1 "$PORT"; printf '%s' "$FOO" > once-env.txt; printf '%s' "$NULLKEY_SECRET" > once-leak.txt`
	writeManifest(t, ws.RootPath, serviceManifest{Version: 1, Services: []serviceDef{
		{Name: "web", Command: []string{"/bin/sh", "-c", script}, Port: port, Health: ""},
		{
			Name: "once", Command: []string{"/bin/sh", "-c", onceScript}, Port: oncePort,
			Health: "health", Env: map[string]string{"FOO": "bar", "NULLKEY_SECRET": "leak"},
		},
	}})

	sb, err := svc.CreateSandbox(ctx, CreateSandboxRequest{
		HarnessID: "shell", WorkspaceID: ws.ID, NetworkMode: "filtered",
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if sb.Status != domain.SandboxRunning || sb.PID <= 0 {
		t.Fatalf("sandbox = %+v", sb)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = svc.StopSandbox(stopCtx, sb.ID)
	})

	deadline := time.Now().Add(20 * time.Second)
	var services []domain.Service
	byName := func(items []domain.Service, name string) *domain.Service {
		for i := range items {
			if items[i].Name == name {
				return &items[i]
			}
		}
		return nil
	}
	webReady, onceExited := false, false
	for time.Now().Before(deadline) {
		services, err = svc.ListSandboxServices(ctx, sb.ID)
		if err != nil {
			t.Fatalf("list services: %v", err)
		}
		if web := byName(services, "web"); web != nil && web.Status == domain.ServiceReady {
			webReady = true
		}
		if once := byName(services, "once"); once != nil && once.Status == domain.ServiceError {
			onceExited = true
		}
		if webReady && onceExited {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !webReady || !onceExited {
		evs, _, _ := svc.Audit.List(ctx, 50, "", "")
		t.Logf("audit events: %+v", evs)
		t.Logf("supervisor: %+v", svc.servicesFor(sb.ID))
		t.Logf("workspace: %+v", ws.RootPath)
		if raw, err := os.ReadFile(filepath.Join(ws.RootPath, ".nullkey", "services.json")); err == nil {
			t.Logf("manifest: %s", raw)
		} else {
			t.Logf("manifest read error: %v", err)
		}
		t.Fatalf("services did not reach expected states: %+v", services)
	}
	web := byName(services, "web")
	if web.TargetPort != port || web.TargetHost != "127.0.0.1" {
		t.Fatalf("service target = %+v", web)
	}
	if once := byName(services, "once"); once.TargetPort != oncePort {
		t.Fatalf("once target = %+v", once)
	}
	// The one-shot service writes its environment before exiting; NULLKEY_*
	// variables supplied by the manifest must be filtered out.
	if raw, err := os.ReadFile(filepath.Join(ws.RootPath, "once-env.txt")); err != nil || string(raw) != "bar" {
		t.Fatalf("service env = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(ws.RootPath, "once-leak.txt")); err != nil || string(raw) != "" {
		t.Fatalf("NULLKEY_ env leaked into service: %q, %v", raw, err)
	}
	sup := svc.servicesFor(sb.ID)
	if sup == nil {
		t.Fatalf("supervisor missing")
	}
	sup.mu.Lock()
	procs := len(sup.procs)
	order := append([]string(nil), sup.order...)
	sup.mu.Unlock()
	if procs != 2 || len(order) != 2 || order[0] != "once" || order[1] != "web" {
		t.Fatalf("procs=%d order=%v", procs, order)
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "service.started")
	if err != nil {
		t.Fatalf("service.started audit: %v", err)
	}
	// A one-shot service may exit before the first health probe, in which case
	// the failure path is service.error rather than service.started. The web
	// service must always reach ready and emit its start event.
	webStarted := false
	for _, ev := range evs {
		if ev.Detail["service"] == "web" {
			webStarted = true
		}
	}
	if !webStarted {
		t.Fatalf("web service.started audit missing: %+v", evs)
	}
	exited, _, err := svc.Audit.List(ctx, 50, "", "service.exited")
	if err != nil || len(exited) == 0 {
		t.Fatalf("service.exited audit = %d, %v", len(exited), err)
	}

	// The terminal and generated config belong to the same sandbox.
	ticket, err := svc.TerminalTicket(ctx, sb.ID)
	if err != nil || ticket.Ticket == "" {
		t.Fatalf("terminal ticket: %+v, %v", ticket, err)
	}
	state, err := svc.GetState(ctx, sb.StateID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	configPath := filepath.Join(state.Path, "nullkey", "config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("generated config: %v", err)
	}
	stats, err := svc.Stats(ctx, sb.ID)
	if err != nil || stats.PID <= 0 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}

	svc.stopServices(ctx, sb.ID)
	if svc.servicesFor(sb.ID) != nil {
		t.Fatalf("supervisor not removed")
	}
	services, err = svc.ListSandboxServices(ctx, sb.ID)
	if err != nil || len(services) != 0 {
		t.Fatalf("services after stop = %+v, %v", services, err)
	}
	evs, _, err = svc.Audit.List(ctx, 50, "", "service.stopped")
	if err != nil || len(evs) == 0 {
		t.Fatalf("service.stopped audit = %d, %v", len(evs), err)
	}

	if err := svc.StopSandbox(ctx, sb.ID); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if wsAfter, err := svc.GetWorkspace(ctx, ws.ID); err != nil || wsAfter.LockedBy != nil {
		t.Fatalf("lock after stop = %+v, %v", wsAfter, err)
	}
}

func TestServiceDefinitionsJSON(t *testing.T) {
	raw := []byte(`{"version":1,"services":[{"name":"web","command":["/bin/sh"],"port":20001,"health_path":"/health","websocket":true,"depends_on":["db"],"auth":true,"restart":"always","env":{"A":"b"}}]}`)
	var manifest serviceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(manifest.Services) != 1 {
		t.Fatalf("services = %+v", manifest.Services)
	}
	def := manifest.Services[0]
	if def.Name != "web" || def.Port != 20001 || def.Health != "/health" || !def.WebSocket || !def.Auth || def.Restart != "always" || def.Env["A"] != "b" || len(def.DependsOn) != 1 {
		t.Fatalf("def = %+v", def)
	}
}

func TestInitSupervisorMaps(t *testing.T) {
	svc := &Service{}
	svc.initSupervisorMaps()
	if svc.services == nil {
		t.Fatalf("services map not initialized")
	}
	svc.services["x"] = &serviceSupervisor{}
	svc.initSupervisorMaps()
	if _, ok := svc.services["x"]; !ok {
		t.Fatalf("existing map replaced")
	}
}

func TestWaitHealthyIgnoresHostProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	svc, _ := newTestService(t)
	if err := svc.waitHealthy(context.Background(), serviceDef{Health: "/healthz", Port: port}); err != nil {
		t.Fatalf("waitHealthy used the host proxy: %v", err)
	}
}
