package auth

import (
	"encoding/base64"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestLoginAccountHeaderComesFromTheConfiguredIDTokenClaim(t *testing.T) {
	s := OAuthSettings{Issuer: "https://issuer.example", ClientID: "client", ClaimHeaders: map[string][]string{"chatgpt-account-id": {"https://api.openai.com/auth", "chatgpt_account_id"}}}
	claims := map[string]any{"iss": s.Issuer, "aud": s.ClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-123"}}
	token := func() string {
		raw, _ := json.Marshal(claims)
		return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
	}
	headers, err := s.LoginHeaders(token())
	require.NoError(t, err)
	require.Equal(t, "account-123", headers["Chatgpt-Account-Id"])
	for _, field := range []string{"iss", "aud", "exp"} {
		old := claims[field]
		claims[field] = "canary-secret"
		_, err = s.LoginHeaders(token())
		require.Error(t, err)
		require.NotContains(t, err.Error(), "canary-secret")
		claims[field] = old
	}
	claims["https://api.openai.com/auth"] = map[string]any{"chatgpt_account_id": "bad\r\nheader"}
	_, err = s.LoginHeaders(token())
	require.Error(t, err)
	s.ClaimHeaders = map[string][]string{"Authorization": {"iss"}}
	_, err = s.LoginHeaders(token())
	require.Error(t, err)
}
