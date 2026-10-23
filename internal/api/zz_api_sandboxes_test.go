package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

type sandboxPage struct {
	Items      []domain.Sandbox `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

func TestAPISandboxLifecycle(t *testing.T) {
	api := newTestAPI(t)
	ws := api.createWorkspace(t, map[string]any{"name": "sandbox-ws"})

	rec := api.auth(http.MethodGet, "/api/v1/sandboxes", "")
	wantStatus(t, rec, http.StatusOK)
	if page := mustDecode[sandboxPage](t, rec); page.Items == nil {
		t.Fatal("items must be an array, not null")
	}

	// Creation validation and runtime failure paths.
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{
		"harness_id":   "shell",
		"workspace_id": ws.ID,
		"mounts":       []map[string]string{{"source": "x", "target": "y"}},
	})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{
		"harness_id":   "shell",
		"workspace_id": ws.ID,
		"mounts":       []map[string]string{{"source": "host", "host_path": "/tmp"}},
	})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{
		"harness_id":   "shell",
		"workspace_id": ws.ID,
		"mounts":       []map[string]string{{"source": "workspace", "target": "/work", "mode": "rwx"}},
	})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	// A syntactically valid workspace mount passes transport validation and
	// then fails at the unavailable runtime.
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{
		"harness_id":   "shell",
		"workspace_id": ws.ID,
		"mounts":       []map[string]string{{"source": "workspace", "target": "/work", "mode": "ro"}},
	})
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeRuntimeUnavailable)
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{"workspace_id": ws.ID})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{
		"harness_id":   "shell",
		"workspace_id": ws.ID,
		"runtime":      "desktop",
	})
	wantErrorCode(t, rec, http.StatusNotImplemented, CodeDesktopUnavailable)
	rec = api.postJSON("/api/v1/sandboxes", map[string]any{
		"harness_id":   "shell",
		"workspace_id": ws.ID,
		"network_mode": "filtered",
	})
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeRuntimeUnavailable)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes", `{"harness_id":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	now := time.Now().UTC()
	running := api.insertSandbox(t, domain.Sandbox{
		ID:          "sbx_running",
		HarnessID:   "shell",
		WorkspaceID: ws.ID,
		Runtime:     domain.RuntimeNative,
		NetworkMode: domain.NetworkFiltered,
		Status:      domain.SandboxRunning,
		StartedAt:   &now,
		CreatedAt:   now.Add(-time.Minute),
	})
	stopped := api.insertSandbox(t, domain.Sandbox{
		ID:          "sbx_stopped",
		HarnessID:   "shell",
		WorkspaceID: ws.ID,
		Runtime:     domain.RuntimeNative,
		NetworkMode: domain.NetworkFiltered,
		Status:      domain.SandboxStopped,
		CreatedAt:   now.Add(-2 * time.Minute),
	})

	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/"+running.ID, "")
	wantStatus(t, rec, http.StatusOK)
	if got := mustDecode[domain.Sandbox](t, rec); got.ID != running.ID || got.Status != domain.SandboxRunning {
		t.Fatalf("get sandbox = %+v", got)
	}
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/sbx_missing", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	rec = api.auth(http.MethodGet, "/api/v1/sandboxes?limit=1", "")
	wantStatus(t, rec, http.StatusOK)
	first := mustDecode[sandboxPage](t, rec)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first page = %+v", first)
	}
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes?limit=1&cursor="+*first.NextCursor, "")
	wantStatus(t, rec, http.StatusOK)
	if second := mustDecode[sandboxPage](t, rec); len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second page = %+v", second)
	}
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes?cursor=%40%40%40", "")
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)

	// Stats.
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/"+running.ID+"/stats", "")
	wantStatus(t, rec, http.StatusOK)
	if stats := mustDecode[domain.Stats](t, rec); stats.PID != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/sbx_missing/stats", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Terminal tickets.
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/sbx_missing/terminal", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+stopped.ID+"/terminal", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeConflict)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/terminal", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeTerminalNotReady)
	t.Cleanup(func() { api.svc.Terminal.Close(running.ID) })
	api.svc.Terminal.Open(running.ID, newFakeTerminalSource())
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/terminal", "")
	wantStatus(t, rec, http.StatusOK)
	ticket := mustDecode[domain.TerminalTicket](t, rec)
	if ticket.Ticket == "" || ticket.WSURL == "" || ticket.ExpiresAt.IsZero() {
		t.Fatalf("terminal ticket = %+v", ticket)
	}

	// Desktop is always unavailable in v1.
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/desktop", "")
	wantErrorCode(t, rec, http.StatusNotImplemented, CodeDesktopUnavailable)

	// Services and service tickets.
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/sbx_missing/services", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/"+running.ID+"/services", "")
	wantStatus(t, rec, http.StatusOK)
	if page := mustDecode[struct {
		Items []domain.Service `json:"items"`
	}](t, rec); page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("services = %+v", page.Items)
	}
	if err := api.store.ReplaceServices(context.Background(), running.ID, []domain.Service{{
		ID:         "svc_web",
		SandboxID:  running.ID,
		Name:       "web",
		Protocol:   "http",
		TargetHost: "127.0.0.1",
		TargetPort: 20000,
		Status:     domain.ServiceReady,
		CreatedAt:  now,
	}}); err != nil {
		t.Fatalf("replace services: %v", err)
	}
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/"+running.ID+"/services", "")
	wantStatus(t, rec, http.StatusOK)
	if page := mustDecode[struct {
		Items []domain.Service `json:"items"`
	}](t, rec); len(page.Items) != 1 || page.Items[0].ID != "svc_web" {
		t.Fatalf("services = %+v", page.Items)
	}

	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/sbx_missing/services/svc_web/ticket", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+stopped.ID+"/services/svc_web/ticket", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeConflict)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/services/svc_missing/ticket", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeServiceNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/services/svc_web/ticket", "")
	wantStatus(t, rec, http.StatusOK)
	svcTicket := mustDecode[domain.ServiceTicket](t, rec)
	if svcTicket.Ticket == "" || svcTicket.URL == "" || svcTicket.ServiceID != "svc_web" {
		t.Fatalf("service ticket = %+v", svcTicket)
	}

	// Scan reports generated-runtime scope.
	state := api.createState(t, "shell", "scan")
	scanTarget := api.insertSandbox(t, domain.Sandbox{
		ID:          "sbx_scan",
		HarnessID:   "shell",
		WorkspaceID: ws.ID,
		StateID:     state.ID,
		Runtime:     domain.RuntimeNative,
		NetworkMode: domain.NetworkFiltered,
		Status:      domain.SandboxRunning,
		CreatedAt:   now,
	})
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+scanTarget.ID+"/scan", "")
	wantStatus(t, rec, http.StatusOK)
	result := mustDecode[domain.ScanResult](t, rec)
	if !result.OK || result.Scope != "generated_runtime" || result.Checks == nil {
		t.Fatalf("scan = %+v", result)
	}
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/sbx_missing/scan", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Stop is idempotent.
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/sbx_missing/stop", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/stop", "")
	wantStatus(t, rec, http.StatusOK)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/"+running.ID+"/stop", "")
	wantStatus(t, rec, http.StatusOK)

	// Delete rejects legacy cascade parameters and cascades nothing.
	rec = api.auth(http.MethodDelete, "/api/v1/sandboxes/"+stopped.ID+"?delete_state=true", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodDelete, "/api/v1/sandboxes/"+stopped.ID+"?delete_workspace=false", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodDelete, "/api/v1/sandboxes/sbx_missing", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodDelete, "/api/v1/sandboxes/"+stopped.ID, "")
	wantStatus(t, rec, http.StatusNoContent)
	rec = api.auth(http.MethodGet, "/api/v1/sandboxes/"+stopped.ID, "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}
