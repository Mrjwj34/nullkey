package api

import (
	"net/http"

	"github.com/Mrjwj34/nullkey/internal/core"
)

func (s *Server) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	limit, cursor := pageParams(r)
	items, next, err := s.cfg.Service.ListSandboxes(r.Context(), limit, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, next)
}

func (s *Server) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		HarnessID   string `json:"harness_id"`
		WorkspaceID string `json:"workspace_id"`
		StateID     string `json:"state_id"`
		Runtime     string `json:"runtime"`
		NetworkMode string `json:"network_mode"`
		Mounts      []struct {
			Source   string `json:"source"`
			Target   string `json:"target"`
			Mode     string `json:"mode"`
			HostPath string `json:"host_path"`
		} `json:"mounts"`
	}](w, r)
	if !ok {
		return
	}
	for _, mount := range body.Mounts {
		switch mount.Source {
		case "workspace", "state", "cache":
			if mount.Mode != "" && mount.Mode != "rw" && mount.Mode != "ro" {
				s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "mount mode must be rw or ro", nil)
				return
			}
		case "host":
			s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "host mounts require an explicit confirmation flow that is not available in v1", nil)
			return
		default:
			s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "unknown mount source", nil)
			return
		}
	}
	sb, err := s.cfg.Service.CreateSandbox(r.Context(), core.CreateSandboxRequest{
		HarnessID:   body.HarnessID,
		WorkspaceID: body.WorkspaceID,
		StateID:     body.StateID,
		Runtime:     body.Runtime,
		NetworkMode: body.NetworkMode,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, sb)
}

func (s *Server) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	sb, err := s.cfg.Service.GetSandbox(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, sb)
}

func (s *Server) handleDeleteSandbox(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	// Legacy cascade parameters are rejected even when false.
	if r.URL.Query().Has("delete_state") || r.URL.Query().Has("delete_workspace") {
		s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "delete_state and delete_workspace are not supported; delete workspace/state separately", nil)
		return
	}
	if err := s.cfg.Service.DeleteSandbox(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStopSandbox(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	if err := s.cfg.Service.StopSandbox(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	ticket, err := s.cfg.Service.TerminalTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, ticket)
}

func (s *Server) handleDesktopTicket(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotImplemented, CodeDesktopUnavailable, "desktop runtime is not available", nil)
}

func (s *Server) handleSandboxServices(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	items, err := s.cfg.Service.ListSandboxServices(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, "")
}

func (s *Server) handleServiceTicket(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	ticket, err := s.cfg.Service.ServiceTicket(r.Context(), r.PathValue("id"), r.PathValue("service_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, ticket)
}

func (s *Server) handleSandboxStats(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	stats, err := s.cfg.Service.Stats(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, stats)
}

func (s *Server) handleSandboxScan(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	result, err := s.cfg.Service.Scan(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, result)
}
