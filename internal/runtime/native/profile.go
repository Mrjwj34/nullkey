package native

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// ProfileOptions describes the Seatbelt policy for one sandbox.
type ProfileOptions struct {
	Workspace string
	State     string
	Cache     string
	HostHome  string
	Mode      domain.NetworkMode
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

	allowed := []string{opts.Workspace, opts.State, opts.Cache}
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
