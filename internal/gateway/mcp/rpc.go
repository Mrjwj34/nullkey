package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// rpcResponse is the subset of a JSON-RPC 2.0 response the gateway needs.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (r *rpcResponse) isResponse() bool {
	return r != nil && (len(r.Result) > 0 || r.Error != nil)
}

// rpcRequest is the subset of a JSON-RPC 2.0 request needed for tool
// allowlist enforcement.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Test performs the MCP initialization handshake followed by tools/list.
func (p *Proxy) Test(ctx context.Context, conn Connection) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayTestTimeout)
	defer cancel()

	sessionID, err := p.initialize(ctx, conn)
	if err != nil {
		return nil, err
	}
	return p.listTools(ctx, conn, sessionID)
}

func (p *Proxy) initialize(ctx context.Context, conn Connection) (string, error) {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "nullkey",
			"version": "0.1.0",
		},
	}
	body, err := json.Marshal(struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      int            `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  params,
	})
	if err != nil {
		return "", newError(http.StatusInternalServerError, errCodeMCPError, "failed to build MCP initialize request")
	}
	resp, headers, err := p.sendRPC(ctx, conn, body, "")
	if err != nil {
		return "", err
	}
	if _, err := decodeResult(resp); err != nil {
		return "", err
	}
	return headers.Get("Mcp-Session-Id"), nil
}

func (p *Proxy) listTools(ctx context.Context, conn Connection, sessionID string) ([]string, error) {
	body, err := json.Marshal(struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      int            `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]any{},
	})
	if err != nil {
		return nil, newError(http.StatusInternalServerError, errCodeMCPError, "failed to build MCP tools/list request")
	}
	resp, _, err := p.sendRPC(ctx, conn, body, sessionID)
	if err != nil {
		return nil, err
	}
	result, err := decodeResult(resp)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, newError(http.StatusBadGateway, errCodeMCPProtocol, "mcp server returned an invalid tools/list result")
	}
	tools := make([]string, 0, len(parsed.Tools))
	for _, tool := range parsed.Tools {
		tools = append(tools, tool.Name)
	}
	return tools, nil
}

func decodeResult(resp *rpcResponse) (json.RawMessage, error) {
	if resp == nil || resp.Error != nil {
		return nil, newError(http.StatusBadGateway, errCodeMCPError, "mcp server rejected the request")
	}
	if len(resp.Result) == 0 {
		return nil, newError(http.StatusBadGateway, errCodeMCPProtocol, "mcp server returned no result")
	}
	return resp.Result, nil
}

// sendRPC sends one JSON-RPC request and decodes the response. It accepts both
// application/json and text/event-stream responses.
func (p *Proxy) sendRPC(ctx context.Context, conn Connection, body []byte, sessionID string) (*rpcResponse, http.Header, error) {
	req, err := p.newRequest(ctx, http.MethodPost, conn.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	applyCredential(req.Header, conn)

	resp, err := p.do(req)
	if err != nil {
		return nil, nil, newError(http.StatusServiceUnavailable, errCodeMCPUnavailable, "mcp server is unavailable")
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, newError(http.StatusBadGateway, errCodeMCPUpstream, "mcp server returned an unsuccessful HTTP status")
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, newError(http.StatusBadGateway, errCodeMCPUpstream, "failed to read mcp server response")
	}
	rpc, err := parseRPCResponse(resp.Header.Get("Content-Type"), raw)
	if err != nil {
		return nil, nil, newError(http.StatusBadGateway, errCodeMCPProtocol, "mcp server returned an invalid response")
	}
	if rpc.Error != nil {
		return nil, nil, newError(http.StatusBadGateway, errCodeMCPError, "mcp server rejected the request")
	}
	return rpc, resp.Header, nil
}

func parseRPCResponse(contentType string, body []byte) (*rpcResponse, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return parseJSONRPC(body)
	}
	if isEventStream(contentType) || looksLikeSSE(body) {
		return parseSSE(body)
	}
	return parseJSONRPC(body)
}

func isEventStream(contentType string) bool {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.EqualFold(strings.TrimSpace(contentType), "text/event-stream")
}

func looksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("data:")) ||
		bytes.HasPrefix(trimmed, []byte(":"))
}

var (
	errNoRPCResponse      = errors.New("mcp: no JSON-RPC response")
	errInvalidRPCResponse = errors.New("mcp: invalid JSON-RPC response")
)

func parseJSONRPC(body []byte) (*rpcResponse, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errNoRPCResponse
	}
	if body[0] == '[' {
		var batch []rpcResponse
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, errInvalidRPCResponse
		}
		for i := range batch {
			if batch[i].isResponse() {
				return &batch[i], nil
			}
		}
		return nil, errNoRPCResponse
	}
	var resp rpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errInvalidRPCResponse
	}
	if !resp.isResponse() {
		return nil, errNoRPCResponse
	}
	return &resp, nil
}

// parseSSE parses an SSE stream and returns the first JSON-RPC response event.
// Non-JSON data events (for example legacy endpoint events) are ignored.
func parseSSE(body []byte) (*rpcResponse, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var data []string

	dispatch := func() *rpcResponse {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		resp, err := parseJSONRPC([]byte(payload))
		if err != nil {
			return nil
		}
		return resp
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if resp := dispatch(); resp != nil {
				return resp, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if field != "data" {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		data = append(data, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, errInvalidRPCResponse
	}
	if resp := dispatch(); resp != nil {
		return resp, nil
	}
	return nil, errNoRPCResponse
}
