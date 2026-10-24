//go:build acceptance

package acceptance

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func acceptanceJWT(t *testing.T, exp int64, account string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"exp":                         exp,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": account},
	})
	if err != nil {
		t.Fatalf("marshal jwt: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestAC_CRED_006_CodexHostImportFullBundle(t *testing.T) {
	d := startDaemon(t)
	hostDir := filepath.Join(d.home, "host-import")
	exp := time.Now().Add(2 * time.Hour).Unix()
	access := acceptanceJWT(t, exp, "acct_acceptance")
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"rt.acceptance.secret","account_id":"acct_acceptance"}}`,
		access, acceptanceJWT(t, exp, "acct_acceptance"))
	writeFile(t, filepath.Join(hostDir, ".codex", "auth.json"), auth)

	resp, raw := d.doAuthed(t, http.MethodPost, "/oauth/import", map[string]any{"provider_id": "codex", "source": "host"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d; body=%s", resp.StatusCode, raw)
	}
	_, raw = d.doAuthed(t, http.MethodGet, "/credentials", nil)
	if strings.Contains(string(raw), access) || strings.Contains(string(raw), "rt.acceptance.secret") {
		t.Fatal("credential list leaked token material")
	}
	var list struct {
		Items []struct {
			ProviderID string  `json:"provider_id"`
			ExpiresAt  *string `json:"expires_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	found := false
	for _, item := range list.Items {
		if item.ProviderID != "codex" {
			continue
		}
		found = true
		if item.ExpiresAt == nil {
			t.Fatalf("codex expires_at is null: %s", raw)
		}
		parsed, err := time.Parse(time.RFC3339, *item.ExpiresAt)
		if err != nil || parsed.Unix() != exp {
			t.Fatalf("expires_at = %v, %v", item.ExpiresAt, err)
		}
	}
	if !found {
		t.Fatalf("codex credential missing: %s", raw)
	}
}

func TestAC_NET_009_HTTPSGateway(t *testing.T) {
	requireDarwin(t)
	d := startDaemon(t)
	ws := d.createWorkspace(t, "https-gateway")
	sb := d.createSandbox(t, ws.ID, "shell", "")
	token := sandboxToken(t, d, sb.ID)

	ca, err := os.ReadFile(filepath.Join(d.home, "gateway-tls", "ca.pem"))
	if err != nil {
		t.Fatalf("read gateway CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("gateway CA did not parse")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	req, _ := http.NewRequest(http.MethodGet, "https://"+d.gatewayAddr+"/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTPS gateway: %v", err)
	}
	defer resp.Body.Close()
	// No model credential is configured, so authentication on the data plane
	// succeeds and the missing host credential is a 401. A TLS failure would
	// surface as a transport error above.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
