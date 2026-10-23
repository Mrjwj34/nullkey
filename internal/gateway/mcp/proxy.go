package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// Proxy forwards one sandbox JSON-RPC request to the configured host-side MCP
// server. It streams the upstream response without buffering it, and enforces
// the connection tool allowlist before contacting upstream.
func (p *Proxy) Proxy(ctx context.Context, conn Connection, w http.ResponseWriter, r *http.Request) error {
	if r == nil {
		e := newError(http.StatusBadRequest, errCodeInvalidRequest, "mcp request is required")
		writeProxyError(w, e)
		return e
	}
	if ctx == nil {
		ctx = r.Context()
	}
	start := time.Now()
	logger := p.logger()

	body, err := readBody(r.Body)
	if err != nil {
		e := newError(http.StatusBadRequest, errCodeInvalidRequest, "failed to read mcp request body")
		writeProxyError(w, e)
		logger.Warn("mcp_request_failed",
			"connection_id", conn.ID,
			"server_id", conn.ServerID,
			"method", r.Method,
			"status", e.Status,
			"code", e.Code,
		)
		return e
	}

	if len(conn.AllowedTools) > 0 {
		if names, found := inspectToolCalls(body); found {
			for _, name := range names {
				if !toolAllowed(conn.AllowedTools, name) {
					e := newError(http.StatusForbidden, errCodeToolDenied, "tool is not allowed for this connection")
					writeProxyError(w, e)
					logger.Warn("mcp_tool_denied",
						"connection_id", conn.ID,
						"server_id", conn.ServerID,
						"method", r.Method,
						"status", e.Status,
						"code", e.Code,
					)
					return e
				}
			}
		}
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := p.newRequest(ctx, r.Method, conn.BaseURL, reqBody)
	if err != nil {
		e, ok := err.(*Error)
		if !ok {
			e = newError(http.StatusBadRequest, errCodeInvalidRequest, "invalid mcp request")
		}
		writeProxyError(w, e)
		logger.Warn("mcp_request_failed",
			"connection_id", conn.ID,
			"server_id", conn.ServerID,
			"method", r.Method,
			"status", e.Status,
			"code", e.Code,
		)
		return e
	}
	req.Header = upstreamHeader(r.Header)
	applyCredential(req.Header, conn)

	resp, err := p.do(req)
	if err != nil {
		e := newError(http.StatusServiceUnavailable, errCodeMCPUnavailable, "mcp server is unavailable")
		writeProxyError(w, e)
		logger.Error("mcp_upstream_unavailable",
			"connection_id", conn.ID,
			"server_id", conn.ServerID,
			"method", r.Method,
			"status", e.Status,
			"code", e.Code,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return e
	}
	defer resp.Body.Close()

	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	// A Streamable HTTP MCP client must echo the session id returned by the
	// server; pass that protocol header through without copying other headers.
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}
	w.WriteHeader(resp.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	written, copyErr := flushCopy(w, resp.Body)
	logger.Debug("mcp_proxy",
		"connection_id", conn.ID,
		"server_id", conn.ServerID,
		"method", r.Method,
		"status", resp.StatusCode,
		"bytes", written,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	if copyErr != nil {
		return errors.New("mcp: response stream interrupted")
	}
	return nil
}

func readBody(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	return io.ReadAll(body)
}

// upstreamHeader clones sandbox request headers and removes credentials and
// hop-by-hop headers before the gateway injects its own credential.
func upstreamHeader(src http.Header) http.Header {
	dst := src.Clone()
	if dst == nil {
		dst = make(http.Header)
	}
	for _, value := range src.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if token = strings.TrimSpace(token); token != "" {
				dst.Del(token)
			}
		}
	}
	for _, header := range hopByHopHeaders {
		dst.Del(header)
	}
	dst.Del("Authorization")
	dst.Del("Cookie")
	dst.Del("Proxy-Authorization")
	return dst
}

func inspectToolCalls(body []byte) ([]string, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, false
	}
	switch body[0] {
	case '{':
		name, ok := toolCallName(body)
		if !ok {
			return nil, false
		}
		return []string{name}, true
	case '[':
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, false
		}
		var names []string
		for _, item := range batch {
			if name, ok := toolCallName(item); ok {
				names = append(names, name)
			}
		}
		return names, len(names) > 0
	default:
		return nil, false
	}
}

func toolCallName(body []byte) (string, bool) {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Method != "tools/call" {
		return "", false
	}
	var params struct {
		Name string `json:"name"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	return params.Name, true
}

func toolAllowed(allowed []string, name string) bool {
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}

// flushCopy copies src to w, flushing after every read so SSE chunks reach the
// sandbox without waiting for upstream EOF.
func flushCopy(w http.ResponseWriter, src io.Reader) (int64, error) {
	var total int64
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := w.Write(buf[:n])
			total += int64(written)
			if flusher != nil {
				flusher.Flush()
			}
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}
