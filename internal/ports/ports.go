// Package ports defines the interfaces between the kernel core and its
// adapters (SQLite, Keychain, runtime, gateways, proxy, tunnel, artifacts).
//
// Adapters depend on this package; this package depends only on domain. The
// composition root (internal/kernel) wires concrete implementations. Keep these
// interfaces small and add methods only when an adapter needs them.
package ports

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

// Runtime starts and supervises sandbox processes.
//
// The runtime must never accept or retain real credentials. Per-sandbox tokens
// are passed through the child environment at launch time and are not part of
// SandboxSpec.
type Runtime interface {
	Create(ctx context.Context, spec domain.SandboxSpec) (domain.Sandbox, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, argv []string) (domain.ExecResult, error)
	Terminal(ctx context.Context, id string) (Terminal, error)
	Desktop(ctx context.Context, id string) (domain.DesktopEndpoint, error)
	Services(ctx context.Context, id string) ([]domain.Service, error)
	Stats(ctx context.Context, id string) (domain.Stats, error)
}

// Terminal is a bidirectional PTY attachment.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() (exitCode int, err error)
}

// SecretStore persists real credentials in the host trust boundary. Values are
// passed as bytes and must not be logged, serialized, or placed in argv.
type SecretStore interface {
	Put(ctx context.Context, account string, secret []byte) error
	Get(ctx context.Context, account string) ([]byte, error)
	Delete(ctx context.Context, account string) error
}

// ModelGateway proxies model API traffic from a sandbox to an upstream,
// replacing the sandbox token with a real credential.
type ModelGateway interface {
	Proxy(ctx context.Context, sandboxID string, w http.ResponseWriter, r *http.Request) error
}

// MCPGateway proxies MCP JSON-RPC traffic from a sandbox to a host-side MCP
// server, injecting the configured credential and enforcing the tool allowlist.
type MCPGateway interface {
	Proxy(ctx context.Context, sandboxID, connectionID string, w http.ResponseWriter, r *http.Request) error
}

// OAuthBroker performs host-side Authorization Code + PKCE flows and refreshes
// tokens. It never exposes tokens to a sandbox.
type OAuthBroker interface {
	Start(ctx context.Context, providerID, label string) (flow domain.OAuthFlow, authURL string, err error)
	Complete(ctx context.Context, code, state string) (domain.OAuthFlow, error)
	Refresh(ctx context.Context, credentialID string) (expiresAt time.Time, err error)
	Import(ctx context.Context, providerID, source, label string) (domain.Credential, error)
}

// GitProxy proxies smart HTTP git traffic with an injected credential.
type GitProxy interface {
	Proxy(ctx context.Context, sandboxID string, w http.ResponseWriter, r *http.Request) error
}

// EgressProxy is the only path to the internet for unauthenticated sandbox
// traffic. Implementations must be loopback-only and must not MITM TLS.
type EgressProxy interface {
	http.Handler
	Mode() domain.NetworkMode
}

// Tunnel exposes declared sandbox services to the host GUI through one-time
// tickets. It must not provide arbitrary port forwarding.
type Tunnel interface {
	Services(ctx context.Context, sandboxID string) ([]domain.Service, error)
	Ticket(ctx context.Context, sandboxID, serviceID string) (domain.ServiceTicket, error)
	http.Handler
}

// ArtifactReader provides read-only workspace inspection.
type ArtifactReader interface {
	List(ctx context.Context, workspaceID, path string) ([]domain.FileEntry, error)
	Read(ctx context.Context, workspaceID, path string) (content []byte, mime string, truncated bool, err error)
	Diff(ctx context.Context, workspaceID string) (patch string, files []domain.FileEntry, err error)
}

// AuditSink appends redacted audit events and supports paginated reads.
type AuditSink interface {
	Append(ctx context.Context, event domain.AuditEvent) error
	List(ctx context.Context, limit int, cursor, eventType string) ([]domain.AuditEvent, string, error)
}

// EventBus fans out best-effort SSE events to GUI subscribers.
type EventBus interface {
	Publish(ctx context.Context, event domain.Event) error
	Subscribe(ctx context.Context) (<-chan domain.Event, func(), error)
}

// Store is the kernel's persistence boundary. Repository methods are added on
// the concrete SQLite store as features land; this base interface only covers
// lifecycle checks used by the composition root.
type Store interface {
	Ping(ctx context.Context) error
	Close() error
}
