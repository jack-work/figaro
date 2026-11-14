package routed

import (
	"github.com/jack-work/figaro/internal/provider"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDeclaredSubscriptionSettingsSurviveRegistrationAndBuild(t *testing.T) {
	loaded := declaredDir(t, map[string]string{"subscription-settings": `preset="gateway"
base_url="https://backend.example/responses-base"
[oauth]
issuer="https://issuer.example"
client_id="public-client"
flow="pkce"
redirect_uri="http://localhost:1455/auth/callback"
authorize_url="https://issuer.example/oauth/authorize"
token_url="https://issuer.example/oauth/token"
scopes=["openid","offline_access"]
[oauth.authorize_params]
originator="test-client"
[routing]
catalog="off"
default="responses"
[responses]
omit_fields=["max_output_tokens"]
`})
	require.Contains(t, RegisterDeclared(loaded), "subscription-settings")
	reg := provider.Lookup("subscription-settings")
	require.NotNil(t, reg.OAuth)
	require.Equal(t, "http://localhost:1455/auth/callback", reg.OAuth.RedirectURI)
	require.Equal(t, "test-client", reg.OAuth.AuthorizeParams["originator"])
	cfg, err := reg.OAuth.PKCEConfig("subscription-settings")
	require.NoError(t, err)
	require.Equal(t, "https://issuer.example/oauth/token", cfg.TokenURL)
	require.Equal(t, "https://issuer.example/oauth/authorize", cfg.AuthorizeURL)
	built, err := reg.Build(provider.BuildContext{Loaded: loaded, Resolver: staticToken("fake")})
	require.NoError(t, err)
	p := built.(*Provider)
	require.Equal(t, []string{"max_output_tokens"}, p.spec.ResponsesOmitFields)
	require.Empty(t, p.spec.Catalog)
	require.Equal(t, Responses, p.spec.Default)
}
