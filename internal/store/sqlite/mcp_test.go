package sqlite

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

var mcpBase = time.Date(2024, 5, 1, 11, 0, 0, 0, time.UTC)

func TestMCPCreateGet(t *testing.T) {
	s, ctx := openTestStore(t)
	credID := "cred_1"
	mustCreateMCP(t, ctx, s, domain.MCPConnection{
		ID:           "mcp_1",
		ServerID:     "github",
		Label:        "github tools",
		Status:       "connected",
		CredentialID: &credID,
		ConfigJSON:   `{"base_url":"https://mcp.example.test","tools":["search","read"]}`,
		CreatedAt:    mcpBase,
	})
	got, err := s.GetMCPConnection(ctx, "mcp_1")
	if err != nil {
		t.Fatalf("GetMCPConnection() error = %v", err)
	}
	if got.ServerID != "github" || got.Label != "github tools" || got.Status != "connected" {
		t.Errorf("GetMCPConnection() = %+v", got)
	}
	if got.CredentialID == nil || *got.CredentialID != credID {
		t.Errorf("CredentialID = %v, want %q", got.CredentialID, credID)
	}
	if got.SandboxID != "" {
		t.Errorf("SandboxID = %q, want empty", got.SandboxID)
	}
	if !reflect.DeepEqual(got.Tools, []string{"search", "read"}) {
		t.Errorf("Tools = %v", got.Tools)
	}
	if !got.CreatedAt.Equal(mcpBase) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, mcpBase)
	}

	mustCreateMCP(t, ctx, s, domain.MCPConnection{
		ID: "mcp_badjson", ServerID: "s", Label: "bad", Status: "connected",
		ConfigJSON: `not-json`, CreatedAt: mcpBase,
	})
	bad, err := s.GetMCPConnection(ctx, "mcp_badjson")
	if err != nil {
		t.Fatalf("GetMCPConnection(bad json) error = %v", err)
	}
	if bad.CredentialID != nil || bad.Tools != nil {
		t.Errorf("bad json connection = %+v", bad)
	}
	if _, err := s.GetMCPConnection(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetMCPConnection(missing) error = %v, want sql.ErrNoRows", err)
	}
	if err := s.CreateMCPConnection(ctx, domain.MCPConnection{
		ID: "mcp_1", ServerID: "dup", Label: "dup", Status: "connected", ConfigJSON: `{}`, CreatedAt: mcpBase,
	}); err == nil {
		t.Error("CreateMCPConnection() duplicate id error = nil, want error")
	}
}

func TestMCPListPagination(t *testing.T) {
	s, ctx := openTestStore(t)
	for i, id := range []string{"mcp_a", "mcp_b", "mcp_c"} {
		mustCreateMCP(t, ctx, s, domain.MCPConnection{
			ID: id, ServerID: "s", Label: id, Status: "connected",
			ConfigJSON: `{"tools":["` + id + `"]}`, CreatedAt: mcpBase.Add(time.Duration(i) * time.Minute),
		})
	}
	all, next, err := s.ListMCPConnections(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListMCPConnections() error = %v", err)
	}
	if next != "" || len(all) != 3 || all[0].ID != "mcp_c" || all[2].ID != "mcp_a" {
		t.Fatalf("ListMCPConnections() = %v, next=%q", mcpIDs(all), next)
	}
	var seen []string
	cursor := ""
	for {
		page, next, err := s.ListMCPConnections(ctx, 1, cursor)
		if err != nil {
			t.Fatalf("ListMCPConnections(limit=1) error = %v", err)
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		seen = append(seen, page[0].ID)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 || seen[0] != "mcp_c" || seen[1] != "mcp_b" || seen[2] != "mcp_a" {
		t.Errorf("paged MCP connections = %v", seen)
	}
	if _, _, err := s.ListMCPConnections(ctx, 1, "%%%bad"); err == nil {
		t.Error("ListMCPConnections() with invalid cursor error = nil, want error")
	}
}

func TestMCPBindAndStatus(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateMCP(t, ctx, s, domain.MCPConnection{
		ID: "mcp_bind", ServerID: "s", Label: "bind", Status: "connected",
		ConfigJSON: `{"base_url":"https://mcp.example.test","tools":[]}`, CreatedAt: mcpBase,
	})
	if err := s.BindMCPConnection(ctx, "mcp_bind", "sb_1"); err != nil {
		t.Fatalf("BindMCPConnection() error = %v", err)
	}
	got, err := s.GetMCPConnection(ctx, "mcp_bind")
	if err != nil {
		t.Fatalf("GetMCPConnection() error = %v", err)
	}
	if got.SandboxID != "sb_1" {
		t.Fatalf("SandboxID = %q, want sb_1", got.SandboxID)
	}
	// The first binding wins even when another sandbox tries to claim it.
	if err := s.BindMCPConnection(ctx, "mcp_bind", "sb_2"); err != nil {
		t.Fatalf("BindMCPConnection(second) error = %v", err)
	}
	got, err = s.GetMCPConnection(ctx, "mcp_bind")
	if err != nil {
		t.Fatalf("GetMCPConnection() error = %v", err)
	}
	if got.SandboxID != "sb_1" {
		t.Errorf("SandboxID after second bind = %q, want sb_1", got.SandboxID)
	}
	// Rebinding the same sandbox is idempotent.
	if err := s.BindMCPConnection(ctx, "mcp_bind", "sb_1"); err != nil {
		t.Errorf("BindMCPConnection(same) error = %v", err)
	}
	if err := s.BindMCPConnection(ctx, "missing", "sb_1"); err != nil {
		t.Errorf("BindMCPConnection(missing) error = %v, want nil", err)
	}

	if err := s.SetMCPConnectionStatus(ctx, "mcp_bind", "connected", []string{"alpha", "beta"}); err != nil {
		t.Fatalf("SetMCPConnectionStatus() error = %v", err)
	}
	got, err = s.GetMCPConnection(ctx, "mcp_bind")
	if err != nil {
		t.Fatalf("GetMCPConnection() error = %v", err)
	}
	if got.Status != "connected" || !reflect.DeepEqual(got.Tools, []string{"alpha", "beta"}) {
		t.Errorf("after SetMCPConnectionStatus() = %+v", got)
	}
	if err := s.SetMCPConnectionStatus(ctx, "mcp_bind", "error", nil); err != nil {
		t.Fatalf("SetMCPConnectionStatus(nil) error = %v", err)
	}
	got, err = s.GetMCPConnection(ctx, "mcp_bind")
	if err != nil {
		t.Fatalf("GetMCPConnection() error = %v", err)
	}
	if got.Status != "error" || got.Tools != nil {
		t.Errorf("after clearing tools = %+v", got)
	}
}

func TestMCPDelete(t *testing.T) {
	s, ctx := openTestStore(t)
	mustCreateMCP(t, ctx, s, domain.MCPConnection{
		ID: "mcp_del", ServerID: "s", Label: "del", Status: "connected", ConfigJSON: `{}`, CreatedAt: mcpBase,
	})
	if err := s.DeleteMCPConnection(ctx, "mcp_del"); err != nil {
		t.Fatalf("DeleteMCPConnection() error = %v", err)
	}
	if _, err := s.GetMCPConnection(ctx, "mcp_del"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetMCPConnection() after delete error = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteMCPConnection(ctx, "mcp_del"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteMCPConnection(missing) error = %v, want domain.ErrNotFound", err)
	}
}

func mcpIDs(in []domain.MCPConnection) []string {
	out := make([]string, len(in))
	for i, m := range in {
		out[i] = m.ID
	}
	return out
}
