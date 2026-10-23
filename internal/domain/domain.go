// Package domain contains the kernel's shared domain types and enums.
//
// This package deliberately has no dependencies on HTTP, storage, or adapters so
// the same types can be used by the API layer, services, and runtime backends.
// Secrets never belong in these types.
package domain

import "time"

// SandboxStatus is the lifecycle state of a sandbox.
type SandboxStatus string

const (
	SandboxCreating SandboxStatus = "creating"
	SandboxStarting SandboxStatus = "starting"
	SandboxRunning  SandboxStatus = "running"
	SandboxStopping SandboxStatus = "stopping"
	SandboxStopped  SandboxStatus = "stopped"
	SandboxFailed   SandboxStatus = "failed"
)

// JobStatus is the lifecycle state of an asynchronous job.
type JobStatus string

const (
	JobQueued     JobStatus = "queued"
	JobRunning    JobStatus = "running"
	JobCancelling JobStatus = "cancelling"
	JobCancelled  JobStatus = "cancelled"
	JobSucceeded  JobStatus = "succeeded"
	JobFailed     JobStatus = "failed"
)

// OAuthStatus is the lifecycle state of an OAuth flow.
type OAuthStatus string

const (
	OAuthPending   OAuthStatus = "pending"
	OAuthSucceeded OAuthStatus = "succeeded"
	OAuthFailed    OAuthStatus = "failed"
	OAuthExpired   OAuthStatus = "expired"
	OAuthCancelled OAuthStatus = "cancelled"
)

// NetworkMode controls sandbox egress. None of the modes is offline: the
// gateway remains reachable in all of them.
type NetworkMode string

const (
	NetworkServicesOnly NetworkMode = "services_only"
	NetworkFiltered     NetworkMode = "filtered"
	NetworkOpen         NetworkMode = "open"
)

// RuntimeKind identifies a sandbox runtime backend.
type RuntimeKind string

const (
	RuntimeNative  RuntimeKind = "native"
	RuntimeDesktop RuntimeKind = "desktop"
)

// AuthType identifies how a credential is attached to an upstream request.
type AuthType string

const (
	AuthAPIKey AuthType = "api_key"
	AuthOAuth  AuthType = "oauth"
	AuthPAT    AuthType = "pat"
	AuthNone   AuthType = "none"
)

// CredentialStatus is the user-visible credential state.
type CredentialStatus string

const (
	CredentialConnected CredentialStatus = "connected"
	CredentialExpired   CredentialStatus = "expired"
	CredentialRevoked   CredentialStatus = "revoked"
)

// ServiceStatus is the health state of a sandbox service.
type ServiceStatus string

const (
	ServiceStarting ServiceStatus = "starting"
	ServiceReady    ServiceStatus = "ready"
	ServiceError    ServiceStatus = "error"
)

// Sandbox is a running (or runnable) harness instance.
//
// TokenHash is SHA-256 of the per-sandbox token and is never serialized.
type Sandbox struct {
	ID          string        `json:"id"`
	HarnessID   string        `json:"harness_id"`
	WorkspaceID string        `json:"workspace_id"`
	StateID     string        `json:"state_id"`
	Runtime     RuntimeKind   `json:"runtime"`
	NetworkMode NetworkMode   `json:"network_mode"`
	Status      SandboxStatus `json:"status"`
	PID         int           `json:"pid,omitempty"`
	LastError   *string       `json:"last_error,omitempty"`
	TokenHash   []byte        `json:"-"`
	CreatedAt   time.Time     `json:"created_at"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	StoppedAt   *time.Time    `json:"stopped_at,omitempty"`
}

// SandboxSpec is the launch contract for a runtime backend. It contains no
// credentials: the runtime receives the per-sandbox token separately and only
// exposes it through the child environment.
type SandboxSpec struct {
	HarnessID   string      `json:"harness_id"`
	WorkspaceID string      `json:"workspace_id"`
	StateID     string      `json:"state_id"`
	Runtime     RuntimeKind `json:"runtime"`
	NetworkMode NetworkMode `json:"network_mode"`
	Mounts      []Mount     `json:"mounts,omitempty"`
}

// Mount is a declared filesystem mount. Source is a logical source
// (workspace/state/cache) or an explicitly confirmed host path.
type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Mode   string `json:"mode"`
}

// ExecResult is the result of a one-shot command in a sandbox.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// StorageKind records workspace ownership. Only managed workspaces may be
// deleted by Nullkey; external directories are only removed from the list.
type StorageKind string

const (
	StorageManaged  StorageKind = "managed"
	StorageExternal StorageKind = "external"
)

// Workspace is a persistent project directory.
type Workspace struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	RootPath      string      `json:"root_path"`
	StorageKind   StorageKind `json:"storage_kind"`
	DefaultMode   string      `json:"default_mode"`
	SizeBytes     int64       `json:"size_bytes"`
	LastSandboxID *string     `json:"last_sandbox_id,omitempty"`
	LockedBy      *string     `json:"locked_by,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	LastUsedAt    *time.Time  `json:"last_used_at,omitempty"`
}

// State is persistent harness configuration/session data (never credentials).
type State struct {
	ID            string     `json:"id"`
	HarnessID     string     `json:"harness_id"`
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	SizeBytes     int64      `json:"size_bytes"`
	LastSandboxID *string    `json:"last_sandbox_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
}

// Credential is metadata for a secret stored in the host Keychain. It never
// contains the secret itself.
type Credential struct {
	ID              string           `json:"id"`
	ProviderID      string           `json:"provider_id"`
	Label           string           `json:"label"`
	AuthType        AuthType         `json:"auth_type"`
	Fingerprint     string           `json:"fingerprint"`
	Status          CredentialStatus `json:"status"`
	KeychainAccount string           `json:"-"`
	ExpiresAt       *time.Time       `json:"expires_at,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	LastUsedAt      *time.Time       `json:"last_used_at,omitempty"`
}

// MCPConnection is a host-side MCP server binding.
type MCPConnection struct {
	ID           string    `json:"id"`
	ServerID     string    `json:"server_id"`
	Label        string    `json:"label"`
	Status       string    `json:"status"`
	CredentialID *string   `json:"credential_id,omitempty"`
	Tools        []string  `json:"tools,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Service is a sandbox-local HTTP/WebSocket service exposed through the tunnel.
type Service struct {
	ID         string        `json:"id"`
	SandboxID  string        `json:"sandbox_id"`
	Name       string        `json:"name"`
	Protocol   string        `json:"protocol"`
	TargetHost string        `json:"-"`
	TargetPort int           `json:"-"`
	HealthPath string        `json:"-"`
	WebSocket  bool          `json:"websocket"`
	Status     ServiceStatus `json:"status"`
}

// AuditEvent is an append-only kernel event. Detail must already be redacted.
type AuditEvent struct {
	ID        string         `json:"id"`
	TS        time.Time      `json:"ts"`
	Type      string         `json:"type"`
	SandboxID *string        `json:"sandbox_id,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Event is a best-effort SSE event delivered to the GUI.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Job is an asynchronous install/OAuth/build task.
type Job struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Status      JobStatus `json:"status"`
	Cancellable bool      `json:"cancellable"`
	Progress    float64   `json:"progress"`
	Message     *string   `json:"message,omitempty"`
	Error       *string   `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OAuthFlow tracks a host-side authorization-code flow. StateHash is the
// SHA-256 of the one-time state value and is never serialized.
type OAuthFlow struct {
	ID           string      `json:"id"`
	ProviderID   string      `json:"provider_id"`
	Status       OAuthStatus `json:"status"`
	CredentialID *string     `json:"credential_id,omitempty"`
	Error        *string     `json:"error,omitempty"`
	StateHash    []byte      `json:"-"`
	CreatedAt    time.Time   `json:"created_at"`
	ExpiresAt    time.Time   `json:"expires_at"`
}

// Stats is a point-in-time resource sample for a sandbox.
type Stats struct {
	MemoryBytes      int64   `json:"memory_bytes"`
	CPUPercent       float64 `json:"cpu_percent"`
	DiskBytes        int64   `json:"disk_bytes"`
	PID              int     `json:"pid"`
	UptimeMS         int64   `json:"uptime_ms"`
	StartupMS        int64   `json:"startup_ms"`
	TerminalRTTMSP95 float64 `json:"terminal_rtt_ms_p95"`
}

// ScanCheck is one generated-runtime credential check.
type ScanCheck struct {
	Name string `json:"name"`
	Hits int    `json:"hits"`
}

// ScanResult is the heuristic check of Nullkey-generated environment and
// configuration. It does not cover workspace files, user imports, or user input.
type ScanResult struct {
	OK        bool        `json:"ok"`
	Hits      int         `json:"hits"`
	Scope     string      `json:"scope"`
	Excluded  []string    `json:"excluded"`
	Checks    []ScanCheck `json:"checks"`
	ScannedAt time.Time   `json:"scanned_at"`
}

// FileEntry is a read-only artifact listing entry.
type FileEntry struct {
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// DesktopEndpoint is an optional desktop backend endpoint.
type DesktopEndpoint struct {
	URL       string    `json:"url"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TerminalTicket authorizes a one-time terminal WebSocket connection.
type TerminalTicket struct {
	WSURL     string    `json:"ws_url"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ServiceTicket authorizes a one-time service-tunnel connection.
type ServiceTicket struct {
	URL       string    `json:"url"`
	Ticket    string    `json:"ticket"`
	ServiceID string    `json:"service_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
