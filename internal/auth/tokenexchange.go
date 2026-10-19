package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// GrantClientCredentials and GrantTokenExchange name a service token's
	// grant in config.toml; the empty grant means client_credentials.
	GrantClientCredentials = "client_credentials"
	GrantTokenExchange     = "token-exchange"

	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"

	// TokenTypeAccessToken is RFC 8693's subject_token_type for an OAuth access
	// token, which is what a Kanidm service-account API token is presented as.
	TokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
)

// TokenExchange mints a bearer by RFC 8693 token exchange: it presents a
// long-lived subject token (a service account's API token) and receives a
// short-lived access token for this client. It is a PUBLIC client: client_id
// rides in the body and no secret is sent, because an IdP that holds the
// client as public refuses one (Kanidm answers invalid_request).
//
// The subject is read on every mint, never cached, so a rotated token file is
// used from the next exchange on.
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

// Invalidate drops a token a server rejected; the next Resolve re-exchanges.
func (x *TokenExchange) Invalidate(token string) error {
	x.cache.invalidate(token)
	return nil
}

func (x *TokenExchange) endpoint() (string, error) {
	if x.TokenURL == "" {
		u, err := discoverTokenEndpoint(httpOr(x.HTTP), x.Issuer, x.ClientID)
		if err != nil {
			return "", err
		}
		x.TokenURL = u
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
	return postToken(httpOr(x.HTTP), endpoint, x.ClientID, form, nil)
}
