package peertest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/jack-work/figaro/internal/config"
)

const IssuerURL = "https://issuer.test.figaro"

type Issuer struct {
	t    testing.TB
	key  *rsa.PrivateKey
	JWKS *httptest.Server
}

func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &Issuer{t: t, key: k}
	is.JWKS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	}))
	t.Cleanup(is.JWKS.Close)
	return is
}

func (is *Issuer) Mint(client string, ttl time.Duration) string {
	is.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: is.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		is.t.Fatal(err)
	}
	now := time.Now()
	raw, err := josejwt.Signed(signer).Claims(map[string]any{
		"iss": IssuerURL, "sub": client, "client_id": client,
		"iat": now.Unix(), "exp": now.Add(ttl).Unix(),
	}).Serialize()
	if err != nil {
		is.t.Fatal(err)
	}
	return raw
}

func (is *Issuer) Config(clients ...string) []config.IssuerConfig {
	m := map[string]string{}
	for _, c := range clients {
		m[c] = "user"
	}
	return []config.IssuerConfig{{Issuer: IssuerURL, JWKS: is.JWKS.URL, Clients: m}}
}
