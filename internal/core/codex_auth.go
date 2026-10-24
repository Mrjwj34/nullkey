package core

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// codexOAuthClientID is the public OAuth client used by the Codex CLI. It is
// not a secret; keeping it here lets host imports refresh tokens without
// requiring the operator to configure a private OAuth app.
const codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const codexAccountClaim = "https://api.openai.com/auth"

type codexHostLogin struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	ExpiresAt    *time.Time
}

// parseCodexHostLogin extracts the complete ChatGPT login from
// ~/.codex/auth.json. Recent Codex versions store only the token bundle; the
// expiry lives in the access token's JWT exp claim.
func parseCodexHostLogin(raw []byte) (codexHostLogin, bool) {
	var payload struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
			ExpiresAt    string `json:"expires_at"`
		} `json:"tokens"`
		ExpiresAt    string `json:"expires_at"`
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`
		APIKey       string `json:"api_key"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return codexHostLogin{}, false
	}
	if payload.Tokens.AccessToken == "" {
		return codexHostLogin{}, false
	}
	login := codexHostLogin{
		AccessToken:  payload.Tokens.AccessToken,
		RefreshToken: payload.Tokens.RefreshToken,
		IDToken:      payload.Tokens.IDToken,
		AccountID:    payload.Tokens.AccountID,
	}
	if login.AccountID == "" {
		login.AccountID = jwtAccountID(payload.Tokens.IDToken)
	}
	if login.AccountID == "" {
		login.AccountID = jwtAccountID(payload.Tokens.AccessToken)
	}
	if expiry := jwtExpiry(payload.Tokens.AccessToken); expiry != nil {
		login.ExpiresAt = expiry
	} else if expiry := parseRFC3339(payload.Tokens.ExpiresAt); expiry != nil {
		login.ExpiresAt = expiry
	} else if expiry := parseRFC3339(payload.ExpiresAt); expiry != nil {
		login.ExpiresAt = expiry
	} else if expiry := jwtExpiry(payload.Tokens.IDToken); expiry != nil {
		login.ExpiresAt = expiry
	}
	return login, true
}

func parseRFC3339(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

// jwtClaims decodes the payload of a JWT without verifying it. The token came
// from the host Keychain/auth file and is only inspected for non-secret
// metadata (expiry and account id); it is never trusted for authorization.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some encoders include padding.
		raw, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

func jwtExpiry(token string) *time.Time {
	claims := jwtClaims(token)
	if claims == nil {
		return nil
	}
	switch value := claims["exp"].(type) {
	case float64:
		if value <= 0 {
			return nil
		}
		t := time.Unix(int64(value), 0).UTC()
		return &t
	case json.Number:
		if n, err := value.Int64(); err == nil && n > 0 {
			t := time.Unix(n, 0).UTC()
			return &t
		}
	}
	return nil
}

func jwtAccountID(token string) string {
	claims := jwtClaims(token)
	if claims == nil {
		return ""
	}
	if nested, ok := claims[codexAccountClaim].(map[string]any); ok {
		if id, ok := nested["chatgpt_account_id"].(string); ok && id != "" {
			return id
		}
	}
	if id, ok := claims["chatgpt_account_id"].(string); ok {
		return id
	}
	return ""
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
