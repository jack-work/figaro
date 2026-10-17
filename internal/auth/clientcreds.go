package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

	mu      sync.Mutex
	token   string
	expires time.Time
}

var _ TokenResolver = (*ClientCredentials)(nil)

const clientCredentialsSkew = 60 * time.Second

func (c *ClientCredentials) Resolve() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Before(c.expires) {
		return c.token, nil
	}
	tok, ttl, err := c.mint()
	if err != nil {
		return "", err
	}
	c.token, c.expires = tok, now.Add(ttl-min(clientCredentialsSkew, ttl/2))
	return tok, nil
}

func (c *ClientCredentials) Invalidate(token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token, c.expires = "", time.Time{}
	}
	return nil
}

func (c *ClientCredentials) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *ClientCredentials) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return oidcHTTP
}

func (c *ClientCredentials) endpoint() (string, error) {
	if c.TokenURL != "" {
		return c.TokenURL, nil
	}
	issuer := strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" || iu.Host == "" {
		return "", fmt.Errorf("client %s: issuer %q is not an https URL", c.ClientID, c.Issuer)
	}
	var oc oidcConfiguration
	if err := getJSONWith(c.client(), issuer+"/.well-known/openid-configuration", &oc); err != nil {
		return "", fmt.Errorf("client %s: oidc discovery: %w", c.ClientID, err)
	}
	if strings.TrimRight(oc.Issuer, "/") != issuer {
		return "", fmt.Errorf("client %s: discovery names issuer %q, configured %q", c.ClientID, oc.Issuer, c.Issuer)
	}
	if !sameOrigin(oc.TokenEndpoint, iu) {
		return "", fmt.Errorf("client %s: token endpoint %q is not on the issuer %s", c.ClientID, oc.TokenEndpoint, issuer)
	}
	c.TokenURL = oc.TokenEndpoint
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
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if method == ClientSecretBasic {
		req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(secret))
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("client %s: token request: %w", c.ClientID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", 0, fmt.Errorf("client %s: token endpoint: HTTP %d: %s: %s", c.ClientID, resp.StatusCode, out.Error, out.Description)
		}
		return "", 0, fmt.Errorf("client %s: token endpoint: HTTP %d", c.ClientID, resp.StatusCode)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("client %s: token endpoint returned no access_token", c.ClientID)
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return "", 0, fmt.Errorf("client %s: token type %q, want bearer", c.ClientID, out.TokenType)
	}
	return out.AccessToken, time.Duration(out.ExpiresIn) * time.Second, nil
}
