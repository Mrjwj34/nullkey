package artifacts

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestListSortedDirectoriesFirst(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "zdir", "nested.txt"), []byte("nested"))
	writeTestFile(t, filepath.Join(root, "adir", "nested.txt"), []byte("nested"))
	writeTestFile(t, filepath.Join(root, "b.txt"), []byte("bb"))
	writeTestFile(t, filepath.Join(root, "a.txt"), []byte("a"))

	entries, err := New().List(root, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	want := []string{"adir", "zdir", "a.txt", "b.txt"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}

	if entries[0].Type != directoryType || entries[1].Type != directoryType {
		t.Fatalf("directories not first: %+v", entries)
	}
	if entries[2].Type != fileType || entries[3].Type != fileType {
		t.Fatalf("files not last: %+v", entries)
	}
	if entries[2].Size != 1 || entries[3].Size != 2 {
		t.Fatalf("unexpected sizes: %+v", entries)
	}
	for _, entry := range entries {
		if entry.MTime.Location() != time.UTC {
			t.Fatalf("entry %q mtime is not UTC: %v", entry.Name, entry.MTime.Location())
		}
	}

	// "." and "" both mean the workspace root for listing.
	dotEntries, err := New().List(root, ".")
	if err != nil {
		t.Fatalf("List(.): %v", err)
	}
	if !reflect.DeepEqual(dotEntries, entries) {
		t.Fatalf("List(.) = %+v, want %+v", dotEntries, entries)
	}

	sub, err := New().List(root, "zdir")
	if err != nil {
		t.Fatalf("List(zdir): %v", err)
	}
	if len(sub) != 1 || sub[0].Name != "nested.txt" || sub[0].Type != fileType {
		t.Fatalf("unexpected zdir listing: %+v", sub)
	}
}

func TestPathTraversalAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "file.txt"), []byte("data"))
	reader := New()

	bad := []string{
		"..",
		"../file.txt",
		"../outside",
		"dir/../../file.txt",
		"dir/../../../etc/passwd",
		"/etc/passwd",
		"/",
		"file.txt\x00hidden",
		".." + string(filepath.Separator) + "file.txt",
	}
	for _, rel := range bad {
		if _, err := reader.List(root, rel); err == nil {
			t.Errorf("List(%q) succeeded, want path error", rel)
		}
		if _, _, _, err := reader.Read(root, rel); err == nil {
			t.Errorf("Read(%q) succeeded, want path error", rel)
		}
	}

	if _, _, _, err := reader.Read(root, "file.txt"); err != nil {
		t.Fatalf("Read(file.txt): %v", err)
	}
	for _, rel := range []string{"", "."} {
		if _, _, _, err := reader.Read(root, rel); err == nil {
			t.Errorf("Read(%q) succeeded, want workspace-root error", rel)
		}
	}

	if _, err := reader.List(root, "missing"); err == nil {
		t.Error("List(missing) succeeded, want error")
	}
	if _, _, _, err := reader.Read(root, "missing"); err == nil {
		t.Error("Read(missing) succeeded, want error")
	}
	if _, err := reader.List(root, "file.txt"); err == nil {
		t.Error("List(file as dir) succeeded, want error")
	}
	if _, _, _, err := reader.Read(root, "file.txt"); err != nil {
		t.Fatalf("Read(file.txt): %v", err)
	}

	// Root validation.
	if _, err := reader.List("", ""); err == nil {
		t.Error("List with empty root succeeded")
	}
	if _, err := reader.List(root+"\x00x", ""); err == nil {
		t.Error("List with NUL root succeeded")
	}
	if _, err := reader.List(filepath.Join(root, "file.txt"), ""); err == nil {
		t.Error("List with file root succeeded")
	}
	if _, _, _, err := reader.Read(filepath.Join(root, "missing"), ""); err == nil {
		t.Error("Read with missing root succeeded")
	}
}

func TestSymlinkEscapeAndInside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "secret.txt"), []byte("TOPSECRET"))
	writeTestFile(t, filepath.Join(root, "real.txt"), []byte("inside"))

	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape-dir")); err != nil {
		t.Fatalf("symlink outside dir: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink inside file: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "sub", "inner.txt"), []byte("inner"))
	if err := os.Symlink("sub", filepath.Join(root, "linkdir")); err != nil {
		t.Fatalf("symlink inside dir: %v", err)
	}
	if err := os.Symlink("does-not-exist", filepath.Join(root, "broken")); err != nil {
		t.Fatalf("symlink broken: %v", err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}

	reader := New()
	if _, _, _, err := reader.Read(root, "escape.txt"); err == nil {
		t.Fatal("Read followed escaping symlink")
	}
	if _, err := reader.List(root, "escape-dir"); err == nil {
		t.Fatal("List followed escaping symlink")
	}

	content, mime, truncated, err := reader.Read(root, "link.txt")
	if err != nil {
		t.Fatalf("Read(link.txt): %v", err)
	}
	if string(content) != "inside" || mime != "text/plain; charset=utf-8" || truncated {
		t.Fatalf("Read(link.txt) = %q, %q, %v", content, mime, truncated)
	}

	content, _, _, err = reader.Read(root, "linkdir/inner.txt")
	if err != nil {
		t.Fatalf("Read(linkdir/inner.txt): %v", err)
	}
	if string(content) != "inner" {
		t.Fatalf("linkdir content = %q", content)
	}

	entries, err := reader.List(root, "")
	if err != nil {
		t.Fatalf("List(root): %v", err)
	}
	byName := map[string]struct {
		typ  string
		size int64
	}{}
	for _, entry := range entries {
		byName[entry.Name] = struct {
			typ  string
			size int64
		}{entry.Type, entry.Size}
	}

	if got := byName["escape.txt"].typ; got != fileType {
		t.Errorf("escape.txt type = %q, want %q", got, fileType)
	}
	if got := byName["escape-dir"].typ; got != fileType {
		t.Errorf("escape-dir type = %q, want %q", got, fileType)
	}
	if got := byName["broken"].typ; got != fileType {
		t.Errorf("broken type = %q, want %q", got, fileType)
	}
	if got := byName["loop"].typ; got != fileType {
		t.Errorf("loop type = %q, want %q", got, fileType)
	}
	if got := byName["linkdir"].typ; got != directoryType {
		t.Errorf("linkdir type = %q, want %q", got, directoryType)
	}
	if got := byName["link.txt"]; got.typ != fileType || got.size != int64(len("inside")) {
		t.Errorf("link.txt = %+v, want file size %d", got, len("inside"))
	}
}

func TestReadTruncationAndMIME(t *testing.T) {
	root := t.TempDir()
	reader := New()

	text := filepath.Join(root, "text.txt")
	writeTestFile(t, text, []byte("hello\n"))
	content, mime, truncated, err := reader.Read(root, "text.txt")
	if err != nil {
		t.Fatalf("Read(text): %v", err)
	}
	if string(content) != "hello\n" || mime != "text/plain; charset=utf-8" || truncated {
		t.Fatalf("Read(text) = %q, %q, %v", content, mime, truncated)
	}

	empty := filepath.Join(root, "empty.txt")
	writeTestFile(t, empty, nil)
	content, mime, truncated, err = reader.Read(root, "empty.txt")
	if err != nil {
		t.Fatalf("Read(empty): %v", err)
	}
	if len(content) != 0 || mime != "text/plain; charset=utf-8" || truncated {
		t.Fatalf("Read(empty) = %q, %q, %v", content, mime, truncated)
	}

	largeData := bytes.Repeat([]byte("x"), maxReadBytes+257)
	writeTestFile(t, filepath.Join(root, "large.txt"), largeData)
	content, mime, truncated, err = reader.Read(root, "large.txt")
	if err != nil {
		t.Fatalf("Read(large): %v", err)
	}
	if len(content) != maxReadBytes || !truncated {
		t.Fatalf("Read(large) len=%d truncated=%v, want %d, true", len(content), truncated, maxReadBytes)
	}
	if string(content) != strings.Repeat("x", maxReadBytes) {
		t.Fatal("Read(large) content is not the file prefix")
	}
	if mime != "text/plain; charset=utf-8" {
		t.Fatalf("Read(large) mime = %q", mime)
	}

	writeTestFile(t, filepath.Join(root, "exact.txt"), bytes.Repeat([]byte("y"), maxReadBytes))
	content, _, truncated, err = reader.Read(root, "exact.txt")
	if err != nil {
		t.Fatalf("Read(exact): %v", err)
	}
	if len(content) != maxReadBytes || truncated {
		t.Fatalf("Read(exact) len=%d truncated=%v, want %d, false", len(content), truncated, maxReadBytes)
	}

	writeTestFile(t, filepath.Join(root, "over.txt"), bytes.Repeat([]byte("z"), maxReadBytes+1))
	content, _, truncated, err = reader.Read(root, "over.txt")
	if err != nil {
		t.Fatalf("Read(over): %v", err)
	}
	if len(content) != maxReadBytes || !truncated {
		t.Fatalf("Read(over) len=%d truncated=%v, want %d, true", len(content), truncated, maxReadBytes)
	}

	binaryCases := map[string][]byte{
		"nul.bin":     {'a', 0x00, 'b'},
		"invalid.bin": {0xff, 0xfe, 0xfd},
		"control.bin": {'a', 0x01, 'b'},
		"delete.bin":  {'a', 0x7f, 'b'},
		"pdf.txt":     []byte("%PDF-1.7\n"),
	}
	for name, data := range binaryCases {
		writeTestFile(t, filepath.Join(root, name), data)
		_, mime, _, err := reader.Read(root, name)
		if err != nil {
			t.Fatalf("Read(%s): %v", name, err)
		}
		if mime != "application/octet-stream" {
			t.Errorf("Read(%s) mime = %q, want application/octet-stream", name, mime)
		}
	}

	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reader.Read(root, "subdir"); err == nil {
		t.Error("Read(directory) succeeded")
	}
}

func TestReadPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests are not meaningful as root")
	}

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "locked.txt"), []byte("secret"))
	if err := os.Chmod(filepath.Join(root, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "locked.txt"), 0o644)

	if _, _, _, err := New().Read(root, "locked.txt"); err == nil {
		t.Fatal("Read of unreadable file succeeded")
	}

	if err := os.Mkdir(filepath.Join(root, "locked-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "locked-dir"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "locked-dir"), 0o755)

	if _, err := New().List(root, "locked-dir"); err == nil {
		t.Fatal("List of unreadable directory succeeded")
	}
}

func TestNonGitDiff(t *testing.T) {
	newTree := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "b.txt"), []byte("b"))
		writeTestFile(t, filepath.Join(root, "a.txt"), []byte("a"))
		writeTestFile(t, filepath.Join(root, "sub", "c.txt"), []byte("c"))
		if err := os.Symlink("a.txt", filepath.Join(root, "link.txt")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		return root
	}

	want := []string{"a.txt", "b.txt", "link.txt", "sub/c.txt"}

	t.Run("missing git metadata", func(t *testing.T) {
		root := newTree(t)
		patch, files, err := New().Diff(context.Background(), root)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		if patch != "" {
			t.Fatalf("patch = %q, want empty", patch)
		}
		if !slices.Equal(files, want) {
			t.Fatalf("files = %v, want %v", files, want)
		}
	})

	t.Run("unusable git metadata", func(t *testing.T) {
		root := newTree(t)
		if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(root, ".git", "HEAD"), []byte("not a repo\n"))

		patch, files, err := New().Diff(context.Background(), root)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		if patch != "" {
			t.Fatalf("patch = %q, want empty", patch)
		}
		if !slices.Equal(files, want) {
			t.Fatalf("files = %v, want %v", files, want)
		}
	})

	// Direct helper coverage for git failure paths and context handling.
	if _, err := gitDiff(context.Background(), t.TempDir()); err == nil {
		t.Error("gitDiff outside a repository succeeded")
	}
	if _, err := gitChangedFiles(context.Background(), t.TempDir()); err == nil {
		t.Error("gitChangedFiles outside a repository succeeded")
	}
	if _, err := gitUntracked(context.Background(), t.TempDir()); err == nil {
		t.Error("gitUntracked outside a repository succeeded")
	}

	if _, err := walkFiles(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("walkFiles with missing root succeeded")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := walkFiles(cancelled, t.TempDir()); err == nil {
		t.Error("walkFiles with cancelled context succeeded")
	}
	if _, _, err := New().Diff(cancelled, t.TempDir()); err == nil {
		t.Error("Diff with cancelled context succeeded")
	}
	if _, _, err := New().Diff(nil, t.TempDir()); err == nil {
		t.Error("Diff with nil context succeeded")
	}
	if _, _, err := New().Diff(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("Diff with missing root succeeded")
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	writeTestFile(t, fileRoot, []byte("x"))
	if _, _, err := New().Diff(context.Background(), fileRoot); err == nil {
		t.Error("Diff with file root succeeded")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "artifacts@example.com")
	runGit(t, root, "config", "user.name", "Artifacts Test")
	runGit(t, root, "config", "commit.gpgsign", "false")
	runGit(t, root, "config", "core.quotePath", "true")
	return root
}

func TestDiffToleratesGitAuxiliaryFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git script requires a Unix shell")
	}

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), []byte("a"))
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  diff)\n" +
		"    case \"$*\" in\n" +
		"      *--name-only*) exit 1 ;;\n" +
		"      *) printf 'diff --git a/a.txt b/a.txt\\n'; exit 0 ;;\n" +
		"    esac ;;\n" +
		"  ls-files) exit 1 ;;\n" +
		"esac\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	patch, files, err := New().Diff(context.Background(), root)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if patch != "diff --git a/a.txt b/a.txt\n" {
		t.Fatalf("patch = %q", patch)
	}
	if files == nil || len(files) != 0 {
		t.Fatalf("files = %v, want empty non-nil slice", files)
	}
}

func TestGitRepoDiffAndUntracked(t *testing.T) {
	root := initGitRepo(t)

	writeTestFile(t, filepath.Join(root, "tracked.txt"), []byte("one\n"))
	writeTestFile(t, filepath.Join(root, "space name.txt"), []byte("one\n"))
	writeTestFile(t, filepath.Join(root, "gone.txt"), []byte("gone\n"))
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "initial")

	writeTestFile(t, filepath.Join(root, "tracked.txt"), []byte("two\n"))
	writeTestFile(t, filepath.Join(root, "space name.txt"), []byte("two\n"))
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "untracked.txt"), []byte("new\n"))
	writeTestFile(t, filepath.Join(root, "new file.txt"), []byte("new\n"))
	writeTestFile(t, filepath.Join(root, "héllo.txt"), []byte("unicode\n"))

	patch, files, err := New().Diff(context.Background(), root)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(patch, "diff --git") || !strings.Contains(patch, "tracked.txt") {
		t.Fatalf("patch does not look like a git diff:\n%s", patch)
	}
	if !strings.Contains(patch, "-one") || !strings.Contains(patch, "+two") {
		t.Fatalf("patch does not contain modification:\n%s", patch)
	}

	want := []string{"gone.txt", "héllo.txt", "new file.txt", "space name.txt", "tracked.txt", "untracked.txt"}
	sort.Strings(want)
	if !slices.Equal(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	if len(files) != len(mergeSorted(files)) {
		t.Fatalf("files contains duplicates: %v", files)
	}
}

func TestGitRepoDiffPatchCap(t *testing.T) {
	root := initGitRepo(t)

	large := bytes.Repeat([]byte("a"), maxPatchBytes+(1<<20))
	writeTestFile(t, filepath.Join(root, "big.txt"), large)
	runGit(t, root, "add", "big.txt")
	runGit(t, root, "commit", "-m", "large")

	large[0] = 'b'
	writeTestFile(t, filepath.Join(root, "big.txt"), large)

	patch, files, err := New().Diff(context.Background(), root)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(patch) != maxPatchBytes {
		t.Fatalf("patch len = %d, want %d", len(patch), maxPatchBytes)
	}
	if !strings.Contains(patch, "diff --git") {
		t.Fatal("patch does not start with a diff header")
	}
	if !slices.Contains(files, "big.txt") {
		t.Fatalf("files = %v, want big.txt", files)
	}
}

func TestCappedBuffer(t *testing.T) {
	var buf cappedBuffer
	buf.limit = 4
	if n, err := buf.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.truncated || buf.String() != "abc" {
		t.Fatalf("buf = %q truncated=%v", buf.String(), buf.truncated)
	}
	if n, err := buf.Write([]byte("defg")); n != 4 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if !buf.truncated || buf.String() != "abcd" {
		t.Fatalf("buf = %q truncated=%v", buf.String(), buf.truncated)
	}
	if n, err := buf.Write([]byte("h")); n != 1 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.String() != "abcd" {
		t.Fatalf("buf = %q", buf.String())
	}

	var empty cappedBuffer
	empty.limit = 0
	if n, err := empty.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if !empty.truncated || empty.Len() != 0 {
		t.Fatalf("empty buffer len=%d truncated=%v", empty.Len(), empty.truncated)
	}

	var negative cappedBuffer
	negative.limit = -1
	if n, err := negative.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if !negative.truncated {
		t.Fatal("negative-limit buffer did not report truncation")
	}
}

func TestDetectMIMEAndHelpers(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		want    string
	}{
		{"empty", nil, "text/plain; charset=utf-8"},
		{"utf8", []byte("hallo, 世界\n"), "text/plain; charset=utf-8"},
		{"json", []byte(`{"a":1}`), "text/plain; charset=utf-8"},
		{"html", []byte("<!doctype html><html></html>"), "text/plain; charset=utf-8"},
		{"xml", []byte("<?xml version=\"1.0\"?><root/>"), "text/plain; charset=utf-8"},
		{"pdf", []byte("%PDF-1.7\n"), "application/octet-stream"},
		{"nul", []byte{0x00}, "application/octet-stream"},
		{"invalid", []byte{0xff}, "application/octet-stream"},
		{"control", []byte{0x01}, "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectMIME(tc.content); got != tc.want {
				t.Fatalf("detectMIME = %q, want %q", got, tc.want)
			}
		})
	}

	if isBinary([]byte("plain text")) {
		t.Error("plain text classified as binary")
	}
	if !isBinary([]byte{0x00}) {
		t.Error("NUL classified as text")
	}

	if got := splitNULPaths([]byte("b\x00a\x00\x00")); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("splitNULPaths = %v", got)
	}
	if got := mergeSorted([]string{"b", "a"}, []string{"b", "", "c"}); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("mergeSorted = %v", got)
	}
	if !withinRoot("/root", "/root/sub") || withinRoot("/root", "/other") || withinRoot("/root", "/root/../other") {
		t.Fatal("withinRoot containment check is wrong")
	}
}
