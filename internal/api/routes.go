package api

import "net/http"

// RouteSpec is the contract-facing description of a route. The OpenAPI contract
// test compares this list with api/openapi.yaml.
type RouteSpec struct {
	Method      string
	Path        string
	Summary     string
	Implemented bool
}

type handlerFunc func(*Server, http.ResponseWriter, *http.Request)

type route struct {
	RouteSpec
	Handler handlerFunc
}

// routeTable is the single source of truth for control-plane routing. Every
// path+method in api/openapi.yaml must appear exactly once.
var routeTable = []route{
	{RouteSpec{http.MethodGet, "/api/v1/health", "Health check", true}, (*Server).handleHealth},
	{RouteSpec{http.MethodGet, "/api/v1/capabilities", "Runtime and feature capabilities", true}, (*Server).handleCapabilities},

	{RouteSpec{http.MethodGet, "/api/v1/harnesses", "List harnesses", true}, (*Server).handleListHarnesses},
	{RouteSpec{http.MethodPost, "/api/v1/harnesses/{id}/install", "Install a pinned harness version", true}, (*Server).handleInstallHarness},
	{RouteSpec{http.MethodGet, "/api/v1/jobs/{job_id}", "Get job status", true}, (*Server).handleGetJob},
	{RouteSpec{http.MethodPost, "/api/v1/jobs/{job_id}/cancel", "Cancel an installation job", true}, (*Server).handleCancelJob},

	{RouteSpec{http.MethodGet, "/api/v1/apps", "List desktop apps", true}, (*Server).handleListApps},
	{RouteSpec{http.MethodPost, "/api/v1/apps/{id}/install", "Install a desktop app", true}, (*Server).handleInstallApp},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/apps/{app_id}/launch", "Launch a desktop app", true}, (*Server).handleLaunchApp},

	{RouteSpec{http.MethodGet, "/api/v1/workspaces", "List workspaces", true}, (*Server).handleListWorkspaces},
	{RouteSpec{http.MethodPost, "/api/v1/workspaces", "Create a workspace", true}, (*Server).handleCreateWorkspace},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}", "Get a workspace", true}, (*Server).handleGetWorkspace},
	{RouteSpec{http.MethodDelete, "/api/v1/workspaces/{id}", "Delete a workspace", true}, (*Server).handleDeleteWorkspace},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}/files", "List workspace files", true}, (*Server).handleWorkspaceFiles},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}/file", "Read a workspace file", true}, (*Server).handleWorkspaceFile},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}/diff", "Get workspace diff", true}, (*Server).handleWorkspaceDiff},

	{RouteSpec{http.MethodGet, "/api/v1/states", "List states", true}, (*Server).handleListStates},
	{RouteSpec{http.MethodPost, "/api/v1/states", "Create a state", true}, (*Server).handleCreateState},
	{RouteSpec{http.MethodDelete, "/api/v1/states/{id}", "Delete a state", true}, (*Server).handleDeleteState},
	{RouteSpec{http.MethodPost, "/api/v1/states/{id}/reset", "Reset a state", true}, (*Server).handleResetState},

	{RouteSpec{http.MethodGet, "/api/v1/sandboxes", "List sandboxes", true}, (*Server).handleListSandboxes},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes", "Create a sandbox", true}, (*Server).handleCreateSandbox},
	{RouteSpec{http.MethodGet, "/api/v1/sandboxes/{id}", "Get a sandbox", true}, (*Server).handleGetSandbox},
	{RouteSpec{http.MethodDelete, "/api/v1/sandboxes/{id}", "Delete a sandbox", true}, (*Server).handleDeleteSandbox},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/stop", "Stop a sandbox", true}, (*Server).handleStopSandbox},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/terminal", "Open a terminal ticket", true}, (*Server).handleTerminalTicket},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/desktop", "Open a desktop ticket", true}, (*Server).handleDesktopTicket},
	{RouteSpec{http.MethodGet, "/api/v1/sandboxes/{id}/services", "List sandbox services", true}, (*Server).handleSandboxServices},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/services/{service_id}/ticket", "Create a service tunnel ticket", true}, (*Server).handleServiceTicket},
	{RouteSpec{http.MethodGet, "/api/v1/sandboxes/{id}/stats", "Get sandbox stats", true}, (*Server).handleSandboxStats},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/scan", "Scan sandbox for secrets", true}, (*Server).handleSandboxScan},

	{RouteSpec{http.MethodGet, "/api/v1/providers", "List credential providers", true}, (*Server).handleProviders},
	{RouteSpec{http.MethodGet, "/api/v1/credentials", "List credentials", true}, (*Server).handleListCredentials},
	{RouteSpec{http.MethodPost, "/api/v1/credentials", "Create a credential", true}, (*Server).handleCreateCredential},
	{RouteSpec{http.MethodDelete, "/api/v1/credentials/{id}", "Delete a credential", true}, (*Server).handleDeleteCredential},
	{RouteSpec{http.MethodPost, "/api/v1/credentials/{id}/test", "Test a credential", true}, (*Server).handleTestCredential},

	{RouteSpec{http.MethodGet, "/api/v1/oauth/providers", "List OAuth providers", true}, (*Server).handleOAuthProviders},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/start", "Start an OAuth flow", true}, (*Server).handleOAuthStart},
	{RouteSpec{http.MethodGet, "/api/v1/oauth/callback", "OAuth callback", true}, (*Server).handleOAuthCallback},
	{RouteSpec{http.MethodGet, "/api/v1/oauth/flows/{flow_id}", "Get OAuth flow status", true}, (*Server).handleOAuthFlow},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/flows/{flow_id}/cancel", "Cancel a pending OAuth flow", true}, (*Server).handleOAuthCancel},
	{RouteSpec{http.MethodDelete, "/api/v1/oauth/connections/{credential_id}", "Delete an OAuth connection", true}, (*Server).handleOAuthDeleteConnection},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/connections/{credential_id}/refresh", "Refresh an OAuth connection", true}, (*Server).handleOAuthRefresh},
	{RouteSpec{http.MethodGet, "/api/v1/oauth/importable", "List importable host logins", true}, (*Server).handleOAuthImportable},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/import", "Import a host login", true}, (*Server).handleOAuthImport},

	{RouteSpec{http.MethodGet, "/api/v1/mcp/catalog", "List MCP catalog", true}, (*Server).handleMCPCatalog},
	{RouteSpec{http.MethodGet, "/api/v1/mcp/connections", "List MCP connections", true}, (*Server).handleListMCPConnections},
	{RouteSpec{http.MethodPost, "/api/v1/mcp/connections", "Create an MCP connection", true}, (*Server).handleCreateMCPConnection},
	{RouteSpec{http.MethodDelete, "/api/v1/mcp/connections/{id}", "Delete an MCP connection", true}, (*Server).handleDeleteMCPConnection},
	{RouteSpec{http.MethodPost, "/api/v1/mcp/connections/{id}/test", "Test an MCP connection", true}, (*Server).handleTestMCPConnection},
	{RouteSpec{http.MethodPost, "/api/v1/mcp/connections/{id}/oauth/start", "Start MCP OAuth", true}, (*Server).handleMCPOAuthStart},

	{RouteSpec{http.MethodGet, "/api/v1/audit", "List audit events", true}, (*Server).handleAudit},
	{RouteSpec{http.MethodGet, "/api/v1/events", "Server-sent events", true}, (*Server).handleEvents},
}

// RouteSpecs returns a copy of the route metadata for contract tests and docs.
func RouteSpecs() []RouteSpec {
	out := make([]RouteSpec, 0, len(routeTable))
	for _, route := range routeTable {
		out = append(out, route.RouteSpec)
	}
	return out
}
