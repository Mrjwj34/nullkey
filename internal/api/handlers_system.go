package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

type jobResponse struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	Cancellable bool    `json:"cancellable"`
	Progress    float64 `json:"progress"`
	Message     *string `json:"message"`
	Error       *string `json:"error"`
}

func jobDTO(job domain.Job) jobResponse {
	return jobResponse{
		ID:          job.ID,
		Type:        job.Type,
		Status:      string(job.Status),
		Cancellable: job.Cancellable,
		Progress:    job.Progress,
		Message:     job.Message,
		Error:       job.Error,
	}
}

func (s *Server) handleListHarnesses(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	s.writeList(w, r, http.StatusOK, s.cfg.Service.ListHarnesses(), "")
}

func (s *Server) handleInstallHarness(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	body, ok := decodeBody[struct {
		Version string `json:"version"`
	}](w, r)
	if !ok {
		return
	}
	job, err := s.cfg.Service.InstallHarness(r.Context(), r.PathValue("id"), body.Version)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	job, err := s.cfg.Service.GetJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, jobDTO(job))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	job, err := s.cfg.Service.CancelJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, jobDTO(job))
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	s.writeList(w, r, http.StatusOK, s.cfg.Service.ListApps(), "")
}

func (s *Server) handleInstallApp(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotImplemented, CodeDesktopUnavailable, "desktop apps are not available in this runtime", nil)
}

func (s *Server) handleLaunchApp(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotImplemented, CodeDesktopUnavailable, "desktop apps are not available in this runtime", nil)
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	s.writeList(w, r, http.StatusOK, s.cfg.Service.Providers(), "")
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil || s.cfg.Service.Audit == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	limit, cursor := pageParams(r)
	items, next, err := s.cfg.Service.Audit.List(r.Context(), limit, cursor, r.URL.Query().Get("type"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeList(w, r, http.StatusOK, items, next)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Service == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "kernel is not ready", nil)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, CodeInternal, "streaming is unavailable", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ch, cancel, err := s.cfg.Service.Events.Subscribe(r.Context())
	if err != nil {
		return
	}
	defer cancel()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(event.Data)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("event: " + event.Type + "\ndata: " + string(payload) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
