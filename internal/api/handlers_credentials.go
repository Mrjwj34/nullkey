package api

import (
	"net/http"
	"time"

	"github.com/Mrjwj34/nullkey/internal/core"
)

func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	limit, cursor := pageParams(r)
	items, next, err := s.cfg.Service.ListCredentials(r.Context(), limit, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, next)
}

func (s *Server) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		ProviderID string `json:"provider_id"`
		Label      string `json:"label"`
		AuthType   string `json:"auth_type"`
		Value      string `json:"value"`
		BaseURL    string `json:"base_url"`
		HeaderName string `json:"header_name"`
	}](w, r)
	if !ok {
		return
	}
	cred, err := s.cfg.Service.CreateCredential(r.Context(), core.CreateCredentialRequest{
		ProviderID: body.ProviderID,
		Label:      body.Label,
		AuthType:   body.AuthType,
		Value:      body.Value,
		BaseURL:    body.BaseURL,
		HeaderName: body.HeaderName,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, cred)
}

func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	if err := s.cfg.Service.DeleteCredential(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestCredential(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	ok, latency, message, err := s.cfg.Service.TestCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var errText *string
	if message != "" {
		errText = &message
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"ok": ok, "latency_ms": latency, "error": errText})
}

func (s *Server) handleOAuthProviders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	s.writeList(w, r, http.StatusOK, s.cfg.Service.OAuthProviders(), "")
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		ProviderID string `json:"provider_id"`
		Label      string `json:"label"`
	}](w, r)
	if !ok {
		return
	}
	flow, authURL, err := s.cfg.Service.StartOAuth(r.Context(), body.ProviderID, body.Label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"flow_id":    flow.ID,
		"auth_url":   authURL,
		"expires_at": flow.ExpiresAt,
	})
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	_, err := s.cfg.Service.CompleteOAuth(r.Context(), code, state)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		status := http.StatusBadRequest
		if ce, ok := core.AsError(err); ok && ce.Status >= 500 {
			status = ce.Status
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("<!doctype html><html><body><h1>Nullkey OAuth failed</h1><p>You can close this window.</p></body></html>"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("<!doctype html><html><body><h1>Nullkey connected</h1><p>You can close this window.</p></body></html>"))
}

func (s *Server) handleOAuthFlow(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	flow, err := s.cfg.Service.GetOAuthFlow(r.Context(), r.PathValue("flow_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, flow)
}

func (s *Server) handleOAuthCancel(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	flow, err := s.cfg.Service.CancelOAuthFlow(r.Context(), r.PathValue("flow_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, flow)
}

func (s *Server) handleOAuthDeleteConnection(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	if err := s.cfg.Service.DeleteCredential(r.Context(), r.PathValue("credential_id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	expires, err := s.cfg.Service.RefreshOAuth(r.Context(), r.PathValue("credential_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"ok": true, "expires_at": expires.UTC().Format(time.RFC3339)})
}

func (s *Server) handleOAuthImportable(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	s.writeList(w, r, http.StatusOK, s.cfg.Service.ImportableLogins(), "")
}

func (s *Server) handleOAuthImport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		ProviderID string `json:"provider_id"`
		Source     string `json:"source"`
		Label      string `json:"label"`
	}](w, r)
	if !ok {
		return
	}
	result, err := s.cfg.Service.ImportHostLogin(r.Context(), body.ProviderID, body.Source, body.Label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, result)
}
