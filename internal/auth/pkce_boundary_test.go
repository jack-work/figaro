package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoginPKCEValidatesHandConstructedEndpointPairs(t *testing.T) {
	p := newPKCEIdP(t)
	for _, field := range []string{"authorize", "token"} {
		for _, endpoint := range []string{"http://untrusted.invalid/endpoint", "https://untrusted.invalid/endpoint", p.srv.URL + "/endpoint#fragment", strings.Replace(p.srv.URL, "https://", "https://user@", 1) + "/endpoint", "/relative", p.srv.URL + "/endpoint?state=override"} {
			t.Run(field+"/"+endpoint, func(t *testing.T) {
				cfg := p.config(t)
				if field == "authorize" {
					cfg.AuthorizeURL = endpoint
				} else {
					cfg.TokenURL = endpoint
				}
				opened := false
				_, err := loginPKCE(context.Background(), cfg, func(string) { opened = true }, 10*time.Millisecond, quiet{})
				require.Error(t, err)
				require.False(t, opened, "invalid endpoints must be rejected before browser launch")
			})
		}
	}
}

func TestLoginPKCERejectsInvalidEndpointsBeforeListening(t *testing.T) {
	cfg := PKCEConfig{ClientID: "declared", AuthorizeURL: "https://issuer.invalid/authorize", TokenURL: "https://other.invalid/token", RedirectURI: "http://localhost:1455/auth/callback"}
	_, err := loginPKCE(context.Background(), cfg, func(string) { t.Fatal("browser opened") }, time.Second, quiet{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "loopback listener")
}
