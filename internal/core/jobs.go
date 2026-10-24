package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// InstallHarness starts a cancellable local install job. Remote downloads are
// deliberately out of scope for v1; the job stages an adapter marker and
// commits atomically, and cancellation removes staging.
func (s *Service) InstallHarness(ctx context.Context, harnessID, version string) (domain.Job, error) {
	found := false
	for _, h := range harnessCatalog {
		if h.ID == harnessID {
			found = true
			break
		}
	}
	if !found {
		return domain.Job{}, NotFound("harness not found")
	}
	if version == "" {
		version = "latest"
	}
	job := domain.Job{
		ID:          newID("job_"),
		Type:        "harness_install",
		Status:      domain.JobQueued,
		Cancellable: true,
		Progress:    0,
		CreatedAt:   s.now(),
		UpdatedAt:   s.now(),
	}
	if err := s.Store.CreateJob(ctx, job); err != nil {
		return domain.Job{}, Wrap(http.StatusInternalServerError, "internal", "could not create install job", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.jobsMu.Lock()
	s.jobs[job.ID] = cancel
	s.jobsMu.Unlock()
	s.Audit.Record(ctx, "job.created", nil, map[string]any{"job_id": job.ID, "type": job.Type, "harness_id": harnessID})
	s.PublishEvent("job.created", map[string]any{"job_id": job.ID, "status": string(job.Status), "type": job.Type})

	go s.runInstall(runCtx, job.ID, harnessID, version)
	return job, nil
}

func (s *Service) runInstall(ctx context.Context, jobID, harnessID, version string) {
	staging := filepath.Join(s.Home, "harnesses", ".staging-"+jobID)
	defer func() {
		s.jobsMu.Lock()
		delete(s.jobs, jobID)
		s.jobsMu.Unlock()
	}()

	update := func(status domain.JobStatus, progress float64, message string, lastErr *string) {
		job, err := s.Store.GetJob(context.Background(), jobID)
		if err != nil {
			return
		}
		job.Status = status
		job.Progress = progress
		if message != "" {
			job.Message = &message
		}
		job.Error = lastErr
		job.UpdatedAt = s.now()
		_ = s.Store.UpdateJob(context.Background(), job)
		s.PublishEvent("job.updated", map[string]any{"job_id": jobID, "status": string(status), "progress": progress})
	}

	update(domain.JobRunning, 0.1, "validating harness", nil)
	select {
	case <-ctx.Done():
		s.Audit.Record(context.Background(), "job.cancelled", nil, map[string]any{"job_id": jobID})
		update(domain.JobCancelled, 0, "cancelled", nil)
		return
	case <-time.After(150 * time.Millisecond):
	}

	if err := os.MkdirAll(staging, 0o700); err != nil {
		msg := "could not create install staging directory"
		update(domain.JobFailed, 0, msg, &msg)
		return
	}
	update(domain.JobRunning, 0.5, "staging harness", nil)
	select {
	case <-ctx.Done():
		_ = os.RemoveAll(staging)
		s.Audit.Record(context.Background(), "job.cancelled", nil, map[string]any{"job_id": jobID})
		update(domain.JobCancelled, 0, "cancelled", nil)
		return
	case <-time.After(250 * time.Millisecond):
	}

	marker, _ := json.Marshal(map[string]any{
		"harness_id":   harnessID,
		"version":      version,
		"installed_at": s.now().Format(time.RFC3339),
	})
	if err := os.WriteFile(filepath.Join(staging, "installed.json"), marker, 0o600); err != nil {
		_ = os.RemoveAll(staging)
		msg := "could not write install marker"
		update(domain.JobFailed, 0, msg, &msg)
		return
	}
	finalDir := filepath.Join(s.Home, "harnesses", harnessID)
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
		_ = os.RemoveAll(staging)
		msg := "could not create harness directory"
		update(domain.JobFailed, 0, msg, &msg)
		return
	}
	_ = os.RemoveAll(finalDir)
	if err := os.Rename(staging, finalDir); err != nil {
		_ = os.RemoveAll(staging)
		msg := "could not commit harness install"
		update(domain.JobFailed, 0, msg, &msg)
		return
	}
	update(domain.JobSucceeded, 1, "installed", nil)
	s.Audit.Record(context.Background(), "job.succeeded", nil, map[string]any{"job_id": jobID, "harness_id": harnessID, "version": version})
}

// GetJob returns a job.
func (s *Service) GetJob(ctx context.Context, id string) (domain.Job, error) {
	job, err := s.Store.GetJob(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return domain.Job{}, NotFound("job not found")
		}
		return domain.Job{}, Wrap(http.StatusInternalServerError, "internal", "could not read job", err)
	}
	return job, nil
}

// CancelJob applies the cancellation state machine.
func (s *Service) CancelJob(ctx context.Context, id string) (domain.Job, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if !job.Cancellable {
		return domain.Job{}, Conflict("conflict", "job is not cancellable")
	}
	switch job.Status {
	case domain.JobCancelled, domain.JobCancelling:
		return job, nil
	case domain.JobSucceeded, domain.JobFailed:
		return domain.Job{}, Conflict("conflict", "job has already completed")
	case domain.JobQueued, domain.JobRunning:
		job.Status = domain.JobCancelling
		job.UpdatedAt = s.now()
		if err := s.Store.UpdateJob(ctx, job); err != nil {
			return domain.Job{}, Wrap(http.StatusInternalServerError, "internal", "could not cancel job", err)
		}
		s.jobsMu.Lock()
		cancel := s.jobs[id]
		s.jobsMu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.Audit.Record(ctx, "job.cancelling", nil, map[string]any{"job_id": id})
		s.PublishEvent("job.updated", map[string]any{"job_id": id, "status": string(domain.JobCancelling)})
		return job, nil
	default:
		return domain.Job{}, Conflict("conflict", "job cannot be cancelled")
	}
}

// InstallApp is a desktop operation and is unavailable in v1.
func (s *Service) InstallApp(ctx context.Context, id string) (string, error) {
	return "", E(http.StatusNotImplemented, "desktop_unavailable", "desktop apps are not available in this runtime")
}

// LaunchApp is a desktop operation and is unavailable in v1.
func (s *Service) LaunchApp(ctx context.Context, sandboxID, appID string) (string, error) {
	return "", E(http.StatusNotImplemented, "desktop_unavailable", "desktop apps are not available in this runtime")
}
