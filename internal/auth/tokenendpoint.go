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

const tokenSkew = 60 * time.Second

type tokenCache struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

func (c *tokenCache) resolve(now time.Time, mint func() (string, time.Duration, error)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && now.Before(c.expires) {
		return c.token, nil
	}
	tok, ttl, err := mint()
	if err != nil {
		return "", err
	}
	c.token, c.expires = tok, now.Add(ttl-min(tokenSkew, ttl/2))
	return tok, nil
}

func (c *tokenCache) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token, c.expires = "", time.Time{}
	}
}

func nowOr(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now()
}

func httpOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return oidcHTTP
}

func discover(client *http.Client, issuer string) (oidcConfiguration, *url.URL, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" || iu.Host == "" {
		return oidcConfiguration{}, nil, fmt.Errorf("issuer %q is not an https URL", issuer)
	}
	doc := issuer + "/.well-known/openid-configuration"
	var oc oidcConfiguration
	if err := getJSONWith(client, doc, &oc); err != nil {
		return oidcConfiguration{}, nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if strings.TrimRight(oc.Issuer, "/") != issuer {
		return oidcConfiguration{}, nil, fmt.Errorf("%s names issuer %q, configured %q", doc, oc.Issuer, issuer)
	}
	if !sameOrigin(oc.TokenEndpoint, iu) {
		return oidcConfiguration{}, nil, fmt.Errorf("token endpoint %q is not on the issuer %s", oc.TokenEndpoint, issuer)
	}
	return oc, iu, nil
}

type tokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

func (t tokenResponse) lifetime() time.Duration { return time.Duration(t.ExpiresIn) * time.Second }

func requestToken(client *http.Client, endpoint, clientID string, form url.Values, authorize func(*http.Request)) (tokenResponse, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if authorize != nil {
		authorize(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("client %s: token request: %w", clientID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, err
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return tokenResponse{}, fmt.Errorf("client %s: token endpoint: HTTP %d: %s: %s", clientID, resp.StatusCode, out.Error, out.Description)
		}
		return tokenResponse{}, fmt.Errorf("client %s: token endpoint: HTTP %d", clientID, resp.StatusCode)
	}
	if out.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("client %s: token endpoint returned no access_token", clientID)
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return tokenResponse{}, fmt.Errorf("client %s: token type %q, want bearer", clientID, out.TokenType)
	}
	return tokenResponse{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresIn: out.ExpiresIn}, nil
}
