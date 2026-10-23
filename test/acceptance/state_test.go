//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAC_STATE_001_WorkspaceValidation(t *testing.T) {
	d := startDaemon(t)

	managed := d.createWorkspace(t, "managed")
	if managed.StorageKind != "managed" {
		t.Fatalf("storage_kind = %q, want managed", managed.StorageKind)
	}
	if info, err := os.Stat(managed.RootPath); err != nil || !info.IsDir() {
		t.Fatalf("managed root missing: %v", err)
	}

	external := t.TempDir()
	resp, raw := d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{"name": "external", "root_path": external})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("external workspace status = %d; body=%s", resp.StatusCode, raw)
	}
	created := decodeJSON[workspaceResponse](t, raw)
	if created.StorageKind != "external" {
		t.Fatalf("storage_kind = %q, want external", created.StorageKind)
	}
	resolved, _ := filepath.EvalSymlinks(external)
	if created.RootPath != resolved {
		t.Fatalf("root_path = %q, want canonical %q", created.RootPath, resolved)
	}

	external2 := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(external2, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{"name": "linked", "root_path": link})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("symlinked workspace status = %d; body=%s", resp.StatusCode, raw)
	}
	linked := decodeJSON[workspaceResponse](t, raw)
	resolved2, _ := filepath.EvalSymlinks(external2)
	if linked.RootPath != resolved2 {
		t.Fatalf("symlink root = %q, want %q", linked.RootPath, resolved2)
	}

	hostHome, _ := os.UserHomeDir()
	blocked := []string{
		"/", "/etc", "/System", hostHome, filepath.Join(hostHome, ".ssh"),
		filepath.Join(hostHome, ".aws"), filepath.Join(hostHome, "Library", "Keychains"),
		"/var/run/docker.sock",
	}
	for _, path := range blocked {
		resp, raw := d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{"name": "blocked", "root_path": path})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("root_path %q status = %d, want 400; body=%s", path, resp.StatusCode, raw)
		}
		env := decodeJSON[apiError](t, raw)
		if env.Error.Code != "invalid_request" {
			t.Fatalf("root_path %q code = %q, want invalid_request", path, env.Error.Code)
		}
	}
}

func TestAC_STATE_002_WorkspaceDelete(t *testing.T) {
	d := startDaemon(t)
	managed := d.createWorkspace(t, "delete-me")
	writeFile(t, filepath.Join(managed.RootPath, "keep.txt"), "keep")

	resp, _ := d.doAuthed(t, http.MethodDelete, "/workspaces/"+managed.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("default delete status = %d, want 204", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(managed.RootPath, "keep.txt")); err != nil {
		t.Fatalf("default delete removed files: %v", err)
	}

	managed2 := d.createWorkspace(t, "delete-files")
	resp, raw := d.doAuthed(t, http.MethodDelete, "/workspaces/"+managed2.ID+"?delete_files=true&confirm_name=wrong", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong confirm_name status = %d; body=%s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(managed2.RootPath); err != nil {
		t.Fatalf("wrong confirm_name deleted files: %v", err)
	}
	resp, raw = d.doAuthed(t, http.MethodDelete, "/workspaces/"+managed2.ID+"?delete_files=true&confirm_name=delete-files", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("managed delete_files status = %d; body=%s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(managed2.RootPath); !os.IsNotExist(err) {
		t.Fatalf("managed directory still exists after delete_files: %v", err)
	}

	externalDir := t.TempDir()
	external := decodeJSON[workspaceResponse](t, func() []byte {
		resp, raw := d.doAuthed(t, http.MethodPost, "/workspaces", map[string]any{"name": "external", "root_path": externalDir})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create external: %d %s", resp.StatusCode, raw)
		}
		return raw
	}())
	resp, raw = d.doAuthed(t, http.MethodDelete, "/workspaces/"+external.ID+"?delete_files=true&confirm_name=external", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("external delete_files status = %d; body=%s", resp.StatusCode, raw)
	}
	resp, _ = d.doAuthed(t, http.MethodDelete, "/workspaces/"+external.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("external default delete status = %d", resp.StatusCode)
	}
	if _, err := os.Stat(externalDir); err != nil {
		t.Fatalf("external directory was deleted: %v", err)
	}

	locked := d.createWorkspace(t, "locked")
	sb := d.createSandbox(t, locked.ID, "shell", "")
	resp, raw = d.doAuthed(t, http.MethodDelete, "/workspaces/"+locked.ID, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("active workspace delete status = %d; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "state_locked" {
		t.Fatalf("code = %q, want state_locked", env.Error.Code)
	}
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop sandbox = %d", resp.StatusCode)
	}
	if resp, _ := d.doAuthed(t, http.MethodDelete, "/workspaces/"+locked.ID, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("workspace delete after stop = %d", resp.StatusCode)
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/workspaces/"+managed.ID+"/reset", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("workspace reset status = %d, want 404 (removed from contract); body=%s", resp.StatusCode, raw)
	}
}

func TestAC_STATE_003_StateResetDelete(t *testing.T) {
	d := startDaemon(t)
	ws := d.createWorkspace(t, "states")
	writeFile(t, filepath.Join(ws.RootPath, "workspace.txt"), "workspace")

	resp, raw := d.doAuthed(t, http.MethodPost, "/states", map[string]any{"harness_id": "shell", "name": "default"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create state = %d; body=%s", resp.StatusCode, raw)
	}
	st := decodeJSON[struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}](t, raw)
	writeFile(t, filepath.Join(st.Path, "history.txt"), "history")

	resp, raw = d.doAuthed(t, http.MethodPost, "/states/"+st.ID+"/reset", map[string]any{"confirm": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset state = %d; body=%s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(filepath.Join(st.Path, "history.txt")); !os.IsNotExist(err) {
		t.Fatalf("reset did not clear state contents: %v", err)
	}
	resp, raw = d.doAuthed(t, http.MethodGet, "/states", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), st.ID) {
		t.Fatalf("state record missing after reset: %d %s", resp.StatusCode, raw)
	}

	resp, raw = d.doAuthed(t, http.MethodDelete, "/states/"+st.ID, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete without confirm = %d, want 400; body=%s", resp.StatusCode, raw)
	}
	resp, raw = d.doAuthed(t, http.MethodDelete, "/states/"+st.ID+"?confirm=true", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete state = %d; body=%s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("state directory still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.RootPath, "workspace.txt")); err != nil {
		t.Fatalf("state operations removed workspace files: %v", err)
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/states", map[string]any{"harness_id": "shell", "name": "active"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create active state = %d; body=%s", resp.StatusCode, raw)
	}
	active := decodeJSON[struct {
		ID string `json:"id"`
	}](t, raw)
	sb := d.createSandboxWithState(t, ws.ID, active.ID)
	resp, raw = d.doAuthed(t, http.MethodDelete, "/states/"+active.ID+"?confirm=true", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete active state = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop sandbox = %d", resp.StatusCode)
	}
	resp, raw = d.doAuthed(t, http.MethodDelete, "/states/"+active.ID+"?confirm=true", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete state after stop = %d; body=%s", resp.StatusCode, raw)
	}
}

func TestAC_STATE_004_SandboxDelete(t *testing.T) {
	d := startDaemon(t)
	ws := d.createWorkspace(t, "sandbox-delete")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	stateID := sb.StateID

	for _, query := range []string{"?delete_state=false", "?delete_state=true", "?delete_workspace=false", "?delete_workspace=true"} {
		resp, raw := d.doAuthed(t, http.MethodDelete, "/sandboxes/"+sb.ID+query, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("delete %s status = %d, want 400; body=%s", query, resp.StatusCode, raw)
		}
	}
	resp, raw := d.doAuthed(t, http.MethodDelete, "/sandboxes/"+sb.ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete sandbox = %d; body=%s", resp.StatusCode, raw)
	}
	if resp, raw := d.doAuthed(t, http.MethodGet, "/workspaces/"+ws.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("workspace removed by sandbox delete: %d %s", resp.StatusCode, raw)
	}
	var states struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	resp, raw = d.doAuthed(t, http.MethodGet, "/states", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list states = %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &states); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	found := false
	for _, item := range states.Items {
		if item.ID == stateID {
			found = true
		}
	}
	if !found {
		t.Fatalf("state %s removed by sandbox delete", stateID)
	}
}
