package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	localTestCredential = "sk-test-credential"
	localTestToolBody   = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`
)

func localTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func localWriteJSONResponse(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func localWriteSSEResponse(t *testing.T, w http.ResponseWriter, events ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for _, event := range events {
		if _, err := io.WriteString(w, event); err != nil {
			t.Errorf("write SSE response: %v", err)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func TestLocalMCP_TestPlainJSON(t *testing.T) {
	var (
		mu        sync.Mutex
		methods   []string
		sessionOK bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+localTestCredential {
			t.Errorf("Authorization = %q, want injected bearer credential", got)
		}
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		methods = append(methods, req.Method)
		mu.Unlock()

		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-test")
			localWriteJSONResponse(t, w, http.StatusOK,
				`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake"}}}`)
		case "tools/list":
			mu.Lock()
			sessionOK = r.Header.Get("Mcp-Session-Id") == "session-test"
			mu.Unlock()
			localWriteJSONResponse(t, w, http.StatusOK,
				`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo"},{"name":"read_file"}]}}`)
		default:
			t.Errorf("unexpected method %q", req.Method)
			localWriteJSONResponse(t, w, http.StatusBadRequest, `{"jsonrpc":"2.0","id":0,"error":{"code":-32600,"message":"bad"}}`)
		}
	}))
	defer server.Close()

	proxy := New(nil, nil)
	tools, err := proxy.Test(nil, Connection{
		ID:         "mcp_test",
		ServerID:   "fake",
		BaseURL:    server.URL,
		Credential: localTestCredential,
	})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if got, want := strings.Join(tools, ","), "echo,read_file"; got != want {
		t.Fatalf("tools = %q, want %q", got, want)
	}
	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	gotSessionOK := sessionOK
	mu.Unlock()
	if got, want := strings.Join(gotMethods, ","), "initialize,tools/list"; got != want {
		t.Fatalf("methods = %q, want %q", got, want)
	}
	if !gotSessionOK {
		t.Fatal("tools/list did not forward Mcp-Session-Id from initialize")
	}
}

func TestLocalMCP_TestSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); !strings.Contains(got, "text/event-stream") {
			t.Errorf("Accept = %q, want text/event-stream", got)
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sse-session")
			localWriteSSEResponse(t, w,
				": keepalive\n\n",
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2024-11-05\"}}\n\n",
			)
		case "tools/list":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sse-session" {
				t.Errorf("Mcp-Session-Id = %q, want sse-session", got)
			}
			localWriteSSEResponse(t, w,
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n",
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\n"+
					"data: \"result\":{\"tools\":[{\"name\":\"alpha\"},{\"name\":\"beta\"}]}}\n\n",
			)
		default:
			t.Errorf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()

	proxy := New(server.Client(), localTestLogger())
	tools, err := proxy.Test(context.Background(), Connection{
		BaseURL:    server.URL,
		Credential: localTestCredential,
	})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if got, want := strings.Join(tools, ","), "alpha,beta"; got != want {
		t.Fatalf("tools = %q, want %q", got, want)
	}
}

func TestLocalMCP_TestCredentialHeaders(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		prefix     string
		headerName string
		want       string
	}{
		{
			name:       "default bearer",
			headerName: "Authorization",
			want:       "Bearer " + localTestCredential,
		},
		{
			name:       "custom header raw credential",
			header:     "X-Api-Key",
			headerName: "X-Api-Key",
			want:       localTestCredential,
		},
		{
			name:       "custom header and prefix",
			header:     "X-Api-Key",
			prefix:     "Token ",
			headerName: "X-Api-Key",
			want:       "Token " + localTestCredential,
		},
		{
			name:       "authorization custom prefix",
			header:     "Authorization",
			prefix:     "Basic ",
			headerName: "Authorization",
			want:       "Basic " + localTestCredential,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get(tt.headerName); got != tt.want {
					t.Errorf("%s = %q, want %q", tt.headerName, got, tt.want)
				}
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "initialize" {
					localWriteJSONResponse(t, w, http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`)
					return
				}
				localWriteJSONResponse(t, w, http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`)
			}))
			defer server.Close()

			proxy := New(server.Client(), localTestLogger())
			_, err := proxy.Test(context.Background(), Connection{
				BaseURL:          server.URL,
				Credential:       localTestCredential,
				CredentialHeader: tt.header,
				CredentialPrefix: tt.prefix,
			})
			if err != nil {
				t.Fatalf("Test() error = %v", err)
			}
		})
	}
}

func TestLocalMCP_TestErrors(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		baseURL    string
		ctx        context.Context
		wantCode   string
		wantInText bool
	}{
		{
			name: "upstream HTTP error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				localWriteJSONResponse(t, w, http.StatusInternalServerError, `{"error":"boom"}`)
			},
			wantCode: errCodeMCPUpstream,
		},
		{
			name: "JSON-RPC error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				localWriteJSONResponse(t, w, http.StatusOK,
					`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"sk-test-secret should not leak"}}`)
			},
			wantCode: errCodeMCPError,
		},
		{
			name: "invalid JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				localWriteJSONResponse(t, w, http.StatusOK, `{`)
			},
			wantCode: errCodeMCPProtocol,
		},
		{
			name: "no result",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				localWriteJSONResponse(t, w, http.StatusOK, `{"jsonrpc":"2.0","id":1}`)
			},
			wantCode: errCodeMCPProtocol,
		},
		{
			name: "invalid tools result",
			handler: func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "initialize" {
					localWriteJSONResponse(t, w, http.StatusOK, `{"jsonrpc":"2.0","id":1,"result":{}}`)
					return
				}
				localWriteJSONResponse(t, w, http.StatusOK, `{"jsonrpc":"2.0","id":2,"result":"not-an-object"}`)
			},
			wantCode: errCodeMCPProtocol,
		},
		{
			name:     "invalid base URL",
			baseURL:  "://not-a-url",
			wantCode: errCodeInvalidConnection,
		},
		{
			name:     "empty base URL",
			baseURL:  " ",
			wantCode: errCodeInvalidConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseURL := tt.baseURL
			if tt.handler != nil {
				server := httptest.NewServer(tt.handler)
				defer server.Close()
				baseURL = server.URL
			}
			proxy := New(http.DefaultClient, localTestLogger())
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			_, err := proxy.Test(ctx, Connection{BaseURL: baseURL, Credential: localTestCredential})
			if err == nil {
				t.Fatal("Test() error = nil, want error")
			}
			var typed *Error
			if !errors.As(err, &typed) {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if typed.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q; err=%v", typed.Code, tt.wantCode, err)
			}
			if strings.Contains(err.Error(), localTestCredential) || strings.Contains(err.Error(), "sk-test-secret") {
				t.Fatalf("error leaked credential or upstream message: %v", err)
			}
		})
	}
}

func TestLocalMCP_TestContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	proxy := New(nil, localTestLogger())
	_, err := proxy.Test(ctx, Connection{BaseURL: "http://127.0.0.1:1", Credential: localTestCredential})
	if err == nil {
		t.Fatal("Test() error = nil, want error")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != errCodeMCPUnavailable {
		t.Fatalf("error = %v, want mcp_unavailable", err)
	}
}

func TestLocalMCP_ProxyAllowedTool(t *testing.T) {
	type capturedRequest struct {
		Method        string
		Authorization string
		ProxyAuth     string
		Cookie        string
		Connection    string
		XHop          string
		XKeep         string
		ContentType   string
		Body          string
	}
	captured := make(chan capturedRequest, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- capturedRequest{
			Method:        r.Method,
			Authorization: r.Header.Get("Authorization"),
			ProxyAuth:     r.Header.Get("Proxy-Authorization"),
			Cookie:        r.Header.Get("Cookie"),
			Connection:    r.Header.Get("Connection"),
			XHop:          r.Header.Get("X-Hop"),
			XKeep:         r.Header.Get("X-Keep"),
			ContentType:   r.Header.Get("Content-Type"),
			Body:          string(body),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Mcp-Session-Id", "proxy-session")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":7,"result":{"content":[]}}`)
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader(localTestToolBody))
	req.Header.Set("Authorization", "Bearer sandbox-token")
	req.Header.Set("Cookie", "sandbox=secret")
	req.Header.Set("Proxy-Authorization", "Basic sandbox")
	req.Header.Set("Connection", "X-Hop")
	req.Header.Set("X-Hop", "remove-me")
	req.Header.Set("X-Keep", "keep-me")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	proxy := New(server.Client(), localTestLogger())
	err := proxy.Proxy(context.Background(), Connection{
		ID:           "mcp_test",
		ServerID:     "fake",
		BaseURL:      server.URL,
		Credential:   localTestCredential,
		AllowedTools: []string{"echo"},
	}, rec, req)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Mcp-Session-Id"); got != "proxy-session" {
		t.Fatalf("Mcp-Session-Id = %q, want proxy-session", got)
	}
	if got, want := rec.Body.String(), `{"jsonrpc":"2.0","id":7,"result":{"content":[]}}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	got := <-captured
	if got.Method != http.MethodPost {
		t.Fatalf("upstream method = %q, want POST", got.Method)
	}
	if got.Authorization != "Bearer "+localTestCredential {
		t.Fatalf("upstream Authorization = %q, want injected credential", got.Authorization)
	}
	if got.ProxyAuth != "" || got.Cookie != "" || got.Connection != "" || got.XHop != "" {
		t.Fatalf("sandbox/ hop-by-hop headers leaked upstream: %+v", got)
	}
	if got.XKeep != "keep-me" {
		t.Fatalf("X-Keep = %q, want keep-me", got.XKeep)
	}
	if got.ContentType != "application/json" {
		t.Fatalf("upstream Content-Type = %q", got.ContentType)
	}
	if got.Body != localTestToolBody {
		t.Fatalf("upstream body = %q, want %q", got.Body, localTestToolBody)
	}
}

func TestLocalMCP_ProxyToolDenied(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn",
		strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"shell"}}`))
	rec := httptest.NewRecorder()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	proxy := New(server.Client(), logger)
	err := proxy.Proxy(context.Background(), Connection{
		ID:           "mcp_denied",
		ServerID:     "fake",
		BaseURL:      server.URL,
		Credential:   localTestCredential,
		AllowedTools: []string{"echo"},
	}, rec, req)

	if calls.Load() != 0 {
		t.Fatal("upstream was called for a denied tool")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if typed.Status != http.StatusForbidden || typed.Code != errCodeToolDenied {
		t.Fatalf("error = %+v, want 403/tool_denied", typed)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decode error body: %v", decodeErr)
	}
	if body.Error.Code != errCodeToolDenied {
		t.Fatalf("error body code = %q, want tool_denied", body.Error.Code)
	}
	if strings.Contains(logs.String(), localTestCredential) {
		t.Fatalf("log leaked credential: %s", logs.String())
	}
}

func TestLocalMCP_ProxyBatchAllowlist(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	body := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}},` +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"write_file"}}]`
	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader(body))
	rec := httptest.NewRecorder()
	proxy := New(server.Client(), localTestLogger())
	err := proxy.Proxy(context.Background(), Connection{
		BaseURL:      server.URL,
		AllowedTools: []string{"echo"},
	}, rec, req)
	if err == nil {
		t.Fatal("Proxy() error = nil, want tool_denied")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != errCodeToolDenied {
		t.Fatalf("error = %v, want tool_denied", err)
	}
	if calls.Load() != 0 {
		t.Fatal("upstream was called for a denied batch entry")
	}
}

func TestLocalMCP_ProxyForwardsNonToolCallWithAllowlist(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	rec := httptest.NewRecorder()
	proxy := New(server.Client(), localTestLogger())
	err := proxy.Proxy(context.Background(), Connection{
		BaseURL:      server.URL,
		AllowedTools: []string{"echo"},
	}, rec, req)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if !called.Load() {
		t.Fatal("tools/list was not forwarded")
	}
}

func TestLocalMCP_ProxyUpstreamErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "upstream busy")
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader(localTestToolBody))
	rec := httptest.NewRecorder()
	proxy := New(server.Client(), localTestLogger())
	if err := proxy.Proxy(context.Background(), Connection{BaseURL: server.URL}, rec, req); err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", got)
	}
	if got := rec.Body.String(); got != "upstream busy" {
		t.Fatalf("body = %q, want upstream body", got)
	}
}

func TestLocalMCP_ProxyTransportUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL
	server.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader(localTestToolBody))
	rec := httptest.NewRecorder()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	proxy := New(http.DefaultClient, logger)
	err := proxy.Proxy(context.Background(), Connection{
		ID:         "mcp_down",
		ServerID:   "fake",
		BaseURL:    baseURL,
		Credential: localTestCredential,
	}, rec, req)
	if err == nil {
		t.Fatal("Proxy() error = nil, want error")
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if typed.Status != http.StatusServiceUnavailable || typed.Code != errCodeMCPUnavailable {
		t.Fatalf("error = %+v, want 503/mcp_unavailable", typed)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(logs.String(), localTestCredential) {
		t.Fatalf("log leaked credential: %s", logs.String())
	}
}

func TestLocalMCP_ProxyDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect target received Authorization %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader(localTestToolBody))
	rec := httptest.NewRecorder()
	proxy := New(upstream.Client(), localTestLogger())
	if err := proxy.Proxy(context.Background(), Connection{
		BaseURL:    upstream.URL,
		Credential: localTestCredential,
	}, rec, req); err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	if redirected.Load() != 0 {
		t.Fatal("Proxy followed a redirect to another destination")
	}
}

func TestLocalMCP_ProxyStreamsIncrementally(t *testing.T) {
	firstChunk := []byte("data: first\n\n")
	secondChunk := []byte("data: second\n\n")
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseUpstream()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		if _, err := w.Write(firstChunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if _, err := w.Write(secondChunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	proxy := New(upstream.Client(), localTestLogger())
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = proxy.Proxy(r.Context(), Connection{BaseURL: upstream.URL}, w, r)
	}))
	defer gateway.Close()

	client := gateway.Client()
	client.Timeout = 3 * time.Second
	req, err := http.NewRequest(http.MethodPost, gateway.URL, strings.NewReader(localTestToolBody))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("gateway request failed (response was buffered?): %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	firstRead := make([]byte, len(firstChunk))
	if _, err := io.ReadFull(resp.Body, firstRead); err != nil {
		t.Fatalf("read first streamed chunk: %v", err)
	}
	if !bytes.Equal(firstRead, firstChunk) {
		t.Fatalf("first chunk = %q, want %q", firstRead, firstChunk)
	}

	// The upstream is still blocked on release, so seeing the first chunk
	// proves the proxy flushed it instead of buffering the whole response.
	releaseUpstream()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read remaining stream: %v", err)
	}
	if !bytes.Equal(rest, secondChunk) {
		t.Fatalf("remaining stream = %q, want %q", rest, secondChunk)
	}
}

func TestLocalMCP_ProxyBadRequests(t *testing.T) {
	proxy := New(nil, localTestLogger())
	t.Run("nil request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		err := proxy.Proxy(context.Background(), Connection{}, rec, nil)
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != errCodeInvalidRequest {
			t.Fatalf("error = %v, want invalid_request", err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("body read error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", nil)
		req.Body = localReadCloserFunc(func([]byte) (int, error) {
			return 0, errors.New("read failed")
		})
		rec := httptest.NewRecorder()
		err := proxy.Proxy(context.Background(), Connection{}, rec, req)
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != errCodeInvalidRequest {
			t.Fatalf("error = %v, want invalid_request", err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
	t.Run("empty base URL", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		err := proxy.Proxy(context.Background(), Connection{}, rec, req)
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != errCodeInvalidConnection {
			t.Fatalf("error = %v, want invalid_connection", err)
		}
	})
	t.Run("empty method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader("{}"))
		req.Method = ""
		rec := httptest.NewRecorder()
		err := proxy.Proxy(context.Background(), Connection{BaseURL: "http://127.0.0.1:1"}, rec, req)
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != errCodeInvalidRequest {
			t.Fatalf("error = %v, want invalid_request", err)
		}
	})
}

func TestLocalMCP_ProxyResponseStreamError(t *testing.T) {
	proxy := New(&http.Client{Transport: localRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: localReadCloserFunc(func([]byte) (int, error) {
				return 0, errors.New("upstream stream failed")
			}),
		}, nil
	})}, localTestLogger())

	req := httptest.NewRequest(http.MethodPost, "http://gateway.invalid/mcp/conn", strings.NewReader(localTestToolBody))
	rec := httptest.NewRecorder()
	err := proxy.Proxy(context.Background(), Connection{BaseURL: "http://upstream.invalid"}, rec, req)
	if err == nil {
		t.Fatal("Proxy() error = nil, want stream error")
	}
	if strings.Contains(err.Error(), localTestCredential) {
		t.Fatalf("stream error leaked credential: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want already-committed 200", rec.Code)
	}
}

func TestLocalMCP_ParseJSONRPC(t *testing.T) {
	resp, err := parseJSONRPC([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	if err != nil || resp == nil {
		t.Fatalf("parseJSONRPC() = %v, %v", resp, err)
	}

	batch, err := parseJSONRPC([]byte(`[{"jsonrpc":"2.0","method":"notify"},{"jsonrpc":"2.0","id":2,"result":null}]`))
	if err != nil || batch == nil || string(batch.Result) != "null" {
		t.Fatalf("batch parse = %+v, %v", batch, err)
	}

	if _, err := parseJSONRPC([]byte(" \n")); !errors.Is(err, errNoRPCResponse) {
		t.Fatalf("empty parse error = %v, want errNoRPCResponse", err)
	}
	if _, err := parseJSONRPC([]byte("{")); !errors.Is(err, errInvalidRPCResponse) {
		t.Fatalf("invalid parse error = %v, want errInvalidRPCResponse", err)
	}
	if _, err := parseJSONRPC([]byte(`{"jsonrpc":"2.0"}`)); !errors.Is(err, errNoRPCResponse) {
		t.Fatalf("missing result parse error = %v, want errNoRPCResponse", err)
	}
	if _, err := parseJSONRPC([]byte(`[{"jsonrpc":"2.0","method":"notify"}]`)); !errors.Is(err, errNoRPCResponse) {
		t.Fatalf("batch without response error = %v, want errNoRPCResponse", err)
	}
}

func TestLocalMCP_ParseSSE(t *testing.T) {
	jsonWithSSEContentType, err := parseRPCResponse("text/event-stream",
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	if err != nil || jsonWithSSEContentType == nil {
		t.Fatalf("JSON with SSE content type = %+v, %v", jsonWithSSEContentType, err)
	}

	autoContentType := `event: message
data: {"jsonrpc":"2.0","id":1,"result":{"tools":[]}}

`
	resp, err := parseRPCResponse("", []byte(autoContentType))
	if err != nil || resp == nil {
		t.Fatalf("auto-detected SSE parse = %+v, %v", resp, err)
	}

	resp, err = parseRPCResponse("text/event-stream; charset=utf-8",
		[]byte("data: not-json\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
	if err != nil || resp == nil {
		t.Fatalf("SSE with ignored bad event = %+v, %v", resp, err)
	}

	if _, err := parseSSE([]byte(": only a comment\n\n")); !errors.Is(err, errNoRPCResponse) {
		t.Fatalf("comment-only SSE error = %v, want errNoRPCResponse", err)
	}
	if _, err := parseSSE([]byte("data: {}\n\n")); !errors.Is(err, errNoRPCResponse) {
		t.Fatalf("notification SSE error = %v, want errNoRPCResponse", err)
	}
	if _, err := parseSSE([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}")); err != nil {
		t.Fatalf("SSE without trailing blank line error = %v", err)
	}
}

func TestLocalMCP_HeaderAndToolHelpers(t *testing.T) {
	if body, err := readBody(nil); err != nil || body != nil {
		t.Fatalf("readBody(nil) = %v, %v", body, err)
	}
	if _, err := decodeResult(nil); err == nil {
		t.Fatal("decodeResult(nil) error = nil")
	}
	if _, err := decodeResult(&rpcResponse{}); err == nil {
		t.Fatal("decodeResult(empty) error = nil")
	}

	if isEventStream("application/json") {
		t.Fatal("application/json detected as event stream")
	}
	if !isEventStream(" Text/Event-Stream ; charset=utf-8") {
		t.Fatal("event stream content type not detected")
	}
	if looksLikeSSE([]byte(`{"jsonrpc":"2.0"}`)) {
		t.Fatal("JSON detected as SSE")
	}

	header := upstreamHeader(http.Header{
		"Connection":          []string{"X-Hop, Keep-Me"},
		"X-Hop":               []string{"remove"},
		"Keep-Me":             []string{"also-remove"},
		"Authorization":       []string{"Bearer sandbox"},
		"Cookie":              []string{"session=secret"},
		"Proxy-Authorization": []string{"Basic sandbox"},
		"Upgrade":             []string{"websocket"},
		"X-Keep":              []string{"keep"},
	})
	for _, name := range []string{"Connection", "X-Hop", "Keep-Me", "Authorization", "Cookie", "Proxy-Authorization", "Upgrade"} {
		if got := header.Get(name); got != "" {
			t.Fatalf("header %s = %q, want removed", name, got)
		}
	}
	if got := header.Get("X-Keep"); got != "keep" {
		t.Fatalf("X-Keep = %q, want keep", got)
	}
	if got := upstreamHeader(nil); got == nil {
		t.Fatal("upstreamHeader(nil) = nil, want empty header map")
	}

	if name, value, ok := credentialHeader(Connection{}); ok || name != "" || value != "" {
		t.Fatalf("credentialHeader(empty) = %q, %q, %v", name, value, ok)
	}
	if name, value, ok := credentialHeader(Connection{Credential: localTestCredential}); !ok || name != "Authorization" || value != "Bearer "+localTestCredential {
		t.Fatalf("default credential = %q, %q, %v", name, value, ok)
	}

	if names, found := inspectToolCalls([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`)); !found || len(names) != 1 || names[0] != "" {
		t.Fatalf("missing tool name = %+v, %v", names, found)
	}
	if _, found := inspectToolCalls([]byte(`not-json`)); found {
		t.Fatal("malformed body detected as tool call")
	}
	if _, found := inspectToolCalls([]byte(`{"jsonrpc":"2.0","method":"tools/list"}`)); found {
		t.Fatal("tools/list detected as tools/call")
	}
	if _, found := inspectToolCalls([]byte(`[not-json]`)); found {
		t.Fatal("malformed batch detected as tool call")
	}
	if names, found := inspectToolCalls([]byte(`[{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo"}}]`)); !found || len(names) != 1 || names[0] != "echo" {
		t.Fatalf("batch tool call = %+v, %v", names, found)
	}
	if !toolAllowed([]string{"echo", "read"}, "read") {
		t.Fatal("toolAllowed() = false, want true")
	}
	if toolAllowed([]string{"echo"}, "shell") {
		t.Fatal("toolAllowed() = true for missing tool")
	}

	if err := (*Error)(nil).Error(); err != "mcp: <nil>" {
		t.Fatalf("nil Error() = %q", err)
	}
	if got := (&Error{Code: "code"}).Error(); got != "code" {
		t.Fatalf("code-only Error() = %q", got)
	}
	if got := (&Error{Message: "message"}).Error(); got != "message" {
		t.Fatalf("message-only Error() = %q", got)
	}
	if got := (&Error{Code: "code", Message: "message"}).Error(); got != "code: message" {
		t.Fatalf("full Error() = %q", got)
	}

	writeProxyError(nil, &Error{Status: 500})
	writeProxyError(httptest.NewRecorder(), nil)

	proxy := (*Proxy)(nil)
	if proxy.httpClient() != http.DefaultClient {
		t.Fatal("nil proxy did not fall back to http.DefaultClient")
	}
	if proxy.logger() == nil {
		t.Fatal("nil proxy logger is nil")
	}
}

func TestLocalMCP_FlushCopyEdgeCases(t *testing.T) {
	rec := httptest.NewRecorder()
	n, err := flushCopy(rec, strings.NewReader("hello"))
	if err != nil || n != 5 || rec.Body.String() != "hello" {
		t.Fatalf("flushCopy() = %d, %v, %q", n, err, rec.Body.String())
	}

	readFailure := errors.New("read failure")
	if _, err := flushCopy(httptest.NewRecorder(), localReadCloserFunc(func([]byte) (int, error) {
		return 0, readFailure
	})); !errors.Is(err, readFailure) {
		t.Fatalf("flushCopy read error = %v, want %v", err, readFailure)
	}

	if _, err := flushCopy(&localShortWriter{header: make(http.Header)}, strings.NewReader("hello")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("flushCopy short write error = %v, want io.ErrShortWrite", err)
	}

	writeFailure := errors.New("write failure")
	if _, err := flushCopy(&localFailingWriter{header: make(http.Header), err: writeFailure}, strings.NewReader("hello")); !errors.Is(err, writeFailure) {
		t.Fatalf("flushCopy write error = %v, want %v", err, writeFailure)
	}
}

type localReadCloserFunc func([]byte) (int, error)

func (f localReadCloserFunc) Read(p []byte) (int, error) { return f(p) }
func (localReadCloserFunc) Close() error                 { return nil }

type localRoundTripFunc func(*http.Request) (*http.Response, error)

func (f localRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type localShortWriter struct {
	header http.Header
}

func (w *localShortWriter) Header() http.Header         { return w.header }
func (w *localShortWriter) WriteHeader(int)             {}
func (w *localShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type localFailingWriter struct {
	header http.Header
	err    error
}

func (w *localFailingWriter) Header() http.Header       { return w.header }
func (w *localFailingWriter) WriteHeader(int)           {}
func (w *localFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLocalMCP_TestTimeout(t *testing.T) {
	start := time.Now()
	deadlines := make(chan time.Time, 2)
	client := &http.Client{Transport: localRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			return nil, errors.New("request has no deadline")
		}
		deadlines <- deadline
		body, _ := io.ReadAll(req.Body)
		var parsed struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &parsed)
		payload := `{"jsonrpc":"2.0","id":1,"result":{}}`
		if parsed.Method == "tools/list" {
			payload = `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(payload)),
		}, nil
	})}

	proxy := New(client, localTestLogger())
	if _, err := proxy.Test(context.Background(), Connection{BaseURL: "http://mcp.invalid"}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	for i := 0; i < 2; i++ {
		deadline := <-deadlines
		if deadline.Before(start) || deadline.After(start.Add(11*time.Second)) {
			t.Fatalf("request deadline = %v, want within 10s of %v", deadline, start)
		}
	}
}
