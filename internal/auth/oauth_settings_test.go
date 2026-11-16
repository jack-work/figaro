package auth

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

func TestDeclaredPKCEEndpointOverrides(t *testing.T) {
	p := newPKCEIdP(t)
	var file struct {
		OAuth OAuthSettings `toml:"oauth"`
	}
	_, err := toml.Decode(fmt.Sprintf(`[oauth]
issuer = %q
client_id = "declared-client"
flow = "pkce"
scopes = ["openid", "profile"]
authorize_url = %q
token_url = %q
redirect_uri = "http://localhost:1455/auth/callback"
[oauth.authorize_params]
originator = "declared-cli"
organization_prompt = "true"
`, p.srv.URL, p.srv.URL+"/custom/authorize?prompt=login", p.srv.URL+"/custom/token"), &file)
	require.NoError(t, err)
	cfg, err := file.OAuth.PKCEConfig("declared")
	require.NoError(t, err)
	require.Equal(t, p.srv.URL+"/custom/authorize?prompt=login", cfg.AuthorizeURL)
	require.Equal(t, p.srv.URL+"/custom/token", cfg.TokenURL)
	require.Equal(t, "http://localhost:1455/auth/callback", cfg.RedirectURI)
	require.Equal(t, "declared-cli", cfg.AuthorizeParams["originator"])
}

func TestDeclaredPKCERejectsUnsafeOverrides(t *testing.T) {
	p := newPKCEIdP(t)
	badBoth := []string{
		"http://example.invalid/token",
		p.srv.URL + "/token#fragment",
		strings.Replace(p.srv.URL, "https://", "https://user@", 1) + "/token",
		"/relative",
		p.srv.URL + "/token?state=wrong",
	}
	for _, field := range []string{"authorize_url", "token_url"} {
		cases := badBoth
		if field == "authorize_url" {
			cases = append(cases, "https://other.invalid/authorize")
		}
		for _, raw := range cases {
			t.Run(field+"/"+raw, func(t *testing.T) {
				var file struct {
					OAuth OAuthSettings `toml:"oauth"`
				}
				_, err := toml.Decode(fmt.Sprintf("[oauth]\nissuer=%q\nclient_id=\"c\"\nflow=\"pkce\"\n%s=%q\n", p.srv.URL, field, raw), &file)
				require.NoError(t, err)
				_, err = file.OAuth.PKCEConfig("declared")
				require.Error(t, err)
			})
		}
	}
}

func TestDeclaredPKCEAllowsExplicitCrossOriginHTTPSTokenURLButRejectsDiscoveredOffOrigin(t *testing.T) {
	p := newPKCEIdP(t)
	cfg, err := OAuthSettings{
		Issuer:       p.srv.URL,
		ClientID:     "google-client",
		Flow:         FlowPKCE,
		AuthorizeURL: p.srv.URL + "/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
	}.PKCEConfig("google")
	require.NoError(t, err)
	require.Equal(t, "https://oauth2.googleapis.com/token", cfg.TokenURL)

	// Device flow must still require token_url to stay on the issuer origin.
	_, err = OAuthSettings{
		Issuer:   p.srv.URL,
		ClientID: "c",
		Flow:     FlowDevice,
		TokenURL: "https://oauth2.googleapis.com/token",
	}.DeviceConfig("declared")
	require.Error(t, err)
}

func TestPKCERejectsReservedAuthorizationParameters(t *testing.T) {
	p := newPKCEIdP(t)
	for _, key := range []string{"state", "client_id", "redirect_uri", "response_type", "scope", "code_challenge", "code_challenge_method", "code_verifier", "grant_type", "code", "client_secret", "request", "request_uri", "response_mode", "STATE", "Client_Id", "ReDiReCt_UrI", "nonce", "id_token"} {
		t.Run(key, func(t *testing.T) {
			settings := OAuthSettings{Issuer: p.srv.URL, ClientID: "client", Flow: FlowPKCE, AuthorizeParams: map[string]string{key: "override"}}
			_, err := settings.PKCEConfig("declared")
			require.Error(t, err)
			settings.AuthorizeParams = nil
			settings.AuthorizeURL = p.srv.URL + "/authorize?" + key + "=override"
			_, err = settings.PKCEConfig("declared")
			require.Error(t, err)
		})
	}
}
