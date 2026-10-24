package native

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// ProfileOptions describes the Seatbelt policy for one sandbox.
type ProfileOptions struct {
	Workspace   string
	State       string
	Cache       string
	HostHome    string
	HarnessRoot string
	Mode        domain.NetworkMode
}

// BuildProfile renders a Seatbelt profile. The policy is deny-by-default with
// read access to the system and the sandbox's own directories. The host home
// is denied explicitly, including its firmlink path, and then the sandbox
// directories are re-allowed (Seatbelt uses last-match-wins).
func BuildProfile(opts ProfileOptions) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(allow process*)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow ipc-posix*)\n")
	b.WriteString("(allow socket-ioctl)\n")
	b.WriteString("(allow file-ioctl)\n")
	b.WriteString("(allow file-read*)\n")
	b.WriteString("(allow file-write* (subpath \"/dev\") (subpath \"/private/tmp\"))\n")

	denied := []string{"/Users", "/System/Volumes/Data/Users", "/Volumes", "/private/var/root"}
	if opts.HostHome != "" {
		denied = append(denied, opts.HostHome, "/System/Volumes/Data"+opts.HostHome)
	}
	b.WriteString("(deny file-read*")
	for _, path := range denied {
		b.WriteString(" (subpath ")
		b.WriteString(quote(path))
		b.WriteString(")")
	}
	b.WriteString(")\n")
	b.WriteString("(deny file-write*")
	for _, path := range denied {
		b.WriteString(" (subpath ")
		b.WriteString(quote(path))
		b.WriteString(")")
	}
	b.WriteString(")\n")

	// Node resolves the real path of an npm-installed harness by lstat-ing every
	// ancestor, including /Users. Allow metadata for those ancestors without
	// granting read access to their contents. The harness package itself is
	// allowed below.
	if opts.HarnessRoot != "" {
		// Only the ancestors between the harness package and the host home's
		// parent are needed. Do not allow metadata for the filesystem root or
		// unrelated trees.
		stop := ""
		if opts.HostHome != "" {
			stop = filepath.Dir(opts.HostHome)
		}
		for _, dir := range ancestors(opts.HarnessRoot) {
			if dir == "/" {
				continue
			}
			b.WriteString("(allow file-read-metadata (subpath ")
			b.WriteString(quote(dir))
			b.WriteString("))\n")
			if stop != "" && dir == stop {
				break
			}
		}
	}

	// Resolving an absolute path requires metadata on every component, so a
	// sandbox directory inside a denied tree stays unreachable until its
	// ancestors are visible: Codex canonicalizes CODEX_HOME at startup and
	// exits when any component is denied. A literal grants stat on that exact
	// directory only, not a listing of it nor metadata for its subtree.
	seen := map[string]bool{}
	for _, path := range []string{opts.Workspace, opts.State, opts.Cache} {
		if path == "" {
			continue
		}
		variants := []string{path}
		if resolved := canonicalPath(path); resolved != path {
			variants = append(variants, resolved)
		}
		for _, variant := range variants {
			for _, dir := range ancestors(filepath.Dir(variant)) {
				if dir == "/" || seen[dir] {
					continue
				}
				seen[dir] = true
				b.WriteString("(allow file-read-metadata (literal ")
				b.WriteString(quote(dir))
				b.WriteString("))\n")
			}
		}
	}

	allowed := []string{opts.Workspace, opts.State, opts.Cache, opts.HarnessRoot}
	for _, path := range allowed {
		if path == "" {
			continue
		}
		b.WriteString("(allow file-read* (subpath ")
		b.WriteString(quote(path))
		b.WriteString("))\n")
		b.WriteString("(allow file-write* (subpath ")
		b.WriteString(quote(path))
		b.WriteString("))\n")
		if resolved := canonicalPath(path); resolved != path {
			b.WriteString("(allow file-read* (subpath ")
			b.WriteString(quote(resolved))
			b.WriteString("))\n")
			b.WriteString("(allow file-write* (subpath ")
			b.WriteString(quote(resolved))
			b.WriteString("))\n")
		}
	}
	b.WriteString("(allow file-write* (subpath \"/dev\"))\n")

	b.WriteString("(deny network*)\n")
	b.WriteString("(allow network-outbound (remote ip \"localhost:*\"))\n")
	b.WriteString("(allow network-inbound (local ip \"localhost:*\"))\n")
	b.WriteString("(allow network* (local ip \"localhost:*\"))\n")
	if opts.Mode == domain.NetworkOpen {
		b.WriteString("(allow network-outbound)\n")
	}
	b.WriteString("(allow system-socket)\n")
	return b.String()
}

func ancestors(path string) []string {
	var out []string
	dir := filepath.Clean(path)
	for {
		out = append(out, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func quote(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(s) + `"`
}

// helperError formats a safe helper failure message for the readiness pipe.
func helperError(err error) string {
	msg := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Sprintf("ERR:%s", msg)
}
