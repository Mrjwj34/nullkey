package native

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

func TestBuildProfileExactDefaultRules(t *testing.T) {
	// Use non-existent, absolute paths so canonicalPath cannot resolve them
	// and the output is fully deterministic.
	profile := BuildProfile(ProfileOptions{
		Workspace: "/nonexistent-nullkey/workspace",
		State:     "/nonexistent-nullkey/state",
		Cache:     "/nonexistent-nullkey/cache",
	})

	wantPrefix := `(version 1)
(deny default)
(allow process*)
(allow sysctl-read)
(allow mach-lookup)
(allow ipc-posix*)
(allow socket-ioctl)
(allow file-ioctl)
(allow file-read*)
(allow file-write* (subpath "/dev") (subpath "/private/tmp"))
(deny file-read* (subpath "/Users") (subpath "/System/Volumes/Data/Users") (subpath "/Volumes") (subpath "/private/var/root"))
(deny file-write* (subpath "/Users") (subpath "/System/Volumes/Data/Users") (subpath "/Volumes") (subpath "/private/var/root"))
(allow file-read* (subpath "/nonexistent-nullkey/workspace"))
(allow file-write* (subpath "/nonexistent-nullkey/workspace"))
(allow file-read* (subpath "/nonexistent-nullkey/state"))
(allow file-write* (subpath "/nonexistent-nullkey/state"))
(allow file-read* (subpath "/nonexistent-nullkey/cache"))
(allow file-write* (subpath "/nonexistent-nullkey/cache"))
(allow file-write* (subpath "/dev"))
(deny network*)
(allow network-outbound (remote ip "localhost:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network* (local ip "localhost:*"))
(allow system-socket)
`
	if profile != wantPrefix {
		t.Fatalf("unexpected default profile:\n--- got ---\n%s\n--- want ---\n%s", profile, wantPrefix)
	}
	if strings.Contains(profile, "(allow network-outbound)\n") {
		t.Fatalf("filtered/default profile must not allow arbitrary outbound network:\n%s", profile)
	}
}

func TestBuildProfileHostHomeDeniedAndWorkspaceReallowed(t *testing.T) {
	opts := ProfileOptions{
		Workspace: "/private/tmp/nk-workspace",
		State:     "/private/tmp/nk-state",
		Cache:     "/private/tmp/nk-cache",
		HostHome:  "/Users/alice",
		Mode:      domain.NetworkFiltered,
	}
	profile := BuildProfile(opts)

	for _, want := range []string{
		`(subpath "/Users")`,
		`(subpath "/System/Volumes/Data/Users")`,
		`(subpath "/Users/alice")`,
		`(subpath "/System/Volumes/Data/Users/alice")`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("host home deny missing %q:\n%s", want, profile)
		}
	}
	// The sandbox directories are re-allowed after the deny block, so they win
	// under Seatbelt's last-match-wins semantics.
	denyAt := strings.Index(profile, "(deny file-read*")
	allowAt := strings.Index(profile, `(allow file-read* (subpath "/private/tmp/nk-workspace"))`)
	if denyAt < 0 || allowAt < 0 || allowAt < denyAt {
		t.Fatalf("workspace allow must follow the host home deny:\n%s", profile)
	}
}

func TestBuildProfileCanonicalSymlinkPaths(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}

	profile := BuildProfile(ProfileOptions{
		Workspace: link,
		State:     link,
		Cache:     link,
	})
	for _, path := range []string{link, resolved} {
		want := `(allow file-read* (subpath ` + quote(path) + `))`
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing canonical allow for %q:\n%s", path, profile)
		}
	}
	// A symlink-aliased path must also be writable at its real location.
	wantWrite := `(allow file-write* (subpath ` + quote(resolved) + `))`
	if !strings.Contains(profile, wantWrite) {
		t.Fatalf("profile missing canonical write allow for %q:\n%s", resolved, profile)
	}
}

func TestBuildProfileNetworkModes(t *testing.T) {
	base := ProfileOptions{Workspace: "/nonexistent-nullkey/ws"}

	for _, mode := range []domain.NetworkMode{domain.NetworkFiltered, domain.NetworkServicesOnly, ""} {
		t.Run("mode="+string(mode), func(t *testing.T) {
			opts := base
			opts.Mode = mode
			profile := BuildProfile(opts)
			if strings.Contains(profile, "(allow network-outbound)\n") {
				t.Fatalf("mode %q must not allow generic outbound:\n%s", mode, profile)
			}
			for _, want := range []string{
				`(deny network*)`,
				`(allow network-outbound (remote ip "localhost:*"))`,
				`(allow network-inbound (local ip "localhost:*"))`,
				`(allow network* (local ip "localhost:*"))`,
				`(allow system-socket)`,
			} {
				if !strings.Contains(profile, want) {
					t.Fatalf("mode %q missing %q:\n%s", mode, want, profile)
				}
			}
		})
	}

	t.Run("open", func(t *testing.T) {
		opts := base
		opts.Mode = domain.NetworkOpen
		profile := BuildProfile(opts)
		if !strings.Contains(profile, "(allow network-outbound)\n") {
			t.Fatalf("open mode must allow generic outbound:\n%s", profile)
		}
	})
}

func TestBuildProfileSkipsEmptyPathsAndEscapesQuotes(t *testing.T) {
	profile := BuildProfile(ProfileOptions{
		Workspace: `/tmp/quote "dir" \ path`,
	})
	if !strings.Contains(profile, `(subpath "/tmp/quote \"dir\" \\ path")`) {
		t.Fatalf("quote escaping missing:\n%s", profile)
	}
	// Empty State/Cache must not emit malformed "(subpath )" rules.
	if strings.Contains(profile, `(subpath "")`) {
		t.Fatalf("empty path was emitted:\n%s", profile)
	}
}

func TestCanonicalPathAndQuote(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := canonicalPath(dir); got != resolved {
		t.Fatalf("canonicalPath(%q) = %q, want %q", dir, got, resolved)
	}
	missing := filepath.Join(dir, "missing")
	if got := canonicalPath(missing); got != missing {
		t.Fatalf("canonicalPath(missing) = %q, want unchanged %q", got, missing)
	}
	if got, want := quote(`a"b\c`), `"a\"b\\c"`; got != want {
		t.Fatalf("quote = %q, want %q", got, want)
	}
}

func TestHelperErrorSanitizesAndTruncates(t *testing.T) {
	got := helperError(errors.New("line one\nline two"))
	if got != "ERR:line one line two" {
		t.Fatalf("helperError = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("helperError must not contain newlines: %q", got)
	}

	long := helperError(errors.New(strings.Repeat("x", 500)))
	if !strings.HasPrefix(long, "ERR:") {
		t.Fatalf("helperError prefix missing: %q", long)
	}
	if want := len("ERR:") + 300; len(long) != want {
		t.Fatalf("helperError length = %d, want %d", len(long), want)
	}
	if strings.Contains(long, "\n") {
		t.Fatalf("truncated helperError must not contain newlines: %q", long)
	}
}

func TestProfileAllowsHarnessPackageButNotHostHomeContents(t *testing.T) {
	profile := BuildProfile(ProfileOptions{
		Workspace:   "/tmp/ws",
		State:       "/tmp/state",
		Cache:       "/tmp/cache",
		HostHome:    "/Users/alice",
		HarnessRoot: "/Users/alice/.local/lib/node_modules/@openai/codex",
		Mode:        domain.NetworkFiltered,
	})
	for _, want := range []string{
		`(allow file-read* (subpath "/Users/alice/.local/lib/node_modules/@openai/codex"))`,
		`(allow file-read-metadata (subpath "/Users/alice"))`,
		`(allow file-read-metadata (subpath "/Users"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing %q:\n%s", want, profile)
		}
	}
	if strings.Contains(profile, `(allow file-read* (subpath "/Users/alice"))`) {
		t.Fatal("host home contents were allowed")
	}
}
