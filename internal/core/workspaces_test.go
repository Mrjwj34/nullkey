package core

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// fakeArtifactReader records calls and returns configured results.
type fakeArtifactReader struct {
	listCalls int
	readCalls int
	diffCalls int
	listRoot  string
	listRel   string
	readRoot  string
	readRel   string
	diffRoot  string

	list []domain.FileEntry
	read []byte
	diff string
	err  error
}

func (f *fakeArtifactReader) List(root, rel string) ([]domain.FileEntry, error) {
	f.listCalls++
	f.listRoot, f.listRel = root, rel
	if f.err != nil {
		return nil, f.err
	}
	return f.list, nil
}

func (f *fakeArtifactReader) Read(root, rel string) ([]byte, string, bool, error) {
	f.readCalls++
	f.readRoot, f.readRel = root, rel
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.read, "text/plain", false, nil
}

func (f *fakeArtifactReader) Diff(_ context.Context, root string) (string, []string, error) {
	f.diffCalls++
	f.diffRoot = root
	if f.err != nil {
		return "", nil, f.err
	}
	return f.diff, []string{"a.txt"}, nil
}

func insertState(t *testing.T, s *Service, harness, path string) domain.State {
	t.Helper()
	st := domain.State{
		ID:        newID("st_"),
		HarnessID: harness,
		Name:      "manual",
		Path:      path,
		CreatedAt: s.now(),
	}
	if err := s.Store.CreateState(context.Background(), st); err != nil {
		t.Fatalf("insert state: %v", err)
	}
	return st
}

// ----------------------------------------------------------- workspaces

func TestCreateWorkspaceValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateWorkspace(ctx, "", "", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.CreateWorkspace(ctx, strings.Repeat("a", 201), "", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.CreateWorkspace(ctx, "name", "", "rwx")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
}

func TestCreateWorkspaceManaged(t *testing.T) {
	svc, home := newTestService(t)
	ctx := context.Background()
	ws, err := svc.CreateWorkspace(ctx, "  my project  ", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ws.Name != "my project" || ws.DefaultMode != "rw" || ws.StorageKind != domain.StorageManaged {
		t.Fatalf("unexpected workspace: %+v", ws)
	}
	if want := filepath.Join(home, "workspaces", ws.ID); ws.RootPath != want {
		t.Fatalf("root = %q, want %q", ws.RootPath, want)
	}
	info, err := os.Stat(ws.RootPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("workspace dir missing: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace mode = %o", info.Mode().Perm())
	}

	ro, err := svc.CreateWorkspace(ctx, "read only", "", "ro")
	if err != nil || ro.DefaultMode != "ro" {
		t.Fatalf("ro workspace: %+v %v", ro, err)
	}

	evs, _, err := svc.Audit.List(context.Background(), 50, "", "workspace.created")
	if err != nil || len(evs) != 2 {
		t.Fatalf("audit events = %d, %v", len(evs), err)
	}
}

func TestCreateWorkspaceManagedDirFailure(t *testing.T) {
	svc, home := newTestService(t)
	if err := os.WriteFile(filepath.Join(home, "workspaces"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	_, err := svc.CreateWorkspace(context.Background(), "blocked", "", "")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestCreateWorkspaceExternal(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ws, err := svc.CreateWorkspace(ctx, "external", dir, "ro")
	if err != nil {
		t.Fatalf("create external: %v", err)
	}
	if ws.StorageKind != domain.StorageExternal {
		t.Fatalf("storage kind = %s", ws.StorageKind)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if ws.RootPath != want {
		t.Fatalf("root = %q, want %q", ws.RootPath, want)
	}

	// Symlinked roots canonicalize to the real directory.
	target2 := filepath.Join(t.TempDir(), "project2")
	if err := os.MkdirAll(target2, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target2, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	linked, err := svc.CreateWorkspace(ctx, "linked", link, "")
	if err != nil {
		t.Fatalf("create linked: %v", err)
	}
	if resolved2, err := filepath.EvalSymlinks(target2); err != nil || linked.RootPath != resolved2 {
		t.Fatalf("symlink not canonicalized: %q (%v)", linked.RootPath, err)
	}

	// The same real path conflicts on the UNIQUE root path.
	_, err = svc.CreateWorkspace(ctx, "duplicate", target2, "")
	wantError(t, err, http.StatusConflict, "conflict")

	for _, bad := range []string{
		filepath.Join(t.TempDir(), "missing"),
		"",
	} {
		if bad == "" {
			continue
		}
		_, err := svc.CreateWorkspace(ctx, "bad", bad, "")
		wantError(t, err, http.StatusBadRequest, "invalid_request")
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err = svc.CreateWorkspace(ctx, "not a dir", file, "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
}

func TestExternalPathSensitiveRejection(t *testing.T) {
	fakeHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval home: %v", err)
	}
	t.Setenv("HOME", fakeHome)
	svc, _ := newTestService(t)
	ctx := context.Background()

	for _, name := range []string{".ssh", ".aws", ".gnupg", "Library/Keychains", ".config/gh"} {
		p := filepath.Join(fakeHome, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		_, err := svc.CreateWorkspace(ctx, "sensitive", p, "")
		wantError(t, err, http.StatusBadRequest, "invalid_request")
	}

	// The home directory itself and the filesystem root are rejected.
	if _, err := svc.CreateWorkspace(ctx, "home", fakeHome, ""); err == nil {
		t.Fatalf("home accepted as workspace")
	}
	if _, err := svc.CreateWorkspace(ctx, "root", "/", ""); err == nil {
		t.Fatalf("filesystem root accepted as workspace")
	}

	// A normal directory below the fake home is accepted.
	okDir := filepath.Join(fakeHome, "src", "project")
	if err := os.MkdirAll(okDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := svc.CreateWorkspace(ctx, "ok", okDir, ""); err != nil {
		t.Fatalf("normal dir rejected: %v", err)
	}

	// The nullkey home is always protected. Note that rejectSensitivePath
	// compares against the *unresolved* home, so provide a canonical path.
	resolvedHome, err := filepath.EvalSymlinks(svc.Home)
	if err != nil {
		t.Fatalf("eval nullkey home: %v", err)
	}
	svc.Home = resolvedHome
	if _, err := svc.CreateWorkspace(ctx, "nhome", resolvedHome, ""); err == nil {
		t.Fatalf("nullkey home accepted")
	}

	// rejectSensitivePath direct checks, including the .ssh suffix rule.
	if err := rejectSensitivePath("/", ""); err == nil {
		t.Fatalf("root not rejected")
	}
	if err := rejectSensitivePath("/var/run/docker.sock", ""); err == nil {
		t.Fatalf("docker socket not rejected")
	}
	if err := rejectSensitivePath(filepath.Join(t.TempDir(), ".ssh"), ""); err == nil {
		t.Fatalf(".ssh suffix not rejected")
	}
	if err := rejectSensitivePath(t.TempDir(), ""); err != nil {
		t.Fatalf("normal path rejected: %v", err)
	}
}

func TestListGetWorkspace(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")
	if err := os.WriteFile(filepath.Join(ws.RootPath, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	items, next, err := svc.ListWorkspaces(ctx, 10, "")
	if err != nil || next != "" || len(items) != 1 {
		t.Fatalf("list = %d, %q, %v", len(items), next, err)
	}
	if items[0].SizeBytes != 5 {
		t.Fatalf("size = %d", items[0].SizeBytes)
	}

	got, err := svc.GetWorkspace(ctx, ws.ID)
	if err != nil || got.ID != ws.ID || got.SizeBytes != 5 {
		t.Fatalf("get = %+v, %v", got, err)
	}
	_, err = svc.GetWorkspace(ctx, "ws_missing")
	wantError(t, err, http.StatusNotFound, "not_found")

	// A bad cursor is a store-level error surfaced as internal.
	_, _, err = svc.ListWorkspaces(ctx, 10, "%%%")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestDeleteWorkspaceDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "keep", "", "")
	file := filepath.Join(ws.RootPath, "keep.txt")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := svc.DeleteWorkspace(ctx, "ws_missing", false, ""); err == nil {
		t.Fatalf("missing workspace deleted")
	} else {
		wantError(t, err, http.StatusNotFound, "not_found")
	}
	if err := svc.DeleteWorkspace(ctx, ws.ID, false, ""); err != nil {
		t.Fatalf("delete record: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("default delete removed files: %v", err)
	}
	if _, err := svc.GetWorkspace(ctx, ws.ID); err == nil {
		t.Fatalf("record still present")
	}
}

func TestDeleteWorkspaceDeleteFiles(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "bye", "", "")

	if err := svc.DeleteWorkspace(ctx, ws.ID, true, "wrong"); err == nil {
		t.Fatalf("wrong confirm accepted")
	} else {
		wantError(t, err, http.StatusBadRequest, "invalid_request")
	}
	if _, err := os.Stat(ws.RootPath); err != nil {
		t.Fatalf("files removed despite wrong confirm: %v", err)
	}
	if err := svc.DeleteWorkspace(ctx, ws.ID, true, ws.Name); err != nil {
		t.Fatalf("delete files: %v", err)
	}
	if _, err := os.Stat(ws.RootPath); !os.IsNotExist(err) {
		t.Fatalf("files not removed: %v", err)
	}
}

func TestDeleteWorkspaceExternalNeverDeletesFiles(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	dir := t.TempDir()
	ws, err := svc.CreateWorkspace(ctx, "ext", dir, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = svc.DeleteWorkspace(ctx, ws.ID, true, ws.Name)
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("external dir removed: %v", statErr)
	}
	if err := svc.DeleteWorkspace(ctx, ws.ID, false, ""); err != nil {
		t.Fatalf("plain delete: %v", err)
	}
}

func TestDeleteWorkspaceActiveAndOutsideRoot(t *testing.T) {
	svc, home := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "active", "", "")
	sb := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "shell", WorkspaceID: ws.ID, Status: domain.SandboxRunning,
	})
	if err := svc.DeleteWorkspace(ctx, ws.ID, false, ""); err == nil {
		t.Fatalf("active workspace deleted")
	} else {
		wantError(t, err, http.StatusConflict, "state_locked")
	}
	if err := svc.Store.SetSandboxStatus(ctx, sb.ID, domain.SandboxStopped, nil); err != nil {
		t.Fatalf("stop sandbox row: %v", err)
	}

	// A record whose root path is outside <home>/workspaces must never be
	// removed even with a matching confirmation.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bogus := domain.Workspace{
		ID: newID("ws_"), Name: "bogus", RootPath: outside,
		StorageKind: domain.StorageManaged, DefaultMode: "rw", CreatedAt: svc.now(),
	}
	if err := svc.Store.CreateWorkspace(ctx, bogus); err != nil {
		t.Fatalf("insert bogus: %v", err)
	}
	err := svc.DeleteWorkspace(ctx, bogus.ID, true, bogus.Name)
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("outside dir removed: %v", statErr)
	}
	_ = home
}

func TestWorkspaceArtifactsUnavailable(t *testing.T) {
	svc, _ := newTestService(t, func(o *Options) { o.Artifacts = nil })
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"list", func() error { _, err := svc.WorkspaceFiles(ctx, ws.ID, ""); return err }},
		{"read", func() error { _, _, _, err := svc.WorkspaceFile(ctx, ws.ID, "x"); return err }},
		{"diff", func() error { _, _, err := svc.WorkspaceDiff(ctx, ws.ID); return err }},
	} {
		err := call.fn()
		wantError(t, err, http.StatusNotImplemented, "not_implemented")
	}
}

func TestWorkspaceArtifactsReal(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")
	if err := os.MkdirAll(filepath.Join(ws.RootPath, "sub"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.RootPath, "sub", "hello.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	entries, err := svc.WorkspaceFiles(ctx, ws.ID, "")
	if err != nil || len(entries) != 1 || entries[0].Name != "sub" {
		t.Fatalf("files = %+v, %v", entries, err)
	}
	content, mime, truncated, err := svc.WorkspaceFile(ctx, ws.ID, "sub/hello.txt")
	if err != nil || string(content) != "hello" || !strings.Contains(mime, "text/plain") || truncated {
		t.Fatalf("file = %q %q %v %v", content, mime, truncated, err)
	}
	patch, files, err := svc.WorkspaceDiff(ctx, ws.ID)
	if err != nil || patch != "" || len(files) != 1 || files[0] != "sub/hello.txt" {
		t.Fatalf("diff = %q %v %v", patch, files, err)
	}

	_, _, _, err = svc.WorkspaceFile(ctx, ws.ID, "missing.txt")
	wantError(t, err, http.StatusNotFound, "not_found")

	_, _, _, err = svc.WorkspaceFile(ctx, ws.ID, "../../etc/passwd")
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	_, err = svc.WorkspaceFiles(ctx, "ws_missing", "")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestWorkspaceArtifactsFakeReader(t *testing.T) {
	fake := &fakeArtifactReader{
		list: []domain.FileEntry{{Name: "x"}},
		read: []byte("data"),
		diff: "patch",
	}
	svc, _ := newTestService(t, func(o *Options) { o.Artifacts = fake })
	ctx := context.Background()
	ws := mustWorkspace(t, svc, "ws", "", "")

	items, err := svc.WorkspaceFiles(ctx, ws.ID, "rel/dir")
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %+v, %v", items, err)
	}
	content, _, _, err := svc.WorkspaceFile(ctx, ws.ID, "file.txt")
	if err != nil || string(content) != "data" {
		t.Fatalf("read = %q, %v", content, err)
	}
	patch, files, err := svc.WorkspaceDiff(ctx, ws.ID)
	if err != nil || patch != "patch" || len(files) != 1 {
		t.Fatalf("diff = %q %v %v", patch, files, err)
	}
	if fake.listCalls != 1 || fake.readCalls != 1 || fake.diffCalls != 1 {
		t.Fatalf("calls = %d/%d/%d", fake.listCalls, fake.readCalls, fake.diffCalls)
	}
	if fake.listRoot != ws.RootPath || fake.listRel != "rel/dir" || fake.readRel != "file.txt" || fake.diffRoot != ws.RootPath {
		t.Fatalf("arguments not forwarded: %+v", fake)
	}

	fake.err = errors.New("artifact exploded")
	_, err = svc.WorkspaceFiles(ctx, ws.ID, "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, _, _, err = svc.WorkspaceFile(ctx, ws.ID, "x")
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	_, _, err = svc.WorkspaceDiff(ctx, ws.ID)
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	// os.ErrNotExist maps to 404 even through the fake.
	fake.err = os.ErrNotExist
	_, _, _, err = svc.WorkspaceFile(ctx, ws.ID, "x")
	wantError(t, err, http.StatusNotFound, "not_found")
}

// --------------------------------------------------------------- states

func TestCreateState(t *testing.T) {
	svc, home := newTestService(t)
	ctx := context.Background()
	_, err := svc.CreateState(ctx, "  ", "")
	wantError(t, err, http.StatusBadRequest, "invalid_request")

	st, err := svc.CreateState(ctx, "codex", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if st.Name != "default" || st.HarnessID != "codex" {
		t.Fatalf("state = %+v", st)
	}
	if want := filepath.Join(home, "states", "codex", st.ID); st.Path != want {
		t.Fatalf("path = %q, want %q", st.Path, want)
	}
	info, err := os.Stat(st.Path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir: %v %v", info, err)
	}

	weird, err := svc.CreateState(ctx, "../evil\\name", "named")
	if err != nil {
		t.Fatalf("weird create: %v", err)
	}
	if filepath.Dir(filepath.Dir(weird.Path)) != filepath.Join(home, "states") {
		t.Fatalf("harness segment not sanitized: %q", weird.Path)
	}
	if filepath.Base(filepath.Dir(weird.Path)) == ".." {
		t.Fatalf("harness segment escaped the states root: %q", weird.Path)
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "state.created")
	if err != nil || len(evs) != 2 {
		t.Fatalf("audit events = %d, %v", len(evs), err)
	}
}

func TestCreateStateStoreFailure(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := svc.CreateState(context.Background(), "codex", "default")
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestListGetStates(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	codex := mustState(t, svc, "codex", "one")
	mustState(t, svc, "claude", "two")
	if err := os.WriteFile(filepath.Join(codex.Path, "f"), []byte("123"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	items, next, err := svc.ListStates(ctx, "codex", 10, "")
	if err != nil || next != "" || len(items) != 1 || items[0].ID != codex.ID {
		t.Fatalf("list = %+v, %q, %v", items, next, err)
	}
	if items[0].SizeBytes != 3 {
		t.Fatalf("size = %d", items[0].SizeBytes)
	}
	all, _, err := svc.ListStates(ctx, "", 10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all states = %d, %v", len(all), err)
	}
	if _, _, err := svc.ListStates(ctx, "", 10, "%%%"); err == nil {
		t.Fatalf("bad cursor accepted")
	}

	got, err := svc.GetState(ctx, codex.ID)
	if err != nil || got.ID != codex.ID {
		t.Fatalf("get = %+v, %v", got, err)
	}
	_, err = svc.GetState(ctx, "st_missing")
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestDeleteState(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st := mustState(t, svc, "codex", "default")

	err := svc.DeleteState(ctx, st.ID, false)
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	err = svc.DeleteState(ctx, "st_missing", true)
	wantError(t, err, http.StatusNotFound, "not_found")

	sb, _, _ := runningSandbox(t, svc, "codex")
	active := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "codex", WorkspaceID: sb.WorkspaceID, StateID: st.ID,
		Status: domain.SandboxRunning,
	})
	err = svc.DeleteState(ctx, st.ID, true)
	wantError(t, err, http.StatusConflict, "state_locked")

	if err := svc.Store.SetSandboxStatus(ctx, active.ID, domain.SandboxStopped, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := svc.DeleteState(ctx, st.ID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(st.Path); !os.IsNotExist(err) {
		t.Fatalf("state dir not removed: %v", err)
	}
	_, err = svc.GetState(ctx, st.ID)
	wantError(t, err, http.StatusNotFound, "not_found")
}

func TestResetState(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	st := mustState(t, svc, "codex", "default")
	file := filepath.Join(st.Path, "session.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := svc.ResetState(ctx, st.ID, false)
	wantError(t, err, http.StatusBadRequest, "invalid_request")
	err = svc.ResetState(ctx, "st_missing", true)
	wantError(t, err, http.StatusNotFound, "not_found")

	active := insertSandbox(t, svc, domain.Sandbox{
		HarnessID: "codex", StateID: st.ID, Status: domain.SandboxRunning,
	})
	err = svc.ResetState(ctx, st.ID, true)
	wantError(t, err, http.StatusConflict, "state_locked")
	if err := svc.Store.SetSandboxStatus(ctx, active.ID, domain.SandboxStopped, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if err := svc.ResetState(ctx, st.ID, true); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("contents not removed: %v", err)
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Fatalf("state dir removed: %v", err)
	}
	if got, err := svc.GetState(ctx, st.ID); err != nil || got.ID != st.ID {
		t.Fatalf("record removed: %+v %v", got, err)
	}

	// A state path that no longer exists is recreated by reset.
	if err := os.RemoveAll(st.Path); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if err := svc.ResetState(ctx, st.ID, true); err != nil {
		t.Fatalf("reset missing dir: %v", err)
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Fatalf("state dir not recreated: %v", err)
	}

	// A path that is a regular file makes removeContents fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	bad := insertState(t, svc, "codex", blocker)
	err = svc.ResetState(ctx, bad.ID, true)
	wantError(t, err, http.StatusInternalServerError, "internal")
}

func TestRemoveContentsAndSanitizeSegment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := removeContents(root); err != nil {
		t.Fatalf("create missing: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "x"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := removeContents(root); err != nil {
		t.Fatalf("remove contents: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %d, %v", len(entries), err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := removeContents(file); err == nil {
		t.Fatalf("expected error for file root")
	}

	cases := map[string]string{
		"":          "default",
		"  ":        "default",
		".":         "default",
		"..":        "default",
		"a/b":       "a_b",
		`a\b`:       "a_b",
		" codex ":   "codex",
		"claude-x":  "claude-x",
		"a//b/../c": "a__b_.._c",
	}
	for in, want := range cases {
		if got := sanitizeSegment(in); got != want {
			t.Fatalf("sanitizeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpandUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := expandUser("~"); err != nil || got != home {
		t.Fatalf("expand ~ = %q, %v", got, err)
	}
	if got, err := expandUser("~/project"); err != nil || got != filepath.Join(home, "project") {
		t.Fatalf("expand ~/project = %q, %v", got, err)
	}
	if got, err := expandUser(" relative/path "); err != nil || got != "relative/path" {
		t.Fatalf("expand relative = %q, %v", got, err)
	}
	t.Setenv("HOME", "")
	if _, err := expandUser("~"); err == nil {
		t.Fatalf("expected error when HOME is unset")
	}
}
