package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ClientSecretBasic = "client_secret_basic"
	ClientSecretPost  = "client_secret_post"
)

type ClientCredentials struct {
	Issuer     string
	TokenURL   string
	ClientID   string
	Secret     CredentialStrategy
	AuthMethod string
	Audience   []string
	Scopes     []string
	HTTP       *http.Client
	Now        func() time.Time

	cache tokenCache
}

var _ TokenResolver = (*ClientCredentials)(nil)

func (c *ClientCredentials) Resolve() (string, error) {
	return c.cache.resolve(nowOr(c.Now), c.mint)
}

func (c *ClientCredentials) Invalidate(token string) error {
	c.cache.invalidate(token)
	return nil
}

func (c *ClientCredentials) endpoint() (string, error) {
	if c.TokenURL == "" {
		oc, _, err := discover(httpOr(c.HTTP), c.Issuer)
		if err != nil {
			return "", fmt.Errorf("client %s: %w", c.ClientID, err)
		}
		c.TokenURL = oc.TokenEndpoint
	}
	return c.TokenURL, nil
}

func (c *ClientCredentials) mint() (string, time.Duration, error) {
	if c.ClientID == "" || c.Secret == nil {
		return "", 0, fmt.Errorf("client_credentials: a client id and a secret source are required")
	}
	secret, ok, err := c.Secret.TryResolve()
	if err != nil {
		return "", 0, fmt.Errorf("client %s: secret: %w", c.ClientID, err)
	}
	if !ok {
		return "", 0, fmt.Errorf("client %s: no secret available", c.ClientID)
	}
	endpoint, err := c.endpoint()
	if err != nil {
		return "", 0, err
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if len(c.Audience) > 0 {
		form.Set("audience", strings.Join(c.Audience, " "))
	}
	if len(c.Scopes) > 0 {
		form.Set("scope", strings.Join(c.Scopes, " "))
	}
	method := c.AuthMethod
	if method == "" {
		method = ClientSecretBasic
	}
	switch method {
	case ClientSecretBasic:
	case ClientSecretPost:
		form.Set("client_id", c.ClientID)
		form.Set("client_secret", secret)
	default:
		return "", 0, fmt.Errorf("client %s: auth method %q is not one of %s, %s", c.ClientID, method, ClientSecretBasic, ClientSecretPost)
	}
	var basic func(*http.Request)
	if method == ClientSecretBasic {
		basic = func(r *http.Request) { r.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(secret)) }
	}
	tr, err := requestToken(httpOr(c.HTTP), endpoint, c.ClientID, form, basic)
	return tr.AccessToken, tr.lifetime(), err
}
