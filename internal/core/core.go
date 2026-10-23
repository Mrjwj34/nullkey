package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mrjwj34/nullkey/internal/audit"
	"github.com/Mrjwj34/nullkey/internal/domain"
	"github.com/Mrjwj34/nullkey/internal/events"
	nkid "github.com/Mrjwj34/nullkey/internal/id"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
	"github.com/Mrjwj34/nullkey/internal/store/sqlite"
	"github.com/Mrjwj34/nullkey/internal/terminal"
)

// SecretStore is the host trust boundary for credential values.
type SecretStore interface {
	Put(ctx context.Context, account string, value []byte) error
	Get(ctx context.Context, account string) ([]byte, error)
	Delete(ctx context.Context, account string) error
}

// ArtifactReader provides read-only workspace inspection.
type ArtifactReader interface {
	List(root, rel string) ([]domain.FileEntry, error)
	Read(root, rel string) ([]byte, string, bool, error)
	Diff(ctx context.Context, root string) (string, []string, error)
}

// MCPConn is the core-facing MCP connection projection.
type MCPConn struct {
	ID               string
	ServerID         string
	BaseURL          string
	Credential       string
	CredentialHeader string
	CredentialPrefix string
	AllowedTools     []string
}

// MCPProxy talks to a host-side MCP server.
type MCPProxy interface {
	Test(ctx context.Context, conn MCPConn) ([]string, error)
	Proxy(ctx context.Context, conn MCPConn, w http.ResponseWriter, r *http.Request) error
}

// Options wires the kernel services.
type Options struct {
	Home        string
	Store       *sqlite.Store
	Secrets     SecretStore
	Runtime     *native.Runtime
	Terminal    *terminal.Hub
	Events      *events.Bus
	Audit       *audit.Sink
	Artifacts   ArtifactReader
	MCP         MCPProxy
	Logger      *slog.Logger
	GatewayAddr string
	ProxyAddr   string
	ControlAddr string
	// HostImportRoot overrides the directory scanned for importable host logins.
	HostImportRoot string
	// GatewayClient is used by model/git/OAuth HTTP calls. Tests may inject it.
	GatewayClient *http.Client
	// Now is injectable for tests.
	Now func() time.Time
}

// Service is the kernel service layer.
type Service struct {
	Home          string
	Store         *sqlite.Store
	Secrets       SecretStore
	Runtime       *native.Runtime
	Terminal      *terminal.Hub
	Events        *events.Bus
	Audit         *audit.Sink
	Artifacts     ArtifactReader
	MCP           MCPProxy
	Log           *slog.Logger
	GatewayAddr   string
	ProxyAddr     string
	ControlAddr   string
	HostImportDir string
	HTTP          *http.Client
	now           func() time.Time

	jobsMu sync.Mutex
	jobs   map[string]context.CancelFunc
	oauth  *oauthBroker
	tunnel *tunnelServer

	servicesMu sync.Mutex
	services   map[string]*serviceSupervisor
}

// New builds the service layer.
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("core: store is required")
	}
	if opts.Secrets == nil {
		return nil, errors.New("core: secret store is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Events == nil {
		opts.Events = events.New(128)
	}
	if opts.Audit == nil {
		opts.Audit = audit.New(opts.Store, opts.Logger)
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.GatewayClient == nil {
		opts.GatewayClient = &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil,
				MaxIdleConns:          32,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				DisableCompression:    true,
			},
		}
	}
	hostImport := opts.HostImportRoot
	if hostImport == "" {
		hostImport, _ = os.UserHomeDir()
	}
	s := &Service{
		Home:          opts.Home,
		Store:         opts.Store,
		Secrets:       opts.Secrets,
		Runtime:       opts.Runtime,
		Terminal:      opts.Terminal,
		Events:        opts.Events,
		Audit:         opts.Audit,
		Artifacts:     opts.Artifacts,
		MCP:           opts.MCP,
		Log:           opts.Logger,
		GatewayAddr:   opts.GatewayAddr,
		ProxyAddr:     opts.ProxyAddr,
		ControlAddr:   opts.ControlAddr,
		HostImportDir: hostImport,
		HTTP:          opts.GatewayClient,
		now:           opts.Now,
		jobs:          make(map[string]context.CancelFunc),
		services:      make(map[string]*serviceSupervisor),
	}
	s.oauth = newOAuthBroker(s)
	s.tunnel = newTunnelServer(s)
	return s, nil
}

// Reconcile stops orphan sandbox processes and invalidates old tokens after a
// daemon restart. It never pretends a PTY survived.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.Store.CleanupOrphanLocks(ctx); err != nil {
		return err
	}
	active, err := s.Store.ActiveSandboxes(ctx)
	if err != nil {
		return err
	}
	for _, sb := range active {
		if s.Runtime != nil && sb.PID > 0 {
			_ = native.KillPID(sb.PID)
		}
		_ = s.Store.SetSandboxStopped(ctx, sb.ID, s.now())
		_ = s.Store.ReleaseSandboxLocks(ctx, sb.ID)
		if s.Terminal != nil {
			s.Terminal.Close(sb.ID)
		}
		s.Audit.Record(ctx, "sandbox.stopped", &sb.ID, map[string]any{"reason": "daemon_restart"})
	}
	if err := s.Store.ResetTokenHashes(ctx); err != nil {
		return err
	}
	return s.Store.CleanupIdempotency(ctx, s.now().Add(-24*time.Hour))
}

// Shutdown stops all sandboxes, releases locks, and invalidates tokens.
func (s *Service) Shutdown(ctx context.Context) {
	sandboxes, _, _ := s.Store.ListSandboxes(ctx, 200, "")
	for _, sb := range sandboxes {
		if sb.Status == domain.SandboxRunning || sb.Status == domain.SandboxStarting || sb.Status == domain.SandboxStopping {
			_ = s.StopSandbox(ctx, sb.ID)
		}
	}
	if s.Runtime != nil {
		s.Runtime.StopAll(ctx)
	}
	if s.Terminal != nil {
		for _, sb := range sandboxes {
			s.Terminal.Close(sb.ID)
		}
	}
}

// auditID returns a pointer suitable for domain.AuditEvent.SandboxID.
func ptr(s string) *string { return &s }

// hashToken returns SHA-256 of a sandbox token.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// ResolveProxyToken resolves a per-sandbox token for gateway/proxy use. It
// deliberately ignores the daemon token: the two planes never share auth.
func (s *Service) ResolveProxyToken(token string) (sandboxID string, mode domain.NetworkMode, ok bool) {
	if token == "" {
		return "", "", false
	}
	sb, err := s.Store.FindSandboxByTokenHash(context.Background(), hashToken(token))
	if err != nil {
		return "", "", false
	}
	return sb.ID, sb.NetworkMode, true
}

// SandboxByToken is the gateway authentication hook.
func (s *Service) SandboxByToken(ctx context.Context, token string) (domain.Sandbox, bool) {
	if token == "" {
		return domain.Sandbox{}, false
	}
	sb, err := s.Store.FindSandboxByTokenHash(ctx, hashToken(token))
	if err != nil {
		return domain.Sandbox{}, false
	}
	return sb, true
}

// RecordNetworkBlocked appends an audit event for an egress denial.
func (s *Service) RecordNetworkBlocked(sandboxID, host string) {
	var id *string
	if sandboxID != "" {
		id = &sandboxID
	}
	s.Audit.Record(context.Background(), "network.blocked", id, map[string]any{"host": host})
}

// PublishEvent emits a best-effort SSE event.
func (s *Service) PublishEvent(eventType string, data map[string]any) {
	_ = s.Events.Publish(context.Background(), domain.Event{Type: eventType, Data: data})
}

// IdempotentResponse is a stored POST result.
type IdempotentResponse = sqlite.IdempotentResponse

// LookupIdempotent checks a stored POST response. A key reused with a different
// body is rejected.
func (s *Service) LookupIdempotent(ctx context.Context, key, method, path string, body []byte) (IdempotentResponse, bool, error) {
	sum := sha256.Sum256(body)
	resp, ok, err := s.Store.GetIdempotent(ctx, key, method, path, string(sum[:]))
	if err != nil {
		if errors.Is(err, sqlite.ErrIdempotencyKeyReuse) {
			return IdempotentResponse{}, false, Invalid("Idempotency-Key was reused with a different request body")
		}
		return IdempotentResponse{}, false, Wrap(http.StatusInternalServerError, "internal", "could not read idempotency record", err)
	}
	return resp, ok, nil
}

// StoreIdempotent persists a response.
func (s *Service) StoreIdempotent(ctx context.Context, key, method, path string, body []byte, status int, response []byte) {
	sum := sha256.Sum256(body)
	if err := s.Store.PutIdempotent(ctx, key, method, path, string(sum[:]), status, response); err != nil {
		s.Log.Error("idempotency_store_failed", "error", err)
	}
}

// ---------------------------------------------------------------- catalogs

// Harness describes an installable harness.
type Harness struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Runtime   string `json:"runtime"`
}

var harnessCatalog = []Harness{
	{ID: "shell", Name: "Shell", Kind: "tui", Version: "builtin", Installed: true, Runtime: "native"},
	{ID: "codex", Name: "Codex", Kind: "tui", Version: "latest", Runtime: "native"},
	{ID: "claude", Name: "Claude Code", Kind: "tui", Version: "latest", Runtime: "native"},
	{ID: "dsh", Name: "DSH", Kind: "tui", Version: "latest", Runtime: "native"},
}

// ListHarnesses reports installed harnesses. Installation is a real local
// adapter marker plus a PATH probe; nothing is faked as installed.
func (s *Service) ListHarnesses() []Harness {
	out := make([]Harness, 0, len(harnessCatalog))
	for _, h := range harnessCatalog {
		h.Installed = s.harnessInstalled(h.ID)
		if h.ID == "shell" {
			h.Installed = true
		}
		out = append(out, h)
	}
	return out
}

func (s *Service) harnessInstalled(id string) bool {
	if id == "shell" {
		return true
	}
	if _, err := os.Stat(filepath.Join(s.Home, "harnesses", id, "installed.json")); err == nil {
		return true
	}
	_, err := exec.LookPath(id)
	return err == nil
}

// harnessCommand resolves the command for a sandbox.
func (s *Service) harnessCommand(id string) ([]string, error) {
	if id == "shell" || id == "" {
		return []string{"/bin/sh"}, nil
	}
	if !s.harnessInstalled(id) {
		return nil, E(http.StatusConflict, "harness_not_installed", "harness is not installed")
	}
	path, err := exec.LookPath(id)
	if err != nil {
		return nil, E(http.StatusConflict, "harness_not_installed", "harness binary is not available")
	}
	return []string{path}, nil
}

// App is a desktop app catalog entry.
type App struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Installed       bool   `json:"installed"`
	RequiresDesktop bool   `json:"requires_desktop"`
}

// ListApps returns the (empty in v1) desktop catalog.
func (s *Service) ListApps() []App { return []App{} }

// Provider is a credential provider registry entry.
type Provider struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	AuthTypes   []string `json:"auth_types"`
	DefaultAuth *string  `json:"default_auth"`
	BaseURL     *string  `json:"base_url"`
}

var providerRegistry = []Provider{
	{ID: "codex", Name: "Codex", Kind: "llm", AuthTypes: []string{"oauth", "api_key"}, DefaultAuth: strptr("oauth"), BaseURL: strptr("https://chatgpt.com/backend-api/codex")},
	{ID: "openai", Name: "OpenAI", Kind: "llm", AuthTypes: []string{"api_key", "bearer"}, DefaultAuth: strptr("api_key"), BaseURL: strptr("https://api.openai.com")},
	{ID: "anthropic", Name: "Anthropic", Kind: "llm", AuthTypes: []string{"api_key", "header"}, DefaultAuth: strptr("api_key"), BaseURL: strptr("https://api.anthropic.com")},
	{ID: "github", Name: "GitHub", Kind: "mcp", AuthTypes: []string{"pat", "oauth", "basic"}, DefaultAuth: strptr("pat"), BaseURL: strptr("https://api.github.com")},
}

func strptr(s string) *string { return &s }

// Providers returns the registry.
func (s *Service) Providers() []Provider {
	out := make([]Provider, len(providerRegistry))
	copy(out, providerRegistry)
	return out
}

func providerByID(id string) (Provider, bool) {
	for _, p := range providerRegistry {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// MCPServerCatalogItem is a catalog entry.
type MCPServerCatalogItem struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	AuthTypes        []string `json:"auth_types"`
	RequiresOAuthApp bool     `json:"requires_oauth_app"`
}

// MCPCatalog returns host MCP servers.
func (s *Service) MCPCatalog() []MCPServerCatalogItem {
	return []MCPServerCatalogItem{
		{ID: "github", Name: "GitHub", AuthTypes: []string{"pat", "oauth"}, RequiresOAuthApp: true},
		{ID: "filesystem", Name: "Filesystem", AuthTypes: []string{}, RequiresOAuthApp: false},
	}
}

func mcpServerByID(id string) (MCPServerCatalogItem, bool) {
	for _, item := range []MCPServerCatalogItem{
		{ID: "github", Name: "GitHub", AuthTypes: []string{"pat", "oauth"}, RequiresOAuthApp: true},
		{ID: "filesystem", Name: "Filesystem", AuthTypes: []string{}, RequiresOAuthApp: false},
	} {
		if item.ID == id {
			return item, true
		}
	}
	return MCPServerCatalogItem{}, false
}

// OAuth provider catalog.
type OAuthProvider struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	Status string   `json:"status"`
}

// OAuthProviders lists providers that can actually start a flow.
func (s *Service) OAuthProviders() []OAuthProvider {
	out := []OAuthProvider{}
	for _, id := range []string{"codex", "github"} {
		cfg := s.oauthConfig(id)
		status := "unavailable"
		if cfg.available() {
			status = "available"
		}
		out = append(out, OAuthProvider{ID: id, Name: strings.Title(id), Scopes: cfg.scopes, Status: status})
	}
	return out
}

// dirSize is a best-effort directory size with hard entry/time budgets. It is
// used on API read paths, so a 10k-file workspace must not turn a file preview
// into a full tree walk. Large workspaces return a partial (lower-bound) size.
func dirSize(root string) int64 {
	var total int64
	start := time.Now()
	entries := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		entries++
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		if total > 1<<30 || entries >= 2000 || time.Since(start) > 5*time.Millisecond {
			return filepath.SkipAll
		}
		return nil
	})
	return total
}

// sortedUnique returns a stable, duplicate-free string slice.
func sortedUnique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok || v == "" {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// decodeSecret parses an OAuth secret bundle or returns a raw API key.
func decodeSecret(raw []byte) (accessToken, refreshToken string, expires time.Time) {
	var bundle struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if json.Unmarshal(raw, &bundle) == nil && (bundle.AccessToken != "" || bundle.RefreshToken != "") {
		return bundle.AccessToken, bundle.RefreshToken, bundle.ExpiresAt
	}
	return string(raw), "", time.Time{}
}

// encodeOAuthSecret builds the stored OAuth bundle.
func encodeOAuthSecret(access, refresh string, expires time.Time) []byte {
	raw, _ := json.Marshal(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_at":    expires.UTC().Format(time.RFC3339),
	})
	return raw
}

func newID(prefix string) string { return nkid.New(prefix) }
