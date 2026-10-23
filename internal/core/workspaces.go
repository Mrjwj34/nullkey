package core

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// CreateWorkspace creates a managed or external workspace.
func (s *Service) CreateWorkspace(ctx context.Context, name, rootPath, defaultMode string) (domain.Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Workspace{}, Invalid("name is required")
	}
	if len(name) > 200 {
		return domain.Workspace{}, Invalid("name is too long")
	}
	id := newID("ws_")
	if defaultMode == "" {
		defaultMode = "rw"
	}
	if defaultMode != "rw" && defaultMode != "ro" {
		return domain.Workspace{}, Invalid("default_mode must be rw or ro")
	}
	w := domain.Workspace{
		ID:          id,
		Name:        name,
		DefaultMode: defaultMode,
		CreatedAt:   s.now(),
	}
	var err error
	if strings.TrimSpace(rootPath) == "" {
		w.StorageKind = domain.StorageManaged
		w.RootPath = filepath.Join(s.Home, "workspaces", id)
		if err := os.MkdirAll(w.RootPath, 0o700); err != nil {
			return domain.Workspace{}, Wrap(http.StatusInternalServerError, "internal", "could not create workspace directory", err)
		}
	} else {
		w.StorageKind = domain.StorageExternal
		w.RootPath, err = s.validateExternalPath(rootPath)
		if err != nil {
			return domain.Workspace{}, err
		}
	}
	if err := s.Store.CreateWorkspace(ctx, w); err != nil {
		if w.StorageKind == domain.StorageManaged {
			_ = os.RemoveAll(w.RootPath)
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			return domain.Workspace{}, E(http.StatusConflict, "conflict", "a workspace with this root path already exists")
		}
		return domain.Workspace{}, Wrap(http.StatusInternalServerError, "internal", "could not create workspace", err)
	}
	s.Audit.Record(ctx, "workspace.created", nil, map[string]any{"workspace_id": w.ID, "storage_kind": string(w.StorageKind)})
	s.PublishEvent("workspace.created", map[string]any{"workspace_id": w.ID, "status": "created"})
	return s.Store.GetWorkspace(ctx, w.ID)
}

func (s *Service) validateExternalPath(rootPath string) (string, error) {
	expanded, err := expandUser(rootPath)
	if err != nil {
		return "", Invalid("root_path is invalid")
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", Invalid("root_path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", Invalid("root_path must be an existing directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", Invalid("root_path must be an existing directory")
	}
	if err := rejectSensitivePath(resolved, s.Home); err != nil {
		return "", err
	}
	return resolved, nil
}

func expandUser(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// rejectSensitivePath blocks workspaces that would expose host credentials or
// system state. The check runs after canonicalization.
func rejectSensitivePath(path, nullkeyHome string) error {
	rawClean := filepath.Clean(path)
	clean := canonicalPath(path)
	if clean == string(filepath.Separator) {
		return Invalid("root_path must not be the filesystem root")
	}
	hostHome, _ := os.UserHomeDir()
	hostHome = canonicalPath(hostHome)
	nullkeyHome = canonicalPath(nullkeyHome)
	forbidden := []string{
		"/System", "/dev", "/private/var/root", "/etc", "/private/etc", "/Volumes",
	}
	if hostHome != "" {
		forbidden = append(forbidden,
			filepath.Join(hostHome, ".ssh"),
			filepath.Join(hostHome, ".aws"),
			filepath.Join(hostHome, ".gnupg"),
			filepath.Join(hostHome, ".config", "gh"),
			filepath.Join(hostHome, "Library", "Keychains"),
		)
		if clean == hostHome {
			return Invalid("root_path must not be the user's home directory")
		}
	}
	if nullkeyHome != "" {
		forbidden = append(forbidden, nullkeyHome)
	}
	for _, base := range forbidden {
		if base == "" {
			continue
		}
		base = canonicalPath(base)
		if clean == base || strings.HasPrefix(clean, base+string(filepath.Separator)) {
			return Invalid("root_path points to a protected location")
		}
	}
	if clean == "/var/run/docker.sock" || clean == "/run/docker.sock" ||
		rawClean == "/var/run/docker.sock" || rawClean == "/run/docker.sock" ||
		strings.HasSuffix(clean, "/.ssh") || strings.HasSuffix(rawClean, "/.ssh") {
		return Invalid("root_path points to a protected location")
	}
	return nil
}

// canonicalPath resolves symlinks when possible and otherwise cleans the path.
// Protected-path checks must compare the same canonical form used by the
// workspace record.
func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// ListWorkspaces returns a page.
func (s *Service) ListWorkspaces(ctx context.Context, limit int, cursor string) ([]domain.Workspace, string, error) {
	items, next, err := s.Store.ListWorkspaces(ctx, limit, cursor)
	if err != nil {
		return nil, "", Wrap(http.StatusInternalServerError, "internal", "could not list workspaces", err)
	}
	for i := range items {
		items[i].SizeBytes = dirSize(items[i].RootPath)
	}
	return items, next, nil
}

// GetWorkspace returns one workspace.
func (s *Service) GetWorkspace(ctx context.Context, id string) (domain.Workspace, error) {
	w, err := s.Store.GetWorkspace(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return domain.Workspace{}, NotFound("workspace not found")
		}
		return domain.Workspace{}, Wrap(http.StatusInternalServerError, "internal", "could not read workspace", err)
	}
	w.SizeBytes = dirSize(w.RootPath)
	return w, nil
}

// DeleteWorkspace removes a workspace record and, for managed workspaces with
// explicit confirmation, its files. External files are never deleted.
func (s *Service) DeleteWorkspace(ctx context.Context, id string, deleteFiles bool, confirmName string) error {
	w, err := s.Store.GetWorkspace(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return NotFound("workspace not found")
		}
		return Wrap(http.StatusInternalServerError, "internal", "could not read workspace", err)
	}
	if active, err := s.Store.ActiveSandboxForWorkspace(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not check sandbox references", err)
	} else if active != "" {
		return Conflict("state_locked", "workspace is in use by an active sandbox")
	}
	if deleteFiles {
		if w.StorageKind != domain.StorageManaged {
			return Invalid("delete_files is only allowed for managed workspaces")
		}
		if confirmName != w.Name {
			return Invalid("confirm_name must match the workspace name")
		}
		managedRoot := filepath.Join(s.Home, "workspaces")
		clean := filepath.Clean(w.RootPath)
		if !strings.HasPrefix(clean, managedRoot+string(filepath.Separator)) {
			return Invalid("refusing to delete a path outside the managed workspace root")
		}
		if err := os.RemoveAll(clean); err != nil {
			return Wrap(http.StatusInternalServerError, "internal", "could not delete workspace files", err)
		}
	}
	if err := s.Store.DeleteWorkspace(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not delete workspace", err)
	}
	s.Audit.Record(ctx, "workspace.deleted", nil, map[string]any{"workspace_id": id, "delete_files": deleteFiles})
	s.PublishEvent("workspace.deleted", map[string]any{"workspace_id": id, "status": "deleted"})
	return nil
}

// WorkspaceFiles lists workspace artifacts.
func (s *Service) WorkspaceFiles(ctx context.Context, id, rel string) ([]domain.FileEntry, error) {
	w, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Artifacts == nil {
		return nil, E(http.StatusNotImplemented, "not_implemented", "artifact reader is not available")
	}
	items, err := s.Artifacts.List(w.RootPath, rel)
	if err != nil {
		return nil, artifactError(err)
	}
	return items, nil
}

// WorkspaceFile reads one workspace file.
func (s *Service) WorkspaceFile(ctx context.Context, id, rel string) ([]byte, string, bool, error) {
	w, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, "", false, err
	}
	if s.Artifacts == nil {
		return nil, "", false, E(http.StatusNotImplemented, "not_implemented", "artifact reader is not available")
	}
	content, mime, truncated, err := s.Artifacts.Read(w.RootPath, rel)
	if err != nil {
		return nil, "", false, artifactError(err)
	}
	return content, mime, truncated, nil
}

// WorkspaceDiff returns a git patch or a file-level fallback.
func (s *Service) WorkspaceDiff(ctx context.Context, id string) (string, []string, error) {
	w, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if s.Artifacts == nil {
		return "", nil, E(http.StatusNotImplemented, "not_implemented", "artifact reader is not available")
	}
	patch, files, err := s.Artifacts.Diff(ctx, w.RootPath)
	if err != nil {
		return "", nil, artifactError(err)
	}
	return patch, files, nil
}

func artifactError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return NotFound("file not found")
	}
	return Invalid(err.Error())
}

// CreateState creates a persistent harness state.
func (s *Service) CreateState(ctx context.Context, harnessID, name string) (domain.State, error) {
	harnessID = strings.TrimSpace(harnessID)
	if harnessID == "" {
		return domain.State{}, Invalid("harness_id is required")
	}
	if name == "" {
		name = "default"
	}
	id := newID("st_")
	st := domain.State{
		ID:        id,
		HarnessID: harnessID,
		Name:      name,
		Path:      filepath.Join(s.Home, "states", sanitizeSegment(harnessID), id),
		CreatedAt: s.now(),
	}
	if err := os.MkdirAll(st.Path, 0o700); err != nil {
		return domain.State{}, Wrap(http.StatusInternalServerError, "internal", "could not create state directory", err)
	}
	if err := s.Store.CreateState(ctx, st); err != nil {
		_ = os.RemoveAll(st.Path)
		return domain.State{}, Wrap(http.StatusInternalServerError, "internal", "could not create state", err)
	}
	s.Audit.Record(ctx, "state.created", nil, map[string]any{"state_id": st.ID, "harness_id": harnessID})
	s.PublishEvent("state.created", map[string]any{"state_id": st.ID, "status": "created"})
	return s.Store.GetState(ctx, id)
}

func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s == "" || s == "." || s == ".." {
		return "default"
	}
	return s
}

// ListStates returns states, optionally by harness.
func (s *Service) ListStates(ctx context.Context, harnessID string, limit int, cursor string) ([]domain.State, string, error) {
	items, next, err := s.Store.ListStates(ctx, harnessID, limit, cursor)
	if err != nil {
		return nil, "", Wrap(http.StatusInternalServerError, "internal", "could not list states", err)
	}
	for i := range items {
		items[i].SizeBytes = dirSize(items[i].Path)
	}
	return items, next, nil
}

// GetState returns a state.
func (s *Service) GetState(ctx context.Context, id string) (domain.State, error) {
	st, err := s.Store.GetState(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return domain.State{}, NotFound("state not found")
		}
		return domain.State{}, Wrap(http.StatusInternalServerError, "internal", "could not read state", err)
	}
	return st, nil
}

// DeleteState removes the state directory and record. It requires an explicit
// confirmation and refuses while a sandbox references it.
func (s *Service) DeleteState(ctx context.Context, id string, confirm bool) error {
	if !confirm {
		return Invalid("confirm=true is required to delete a state")
	}
	st, err := s.GetState(ctx, id)
	if err != nil {
		return err
	}
	if active, err := s.Store.ActiveSandboxForState(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not check sandbox references", err)
	} else if active != "" {
		return Conflict("state_locked", "state is in use by an active sandbox")
	}
	if err := os.RemoveAll(st.Path); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not delete state directory", err)
	}
	if err := s.Store.DeleteState(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not delete state", err)
	}
	s.Audit.Record(ctx, "state.deleted", nil, map[string]any{"state_id": id, "harness_id": st.HarnessID})
	s.PublishEvent("state.deleted", map[string]any{"state_id": id, "status": "deleted"})
	return nil
}

// ResetState clears state contents but keeps the record and workspace.
func (s *Service) ResetState(ctx context.Context, id string, confirm bool) error {
	if !confirm {
		return Invalid("confirm=true is required to reset a state")
	}
	st, err := s.GetState(ctx, id)
	if err != nil {
		return err
	}
	if active, err := s.Store.ActiveSandboxForState(ctx, id); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not check sandbox references", err)
	} else if active != "" {
		return Conflict("state_locked", "state is in use by an active sandbox")
	}
	if err := removeContents(st.Path); err != nil {
		return Wrap(http.StatusInternalServerError, "internal", "could not reset state", err)
	}
	s.Audit.Record(ctx, "state.reset", nil, map[string]any{"state_id": id, "harness_id": st.HarnessID})
	s.PublishEvent("state.reset", map[string]any{"state_id": id, "status": "reset"})
	return nil
}

func removeContents(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(root, 0o700)
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isNotFound(err error) bool {
	return errors.Is(err, domain.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}
