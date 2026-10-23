package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/core"
	"github.com/Mrjwj34/nullkey/internal/domain"
)

type workspacePage struct {
	Items      []domain.Workspace `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

type statePage struct {
	Items      []domain.State `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type filePage struct {
	Items []domain.FileEntry `json:"items"`
}

type fileBody struct {
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	Truncated bool   `json:"truncated"`
	Mime      string `json:"mime"`
}

func TestAPIWorkspaceCRUDAndPagination(t *testing.T) {
	api := newTestAPI(t)

	ws := api.createWorkspace(t, map[string]any{"name": "alpha"})
	if ws.StorageKind != domain.StorageManaged {
		t.Fatalf("storage_kind = %q, want managed", ws.StorageKind)
	}
	wantRoot := filepath.Join(api.home, "workspaces", ws.ID)
	if ws.RootPath != wantRoot {
		t.Fatalf("root_path = %q, want %q", ws.RootPath, wantRoot)
	}
	if _, err := os.Stat(ws.RootPath); err != nil {
		t.Fatalf("managed root was not created: %v", err)
	}

	rec := api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID, "")
	wantStatus(t, rec, http.StatusOK)
	got := mustDecode[domain.Workspace](t, rec)
	if got.ID != ws.ID || got.Name != "alpha" {
		t.Fatalf("get workspace = %+v", got)
	}

	extDir := t.TempDir()
	ext := api.createWorkspace(t, map[string]any{
		"name":         "external",
		"root_path":    extDir,
		"default_mode": "ro",
	})
	if ext.StorageKind != domain.StorageExternal || ext.DefaultMode != "ro" {
		t.Fatalf("external workspace = %+v", ext)
	}
	resolved, err := filepath.EvalSymlinks(extDir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if ext.RootPath != resolved {
		t.Fatalf("external root = %q, want %q", ext.RootPath, resolved)
	}

	// Validation errors.
	cases := []struct {
		name string
		body map[string]any
		code ErrorCode
	}{
		{"empty name", map[string]any{"name": ""}, CodeInvalidRequest},
		{"long name", map[string]any{"name": strings.Repeat("n", 201)}, CodeInvalidRequest},
		{"bad mode", map[string]any{"name": "bad-mode", "default_mode": "rwx"}, CodeInvalidRequest},
		{"missing root", map[string]any{"name": "missing", "root_path": filepath.Join(t.TempDir(), "nope")}, CodeInvalidRequest},
		{"sensitive root", map[string]any{"name": "root", "root_path": "/"}, CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := api.postJSON("/api/v1/workspaces", tc.body)
			wantErrorCode(t, rec, http.StatusBadRequest, tc.code)
		})
	}

	// Duplicate external root.
	rec = api.postJSON("/api/v1/workspaces", map[string]any{"name": "dup", "root_path": extDir})
	wantErrorCode(t, rec, http.StatusConflict, CodeConflict)

	// A body that is not valid JSON is rejected before the service is called.
	rec = api.auth(http.MethodPost, "/api/v1/workspaces", `{"name":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	// Create a few more for pagination.
	for _, name := range []string{"beta", "gamma", "delta"} {
		api.createWorkspace(t, map[string]any{"name": name})
	}

	rec = api.auth(http.MethodGet, "/api/v1/workspaces?limit=1", "")
	wantStatus(t, rec, http.StatusOK)
	page := mustDecode[workspacePage](t, rec)
	if len(page.Items) != 1 || page.NextCursor == nil || *page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	rec = api.auth(http.MethodGet, "/api/v1/workspaces?limit=1&cursor="+*page.NextCursor, "")
	wantStatus(t, rec, http.StatusOK)
	next := mustDecode[workspacePage](t, rec)
	if len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("second page = %+v", next)
	}

	// Invalid limit is ignored (defaults to the store fallback).
	rec = api.auth(http.MethodGet, "/api/v1/workspaces?limit=notanumber", "")
	wantStatus(t, rec, http.StatusOK)
	if got := mustDecode[workspacePage](t, rec); len(got.Items) < 2 {
		t.Fatalf("default page = %+v", got)
	}

	// An invalid cursor is a store-level error -> 500 envelope.
	rec = api.auth(http.MethodGet, "/api/v1/workspaces?cursor=%40%40%40", "")
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)

	rec = api.auth(http.MethodGet, "/api/v1/workspaces/ws_missing", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}

func TestAPIWorkspaceDelete(t *testing.T) {
	api := newTestAPI(t)

	t.Run("default keeps files", func(t *testing.T) {
		ws := api.createWorkspace(t, map[string]any{"name": "keep"})
		if err := os.WriteFile(filepath.Join(ws.RootPath, "keep.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID, "")
		wantStatus(t, rec, http.StatusNoContent)
		if _, err := os.Stat(ws.RootPath); err != nil {
			t.Fatalf("files removed by default delete: %v", err)
		}
		rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID, "")
		wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	})

	t.Run("delete_files requires confirm_name", func(t *testing.T) {
		ws := api.createWorkspace(t, map[string]any{"name": "confirmed"})
		rec := api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID+"?delete_files=true&confirm_name=wrong", "")
		wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
		rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID, "")
		wantStatus(t, rec, http.StatusOK)

		rec = api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID+"?delete_files=true&confirm_name=confirmed", "")
		wantStatus(t, rec, http.StatusNoContent)
		if _, err := os.Stat(ws.RootPath); !os.IsNotExist(err) {
			t.Fatalf("managed root still exists: %v", err)
		}
	})

	t.Run("external never deletes files", func(t *testing.T) {
		extDir := t.TempDir()
		ws := api.createWorkspace(t, map[string]any{"name": "external", "root_path": extDir})
		rec := api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID+"?delete_files=true&confirm_name=external", "")
		wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
		rec = api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID, "")
		wantStatus(t, rec, http.StatusNoContent)
		if _, err := os.Stat(extDir); err != nil {
			t.Fatalf("external dir removed: %v", err)
		}
	})

	t.Run("active sandbox blocks delete", func(t *testing.T) {
		ws := api.createWorkspace(t, map[string]any{"name": "active"})
		api.insertSandbox(t, domain.Sandbox{
			ID:          "sbx_active",
			HarnessID:   "shell",
			WorkspaceID: ws.ID,
			Runtime:     domain.RuntimeNative,
			NetworkMode: domain.NetworkFiltered,
			Status:      domain.SandboxRunning,
			CreatedAt:   time.Now().UTC(),
		})
		rec := api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID, "")
		wantErrorCode(t, rec, http.StatusConflict, CodeStateLocked)
	})

	t.Run("invalid and missing", func(t *testing.T) {
		ws := api.createWorkspace(t, map[string]any{"name": "invalid-bool"})
		rec := api.auth(http.MethodDelete, "/api/v1/workspaces/"+ws.ID+"?delete_files=maybe", "")
		wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

		rec = api.auth(http.MethodDelete, "/api/v1/workspaces/ws_missing", "")
		wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	})
}

func TestAPIWorkspaceFilesAndDiff(t *testing.T) {
	api := newTestAPI(t)
	ws := api.createWorkspace(t, map[string]any{"name": "files"})
	root := ws.RootPath

	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "inner.txt"), []byte("inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := []byte{0xff, 0xfe, 0x00, 0x01}
	if err := os.WriteFile(filepath.Join(root, "data.bin"), binary, 0o600); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), (1<<20)+5)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/files", "")
	wantStatus(t, rec, http.StatusOK)
	page := mustDecode[filePage](t, rec)
	names := map[string]domain.FileEntry{}
	for _, entry := range page.Items {
		names[entry.Name] = entry
	}
	for _, want := range []string{"sub", "hello.txt", "data.bin", "big.txt"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing %q in listing: %+v", want, page.Items)
		}
	}
	if names["sub"].Type != "directory" || names["hello.txt"].Type != "file" {
		t.Fatalf("unexpected types: %+v", names)
	}
	if names["hello.txt"].Size != int64(len("hello world\n")) || names["hello.txt"].MTime.Location() != time.UTC {
		t.Fatalf("unexpected hello.txt entry: %+v", names["hello.txt"])
	}

	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/files?path=sub", "")
	wantStatus(t, rec, http.StatusOK)
	sub := mustDecode[filePage](t, rec)
	if len(sub.Items) != 1 || sub.Items[0].Name != "inner.txt" {
		t.Fatalf("sub listing = %+v", sub.Items)
	}

	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/files?path=..", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/ws_missing/files", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Single-file preview: text.
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file?path=hello.txt", "")
	wantStatus(t, rec, http.StatusOK)
	text := mustDecode[fileBody](t, rec)
	if text.Content != "hello world\n" || text.Encoding != "utf-8" || text.Truncated {
		t.Fatalf("text file = %+v", text)
	}
	if !strings.HasPrefix(text.Mime, "text/plain") {
		t.Fatalf("mime = %q", text.Mime)
	}

	// Single-file preview: binary is base64 encoded.
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file?path=data.bin", "")
	wantStatus(t, rec, http.StatusOK)
	bin := mustDecode[fileBody](t, rec)
	if bin.Encoding != "base64" || bin.Mime != "application/octet-stream" {
		t.Fatalf("binary file = %+v", bin)
	}
	decoded, err := base64.StdEncoding.DecodeString(bin.Content)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if !bytes.Equal(decoded, binary) {
		t.Fatalf("binary content = %v, want %v", decoded, binary)
	}

	// Truncation at 1 MiB.
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file?path=big.txt", "")
	wantStatus(t, rec, http.StatusOK)
	large := mustDecode[fileBody](t, rec)
	if !large.Truncated || len(large.Content) != 1<<20 {
		t.Fatalf("large file truncated=%v len=%d", large.Truncated, len(large.Content))
	}

	// Missing path, traversal, directory and missing file.
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file?path=../etc/passwd", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file?path=sub", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/file?path=missing.txt", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/ws_missing/file?path=hello.txt", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Diff falls back to a file listing for a non-git workspace.
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/diff", "")
	wantStatus(t, rec, http.StatusOK)
	diff := mustDecode[struct {
		Patch string   `json:"patch"`
		Files []string `json:"files"`
	}](t, rec)
	if diff.Patch != "" || diff.Files == nil {
		t.Fatalf("diff = %+v", diff)
	}
	if !containsString(diff.Files, "hello.txt") || !containsString(diff.Files, "sub/inner.txt") {
		t.Fatalf("diff files = %v", diff.Files)
	}
	rec = api.auth(http.MethodGet, "/api/v1/workspaces/ws_missing/diff", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAPIStates(t *testing.T) {
	api := newTestAPI(t)

	one := api.createState(t, "codex", "one")
	if one.HarnessID != "codex" || one.Name != "one" {
		t.Fatalf("state = %+v", one)
	}
	if !strings.HasPrefix(one.Path, filepath.Join(api.home, "states", "codex")+string(filepath.Separator)) {
		t.Fatalf("state path = %q", one.Path)
	}
	two := api.createState(t, "shell", "")
	if two.Name != "default" {
		t.Fatalf("default state name = %q", two.Name)
	}

	rec := api.postJSON("/api/v1/states", map[string]any{"name": "no-harness"})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodPost, "/api/v1/states", `{"harness_id":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)

	rec = api.auth(http.MethodGet, "/api/v1/states", "")
	wantStatus(t, rec, http.StatusOK)
	page := mustDecode[statePage](t, rec)
	if len(page.Items) < 2 {
		t.Fatalf("states = %+v", page.Items)
	}

	rec = api.auth(http.MethodGet, "/api/v1/states?harness_id=codex", "")
	wantStatus(t, rec, http.StatusOK)
	filtered := mustDecode[statePage](t, rec)
	for _, st := range filtered.Items {
		if st.HarnessID != "codex" {
			t.Fatalf("filtered state has harness %q", st.HarnessID)
		}
	}

	rec = api.auth(http.MethodGet, "/api/v1/states?limit=1", "")
	wantStatus(t, rec, http.StatusOK)
	first := mustDecode[statePage](t, rec)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first state page = %+v", first)
	}
	rec = api.auth(http.MethodGet, "/api/v1/states?limit=1&cursor="+*first.NextCursor, "")
	wantStatus(t, rec, http.StatusOK)
	second := mustDecode[statePage](t, rec)
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second state page = %+v", second)
	}

	rec = api.auth(http.MethodGet, "/api/v1/states?cursor=%40%40%40", "")
	wantErrorCode(t, rec, http.StatusInternalServerError, CodeInternal)

	// Reset requires confirm and clears contents.
	if err := os.WriteFile(filepath.Join(one.Path, "scratch.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = api.postJSON("/api/v1/states/"+one.ID+"/reset", map[string]any{"confirm": false})
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodPost, "/api/v1/states/"+one.ID+"/reset", `{"confirm":`)
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.postJSON("/api/v1/states/"+one.ID+"/reset", map[string]any{"confirm": true})
	wantStatus(t, rec, http.StatusOK)
	if _, err := os.Stat(filepath.Join(one.Path, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("reset left scratch.txt: %v", err)
	}
	rec = api.postJSON("/api/v1/states/st_missing/reset", map[string]any{"confirm": true})
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Delete requires confirm=true.
	rec = api.auth(http.MethodDelete, "/api/v1/states/"+two.ID, "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodDelete, "/api/v1/states/"+two.ID+"?confirm=maybe", "")
	wantErrorCode(t, rec, http.StatusBadRequest, CodeInvalidRequest)
	rec = api.auth(http.MethodDelete, "/api/v1/states/"+two.ID+"?confirm=true", "")
	wantStatus(t, rec, http.StatusNoContent)
	rec = api.auth(http.MethodDelete, "/api/v1/states/"+two.ID+"?confirm=true", "")
	wantErrorCode(t, rec, http.StatusNotFound, CodeNotFound)

	// Active sandbox locks state deletion and reset.
	locked := api.createState(t, "shell", "locked")
	api.insertSandbox(t, domain.Sandbox{
		ID:          "sbx_state_lock",
		HarnessID:   "shell",
		WorkspaceID: "ws_unrelated",
		StateID:     locked.ID,
		Runtime:     domain.RuntimeNative,
		NetworkMode: domain.NetworkFiltered,
		Status:      domain.SandboxRunning,
		CreatedAt:   time.Now().UTC(),
	})
	rec = api.auth(http.MethodDelete, "/api/v1/states/"+locked.ID+"?confirm=true", "")
	wantErrorCode(t, rec, http.StatusConflict, CodeStateLocked)
	rec = api.postJSON("/api/v1/states/"+locked.ID+"/reset", map[string]any{"confirm": true})
	wantErrorCode(t, rec, http.StatusConflict, CodeStateLocked)
}

// nilFilesArtifacts exercises the handler branch where Diff returns no files.
type nilFilesArtifacts struct{}

func (nilFilesArtifacts) List(string, string) ([]domain.FileEntry, error) {
	return []domain.FileEntry{}, nil
}

func (nilFilesArtifacts) Read(string, string) ([]byte, string, bool, error) {
	return []byte{}, "text/plain; charset=utf-8", false, nil
}

func (nilFilesArtifacts) Diff(context.Context, string) (string, []string, error) {
	return "", nil, nil
}

func TestAPIWorkspaceDiffNilFiles(t *testing.T) {
	api := newTestAPIWith(t, func(opts *core.Options) {
		opts.Artifacts = nilFilesArtifacts{}
	})
	ws := api.createWorkspace(t, map[string]any{"name": "nil-files"})
	rec := api.auth(http.MethodGet, "/api/v1/workspaces/"+ws.ID+"/diff", "")
	wantStatus(t, rec, http.StatusOK)
	diff := mustDecode[struct {
		Patch string   `json:"patch"`
		Files []string `json:"files"`
	}](t, rec)
	if diff.Files == nil || len(diff.Files) != 0 {
		t.Fatalf("files = %v, want empty non-nil slice", diff.Files)
	}
}
