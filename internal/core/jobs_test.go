package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func waitJob(t *testing.T, s *Service, id string, want domain.JobStatus) domain.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := s.GetJob(context.Background(), id)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status == want {
			return job
		}
		if job.Status == domain.JobFailed || job.Status == domain.JobSucceeded || job.Status == domain.JobCancelled {
			if job.Status != want {
				t.Fatalf("job reached %s, want %s", job.Status, want)
			}
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return domain.Job{}
}

func TestInstallHarnessUnknown(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.InstallHarness(context.Background(), "nope", "")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestInstallHarnessSuccess(t *testing.T) {
	svc, home := newTestService(t)
	ctx := context.Background()
	job, err := svc.InstallHarness(ctx, "codex", "1.2.3")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if job.Status != domain.JobQueued || !job.Cancellable || job.Type != "harness_install" || !strings.HasPrefix(job.ID, "job_") {
		t.Fatalf("job = %+v", job)
	}

	done := waitJob(t, svc, job.ID, domain.JobSucceeded)
	if done.Progress != 1 || done.Message == nil || *done.Message != "installed" || done.Error != nil {
		t.Fatalf("succeeded job = %+v", done)
	}

	marker := filepath.Join(home, "harnesses", "codex", "installed.json")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	var payload struct {
		HarnessID string `json:"harness_id"`
		Version   string `json:"version"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("marker json: %v", err)
	}
	if payload.HarnessID != "codex" || payload.Version != "1.2.3" {
		t.Fatalf("marker = %+v", payload)
	}
	entries, err := os.ReadDir(filepath.Join(home, "harnesses"))
	if err != nil {
		t.Fatalf("read harnesses: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") {
			t.Fatalf("staging directory leaked: %s", entry.Name())
		}
	}

	evs, _, err := svc.Audit.List(ctx, 50, "", "job.succeeded")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestInstallHarnessCancelledRemovesArtifact(t *testing.T) {
	svc, home := newTestService(t)
	ctx := context.Background()
	job, err := svc.InstallHarness(ctx, "claude", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	cancelled, err := svc.CancelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != domain.JobCancelling {
		t.Fatalf("cancel status = %s", cancelled.Status)
	}
	done := waitJob(t, svc, job.ID, domain.JobCancelled)
	if done.Message == nil || *done.Message != "cancelled" || done.Progress != 0 {
		t.Fatalf("cancelled job = %+v", done)
	}
	if _, err := os.Stat(filepath.Join(home, "harnesses", "claude", "installed.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled install left a marker: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "harnesses"))
	if err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".staging-") {
				t.Fatalf("staging directory leaked: %s", entry.Name())
			}
		}
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "job.cancelled")
	if err != nil || len(evs) != 1 {
		t.Fatalf("cancel audit = %d, %v", len(evs), err)
	}
}

func TestCancelJobStateMachine(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	mk := func(status domain.JobStatus, cancellable bool) domain.Job {
		job := domain.Job{
			ID: newID("job_"), Type: "test", Status: status, Cancellable: cancellable,
			CreatedAt: svc.now(), UpdatedAt: svc.now(),
		}
		if err := svc.Store.CreateJob(ctx, job); err != nil {
			t.Fatalf("create job: %v", err)
		}
		return job
	}

	cancelled := mk(domain.JobCancelled, true)
	got, err := svc.CancelJob(ctx, cancelled.ID)
	if err != nil || got.Status != domain.JobCancelled {
		t.Fatalf("cancelled job = %+v, %v", got, err)
	}

	cancelling := mk(domain.JobCancelling, true)
	got, err = svc.CancelJob(ctx, cancelling.ID)
	if err != nil || got.Status != domain.JobCancelling {
		t.Fatalf("cancelling job = %+v, %v", got, err)
	}

	succeeded := mk(domain.JobSucceeded, true)
	_, err = svc.CancelJob(ctx, succeeded.ID)
	wantError(t, err, http.StatusConflict, "conflict")

	failed := mk(domain.JobFailed, true)
	_, err = svc.CancelJob(ctx, failed.ID)
	wantError(t, err, http.StatusConflict, "conflict")

	fixed := mk(domain.JobQueued, false)
	_, err = svc.CancelJob(ctx, fixed.ID)
	wantError(t, err, http.StatusConflict, "conflict")

	queued := mk(domain.JobQueued, true)
	called := false
	svc.jobsMu.Lock()
	svc.jobs[queued.ID] = func() { called = true }
	svc.jobsMu.Unlock()
	got, err = svc.CancelJob(ctx, queued.ID)
	if err != nil || got.Status != domain.JobCancelling || !called {
		t.Fatalf("queued cancel = %+v, %v, called=%v", got, err, called)
	}
	stored, err := svc.Store.GetJob(ctx, queued.ID)
	if err != nil || stored.Status != domain.JobCancelling {
		t.Fatalf("stored job = %+v, %v", stored, err)
	}

	running := mk(domain.JobRunning, true)
	if got, err = svc.CancelJob(ctx, running.ID); err != nil || got.Status != domain.JobCancelling {
		t.Fatalf("running cancel = %+v, %v", got, err)
	}

	odd := mk(domain.JobStatus("mystery"), true)
	_, err = svc.CancelJob(ctx, odd.ID)
	wantError(t, err, http.StatusConflict, "conflict")

	_, err = svc.CancelJob(ctx, "job_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestGetJob(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	job := domain.Job{
		ID: newID("job_"), Type: "x", Status: domain.JobQueued, Cancellable: true,
		CreatedAt: svc.now(), UpdatedAt: svc.now(),
	}
	if err := svc.Store.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	got, err := svc.GetJob(ctx, job.ID)
	if err != nil || got.ID != job.ID {
		t.Fatalf("get = %+v, %v", got, err)
	}
	_, err = svc.GetJob(ctx, "job_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestDesktopOperationsUnavailable(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.InstallApp(ctx, "app")
	wantError(t, err, http.StatusNotImplemented, "desktop_unavailable")
	_, err = svc.LaunchApp(ctx, "sandbox", "app")
	wantError(t, err, http.StatusNotImplemented, "desktop_unavailable")
}

func TestInstallHarnessStagingFailure(t *testing.T) {
	svc, home := newTestService(t)
	// A file where the harness directory should be makes staging fail.
	if err := os.WriteFile(filepath.Join(home, "harnesses"), []byte("blocker"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	job, err := svc.InstallHarness(context.Background(), "codex", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	failed := waitJob(t, svc, job.ID, domain.JobFailed)
	if failed.Message == nil || !strings.Contains(*failed.Message, "staging") {
		t.Fatalf("failed job = %+v", failed)
	}
	if failed.Error == nil || *failed.Error != *failed.Message {
		t.Fatalf("error not recorded: %+v", failed)
	}
}

func TestJobStoreErrors(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := svc.InstallHarness(ctx, "codex", "")
	wantError(t, err, http.StatusInternalServerError, "internal")
	_, err = svc.GetJob(ctx, "job_x")
	wantError(t, err, http.StatusInternalServerError, "internal")
}
