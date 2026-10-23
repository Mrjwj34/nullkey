package api

import (
	"encoding/json"
	"net/http"
)

// ErrorCode is a stable, machine-readable API error code. Keep values in sync
// with docs/08-api-contract.md.
type ErrorCode string

const (
	CodeInvalidRequest     ErrorCode = "invalid_request"
	CodeUnauthorized       ErrorCode = "unauthorized"
	CodeNotFound           ErrorCode = "not_found"
	CodeConflict           ErrorCode = "conflict"
	CodeStateLocked        ErrorCode = "state_locked"
	CodeTokenInvalid       ErrorCode = "token_invalid"
	CodeReauthRequired     ErrorCode = "reauth_required"
	CodeRuntimeUnavailable ErrorCode = "runtime_unavailable"
	CodeDesktopUnavailable ErrorCode = "desktop_unavailable"
	CodeServiceNotFound    ErrorCode = "service_not_found"
	CodeTicketInvalid      ErrorCode = "ticket_invalid"
	CodeNetworkDenied      ErrorCode = "network_denied"
	CodeJobFailed          ErrorCode = "job_failed"
	CodeTerminalNotReady   ErrorCode = "terminal_not_ready"
	CodeInternal           ErrorCode = "internal"

	// CodeTerminalReplaced is delivered over the terminal WebSocket, not HTTP.
	// It tells the previous writable client to stop reconnecting.
	CodeTerminalReplaced ErrorCode = "terminal_replaced"

	// CodeNotImplemented is used by the P0 skeleton for endpoints that are
	// registered from OpenAPI but not implemented yet. It must be removed as
	// endpoints land; it is not part of the frozen v1 error set.
	CodeNotImplemented ErrorCode = "not_implemented"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id"`
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		s.log.Error("write_json_failed",
			"request_id", RequestIDFromContext(r.Context()),
			"error", err,
		)
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, message string, details map[string]any) {
	s.writeJSON(w, r, status, errorEnvelope{
		Error: errorBody{
			Code:      code,
			Message:   message,
			Details:   details,
			RequestID: RequestIDFromContext(r.Context()),
		},
	})
}
