package core

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mrjwj34/nullkey/internal/domain"
)

type failResponseWriter struct {
	header http.Header
}

func (f *failResponseWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}
func (f *failResponseWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (f *failResponseWriter) WriteHeader(int)           {}

func TestApplyCredential(t *testing.T) {
	cases := []struct {
		name     string
		cred     domain.Credential
		secret   string
		provider string
		wantKey  string
		wantVal  string
	}{
		{"api-key", domain.Credential{AuthType: domain.AuthAPIKey}, "sk-1", "openai", "Authorization", "Bearer sk-1"},
		{"bearer", domain.Credential{AuthType: domain.AuthBearer}, "b", "openai", "Authorization", "Bearer b"},
		{"pat", domain.Credential{AuthType: domain.AuthPAT}, "p", "github", "Authorization", "Bearer p"},
		{"oauth", domain.Credential{AuthType: domain.AuthOAuth}, "o", "openai", "Authorization", "Bearer o"},
		{"anthropic", domain.Credential{AuthType: domain.AuthAPIKey}, "a", "anthropic", "x-api-key", "a"},
		{"header-named", domain.Credential{AuthType: domain.AuthHeader, HeaderName: "x-custom"}, "h", "anthropic", "x-custom", "h"},
		{"header-default", domain.Credential{AuthType: domain.AuthHeader}, "h", "anthropic", "x-api-key", "h"},
		{"basic", domain.Credential{AuthType: domain.AuthBasic}, "user:pass", "github", "Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
			applyCredential(req, tc.cred, tc.secret, tc.provider)
			if got := req.Header.Get(tc.wantKey); got != tc.wantVal {
				t.Fatalf("%s = %q, want %q", tc.wantKey, got, tc.wantVal)
			}
			if tc.provider == "anthropic" && tc.cred.AuthType != domain.AuthHeader && req.Header.Get("anthropic-version") == "" {
				t.Fatalf("anthropic-version missing")
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.test", nil)
	applyCredential(req, domain.Credential{AuthType: domain.AuthNone}, "x", "openai")
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("auth none added a header")
	}
}

func TestHeaderSanitization(t *testing.T) {
	src := http.Header{}
	src.Set("Authorization", "Bearer secret")
	src.Set("Cookie", "session=1")
	src.Set("X-Api-Key", "key")
	for _, hop := range []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Proxy-Authorization", "Te", "Trailer"} {
		src.Set(hop, "x")
	}
	src.Set("X-Keep", "yes")
	src.Add("X-Multi", "a")
	src.Add("X-Multi", "b")

	dst := http.Header{}
	sanitizeRequestHeaders(dst, src)
	for _, banned := range []string{"Authorization", "Cookie", "X-Api-Key", "Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Proxy-Authorization", "Te", "Trailer"} {
		if dst.Get(banned) != "" {
			t.Fatalf("%s leaked into upstream request", banned)
		}
	}
	if dst.Get("X-Keep") != "yes" || len(dst.Values("X-Multi")) != 2 {
		t.Fatalf("allowed headers missing: %+v", dst)
	}

	respSrc := http.Header{}
	respSrc.Set("Content-Type", "text/plain")
	respSrc.Set("Connection", "close")
	respSrc.Set("Transfer-Encoding", "chunked")
	respSrc.Set("Set-Cookie", "a=b")
	respDst := http.Header{}
	copyResponseHeaders(respDst, respSrc)
	if respDst.Get("Content-Type") != "text/plain" || respDst.Get("Set-Cookie") != "a=b" {
		t.Fatalf("response headers = %+v", respDst)
	}
	if respDst.Get("Connection") != "" || respDst.Get("Transfer-Encoding") != "" {
		t.Fatalf("hop headers copied: %+v", respDst)
	}
}

func TestModelGatewayOpenAI(t *testing.T) {
	var got *http.Request
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		got = r.Clone(context.Background())
		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	svc, _ := newTestService(t)
	ctx := context.Background()
	cred := createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "key", AuthType: "api_key", Value: "sk-test-upstream", BaseURL: upstream.URL,
	})
	sb := domain.Sandbox{ID: "sbx_gateway"}

	req := httptest.NewRequest(http.MethodPost, "http://gateway/openai/v1/chat/completions?stream=1", strings.NewReader(`{"model":"gpt"}`))
	req.Header.Set("Authorization", "Bearer sandbox-only")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	rec := httptest.NewRecorder()
	if err := svc.ModelGateway(ctx, sb, "openai", rec, req); err != nil {
		t.Fatalf("model gateway: %v", err)
	}
	if rec.Code != http.StatusCreated || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Upstream") != "yes" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %+v", rec.Header())
	}
	if got == nil {
		t.Fatalf("upstream not called")
	}
	if got.URL.Path != "/v1/chat/completions" || got.URL.RawQuery != "stream=1" {
		t.Fatalf("upstream url = %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Authorization") != "Bearer sk-test-upstream" {
		t.Fatalf("authorization = %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("Cookie") != "" {
		t.Fatalf("client cookie leaked: %+v", got.Header)
	}
	if gotBody != `{"model":"gpt"}` {
		t.Fatalf("body = %q", gotBody)
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "gateway.model")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
	if evs[0].SandboxID == nil || *evs[0].SandboxID != "sbx_gateway" {
		t.Fatalf("audit sandbox = %+v", evs[0])
	}
	_ = cred
}

func TestModelGatewayAnthropicAndHeader(t *testing.T) {
	var apiKey, version, custom string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("x-api-key")
		version = r.Header.Get("anthropic-version")
		custom = r.Header.Get("x-custom")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	svc, _ := newTestService(t)
	ctx := context.Background()
	createCred(t, svc, CreateCredentialRequest{
		ProviderID: "anthropic", Label: "key", AuthType: "api_key", Value: "sk-ant", BaseURL: upstream.URL,
	})
	req := httptest.NewRequest(http.MethodGet, "http://gateway/anthropic/v1/messages", nil)
	rec := httptest.NewRecorder()
	if err := svc.ModelGateway(ctx, domain.Sandbox{ID: "sbx"}, "anthropic", rec, req); err != nil {
		t.Fatalf("model gateway: %v", err)
	}
	if apiKey != "sk-ant" || version != "2023-06-01" || rec.Body.String() != "ok" {
		t.Fatalf("headers = %q %q body=%q", apiKey, version, rec.Body.String())
	}

	// Header auth uses the stored header name.
	headerSvc, _ := newTestService(t)
	createCred(t, headerSvc, CreateCredentialRequest{
		ProviderID: "anthropic", Label: "header", AuthType: "header", HeaderName: "x-custom", Value: "headerval", BaseURL: upstream.URL,
	})
	rec = httptest.NewRecorder()
	if err := headerSvc.ModelGateway(ctx, domain.Sandbox{ID: "sbx"}, "anthropic", rec,
		httptest.NewRequest(http.MethodGet, "http://gateway/anthropic/v1/messages", nil)); err != nil {
		t.Fatalf("header gateway: %v", err)
	}
	if custom != "headerval" {
		t.Fatalf("custom header = %q", custom)
	}
}

func TestModelGatewayCodexFallback(t *testing.T) {
	var auth, path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		path = r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	svc, _ := newTestService(t)
	ctx := context.Background()
	createCred(t, svc, CreateCredentialRequest{
		ProviderID: "codex", Label: "oauth", AuthType: "oauth", Value: "codex-token", BaseURL: upstream.URL,
	})
	req := httptest.NewRequest(http.MethodPost, "http://gateway/openai/responses", nil)
	rec := httptest.NewRecorder()
	if err := svc.ModelGateway(ctx, domain.Sandbox{ID: "sbx"}, "codex", rec, req); err != nil {
		t.Fatalf("codex gateway: %v", err)
	}
	if auth != "Bearer codex-token" || path != "/responses" {
		t.Fatalf("auth/path = %q %q", auth, path)
	}
	// A bare provider path maps to "/".
	req = httptest.NewRequest(http.MethodGet, "http://gateway/openai", nil)
	if err := svc.ModelGateway(ctx, domain.Sandbox{ID: "sbx"}, "openai", httptest.NewRecorder(), req); err != nil {
		t.Fatalf("root gateway: %v", err)
	}
	if path != "/" {
		t.Fatalf("root path = %q", path)
	}
}

func TestModelGatewayErrors(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "http://gateway/openai/v1/x", nil)

	err := svc.ModelGateway(ctx, domain.Sandbox{}, "unknown", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusForbidden, "network_denied")

	err = svc.ModelGateway(ctx, domain.Sandbox{}, "openai", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	createCred(t, svc, CreateCredentialRequest{
		ProviderID: "openai", Label: "key", AuthType: "api_key", Value: "v", BaseURL: "http://127.0.0.1:1",
	})
	err = svc.ModelGateway(ctx, domain.Sandbox{}, "openai", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusBadGateway, "gateway_unavailable")

	// A write failure to the sandbox is not an upstream error.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body"))
	}))
	defer upstream.Close()
	writeSvc, _ := newTestService(t)
	createCred(t, writeSvc, CreateCredentialRequest{
		ProviderID: "openai", Label: "key", AuthType: "api_key", Value: "v", BaseURL: upstream.URL,
	})
	if err := writeSvc.ModelGateway(ctx, domain.Sandbox{}, "openai", &failResponseWriter{},
		httptest.NewRequest(http.MethodGet, "http://gateway/openai/x", nil)); err != nil {
		t.Fatalf("write failure should be swallowed: %v", err)
	}
}

func TestGitGateway(t *testing.T) {
	var method, path, query, auth, userAgent, body string
	var contentLength int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		auth = r.Header.Get("Authorization")
		userAgent = r.Header.Get("User-Agent")
		body = string(raw)
		contentLength = r.ContentLength
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("git-response"))
	}))
	defer upstream.Close()

	svc, _ := newTestService(t)
	ctx := context.Background()
	createCred(t, svc, CreateCredentialRequest{
		ProviderID: "github", Label: "pat", AuthType: "pat", Value: "ghp_secret", BaseURL: upstream.URL,
	})
	req := httptest.NewRequest(http.MethodPost, "http://gateway/git/github/owner/repo.git/git-upload-pack?service=git-upload-pack", strings.NewReader("want abc"))
	req.Header.Set("Authorization", "Bearer sandbox")
	req.Header.Set("Cookie", "sandbox=1")
	rec := httptest.NewRecorder()
	if err := svc.GitGateway(ctx, domain.Sandbox{ID: "sbx_git"}, "owner", "repo", rec, req); err != nil {
		t.Fatalf("git gateway: %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "git-response" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/x-git-upload-pack-result" {
		t.Fatalf("headers = %+v", rec.Header())
	}
	if method != http.MethodPost || path != "/owner/repo.git/git-upload-pack" || query != "service=git-upload-pack" {
		t.Fatalf("upstream = %s %s?%s", method, path, query)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_secret"))
	if auth != wantAuth {
		t.Fatalf("auth = %q", auth)
	}
	if userAgent != "nullkey-git-proxy" || body != "want abc" || contentLength != int64(len("want abc")) {
		t.Fatalf("request = %q %q %d", userAgent, body, contentLength)
	}
	evs, _, err := svc.Audit.List(ctx, 50, "", "gateway.git")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit = %d, %v", len(evs), err)
	}
}

func TestGitGatewayErrors(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "http://gateway/git/github/o/r.git/info/refs", nil)

	err := svc.GitGateway(ctx, domain.Sandbox{}, "", "r", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusNotFound, "not_found")
	err = svc.GitGateway(ctx, domain.Sandbox{}, "o", "", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusNotFound, "not_found")
	err = svc.GitGateway(ctx, domain.Sandbox{}, "o", "r", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusUnauthorized, "reauth_required")

	createCred(t, svc, CreateCredentialRequest{
		ProviderID: "github", Label: "pat", AuthType: "pat", Value: "v", BaseURL: "http://127.0.0.1:1",
	})
	err = svc.GitGateway(ctx, domain.Sandbox{}, "o", "r", httptest.NewRecorder(), req)
	wantError(t, err, http.StatusBadGateway, "gateway_unavailable")

	// A bare repository URL still forwards "/".
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	bare, _ := newTestService(t)
	createCred(t, bare, CreateCredentialRequest{
		ProviderID: "github", Label: "pat", AuthType: "pat", Value: "v", BaseURL: upstream.URL,
	})
	bareReq := httptest.NewRequest(http.MethodGet, "http://gateway/git/github/o/r.git", nil)
	rec := httptest.NewRecorder()
	if err := bare.GitGateway(ctx, domain.Sandbox{}, "o", "r", rec, bareReq); err != nil {
		t.Fatalf("bare git: %v", err)
	}
	if gotPath != "/o/r.git/" || rec.Code != http.StatusNotFound {
		t.Fatalf("bare path = %q, code = %d", gotPath, rec.Code)
	}
}
