//go:build acceptance

package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fileEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func TestAC_ART_001_FileSafety(t *testing.T) {
	d := startDaemon(t)
	ws := d.createWorkspace(t, "artifacts")
	writeFile(t, filepath.Join(ws.RootPath, "hello.txt"), "hello world")
	writeFile(t, filepath.Join(ws.RootPath, "dir", "nested.txt"), "nested")
	writeFile(t, filepath.Join(ws.RootPath, "binary.bin"), string([]byte{0x00, 0x01, 0x02, 0xff}))
	large := strings.Repeat("x", (1<<20)+100)
	writeFile(t, filepath.Join(ws.RootPath, "large.txt"), large)

	resp, raw := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/files", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d; body=%s", resp.StatusCode, raw)
	}
	var list struct {
		Items []fileEntry `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	names := map[string]fileEntry{}
	for _, item := range list.Items {
		names[item.Name] = item
	}
	if names["hello.txt"].Type != "file" || names["dir"].Type != "directory" {
		t.Fatalf("unexpected entries: %+v", list.Items)
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/file?path=hello.txt", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read = %d; body=%s", resp.StatusCode, raw)
	}
	file := decodeJSON[struct {
		Content   string `json:"content"`
		Encoding  string `json:"encoding"`
		Truncated bool   `json:"truncated"`
	}](t, raw)
	if file.Content != "hello world" || file.Encoding != "utf-8" || file.Truncated {
		t.Fatalf("unexpected file: %+v", file)
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/file?path=large.txt", nil)
	file = decodeJSON[struct {
		Content   string `json:"content"`
		Encoding  string `json:"encoding"`
		Truncated bool   `json:"truncated"`
	}](t, raw)
	if !file.Truncated || len(file.Content) > 1<<20 {
		t.Fatalf("large file not truncated: len=%d truncated=%v", len(file.Content), file.Truncated)
	}

	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/file?path=binary.bin", nil)
	binaryFile := decodeJSON[struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}](t, raw)
	if binaryFile.Encoding != "base64" {
		t.Fatalf("binary encoding = %q, want base64", binaryFile.Encoding)
	}
	if _, err := base64.StdEncoding.DecodeString(binaryFile.Content); err != nil {
		t.Fatalf("binary content is not base64: %v", err)
	}

	for _, path := range []string{"../etc/passwd", "/etc/passwd", "dir/../../etc/passwd"} {
		resp, raw := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/file?path="+path, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("path %q status = %d, want 400; body=%s", path, resp.StatusCode, raw)
		}
	}
	hostHome, _ := os.UserHomeDir()
	link := filepath.Join(ws.RootPath, "escape")
	if err := os.Symlink(hostHome, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/file?path=escape/.ssh/config", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("symlink escape status = %d, want 400; body=%s", resp.StatusCode, raw)
	}
}

func TestAC_ART_002_Diff(t *testing.T) {
	d := startDaemon(t)
	ws := d.createWorkspace(t, "diff")
	writeFile(t, filepath.Join(ws.RootPath, "plain.txt"), "not a repo")
	resp, raw := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/diff", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-git diff = %d; body=%s", resp.StatusCode, raw)
	}
	var nonGit struct {
		Patch string   `json:"patch"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &nonGit); err != nil {
		t.Fatalf("decode non-git diff: %v", err)
	}
	if len(nonGit.Files) == 0 {
		t.Fatalf("non-git diff returned no file-level changes: %s", raw)
	}

	runGit(t, ws.RootPath, "init")
	runGit(t, ws.RootPath, "config", "user.email", "test@example.com")
	runGit(t, ws.RootPath, "config", "user.name", "Test")
	writeFile(t, filepath.Join(ws.RootPath, "tracked.txt"), "before\n")
	runGit(t, ws.RootPath, "add", "tracked.txt")
	runGit(t, ws.RootPath, "commit", "-m", "initial")
	writeFile(t, filepath.Join(ws.RootPath, "tracked.txt"), "after\n")
	writeFile(t, filepath.Join(ws.RootPath, "untracked.txt"), "new\n")
	resp, raw = d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID+"/diff", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("git diff = %d; body=%s", resp.StatusCode, raw)
	}
	var diff struct {
		Patch string   `json:"patch"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &diff); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
	if !strings.Contains(diff.Patch, "tracked.txt") || !strings.Contains(diff.Patch, "-before") || !strings.Contains(diff.Patch, "+after") {
		t.Fatalf("unexpected patch: %s", diff.Patch)
	}
	joined := strings.Join(diff.Files, ",")
	if !strings.Contains(joined, "untracked.txt") {
		t.Fatalf("untracked file missing from diff files: %v", diff.Files)
	}
}
