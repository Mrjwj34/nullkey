package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
	nkid "github.com/Mrjwj34/nullkey/internal/id"
	"github.com/Mrjwj34/nullkey/internal/redact"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
)

type serviceManifest struct {
	Version  int          `json:"version"`
	Services []serviceDef `json:"services"`
}

type serviceDef struct {
	Name      string            `json:"name"`
	Command   []string          `json:"command"`
	Port      int               `json:"port"`
	Health    string            `json:"health_path"`
	WebSocket bool              `json:"websocket"`
	DependsOn []string          `json:"depends_on"`
	Auth      bool              `json:"auth"`
	Restart   string            `json:"restart"`
	Env       map[string]string `json:"env"`
}

type runningService struct {
	def   serviceDef
	proc  *native.ServiceProcess
	token string
	id    string
}

type serviceSupervisor struct {
	sandboxID string
	mu        sync.Mutex
	procs     map[string]*runningService
	order     []string
	stopping  bool
}

func (s *Service) servicesFor(sandboxID string) *serviceSupervisor {
	s.servicesMu.Lock()
	defer s.servicesMu.Unlock()
	return s.services[sandboxID]
}

func (s *Service) initSupervisorMaps() {
	if s.services == nil {
		s.services = make(map[string]*serviceSupervisor)
	}
}

// startServices reads the workspace manifest and starts services in dependency
// order. Failure marks a service error and never blocks the terminal.
func (s *Service) startServices(ctx context.Context, sandboxID, workspaceRoot string) {
	manifestPath := filepath.Join(workspaceRoot, ".nullkey", "services.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var manifest serviceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		s.Audit.Record(ctx, "service.manifest_invalid", &sandboxID, map[string]any{"error": "invalid manifest"})
		return
	}
	defs, err := orderServices(manifest.Services)
	if err != nil {
		s.Audit.Record(ctx, "service.manifest_invalid", &sandboxID, map[string]any{"error": err.Error()})
		return
	}
	sup := &serviceSupervisor{sandboxID: sandboxID, procs: make(map[string]*runningService)}
	s.servicesMu.Lock()
	s.initSupervisorMaps()
	s.services[sandboxID] = sup
	s.servicesMu.Unlock()

	services := make([]domain.Service, 0, len(defs))
	for _, def := range defs {
		svc := domain.Service{
			ID:         "svc_" + sanitizeSegment(def.Name),
			SandboxID:  sandboxID,
			Name:       def.Name,
			Protocol:   "http",
			TargetHost: "127.0.0.1",
			TargetPort: def.Port,
			HealthPath: def.Health,
			WebSocket:  def.WebSocket,
			Status:     domain.ServiceStarting,
			Command:    def.Command,
			Ports:      []int{def.Port},
			CreatedAt:  s.now(),
		}
		services = append(services, svc)
	}
	_ = s.Store.ReplaceServices(ctx, sandboxID, services)

	for _, def := range defs {
		attempts := 0
		if def.Restart == "always" || def.Restart == "on-failure" {
			attempts = 3
		}
		var err error
		for attempt := 0; attempt <= attempts; attempt++ {
			err = s.spawnService(ctx, sup, def, attempt)
			if err == nil {
				break
			}
			s.Log.Warn("service_start_failed", "sandbox_id", sandboxID, "service", def.Name, "attempt", attempt, "error", redact.String(err.Error()))
			if attempt < attempts {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
				}
			}
		}
		if err != nil {
			s.Audit.Record(ctx, "service.error", &sandboxID, map[string]any{"service": def.Name})
			_ = s.Store.SetServiceStatus(ctx, "svc_"+sanitizeSegment(def.Name), domain.ServiceError)
		}
	}
}

func (s *Service) spawnService(ctx context.Context, sup *serviceSupervisor, def serviceDef, restarts int) error {
	sup.mu.Lock()
	stopping := sup.stopping
	sup.mu.Unlock()
	if stopping {
		return errors.New("sandbox is stopping")
	}
	if sb, err := s.Store.GetSandbox(ctx, sup.sandboxID); err != nil || sb.Status != domain.SandboxRunning {
		return errors.New("sandbox is not running")
	}
	token, err := nkid.Random(32)
	if err != nil {
		return err
	}
	env := []string{"NULLKEY_SERVICE_TOKEN=" + token}
	for k, v := range def.Env {
		if strings.HasPrefix(k, "NULLKEY_") {
			continue
		}
		env = append(env, k+"="+v)
	}
	proc, err := s.Runtime.StartService(ctx, sup.sandboxID, native.ServiceSpec{
		Name: def.Name, Command: def.Command, Env: env, Port: def.Port,
	})
	if err != nil {
		return err
	}
	rs := &runningService{def: def, proc: proc, token: token, id: "svc_" + sanitizeSegment(def.Name)}
	sup.mu.Lock()
	sup.procs[def.Name] = rs
	sup.order = append(sup.order, def.Name)
	sup.mu.Unlock()

	if err := s.waitHealthy(ctx, def); err != nil {
		proc.Stop(ctx)
		return err
	}
	// Audit before publishing ready so an observer that sees "ready" can always
	// find the corresponding audit event.
	s.Audit.Record(ctx, "service.started", &sup.sandboxID, map[string]any{"service": def.Name, "port": def.Port})
	_ = s.Store.SetServiceStatus(ctx, rs.id, domain.ServiceReady)
	s.PublishEvent("service.started", map[string]any{"sandbox_id": sup.sandboxID, "service": def.Name, "status": "ready"})

	go s.monitorService(sup, rs, restarts)
	return nil
}

func (s *Service) monitorService(sup *serviceSupervisor, rs *runningService, restarts int) {
	<-rs.proc.Done()
	sup.mu.Lock()
	stopping := sup.stopping
	sup.mu.Unlock()
	if stopping {
		return
	}
	_ = s.Store.SetServiceStatus(context.Background(), rs.id, domain.ServiceError)
	logs := redact.String(rs.proc.Logs())
	_ = logs
	s.Audit.Record(context.Background(), "service.exited", &sup.sandboxID, map[string]any{
		"service":   rs.def.Name,
		"exit_code": rs.proc.ExitCode(),
	})
	policy := rs.def.Restart
	if (policy == "always" || policy == "on-failure") && restarts < 3 && s.now().UnixNano() > 0 {
		time.Sleep(time.Duration(restarts+1) * 200 * time.Millisecond)
		sup.mu.Lock()
		if sup.stopping {
			sup.mu.Unlock()
			return
		}
		sup.mu.Unlock()
		if err := s.spawnService(context.Background(), sup, rs.def, restarts+1); err == nil {
			s.Audit.Record(context.Background(), "service.restarted", &sup.sandboxID, map[string]any{"service": rs.def.Name})
		}
	}
}

func (s *Service) waitHealthy(ctx context.Context, def serviceDef) error {
	deadline := time.Now().Add(8 * time.Second)
	// Never let a host HTTP(S)_PROXY setting redirect loopback health checks.
	client := &http.Client{
		Timeout:   500 * time.Millisecond,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	path := def.Health
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", def.Port, path)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", def.Port), 300*time.Millisecond)
		if err == nil {
			conn.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("service did not become healthy")
}

// stopServices stops services in reverse dependency order, before the harness.
func (s *Service) stopServices(ctx context.Context, sandboxID string) {
	s.servicesMu.Lock()
	sup := s.services[sandboxID]
	delete(s.services, sandboxID)
	s.servicesMu.Unlock()
	if sup == nil {
		return
	}
	sup.mu.Lock()
	sup.stopping = true
	order := append([]string(nil), sup.order...)
	sup.mu.Unlock()
	for i := len(order) - 1; i >= 0; i-- {
		sup.mu.Lock()
		rs := sup.procs[order[i]]
		sup.mu.Unlock()
		if rs != nil {
			rs.proc.Stop(ctx)
			_ = s.Store.SetServiceStatus(ctx, rs.id, domain.ServiceError)
			s.Audit.Record(ctx, "service.stopped", &sandboxID, map[string]any{"service": rs.def.Name})
		}
	}
	_ = s.Store.ReplaceServices(ctx, sandboxID, nil)
}

// ListSandboxServices returns declared services.
func (s *Service) ListSandboxServices(ctx context.Context, sandboxID string) ([]domain.Service, error) {
	if _, err := s.GetSandbox(ctx, sandboxID); err != nil {
		return nil, err
	}
	items, err := s.Store.ListServices(ctx, sandboxID)
	if err != nil {
		return nil, Wrap(http.StatusInternalServerError, "internal", "could not list services", err)
	}
	return items, nil
}

func orderServices(defs []serviceDef) ([]serviceDef, error) {
	byName := make(map[string]serviceDef, len(defs))
	ports := make(map[int]string, len(defs))
	for _, def := range defs {
		if def.Name == "" {
			return nil, errors.New("service name is required")
		}
		if _, ok := byName[def.Name]; ok {
			return nil, fmt.Errorf("duplicate service %q", def.Name)
		}
		if len(def.Command) == 0 {
			return nil, fmt.Errorf("service %q has no command", def.Name)
		}
		if def.Port < 1024 || def.Port > 65535 {
			return nil, fmt.Errorf("service %q port must be an unprivileged TCP port (1024-65535)", def.Name)
		}
		if other, ok := ports[def.Port]; ok {
			return nil, fmt.Errorf("services %q and %q share port %d", other, def.Name, def.Port)
		}
		ports[def.Port] = def.Name
		byName[def.Name] = def
	}
	// DFS topological sort with cycle detection.
	state := make(map[string]int)
	order := make([]serviceDef, 0, len(defs))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("dependency cycle at %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		def := byName[name]
		for _, dep := range def.DependsOn {
			if _, ok := byName[dep]; !ok {
				return fmt.Errorf("service %q depends on unknown %q", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, def)
		return nil
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
