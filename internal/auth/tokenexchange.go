package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	GrantClientCredentials = "client_credentials"
	GrantTokenExchange     = "token-exchange"
	TokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
)

type TokenExchange struct {
	Issuer           string
	TokenURL         string
	ClientID         string
	Subject          CredentialStrategy
	SubjectTokenType string
	Audience         []string
	Scopes           []string
	HTTP             *http.Client
	Now              func() time.Time

	cache tokenCache
}

var _ TokenResolver = (*TokenExchange)(nil)

func (x *TokenExchange) Resolve() (string, error) {
	return x.cache.resolve(nowOr(x.Now), x.mint)
}

func (x *TokenExchange) Invalidate(token string) error {
	x.cache.invalidate(token)
	return nil
}

func (x *TokenExchange) endpoint() (string, error) {
	if x.TokenURL == "" {
		oc, _, err := discover(httpOr(x.HTTP), x.Issuer)
		if err != nil {
			return "", fmt.Errorf("client %s: %w", x.ClientID, err)
		}
		x.TokenURL = oc.TokenEndpoint
	}
	return x.TokenURL, nil
}

func (x *TokenExchange) mint() (string, time.Duration, error) {
	if x.ClientID == "" || x.Subject == nil {
		return "", 0, fmt.Errorf("token-exchange: a client id and a subject token source are required")
	}
	subject, ok, err := x.Subject.TryResolve()
	if err != nil {
		return "", 0, fmt.Errorf("client %s: subject token: %w", x.ClientID, err)
	}
	if !ok {
		return "", 0, fmt.Errorf("client %s: no subject token available", x.ClientID)
	}
	endpoint, err := x.endpoint()
	if err != nil {
		return "", 0, err
	}
	typ := x.SubjectTokenType
	if typ == "" {
		typ = TokenTypeAccessToken
	}
	form := url.Values{
		"grant_type":         {grantTypeTokenExchange},
		"client_id":          {x.ClientID},
		"subject_token":      {subject},
		"subject_token_type": {typ},
	}
	if len(x.Audience) > 0 {
		form.Set("audience", strings.Join(x.Audience, " "))
	}
	if len(x.Scopes) > 0 {
		form.Set("scope", strings.Join(x.Scopes, " "))
	}
	tr, err := requestToken(httpOr(x.HTTP), endpoint, x.ClientID, form, nil)
	return tr.AccessToken, tr.lifetime(), err
}
