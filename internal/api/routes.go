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
// path+method in api/openapi.yaml must appear exactly once. Implemented flips to
// true only when the endpoint has a real implementation and its acceptance
// assertion passes.
var routeTable = []route{
	{RouteSpec{http.MethodGet, "/api/v1/health", "Health check", true}, (*Server).handleHealth},
	{RouteSpec{http.MethodGet, "/api/v1/capabilities", "Runtime and feature capabilities", true}, (*Server).handleCapabilities},

	{RouteSpec{http.MethodGet, "/api/v1/harnesses", "List harnesses", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/harnesses/{id}/install", "Install a pinned harness version", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/jobs/{job_id}", "Get job status", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/jobs/{job_id}/cancel", "Cancel an installation job", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/apps", "List desktop apps", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/apps/{id}/install", "Install a desktop app", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/apps/{app_id}/launch", "Launch a desktop app", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/workspaces", "List workspaces", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/workspaces", "Create a workspace", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}", "Get a workspace", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodDelete, "/api/v1/workspaces/{id}", "Delete a workspace", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}/files", "List workspace files", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}/file", "Read a workspace file", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/workspaces/{id}/diff", "Get workspace diff", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/states", "List states", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/states", "Create a state", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodDelete, "/api/v1/states/{id}", "Delete a state", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/states/{id}/reset", "Reset a state", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/sandboxes", "List sandboxes", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes", "Create a sandbox", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/sandboxes/{id}", "Get a sandbox", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodDelete, "/api/v1/sandboxes/{id}", "Delete a sandbox", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/stop", "Stop a sandbox", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/terminal", "Open a terminal ticket", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/desktop", "Open a desktop ticket", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/sandboxes/{id}/services", "List sandbox services", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/services/{service_id}/ticket", "Create a service tunnel ticket", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/sandboxes/{id}/stats", "Get sandbox stats", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/sandboxes/{id}/scan", "Scan sandbox for secrets", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/providers", "List credential providers", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/credentials", "List credentials", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/credentials", "Create a credential", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodDelete, "/api/v1/credentials/{id}", "Delete a credential", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/credentials/{id}/test", "Test a credential", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/oauth/providers", "List OAuth providers", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/start", "Start an OAuth flow", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/oauth/callback", "OAuth callback", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/oauth/flows/{flow_id}", "Get OAuth flow status", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/flows/{flow_id}/cancel", "Cancel a pending OAuth flow", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodDelete, "/api/v1/oauth/connections/{credential_id}", "Delete an OAuth connection", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/connections/{credential_id}/refresh", "Refresh an OAuth connection", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/oauth/importable", "List importable host logins", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/oauth/import", "Import a host login", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/mcp/catalog", "List MCP catalog", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/mcp/connections", "List MCP connections", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/mcp/connections", "Create an MCP connection", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodDelete, "/api/v1/mcp/connections/{id}", "Delete an MCP connection", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/mcp/connections/{id}/test", "Test an MCP connection", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodPost, "/api/v1/mcp/connections/{id}/oauth/start", "Start MCP OAuth", false}, (*Server).handleNotImplemented},

	{RouteSpec{http.MethodGet, "/api/v1/audit", "List audit events", false}, (*Server).handleNotImplemented},
	{RouteSpec{http.MethodGet, "/api/v1/events", "Server-sent events", false}, (*Server).handleNotImplemented},
}

// RouteSpecs returns a copy of the route metadata for contract tests and docs.
func RouteSpecs() []RouteSpec {
	out := make([]RouteSpec, 0, len(routeTable))
	for _, route := range routeTable {
		out = append(out, route.RouteSpec)
	}
	return out
}
