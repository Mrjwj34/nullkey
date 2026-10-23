//go:build acceptance

package acceptance

import "testing"

// Pending assertions are executable placeholders. In normal CI they skip; in
// strict mode (NULLKEY_ACCEPTANCE_STRICT=1) they fail until implemented.
// Replace the body with the real black-box assertion when the feature lands.

func TestAC_API_008_IdempotencyPagination(t *testing.T) {
	pending(t, "AC-API-008", "requires resource CRUD, idempotency store, and pagination")
}

func TestAC_API_009_SSEEvents(t *testing.T) {
	pending(t, "AC-API-009", "requires event bus and SSE handler")
}

func TestAC_API_010_JobCancel(t *testing.T) {
	pending(t, "AC-API-010", "requires install jobs and cancellation state machine")
}

func TestAC_API_011_OAuthCancel(t *testing.T) {
	pending(t, "AC-API-011", "requires OAuth broker and cancellation semantics")
}

func TestAC_SEC_001_SandboxEnv(t *testing.T) {
	pending(t, "AC-SEC-001", "requires native runtime and sandbox exec")
}

func TestAC_SEC_002_HostFsIsolation(t *testing.T) {
	pending(t, "AC-SEC-002", "requires Seatbelt profile and sandbox exec")
}

func TestAC_SEC_003_TokenFormatAndHash(t *testing.T) {
	pending(t, "AC-SEC-003", "requires sandbox token service and SQLite")
}

func TestAC_SEC_004_StopRevokesToken(t *testing.T) {
	pending(t, "AC-SEC-004", "requires token lifecycle and data-plane gateway")
}

func TestAC_SEC_005_RestartFailsClosed(t *testing.T) {
	pending(t, "AC-SEC-005", "requires daemon restart reconciliation")
}

func TestAC_SEC_006_LogRedaction(t *testing.T) {
	pending(t, "AC-SEC-006", "requires credential storage and log scanning")
}

func TestAC_SEC_007_CredentialsNoPlaintext(t *testing.T) {
	pending(t, "AC-SEC-007", "requires credentials API and Keychain adapter")
}

func TestAC_SEC_008_KeychainNoArgv(t *testing.T) {
	pending(t, "AC-SEC-008", "requires Keychain adapter and process inspection")
}

func TestAC_SEC_009_TokenPlaneSeparation(t *testing.T) {
	pending(t, "AC-SEC-009", "requires separate control and data-plane listeners")
}

func TestAC_SEC_010_NoSecretScan(t *testing.T) {
	pending(t, "AC-SEC-010", "requires scan endpoint, generated config, and harness")
}

func TestAC_RT_001_SandboxLifecycle(t *testing.T) {
	pending(t, "AC-RT-001", "requires native runtime and sandbox persistence")
}

func TestAC_RT_002_TerminalWebSocket(t *testing.T) {
	pending(t, "AC-RT-002", "requires PTY, tickets, and WebSocket ready protocol")
}

func TestAC_RT_003_SeatbeltFailClosed(t *testing.T) {
	pending(t, "AC-RT-003", "requires Seatbelt profile integration")
}

func TestAC_RT_004_WorkspaceLock(t *testing.T) {
	pending(t, "AC-RT-004", "requires workspace locks and sandbox service")
}

func TestAC_RT_005_PersistenceAndRotation(t *testing.T) {
	pending(t, "AC-RT-005", "requires persistent workspace/state and token rotation")
}

func TestAC_RT_006_Stats(t *testing.T) {
	pending(t, "AC-RT-006", "requires runtime process stats")
}

func TestAC_RT_007_GracefulShutdown(t *testing.T) {
	pending(t, "AC-RT-007", "requires sandbox supervisor and shutdown reconciliation")
}

func TestAC_RT_008_TerminalReconnect(t *testing.T) {
	pending(t, "AC-RT-008", "requires persistent PTY and single-writer replacement")
}

func TestAC_RT_009_TerminalState(t *testing.T) {
	pending(t, "AC-RT-009", "requires terminal readiness and sandbox state tracking")
}

func TestAC_NET_001_ModelGateway(t *testing.T) {
	pending(t, "AC-NET-001", "requires model gateway and credential injection")
}

func TestAC_NET_002_InvalidToken(t *testing.T) {
	pending(t, "AC-NET-002", "requires token validation on the data plane")
}

func TestAC_NET_003_EgressAllowlist(t *testing.T) {
	pending(t, "AC-NET-003", "requires egress proxy and allowlist policy")
}

func TestAC_NET_004_DirectOutboundBlocked(t *testing.T) {
	pending(t, "AC-NET-004", "requires Seatbelt network rules and proxy env")
}

func TestAC_NET_005_GatewayLatency(t *testing.T) {
	pending(t, "AC-NET-005", "requires gateway and SLO benchmark harness")
}

func TestAC_NET_006_GitProxy(t *testing.T) {
	pending(t, "AC-NET-006", "requires git smart HTTP proxy")
}

func TestAC_NET_007_FailClosed(t *testing.T) {
	pending(t, "AC-NET-007", "requires gateway/proxy error mapping")
}

func TestAC_NET_008_ModeSemantics(t *testing.T) {
	pending(t, "AC-NET-008", "requires enforcement of all three network modes")
}

func TestAC_CRED_001_ApiKeyLifecycle(t *testing.T) {
	pending(t, "AC-CRED-001", "requires credentials API and Keychain adapter")
}

func TestAC_CRED_002_RevokeAffectsGateway(t *testing.T) {
	pending(t, "AC-CRED-002", "requires credentials and model gateway")
}

func TestAC_CRED_003_OAuthPKCE(t *testing.T) {
	pending(t, "AC-CRED-003", "requires OAuth broker and browser callback")
}

func TestAC_CRED_004_HostImport(t *testing.T) {
	pending(t, "AC-CRED-004", "requires host login discovery and import")
}

func TestAC_CRED_005_ProviderRegistry(t *testing.T) {
	pending(t, "AC-CRED-005", "requires provider registry")
}

func TestAC_MCP_001_GitHubConnection(t *testing.T) {
	pending(t, "AC-MCP-001", "requires MCP gateway and GitHub server")
}

func TestAC_MCP_002_ToolCall(t *testing.T) {
	pending(t, "AC-MCP-002", "requires MCP tool proxy and credential injection")
}

func TestAC_MCP_003_AllowlistAndRevoke(t *testing.T) {
	pending(t, "AC-MCP-003", "requires MCP allowlist and connection lifecycle")
}

func TestAC_STATE_001_WorkspaceValidation(t *testing.T) {
	pending(t, "AC-STATE-001", "requires workspace service, storage_kind, and path validation")
}

func TestAC_STATE_002_WorkspaceDelete(t *testing.T) {
	pending(t, "AC-STATE-002", "requires workspace delete semantics and locks")
}

func TestAC_STATE_003_StateResetDelete(t *testing.T) {
	pending(t, "AC-STATE-003", "requires state reset/delete semantics and locks")
}

func TestAC_STATE_004_SandboxDelete(t *testing.T) {
	pending(t, "AC-STATE-004", "requires sandbox delete parameter rejection and persistence")
}

func TestAC_ART_001_FileSafety(t *testing.T) {
	pending(t, "AC-ART-001", "requires artifact reader and path canonicalization")
}

func TestAC_ART_002_Diff(t *testing.T) {
	pending(t, "AC-ART-002", "requires git diff artifact API")
}

func TestAC_SVC_001_Lifecycle(t *testing.T) {
	pending(t, "AC-SVC-001", "requires service supervisor and manifest")
}

func TestAC_SVC_002_LoopbackAuth(t *testing.T) {
	pending(t, "AC-SVC-002", "requires service ports and generated auth")
}

func TestAC_SVC_003_RestartLogs(t *testing.T) {
	pending(t, "AC-SVC-003", "requires service restart policy and redacted logs")
}

func TestAC_TUN_001_ServiceTicket(t *testing.T) {
	pending(t, "AC-TUN-001", "requires service registry and one-time tickets")
}

func TestAC_TUN_002_HttpWsSse(t *testing.T) {
	pending(t, "AC-TUN-002", "requires tunnel proxy for HTTP/WS/SSE")
}

func TestAC_TUN_003_UndeclaredService(t *testing.T) {
	pending(t, "AC-TUN-003", "requires service registry enforcement")
}

func TestAC_AUD_001_AuditEvents(t *testing.T) {
	pending(t, "AC-AUD-001", "requires audit store, redaction, and event sources")
}

func TestAC_SLO_001_ColdStartAndMemory(t *testing.T) {
	pending(t, "AC-SLO-001", "requires benchmark harness, terminal ready, and RSS sampling")
}

func TestAC_SLO_002_TerminalAndGatewayRTT(t *testing.T) {
	pending(t, "AC-SLO-002", "requires benchmark harness and controlled upstream stub")
}

func TestAC_SLO_003_TunnelAndArtifacts(t *testing.T) {
	pending(t, "AC-SLO-003", "requires tunnel and artifact benchmark harness")
}

func TestAC_SLO_004_Concurrency(t *testing.T) {
	pending(t, "AC-SLO-004", "requires multi-sandbox benchmark harness")
}

func TestAC_SLO_005_HarnessReadiness(t *testing.T) {
	pending(t, "AC-SLO-005", "requires per-harness readiness probe and benchmark harness")
}
