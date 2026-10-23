package api

import (
	"net/http"

	"github.com/Mrjwj34/nullkey/internal/core"
)

func (s *Server) handleMCPCatalog(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	s.writeList(w, r, http.StatusOK, s.cfg.Service.MCPCatalog(), "")
}

func (s *Server) handleListMCPConnections(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	limit, cursor := pageParams(r)
	items, next, err := s.cfg.Service.ListMCPConnections(r.Context(), limit, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, next)
}

func (s *Server) handleCreateMCPConnection(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		ServerID     string   `json:"server_id"`
		Label        string   `json:"label"`
		CredentialID string   `json:"credential_id"`
		AuthType     string   `json:"auth_type"`
		Tools        []string `json:"tools"`
	}](w, r)
	if !ok {
		return
	}
	conn, err := s.cfg.Service.CreateMCPConnection(r.Context(), core.CreateMCPConnectionRequest{
		ServerID:     body.ServerID,
		Label:        body.Label,
		CredentialID: body.CredentialID,
		AuthType:     body.AuthType,
		Tools:        body.Tools,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, conn)
}

func (s *Server) handleDeleteMCPConnection(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	if err := s.cfg.Service.DeleteMCPConnection(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestMCPConnection(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	tools, err := s.cfg.Service.TestMCPConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"ok": true, "tools": tools})
}

func (s *Server) handleMCPOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	flowID, authURL, err := s.cfg.Service.StartMCPOAuth(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"flow_id": flowID, "auth_url": authURL, "expires_at": nil})
}
