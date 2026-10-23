package api

import (
	"encoding/base64"
	"net/http"
	"unicode/utf8"

	"github.com/Mrjwj34/nullkey/internal/core"
)

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	limit, cursor := pageParams(r)
	items, next, err := s.cfg.Service.ListWorkspaces(r.Context(), limit, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, next)
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		Name        string `json:"name"`
		RootPath    string `json:"root_path"`
		DefaultMode string `json:"default_mode"`
	}](w, r)
	if !ok {
		return
	}
	workspace, err := s.cfg.Service.CreateWorkspace(r.Context(), body.Name, body.RootPath, body.DefaultMode)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, workspace)
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	workspace, err := s.cfg.Service.GetWorkspace(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, workspace)
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	deleteFiles, err := queryBool(r, "delete_files")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.cfg.Service.DeleteWorkspace(r.Context(), r.PathValue("id"), deleteFiles, r.URL.Query().Get("confirm_name")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	items, err := s.cfg.Service.WorkspaceFiles(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, "")
}

func (s *Server) handleWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "path is required", nil)
		return
	}
	content, mime, truncated, err := s.cfg.Service.WorkspaceFile(r.Context(), r.PathValue("id"), path)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	encoding := "utf-8"
	text := string(content)
	if !utf8.Valid(content) || mime == "application/octet-stream" {
		encoding = "base64"
		text = base64.StdEncoding.EncodeToString(content)
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"content":   text,
		"encoding":  encoding,
		"truncated": truncated,
		"mime":      mime,
	})
}

func (s *Server) handleWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	patch, files, err := s.cfg.Service.WorkspaceDiff(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"patch": patch, "files": files})
}

func (s *Server) handleListStates(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	limit, cursor := pageParams(r)
	items, next, err := s.cfg.Service.ListStates(r.Context(), r.URL.Query().Get("harness_id"), limit, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, next)
}

func (s *Server) handleCreateState(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		HarnessID string `json:"harness_id"`
		Name      string `json:"name"`
	}](w, r)
	if !ok {
		return
	}
	state, err := s.cfg.Service.CreateState(r.Context(), body.HarnessID, body.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, state)
}

func (s *Server) handleDeleteState(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	confirm, err := queryBool(r, "confirm")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.cfg.Service.DeleteState(r.Context(), r.PathValue("id"), confirm); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetState(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		Confirm bool `json:"confirm"`
	}](w, r)
	if !ok {
		return
	}
	if err := s.cfg.Service.ResetState(r.Context(), r.PathValue("id"), body.Confirm); err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

var _ = core.Invalid
