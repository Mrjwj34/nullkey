//go:build acceptance

package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAC_API_008_IdempotencyPagination(t *testing.T) {
	d := startDaemon(t)
	key := "idem-workspace-1"
	body := map[string]any{"name": "idem"}
	headers := map[string]string{"Idempotency-Key": key, "Authorization": "Bearer " + d.token}
	resp, raw := d.do(t, http.MethodPost, "/workspaces", body, headers)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create = %d; body=%s", resp.StatusCode, raw)
	}
	first := decodeJSON[workspaceResponse](t, raw)
	resp, raw = d.do(t, http.MethodPost, "/workspaces", body, headers)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replayed create = %d; body=%s", resp.StatusCode, raw)
	}
	second := decodeJSON[workspaceResponse](t, raw)
	if first.ID != second.ID {
		t.Fatalf("idempotent replay created a new workspace: %s vs %s\nlogs=%s", first.ID, second.ID, d.logs.String())
	}

	resp, raw = d.do(t, http.MethodPost, "/workspaces", map[string]any{"name": "different"}, headers)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("key reuse with different body = %d, want 400; body=%s", resp.StatusCode, raw)
	}

	for _, name := range []string{"page-a", "page-b", "page-c"} {
		d.createWorkspace(t, name)
	}
	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces?limit=2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page 1 = %d; body=%s", resp.StatusCode, raw)
	}
	var page1 struct {
		Items      []workspaceResponse `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &page1); err != nil {
		t.Fatalf("decode page 1: %v", err)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page 1 items = %d, want 2", len(page1.Items))
	}
	if page1.NextCursor == nil || *page1.NextCursor == "" {
		t.Fatalf("page 1 missing next_cursor: %s", raw)
	}
	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces?limit=2&cursor="+*page1.NextCursor, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page 2 = %d; body=%s", resp.StatusCode, raw)
	}
	var page2 struct {
		Items      []workspaceResponse `json:"items"`
		NextCursor *string             `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &page2); err != nil {
		t.Fatalf("decode page 2: %v", err)
	}
	if len(page2.Items) == 0 {
		t.Fatalf("page 2 empty; body=%s", raw)
	}
	for _, a := range page1.Items {
		for _, b := range page2.Items {
			if a.ID == b.ID {
				t.Fatalf("workspace %s appeared on both pages", a.ID)
			}
		}
	}
}

func TestAC_API_009_SSEEvents(t *testing.T) {
	d := startDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/events", nil)
	if err != nil {
		t.Fatalf("build event request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	go func() { d.createWorkspace(t, "sse") }()

	reader := bufio.NewReader(resp.Body)
	eventCh := make(chan string, 1)
	go func() {
		var event string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
				if event == "workspace.created" {
					eventCh <- event
					return
				}
			}
		}
	}()
	select {
	case event := <-eventCh:
		if event != "workspace.created" {
			t.Fatalf("event = %q", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workspace.created SSE event")
	}
}

func TestAC_API_010_JobCancel(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := startDaemonWithEnv(t, map[string]string{"PATH": binDir})
	resp, raw := d.doAuthed(t, http.MethodPost, "/harnesses/codex/install", map[string]any{"version": "test"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("install status = %d; body=%s", resp.StatusCode, raw)
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil || accepted.JobID == "" {
		t.Fatalf("decode job id: %v; body=%s", err, raw)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/jobs/"+accepted.JobID+"/cancel", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d; body=%s", resp.StatusCode, raw)
	}
	var job struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatalf("decode cancel job: %v", err)
	}
	if job.Status != "cancelling" && job.Status != "cancelled" {
		t.Fatalf("cancel status = %q, want cancelling/cancelled", job.Status)
	}
	var finalStatus string
	waitFor(t, 3*time.Second, "job cancelled", func() bool {
		resp, raw := d.doAuthed(t, http.MethodGet, "/jobs/"+accepted.JobID, nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var current struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &current)
		finalStatus = current.Status
		return current.Status == "cancelled"
	})
	if finalStatus != "cancelled" {
		t.Fatalf("final status = %q", finalStatus)
	}
	if _, err := os.Stat(filepath.Join(d.home, "harnesses", "codex")); !os.IsNotExist(err) {
		t.Fatalf("cancelled install left a committed harness: %v", err)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/jobs/"+accepted.JobID+"/cancel", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("repeat cancel status = %d; body=%s", resp.StatusCode, raw)
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/harnesses/codex/install", map[string]any{"version": "test"})
	var done struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(raw, &done)
	waitFor(t, 5*time.Second, "install succeeded", func() bool {
		resp, raw := d.doAuthed(t, http.MethodGet, "/jobs/"+done.JobID, nil)
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var current struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &current)
		return current.Status == "succeeded"
	})
	resp, raw = d.doAuthed(t, http.MethodPost, "/jobs/"+done.JobID+"/cancel", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel completed job = %d, want 409; body=%s", resp.StatusCode, raw)
	}
}
