package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/core"
	"github.com/Mrjwj34/nullkey/internal/domain"
)

func TestAPIHarnessesAndJobs(t *testing.T) {
	api := newTestAPI(t)

	rec := api.auth(http.MethodGet, "/api/v1/harnesses", "")
	wantStatus(t, rec, http.StatusOK)
	harnesses := mustDecode[struct {
		Items []core.Harness `json:"items"`
	}](t, rec).Items
	if len(harnesses) == 0 {
		t.Fatal("empty harness catalog")
	}
	foundShell := false
	for _, h := range harnesses {
		if h.ID == "shell" && h.Installed {
			foundShell = true
		}
	}
	if !foundShell {
		t.Fatalf("shell harness not reported installed: %+v", harnesses)
	}

	rec = api.postJSON("/api/v1/harnesses/codex/install", map[string]any{"version": "1.2.3"})
	wantStatus(t, rec, http.StatusAccepted)
	installed := mustDecode[struct {
		JobID string `json:"job_id"`
	}](t, rec)
	if !strings.HasPrefix(installed.JobID, "job_") {
		t.Fatalf("job_id = %q", installed.JobID)
	}

	rec = api.auth(http.MethodGet, "/api/v1/jobs/"+installed.JobID, "")
	wantStatus(t, rec, http.StatusOK)
	job := mustDecode[jobResponse](t, rec)
	if job.ID != installed.JobID || job.Type != "harness_install" {
		t.Fatalf("job = %+v", job)
	}

	rec = api.auth(http.MethodPost, "/api/v1/jobs/"+installed.JobID+"/cancel", "")
	wantStatus(t, rec, http.StatusAccepted)
	cancelled := mustDecode[jobResponse](t, rec)
	if cancelled.ID != installed.JobID || !cancelled.Cancellable {
		t.Fatalf("cancelled job = %+v", cancelled)
	}
	// Cancelling an already-cancelling job is idempotent.
	rec = api.auth(http.MethodPost, "/api/v1/jobs/"+installed.JobID+"/cancel", "")
	wantStatus(t, rec, http.StatusAccepted)

	rec = api.postJSON("/api/v1/harnesses/nope/install", map[string]any{})
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/harnesses/codex/install", `{"version":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodGet, "/api/v1/jobs/job_missing", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodPost, "/api/v1/jobs/job_missing/cancel", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	now := time.Now().UTC()
	succeeded := domain.Job{
		ID: "job_succeeded", Type: "harness_install", Status: domain.JobSucceeded,
		Cancellable: true, Progress: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := api.store.CreateJob(context.Background(), succeeded); err != nil {
		t.Fatalf("create job: %v", err)
	}
	rec = api.auth(http.MethodPost, "/api/v1/jobs/"+succeeded.ID+"/cancel", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeConflict)

	notCancellable := domain.Job{
		ID: "job_fixed", Type: "harness_install", Status: domain.JobQueued,
		Cancellable: false, CreatedAt: now, UpdatedAt: now,
	}
	if err := api.store.CreateJob(context.Background(), notCancellable); err != nil {
		t.Fatalf("create job: %v", err)
	}
	rec = api.auth(http.MethodPost, "/api/v1/jobs/"+notCancellable.ID+"/cancel", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeConflict)

	alreadyCancelled := domain.Job{
		ID: "job_cancelled", Type: "harness_install", Status: domain.JobCancelled,
		Cancellable: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := api.store.CreateJob(context.Background(), alreadyCancelled); err != nil {
		t.Fatalf("create job: %v", err)
	}
	rec = api.auth(http.MethodPost, "/api/v1/jobs/"+alreadyCancelled.ID+"/cancel", "")
	wantStatus(t, rec, http.StatusAccepted)
}

func TestAPIAppsAndProviders(t *testing.T) {
	api := newTestAPI(t)

	rec := api.auth(http.MethodGet, "/api/v1/apps", "")
	wantStatus(t, rec, http.StatusOK)
	if page := mustDecode[struct {
		Items []core.App `json:"items"`
	}](t, rec); page.Items == nil {
		t.Fatal("apps items must be an array, not null")
	}

	rec = api.auth(http.MethodPost, "/api/v1/apps/foo/install", "")
	wantErrorCode(t, rec, http.StatusNotImplemented, CodeDesktopUnavailable)
	rec = api.auth(http.MethodPost, "/api/v1/sandboxes/sbx/apps/foo/launch", "")
	wantErrorCode(t, rec, http.StatusNotImplemented, CodeDesktopUnavailable)

	rec = api.auth(http.MethodGet, "/api/v1/providers", "")
	wantStatus(t, rec, http.StatusOK)
	providers := mustDecode[struct {
		Items []core.Provider `json:"items"`
	}](t, rec).Items
	if len(providers) != 4 {
		t.Fatalf("providers = %+v", providers)
	}
	for _, p := range providers {
		if len(p.AuthTypes) == 0 {
			t.Fatalf("provider %q has no auth types", p.ID)
		}
	}
}

func TestAPIAudit(t *testing.T) {
	api := newTestAPI(t)
	for _, name := range []string{"audit-one", "audit-two", "audit-three"} {
		api.createWorkspace(t, map[string]any{"name": name})
	}

	rec := api.auth(http.MethodGet, "/api/v1/audit", "")
	wantStatus(t, rec, http.StatusOK)
	page := mustDecode[struct {
		Items      []domain.AuditEvent `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}](t, rec)
	if len(page.Items) < 3 {
		t.Fatalf("audit items = %d, want >= 3", len(page.Items))
	}

	rec = api.auth(http.MethodGet, "/api/v1/audit?limit=1", "")
	wantStatus(t, rec, http.StatusOK)
	first := mustDecode[struct {
		Items      []domain.AuditEvent `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}](t, rec)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first audit page = %+v", first)
	}
	rec = api.auth(http.MethodGet, "/api/v1/audit?limit=1&cursor="+*first.NextCursor, "")
	wantStatus(t, rec, http.StatusOK)

	rec = api.auth(http.MethodGet, "/api/v1/audit?type=workspace.created", "")
	wantStatus(t, rec, http.StatusOK)
	filtered := mustDecode[struct {
		Items []domain.AuditEvent `json:"items"`
	}](t, rec)
	if len(filtered.Items) == 0 {
		t.Fatal("type filter returned no events")
	}
	for _, event := range filtered.Items {
		if event.Type != "workspace.created" {
			t.Fatalf("unexpected event type %q", event.Type)
		}
	}

	rec = api.auth(http.MethodGet, "/api/v1/audit?type=no.such.type", "")
	wantStatus(t, rec, http.StatusOK)
	if empty := mustDecode[struct {
		Items []domain.AuditEvent `json:"items"`
	}](t, rec); len(empty.Items) != 0 {
		t.Fatalf("unexpected events: %+v", empty.Items)
	}

	rec = api.auth(http.MethodGet, "/api/v1/audit?cursor=%40%40%40", "")
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)

	auditSink := api.svc.Audit
	api.svc.Audit = nil
	rec = api.auth(http.MethodGet, "/api/v1/audit", "")
	wantErrorCode(t, rec, http.StatusServiceUnavailable, CodeInternal)
	api.svc.Audit = auditSink
}

func TestAPIEventsSSE(t *testing.T) {
	api := newTestAPI(t)
	ts := httptest.NewServer(api.handler)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q", got)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read connected line: %v", err)
	}
	if !strings.HasPrefix(line, ": connected") {
		t.Fatalf("first line = %q", line)
	}

	// Wait until the handler has subscribed before publishing; tiny, bounded.
	deadline := time.Now().Add(2 * time.Second)
	for api.svc.Events.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if api.svc.Events.Subscribers() == 0 {
		t.Fatal("SSE handler did not subscribe")
	}

	// A payload that cannot be marshalled must not terminate the stream.
	api.svc.PublishEvent("bad.event", map[string]any{"fn": func() {}})
	api.svc.PublishEvent("good.event", map[string]any{"n": 1})

	sawGood := false
	for i := 0; i < 10; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event line: %v", err)
		}
		if strings.Contains(line, "event: good.event") {
			sawGood = true
			break
		}
		if strings.Contains(line, "event: bad.event") {
			t.Fatal("unmarshalable event was emitted")
		}
	}
	if !sawGood {
		t.Fatal("did not receive good.event")
	}

	cancel()
	_ = resp.Body.Close()
}

// Ensure the SSE handler returns promptly when the client goes away.
func TestAPIEventsClientDisconnect(t *testing.T) {
	api := newTestAPI(t)
	ts := httptest.NewServer(api.handler)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 1)
	cancel()
	_ = resp.Body.Close()
	ts.Close()
}
