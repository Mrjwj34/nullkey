package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var jobBase = time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)

func TestJobCreateGetUpdate(t *testing.T) {
	s, ctx := openTestStore(t)
	updatedAt := jobBase.Add(2 * time.Minute)
	mustCreateJob(t, ctx, s, domain.Job{
		ID:          "job_1",
		Type:        "install",
		Status:      domain.JobRunning,
		Cancellable: true,
		Progress:    0.25,
		Message:     strPtr("installing"),
		Error:       strPtr("previous warning"),
		CreatedAt:   jobBase,
		UpdatedAt:   updatedAt,
	})
	got, err := s.GetJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if got.Type != "install" || got.Status != domain.JobRunning || !got.Cancellable || got.Progress != 0.25 {
		t.Errorf("GetJob() = %+v", got)
	}
	if got.Message == nil || *got.Message != "installing" || got.Error == nil || *got.Error != "previous warning" {
		t.Errorf("message/error = %v/%v", got.Message, got.Error)
	}
	if !got.CreatedAt.Equal(jobBase) || !got.UpdatedAt.Equal(updatedAt) {
		t.Errorf("times = %v/%v", got.CreatedAt, got.UpdatedAt)
	}

	mustCreateJob(t, ctx, s, domain.Job{
		ID: "job_nullable", Type: "build", Status: domain.JobQueued, Cancellable: false,
		CreatedAt: jobBase, UpdatedAt: jobBase,
	})
	nullable, err := s.GetJob(ctx, "job_nullable")
	if err != nil {
		t.Fatalf("GetJob(nullable) error = %v", err)
	}
	if nullable.Cancellable || nullable.Message != nil || nullable.Error != nil {
		t.Errorf("nullable job = %+v", nullable)
	}
	if _, err := s.GetJob(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetJob(missing) error = %v, want sql.ErrNoRows", err)
	}
	if err := s.CreateJob(ctx, domain.Job{
		ID: "job_1", Type: "dup", Status: domain.JobQueued, CreatedAt: jobBase, UpdatedAt: jobBase,
	}); err == nil {
		t.Error("CreateJob() duplicate id error = nil, want error")
	}

	after := jobBase.Add(10 * time.Minute)
	update := domain.Job{
		ID: "job_1", Type: "ignored", Status: domain.JobSucceeded,
		Cancellable: false, Progress: 1, Message: strPtr("done"), UpdatedAt: after,
	}
	if err := s.UpdateJob(ctx, update); err != nil {
		t.Fatalf("UpdateJob() error = %v", err)
	}
	got, err = s.GetJob(ctx, "job_1")
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if got.Status != domain.JobSucceeded || got.Progress != 1 || got.Message == nil || *got.Message != "done" || got.Error != nil {
		t.Errorf("updated job = %+v", got)
	}
	if !got.Cancellable {
		t.Error("UpdateJob() changed the cancellable flag, want it preserved")
	}
	if got.Type != "install" {
		t.Errorf("UpdateJob() changed type to %q", got.Type)
	}
	if !got.UpdatedAt.Equal(after) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, after)
	}
	if err := s.UpdateJob(ctx, domain.Job{ID: "missing", Status: domain.JobFailed, UpdatedAt: after}); err != nil {
		t.Errorf("UpdateJob(missing) error = %v, want nil", err)
	}
}

func TestListJobsForType(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateJob(t, ctx, s, domain.Job{
		ID: "job_a", Type: "install", Status: domain.JobQueued, CreatedAt: jobBase, UpdatedAt: jobBase,
	})
	mustCreateJob(t, ctx, s, domain.Job{
		ID: "job_b", Type: "install", Status: domain.JobRunning, Cancellable: true,
		CreatedAt: jobBase.Add(time.Minute), UpdatedAt: jobBase.Add(time.Minute),
	})
	mustCreateJob(t, ctx, s, domain.Job{
		ID: "job_c", Type: "oauth", Status: domain.JobQueued, CreatedAt: jobBase.Add(2 * time.Minute), UpdatedAt: jobBase.Add(2 * time.Minute),
	})
	jobs, err := s.ListJobsForType(ctx, "install")
	if err != nil {
		t.Fatalf("ListJobsForType() error = %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != "job_b" || jobs[1].ID != "job_a" {
		t.Fatalf("ListJobsForType(install) = %v", jobs)
	}
	if !jobs[0].Cancellable || jobs[1].Cancellable {
		t.Errorf("cancellable flags = %v/%v", jobs[0].Cancellable, jobs[1].Cancellable)
	}
	none, err := s.ListJobsForType(ctx, "missing")
	if err != nil {
		t.Fatalf("ListJobsForType(missing) error = %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListJobsForType(missing) = %v, want empty", none)
	}
}
