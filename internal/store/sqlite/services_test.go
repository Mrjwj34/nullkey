package sqlite

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var svcBase = time.Date(2024, 8, 1, 14, 0, 0, 0, time.UTC)

func TestReplaceServicesAndList(t *testing.T) {
	s, ctx := openTestStore(t)
	services := []domain.Service{
		{
			ID: "svc_a", Name: "alpha", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 8080,
			HealthPath: "/healthz", WebSocket: true, Status: domain.ServiceReady,
			Command: []string{"python", "-m", "http.server"}, Ports: []int{8080, 8081},
		},
		{
			ID: "svc_b", Name: "beta", Protocol: "https", TargetHost: "localhost", TargetPort: 8443,
			Status: domain.ServiceStarting,
		},
		{
			ID: "svc_c", Name: "gamma", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 9000,
			WebSocket: false, Status: domain.ServiceError, Command: []string{}, Ports: []int{},
		},
	}
	if err := s.ReplaceServices(ctx, "sb_1", services); err != nil {
		t.Fatalf("ReplaceServices() error = %v", err)
	}
	got, err := s.ListServices(ctx, "sb_1")
	if err != nil {
		t.Fatalf("ListServices() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListServices() len = %d, want 3", len(got))
	}
	byID := map[string]domain.Service{}
	for _, svc := range got {
		byID[svc.ID] = svc
	}
	alpha := byID["svc_a"]
	if alpha.SandboxID != "sb_1" || alpha.Name != "alpha" || alpha.Protocol != "http" {
		t.Errorf("alpha = %+v", alpha)
	}
	if alpha.TargetHost != "127.0.0.1" || alpha.TargetPort != 8080 || alpha.HealthPath != "/healthz" {
		t.Errorf("alpha target = %+v", alpha)
	}
	if !alpha.WebSocket || alpha.Status != domain.ServiceReady {
		t.Errorf("alpha flags = websocket %v status %q", alpha.WebSocket, alpha.Status)
	}
	if !reflect.DeepEqual(alpha.Command, []string{"python", "-m", "http.server"}) {
		t.Errorf("alpha command = %v", alpha.Command)
	}
	if !reflect.DeepEqual(alpha.Ports, []int{8080, 8081}) {
		t.Errorf("alpha ports = %v", alpha.Ports)
	}
	if alpha.CreatedAt.IsZero() {
		t.Error("alpha CreatedAt is zero")
	}
	gamma := byID["svc_c"]
	if gamma.WebSocket || gamma.Status != domain.ServiceError || gamma.Command == nil || gamma.Ports == nil {
		t.Errorf("gamma = %+v", gamma)
	}

	// Replace removes the old set and updates rows with the same id.
	replacement := []domain.Service{
		{ID: "svc_a", Name: "alpha-renamed", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 8082, Status: domain.ServiceReady},
		{ID: "svc_d", Name: "delta", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 7000, Status: domain.ServiceStarting},
	}
	if err := s.ReplaceServices(ctx, "sb_1", replacement); err != nil {
		t.Fatalf("ReplaceServices(replacement) error = %v", err)
	}
	got, err = s.ListServices(ctx, "sb_1")
	if err != nil {
		t.Fatalf("ListServices() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListServices(replacement) len = %d, want 2", len(got))
	}
	if got, err := s.GetService(ctx, "sb_1", "svc_a"); err != nil || got.Name != "alpha-renamed" || got.TargetPort != 8082 {
		t.Errorf("GetService(svc_a) = (%+v, %v)", got, err)
	}
	if _, err := s.GetService(ctx, "sb_1", "svc_b"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetService(removed) error = %v, want domain.ErrNotFound", err)
	}
}

func TestListServicesOrderAndIsolation(t *testing.T) {
	s, ctx := openTestStore(t)
	services := []domain.Service{
		{ID: "svc_a", Name: "alpha", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 1, Status: domain.ServiceReady},
		{ID: "svc_b", Name: "beta", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 2, Status: domain.ServiceReady},
		{ID: "svc_c", Name: "gamma", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 3, Status: domain.ServiceReady},
	}
	if err := s.ReplaceServices(ctx, "sb_1", services); err != nil {
		t.Fatalf("ReplaceServices() error = %v", err)
	}
	for i, svc := range services {
		if _, err := s.db.ExecContext(ctx, `UPDATE services SET created_at=? WHERE id=?`, formatTime(svcBase.Add(time.Duration(i)*time.Second)), svc.ID); err != nil {
			t.Fatalf("seed created_at: %v", err)
		}
	}
	got, err := s.ListServices(ctx, "sb_1")
	if err != nil {
		t.Fatalf("ListServices() error = %v", err)
	}
	if len(got) != 3 || got[0].ID != "svc_a" || got[1].ID != "svc_b" || got[2].ID != "svc_c" {
		t.Errorf("ListServices() order = %v", serviceIDs(got))
	}

	if err := s.ReplaceServices(ctx, "sb_2", []domain.Service{
		{ID: "svc_other", Name: "other", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 4, Status: domain.ServiceReady},
	}); err != nil {
		t.Fatalf("ReplaceServices(sb_2) error = %v", err)
	}
	first, err := s.ListServices(ctx, "sb_1")
	if err != nil {
		t.Fatalf("ListServices(sb_1) error = %v", err)
	}
	if len(first) != 3 {
		t.Errorf("sb_1 service count = %d, want 3", len(first))
	}

	empty, err := s.ListServices(ctx, "sb_missing")
	if err != nil {
		t.Fatalf("ListServices(empty) error = %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("ListServices(empty) = %#v, want empty non-nil", empty)
	}
}

func TestGetServiceByIDAndName(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.ReplaceServices(ctx, "sb_1", []domain.Service{
		{ID: "svc_a", Name: "alpha", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 8080, Status: domain.ServiceReady},
	}); err != nil {
		t.Fatalf("ReplaceServices() error = %v", err)
	}
	byID, err := s.GetService(ctx, "sb_1", "svc_a")
	if err != nil {
		t.Fatalf("GetService(id) error = %v", err)
	}
	if byID.Name != "alpha" {
		t.Errorf("GetService(id) = %+v", byID)
	}
	byName, err := s.GetService(ctx, "sb_1", "alpha")
	if err != nil {
		t.Fatalf("GetService(name) error = %v", err)
	}
	if byName.ID != "svc_a" {
		t.Errorf("GetService(name) = %+v", byName)
	}
	if _, err := s.GetService(ctx, "sb_1", "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetService(missing) error = %v, want domain.ErrNotFound", err)
	}
	if _, err := s.GetService(ctx, "sb_missing", "alpha"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetService(wrong sandbox) error = %v, want domain.ErrNotFound", err)
	}
}

func TestSetServiceStatus(t *testing.T) {
	s, ctx := openTestStore(t)
	if err := s.ReplaceServices(ctx, "sb_1", []domain.Service{
		{ID: "svc_a", Name: "alpha", Protocol: "http", TargetHost: "127.0.0.1", TargetPort: 8080, Status: domain.ServiceStarting},
	}); err != nil {
		t.Fatalf("ReplaceServices() error = %v", err)
	}
	if err := s.SetServiceStatus(ctx, "svc_a", domain.ServiceReady); err != nil {
		t.Fatalf("SetServiceStatus() error = %v", err)
	}
	got, err := s.GetService(ctx, "sb_1", "svc_a")
	if err != nil {
		t.Fatalf("GetService() error = %v", err)
	}
	if got.Status != domain.ServiceReady {
		t.Errorf("Status = %q, want ready", got.Status)
	}
	if err := s.SetServiceStatus(ctx, "missing", domain.ServiceError); err != nil {
		t.Errorf("SetServiceStatus(missing) error = %v, want nil", err)
	}
}

func serviceIDs(in []domain.Service) []string {
	out := make([]string, len(in))
	for i, svc := range in {
		out[i] = svc.ID
	}
	return out
}
