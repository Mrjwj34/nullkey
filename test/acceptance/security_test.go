//go:build acceptance

package acceptance

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAC_SEC_001_SandboxEnv(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "env")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	c := dialTerminal(t, d, sb.ID)
	defer c.close()
	c.input(fmt.Sprintf("echo ENV_START; env | sort; echo ENV_END; P=DAEMON; ps e -p %d 2>&1 | grep -q \"NULLKEY_${P}_TOKEN\"; echo DAEMON_LEAK=$?; echo PS_END_$((7*7))\n", d.cmd.Process.Pid))
	out := c.readUntil("PS_END_49", 15*time.Second)

	allowed := map[string]bool{
		"HOME": true, "TMPDIR": true, "PATH": true, "SHELL": true, "TERM": true,
		"LANG": true, "LC_ALL": true, "PWD": true, "SHLVL": true, "_": true,
		"NULLKEY_TOKEN": true, "NULLKEY_GATEWAY": true, "NULLKEY_PROXY": true,
		"NULLKEY_SANDBOX_ID": true,
		"HTTP_PROXY":         true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"DO_NOT_TRACK": true, "DISABLE_TELEMETRY": true, "NEXT_TELEMETRY_DISABLED": true,
		"CODEX_DISABLE_TELEMETRY": true, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": true,
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "ENV_START") || strings.Contains(line, "ENV_END") {
			continue
		}
		if strings.Contains(line, "DAEMON_LEAK") || !strings.Contains(line, "=") {
			continue
		}
		key := strings.SplitN(line, "=", 2)[0]
		if strings.HasPrefix(key, "PS_") {
			continue
		}
		if !allowed[key] {
			t.Fatalf("unexpected sandbox environment variable %q in output %q", key, out)
		}
	}
	for _, forbidden := range []string{"NULLKEY_DAEMON_TOKEN", d.token, "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "GITHUB_TOKEN"} {
		if forbidden != "" && strings.Contains(out, forbidden) {
			t.Fatalf("sandbox environment leaked %q", forbidden)
		}
	}
	if !strings.Contains(out, "NULLKEY_TOKEN=") {
		t.Fatalf("expected the per-sandbox token; output=%q", out)
	}
	if strings.Contains(out, "DAEMON_LEAK=0") {
		t.Fatalf("sandbox read the daemon environment: %q", out)
	}
}

func TestAC_SEC_002_HostFsIsolation(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "isolation")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	hostHome, _ := os.UserHomeDir()
	c := dialTerminal(t, d, sb.ID)
	defer c.close()
	cmd := "cat " + hostHome + "/.ssh/config >/dev/null 2>&1; echo SSH_EXIT=$?; " +
		"cat " + hostHome + "/.aws/credentials >/dev/null 2>&1; echo AWS_EXIT=$?; " +
		"ls " + hostHome + "/Library/Keychains >/dev/null 2>&1; echo KC_EXIT=$?; " +
		"cat \"$HOME/.ssh/id_ed25519\" >/dev/null 2>&1; echo OWN_SSH_EXIT=$?; " +
		"echo hello > inside.txt; cat inside.txt; echo FS_DONE_$((7*7))\n"
	c.input(cmd)
	out := c.readUntil("FS_DONE_49", 8*time.Second)
	for _, marker := range []string{"SSH_EXIT=0", "AWS_EXIT=0", "KC_EXIT=0", "OWN_SSH_EXIT=0"} {
		if strings.Contains(out, marker) {
			t.Fatalf("host filesystem isolation failed: found %s in %q", marker, out)
		}
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("workspace write/read failed: %q", out)
	}
}

func TestAC_SEC_003_TokenFormatAndHash(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "tokens")
	sb1 := d.createSandbox(t, ws.ID, "shell", "")
	token1 := sandboxToken(t, d, sb1.ID)
	raw, err := base64.RawURLEncoding.DecodeString(token1)
	if err != nil || len(raw) != 32 {
		t.Fatalf("sandbox token %q is not 32-byte base64url: %v", token1, err)
	}
	ws2 := d.createWorkspace(t, "tokens-2")
	sb2 := d.createSandbox(t, ws2.ID, "shell", "")
	token2 := sandboxToken(t, d, sb2.ID)
	if token1 == token2 {
		t.Fatal("two sandboxes received the same token")
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(d.home, "nullkey.db")+"?mode=ro")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var hash []byte
	if err := db.QueryRow("SELECT token_hash FROM sandboxes WHERE id=?", sb1.ID).Scan(&hash); err != nil {
		t.Fatalf("read token hash: %v", err)
	}
	if len(hash) != 32 {
		t.Fatalf("token hash length = %d, want 32", len(hash))
	}
	if string(hash) == token1 || strings.Contains(string(hash), token1) {
		t.Fatal("database contains the plaintext token")
	}
	fileBytes, err := os.ReadFile(filepath.Join(d.home, "nullkey.db"))
	if err != nil {
		t.Fatalf("read db file: %v", err)
	}
	if strings.Contains(string(fileBytes), token1) {
		t.Fatal("database file contains the plaintext sandbox token")
	}
	if strings.Contains(d.logs.String(), token1) {
		t.Fatal("daemon log contains the plaintext sandbox token")
	}
}

func TestAC_SEC_004_StopRevokesToken(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "revoke")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)

	resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pre-stop gateway = %d; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "reauth_required" {
		t.Fatalf("valid token with no credential = %q, want reauth_required", env.Error.Code)
	}
	if resp, _ := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/stop", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d", resp.StatusCode)
	}
	resp, raw = d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-stop gateway = %d; body=%s", resp.StatusCode, raw)
	}
	env = decodeJSON[apiError](t, raw)
	if env.Error.Code != "token_invalid" {
		t.Fatalf("post-stop code = %q, want token_invalid", env.Error.Code)
	}
}

func TestAC_SEC_005_RestartFailsClosed(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "restart")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	pid := d.getSandbox(t, sb.ID).PID
	home := d.home
	d.crash(t)

	d2 := startDaemonWithHome(t, home, nil)
	current := d2.getSandbox(t, sb.ID)
	if current.Status != "stopped" {
		t.Fatalf("restart status = %q, want stopped", current.Status)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("orphan sandbox pid %d survived restart", pid)
	}
	resp, raw := d2.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old token after restart = %d; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "token_invalid" {
		t.Fatalf("old token code = %q, want token_invalid", env.Error.Code)
	}
}

func TestAC_SEC_006_LogRedaction(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	secret := "sk-test-logredaction-0123456789abcdef"
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "log", "auth_type": "api_key", "value": secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create credential = %d; body=%s", resp.StatusCode, raw)
	}
	ws := d.createWorkspace(t, "logs")
	_ = d.createSandbox(t, ws.ID, "shell", "")
	_, auditRaw := d.doAuthed(t, http.MethodGet, "/audit", nil)

	for label, text := range map[string]string{"daemon log": d.logs.String(), "audit": string(auditRaw)} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s contains the credential plaintext", label)
		}
		if strings.Contains(text, "Authorization: Bearer") {
			t.Fatalf("%s contains authorization header text", label)
		}
	}
}

func TestAC_SEC_007_CredentialsNoPlaintext(t *testing.T) {
	d := startDaemon(t)
	secret := "sk-test-credential-0123456789abcdef"
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "cred", "auth_type": "api_key", "value": secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create credential = %d; body=%s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("credential create response contains plaintext")
	}
	cred := decodeJSON[struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
	}](t, raw)
	if cred.ID == "" || cred.Fingerprint == "" || strings.Contains(cred.Fingerprint, secret) {
		t.Fatalf("invalid credential metadata: %+v", cred)
	}
	_, listRaw := d.doAuthed(t, http.MethodGet, "/credentials", nil)
	if strings.Contains(string(listRaw), secret) {
		t.Fatal("credential list contains plaintext")
	}
	_, auditRaw := d.doAuthed(t, http.MethodGet, "/audit", nil)
	if strings.Contains(string(auditRaw), secret) {
		t.Fatal("audit contains plaintext")
	}
	if strings.Contains(d.logs.String(), secret) {
		t.Fatal("daemon log contains plaintext")
	}
}

func TestAC_SEC_008_KeychainNoArgv(t *testing.T) {
	d := startDaemon(t)
	secret := "sk-test-argv-0123456789abcdef"
	resp, raw := d.doAuthed(t, http.MethodPost, "/credentials", map[string]any{
		"provider_id": "openai", "label": "argv", "auth_type": "api_key", "value": secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create credential = %d; body=%s", resp.StatusCode, raw)
	}
	out, err := commandOutput("/bin/ps", "-axo", "command")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if strings.Contains(out, secret) {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, secret) {
				t.Fatalf("credential appears in the process list (argv leak): %s", line)
			}
		}
		t.Fatal("credential appears in the process list (argv leak)")
	}
	for _, arg := range d.cmd.Args {
		if strings.Contains(arg, secret) {
			t.Fatal("credential appears in the daemon argv")
		}
	}
}

func commandOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func TestAC_SEC_009_TokenPlaneSeparation(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	// The daemon token is not a sandbox token.
	resp, raw := d.gatewayRequest(t, http.MethodPost, "/openai/v1/chat/completions", d.token, []byte(`{}`), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("daemon token on gateway = %d; body=%s", resp.StatusCode, raw)
	}
	env := decodeJSON[apiError](t, raw)
	if env.Error.Code != "token_invalid" {
		t.Fatalf("code = %q, want token_invalid", env.Error.Code)
	}
	// A sandbox token is not a control-plane token.
	ws := d.createWorkspace(t, "planes")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)
	resp, raw = d.do(t, http.MethodGet, "/capabilities", nil, map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sandbox token on control plane = %d; body=%s", resp.StatusCode, raw)
	}
	if d.gatewayAddr == strings.TrimPrefix(d.baseURL, "http://") {
		t.Fatal("control and gateway share a listener")
	}
}

func TestAC_SEC_010_NoSecretScan(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "scan")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	resp, raw := d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/scan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scan = %d; body=%s", resp.StatusCode, raw)
	}
	var result struct {
		OK       bool     `json:"ok"`
		Hits     int      `json:"hits"`
		Scope    string   `json:"scope"`
		Excluded []string `json:"excluded"`
		Checks   []struct {
			Name string `json:"name"`
			Hits int    `json:"hits"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode scan: %v", err)
	}
	if !result.OK || result.Hits != 0 || result.Scope != "generated_runtime" {
		t.Fatalf("unexpected scan: %+v", result)
	}
	wantExcluded := map[string]bool{"workspace": true, "user_imports": true, "user_input": true}
	if len(result.Excluded) != 3 {
		t.Fatalf("excluded = %v, want 3 entries", result.Excluded)
	}
	for _, item := range result.Excluded {
		if !wantExcluded[item] {
			t.Fatalf("unexpected excluded scope %q", item)
		}
	}

	var states struct {
		Items []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"items"`
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/states", nil)
	if err := json.Unmarshal(raw, &states); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	var statePath string
	for _, st := range states.Items {
		if st.ID == sb.StateID {
			statePath = st.Path
		}
	}
	if statePath == "" {
		t.Fatal("state path not found")
	}
	cfgPath := filepath.Join(statePath, "nullkey", "config.json")
	rawCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	writeFile(t, cfgPath, string(rawCfg)+"\nsk-test-scanned-0123456789abcdef\n")
	_, raw = d.doAuthed(t, http.MethodPost, "/sandboxes/"+sb.ID+"/scan", nil)
	var dirty struct {
		Hits int `json:"hits"`
	}
	if err := json.Unmarshal(raw, &dirty); err != nil {
		t.Fatalf("decode dirty scan: %v", err)
	}
	if dirty.Hits == 0 {
		t.Fatalf("scan did not detect a planted generated-runtime secret: %s", raw)
	}
}

var _ = context.Background
