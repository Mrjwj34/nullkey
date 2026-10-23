// Package artifacts implements read-only browsing of workspace files for the
// Nullkey control plane.
//
// The reader never writes to the workspace and never follows a symlink out of
// the workspace root. Paths are resolved with filepath.EvalSymlinks and the
// final target is checked against the canonical root before any file is read.
package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

const (
	fileType      = "file"
	directoryType = "directory"

	// maxReadBytes is the largest file body returned by Read. One extra byte is
	// read so an exactly-at-limit file is not reported as truncated.
	maxReadBytes = 1 << 20
	// maxPatchBytes caps the git diff returned by Diff.
	maxPatchBytes = 4 << 20
)

// Reader provides read-only workspace inspection.
//
// A Reader is stateless and safe for concurrent use.
type Reader struct{}

// New returns a read-only artifact reader.
func New() *Reader {
	return &Reader{}
}

// List returns the entries directly below rel inside root.
//
// Entries are sorted with directories first and then by name. Symlinks inside
// root are followed for type and size; a symlink that points outside root (or
// is broken) is listed as an ordinary file entry without following it.
func (r *Reader) List(root, rel string) ([]domain.FileEntry, error) {
	resolved, rootReal, err := resolvePath(root, rel)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}

	result := make([]domain.FileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat artifact %q: %w", entry.Name(), err)
		}

		entryType := fileType
		size := info.Size()
		mtime := info.ModTime().UTC()
		if info.IsDir() {
			entryType = directoryType
		}

		if info.Mode()&os.ModeSymlink != 0 {
			// ReadDir uses Lstat, so info describes the link itself. Resolve it
			// only when the final target stays inside the workspace.
			target, evalErr := filepath.EvalSymlinks(filepath.Join(resolved, entry.Name()))
			if evalErr == nil && withinRoot(rootReal, target) {
				targetInfo, statErr := os.Stat(target)
				switch {
				case statErr == nil:
					size = targetInfo.Size()
					mtime = targetInfo.ModTime().UTC()
					if targetInfo.IsDir() {
						entryType = directoryType
					}
				case errors.Is(statErr, fs.ErrNotExist):
					// Broken link: keep the lstat data and list the entry.
				default:
					return nil, fmt.Errorf("stat artifact target %q: %w", entry.Name(), statErr)
				}
			}
		}

		result = append(result, domain.FileEntry{
			Name:  entry.Name(),
			Type:  entryType,
			Size:  size,
			MTime: mtime,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type == directoryType
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// Read returns at most the first 1 MiB of rel. When the file is larger the
// returned body is truncated and truncated is true.
//
// Binary content is reported as application/octet-stream. Valid UTF-8 text is
// reported as text/plain; charset=utf-8; http.DetectContentType is used as a
// fallback for content the simple UTF-8 check cannot classify as text.
func (r *Reader) Read(root, rel string) (content []byte, mime string, truncated bool, err error) {
	resolved, rootReal, err := resolvePath(root, rel)
	if err != nil {
		return nil, "", false, err
	}
	if resolved == rootReal {
		return nil, "", false, errors.New("artifacts: cannot read workspace root as a file")
	}

	f, err := os.Open(resolved)
	if err != nil {
		return nil, "", false, fmt.Errorf("open artifact: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, "", false, fmt.Errorf("stat artifact: %w", err)
	}
	if info.IsDir() {
		return nil, "", false, fmt.Errorf("artifacts: %q is a directory", rel)
	}
	if !info.Mode().IsRegular() {
		return nil, "", false, fmt.Errorf("artifacts: %q is not a regular file", rel)
	}

	buf := make([]byte, maxReadBytes+1)
	n, readErr := io.ReadFull(f, buf)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, "", false, fmt.Errorf("read artifact: %w", readErr)
	}

	content = buf[:n]
	if n > maxReadBytes {
		content = content[:maxReadBytes]
		truncated = true
	}

	return content, detectMIME(content), truncated, nil
}

// Diff returns the workspace patch together with the changed file paths.
//
// If root is not a git repository the patch is empty and files contains every
// regular workspace file. In a git repository the patch is produced by git
// diff and files contains tracked changes plus untracked files.
func (r *Reader) Diff(ctx context.Context, root string) (patch string, files []string, err error) {
	if ctx == nil {
		return "", nil, errors.New("artifacts: nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}

	_, rootReal, err := canonicalRoot(root)
	if err != nil {
		return "", nil, err
	}

	if !hasGitMetadata(rootReal) {
		files, err := walkFiles(ctx, rootReal)
		if err != nil {
			return "", nil, err
		}
		return "", files, nil
	}

	patch, err = gitDiff(ctx, rootReal)
	if err != nil {
		// A .git path can exist without being a usable repository. Treat any
		// git failure as "not a repository" and fall back to a tree walk.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", nil, ctxErr
		}
		files, walkErr := walkFiles(ctx, rootReal)
		if walkErr != nil {
			return "", nil, walkErr
		}
		return "", files, nil
	}

	changed, changedErr := gitChangedFiles(ctx, rootReal)
	if changedErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", nil, ctxErr
		}
		changed = nil
	}

	untracked, untrackedErr := gitUntracked(ctx, rootReal)
	if untrackedErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", nil, ctxErr
		}
		untracked = nil
	}

	return patch, mergeSorted(changed, untracked), nil
}

// resolvePath validates rel, resolves symlinks, and verifies that the final
// target remains inside root. It returns the resolved target and the resolved
// workspace root.
func resolvePath(root, rel string) (resolved string, rootReal string, err error) {
	absRoot, rootReal, err := canonicalRoot(root)
	if err != nil {
		return "", "", err
	}
	if err := validateRel(rel); err != nil {
		return "", "", err
	}

	target := filepath.Join(absRoot, rel)
	if !withinRoot(absRoot, target) {
		return "", "", fmt.Errorf("artifacts: path %q escapes workspace root", rel)
	}

	resolved, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact path: %w", err)
	}
	if !withinRoot(rootReal, resolved) {
		return "", "", fmt.Errorf("artifacts: path %q escapes workspace root", rel)
	}

	return resolved, rootReal, nil
}

// canonicalRoot returns the cleaned absolute root and its symlink-resolved
// form. The root must exist and be a directory.
func canonicalRoot(root string) (absRoot string, rootReal string, err error) {
	if root == "" {
		return "", "", errors.New("artifacts: empty workspace root")
	}
	if strings.IndexByte(root, 0) >= 0 {
		return "", "", errors.New("artifacts: workspace root contains NUL")
	}

	absRoot, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	rootReal, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace root: %w", err)
	}

	info, err := os.Stat(rootReal)
	if err != nil {
		return "", "", fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("artifacts: workspace root is not a directory")
	}

	return absRoot, rootReal, nil
}

// validateRel rejects absolute paths, NUL bytes, and any parent traversal.
func validateRel(rel string) error {
	if strings.IndexByte(rel, 0) >= 0 {
		return errors.New("artifacts: path contains NUL")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return errors.New("artifacts: path must be relative")
	}

	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return errors.New("artifacts: path escapes workspace root")
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".." {
			return errors.New("artifacts: path escapes workspace root")
		}
	}
	return nil
}

// withinRoot reports whether target is root or a descendant of root.
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// detectMIME classifies content as text or binary. Only the two MIME values
// promised by the API are returned.
func detectMIME(content []byte) string {
	if isBinary(content) {
		return "application/octet-stream"
	}

	detected := http.DetectContentType(content)
	if strings.HasPrefix(detected, "text/") {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// isBinary reports whether content cannot be treated as UTF-8 text. NUL bytes
// and disallowed control characters are strong binary signals; invalid UTF-8
// catches most remaining binary formats.
func isBinary(content []byte) bool {
	if bytes.IndexByte(content, 0) >= 0 {
		return true
	}
	if !utf8.Valid(content) {
		return true
	}
	for _, b := range content {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
			return true
		}
		if b == 0x7f {
			return true
		}
	}
	return false
}

func hasGitMetadata(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

func gitDiff(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-color", "--no-ext-diff", "--")
	cmd.Dir = root
	cmd.Stderr = io.Discard

	var stdout cappedBuffer
	stdout.limit = maxPatchBytes
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return stdout.String(), nil
}

func gitChangedFiles(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-color", "--no-ext-diff", "--name-only", "-z", "--")
	cmd.Dir = root
	cmd.Stderr = io.Discard

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only: %w", err)
	}
	return splitNULPaths(out), nil
}

func gitUntracked(ctx context.Context, root string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = root
	cmd.Stderr = io.Discard

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}

	files := make([]string, 0)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "\"") {
			if unquoted, unquoteErr := strconv.Unquote(line); unquoteErr == nil {
				line = unquoted
			}
		}
		files = append(files, filepath.ToSlash(line))
	}
	sort.Strings(files)
	return files, nil
}

// walkFiles lists every non-directory entry below root, excluding git metadata.
func walkFiles(ctx context.Context, root string) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk workspace: %w", err)
	}

	sort.Strings(files)
	return files, nil
}

func splitNULPaths(out []byte) []string {
	files := make([]string, 0)
	for _, part := range bytes.Split(out, []byte{0}) {
		if len(part) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(part)))
	}
	sort.Strings(files)
	return files
}

func mergeSorted(groups ...[]string) []string {
	seen := make(map[string]struct{})
	files := make([]string, 0)
	for _, group := range groups {
		for _, file := range group {
			if file == "" {
				continue
			}
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files
}

// cappedBuffer stores at most limit bytes and silently discards the rest. It
// keeps git diff from buffering an unbounded patch in memory.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func (b *cappedBuffer) Len() int {
	return b.buf.Len()
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}

	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}
