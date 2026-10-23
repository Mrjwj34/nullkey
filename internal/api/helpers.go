package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mrjwj34/nullkey/internal/core"
)

type listResponse struct {
	Items      any     `json:"items"`
	NextCursor *string `json:"next_cursor,omitempty"`
}

func (s *Server) writeList(w http.ResponseWriter, r *http.Request, status int, items any, next string) {
	var cursor *string
	if next != "" {
		cursor = &next
	}
	s.writeJSON(w, r, status, listResponse{Items: items, NextCursor: cursor})
}

func pageParams(r *http.Request) (int, string) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	return limit, r.URL.Query().Get("cursor")
}

func queryBool(r *http.Request, key string) (bool, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, core.Invalid(key + " must be a boolean")
	}
	return value, nil
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var out T
	if r.Body == nil {
		var zero T
		return zero, true
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&out); err != nil {
		if errors.Is(err, io.EOF) {
			var zero T
			return zero, true
		}
		writeAPIError(w, r, http.StatusBadRequest, CodeInvalidRequest, "request body is not valid JSON", nil)
		return out, false
	}
	return out, true
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code ErrorCode, message string, details map[string]any) {
	// Reuse the envelope shape without a *Server receiver.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := errorEnvelope{Error: errorBody{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: RequestIDFromContext(r.Context()),
	}}
	raw, _ := json.Marshal(body)
	_, _ = w.Write(append(raw, '\n'))
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	if ce, ok := core.AsError(err); ok {
		s.writeError(w, r, ce.Status, ErrorCode(ce.Code), ce.Message, ce.Details)
		return
	}
	s.log.Error("handler_failed", "request_id", RequestIDFromContext(r.Context()), "error", err)
	s.writeError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil)
}

// idempotency implements the POST Idempotency-Key contract. The response is
// buffered so a retry returns the original status/body.
func (s *Server) idempotency(next handlerFunc, w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || s.cfg.Service == nil {
		next(s, w, r)
		return
	}
	if len(key) > 200 {
		s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "Idempotency-Key is too long", nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "could not read request body", nil)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	stored, found, lookupErr := s.cfg.Service.LookupIdempotent(r.Context(), key, r.Method, r.URL.Path, body)
	if lookupErr != nil {
		s.fail(w, r, lookupErr)
		return
	}
	if found {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(stored.Status)
		body := rewriteErrorRequestID(stored.Body, RequestIDFromContext(r.Context()))
		_, _ = w.Write(body)
		return
	}
	rec := newRecorder()
	next(s, rec, r)
	copyRecorderHeaders(w.Header(), rec.header)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
	s.cfg.Service.StoreIdempotent(r.Context(), key, r.Method, r.URL.Path, body, rec.status, rec.body.Bytes())
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newRecorder() *recorder { return &recorder{header: make(http.Header)} }

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}
func (r *recorder) Flush() {}

// rewriteErrorRequestID keeps a replayed error envelope consistent with the
// fresh X-Request-Id generated for the retry.
func rewriteErrorRequestID(body []byte, requestID string) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	inner, ok := envelope["error"].(map[string]any)
	if !ok {
		return body
	}
	inner["request_id"] = requestID
	raw, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return append(raw, '\n')
}

func copyRecorderHeaders(dst, src http.Header) {
	for key, values := range src {
		if key == "Content-Length" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
