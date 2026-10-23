//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAC_API_011_OAuthCancel(t *testing.T) {
	stub := newOAuthStub(t, 3600)
	defer stub.Close()
	d := startDaemonWithEnv(t, stub.Env())

	_, beforeRaw := d.doAuthed(t, http.MethodGet, "/credentials", nil)
	var before struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(beforeRaw, &before)

	resp, raw := d.doAuthed(t, http.MethodPost, "/oauth/start", map[string]any{"provider_id": "codex"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start = %d; body=%s", resp.StatusCode, raw)
	}
	start := decodeJSON[struct {
		FlowID  string `json:"flow_id"`
		AuthURL string `json:"auth_url"`
	}](t, raw)
	auth, _ := url.Parse(start.AuthURL)
	state := auth.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in %s", start.AuthURL)
	}

	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/flows/"+start.FlowID+"/cancel", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel = %d; body=%s", resp.StatusCode, raw)
	}
	flow := decodeJSON[struct {
		Status string `json:"status"`
	}](t, raw)
	if flow.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", flow.Status)
	}
	resp, raw = d.do(t, http.MethodGet, "/oauth/callback?code=late&state="+url.QueryEscape(state), nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("late callback = %d, want 400; body=%s", resp.StatusCode, raw)
	}
	_, afterRaw := d.doAuthed(t, http.MethodGet, "/credentials", nil)
	var after struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(afterRaw, &after)
	if len(after.Items) != len(before.Items) {
		t.Fatalf("late callback persisted a credential: before=%d after=%d", len(before.Items), len(after.Items))
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/flows/"+start.FlowID+"/cancel", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat cancel = %d; body=%s", resp.StatusCode, raw)
	}

	// A completed flow cannot be cancelled.
	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/start", map[string]any{"provider_id": "codex"})
	var second struct {
		FlowID  string `json:"flow_id"`
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(raw, &second)
	auth2, _ := url.Parse(second.AuthURL)
	resp, _ = d.do(t, http.MethodGet, "/oauth/callback?code=ok&state="+url.QueryEscape(auth2.Query().Get("state")), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("successful callback = %d", resp.StatusCode)
	}
	resp, raw = d.doAuthed(t, http.MethodPost, "/oauth/flows/"+second.FlowID+"/cancel", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel succeeded flow = %d, want 409; body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "conflict") {
		t.Fatalf("unexpected conflict body: %s", raw)
	}
}
