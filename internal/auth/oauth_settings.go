package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func oauthClient() *http.Client {
	client := *oidcHTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func issuerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("oauth issuer must be an https URL without userinfo, query or fragment")
	}
	return u, nil
}

func (s OAuthSettings) endpoints() (oidcConfiguration, *url.URL, error) {
	issuer, err := issuerURL(s.Issuer)
	if err != nil {
		return oidcConfiguration{}, nil, err
	}
	var oc oidcConfiguration
	if s.TokenURL == "" || s.AuthorizeURL == "" || s.Flow != FlowPKCE {
		oc, _, err = discover(oauthClient(), s.Issuer)
		if err != nil {
			return oc, nil, err
		}
	}
	if s.AuthorizeURL != "" {
		oc.AuthorizationEndpoint = s.AuthorizeURL
	}
	if s.TokenURL != "" {
		oc.TokenEndpoint = s.TokenURL
	}
	if err = validateOAuthEndpoint(oc.TokenEndpoint, issuer, false); err != nil {
		return oc, nil, fmt.Errorf("token endpoint: %w", err)
	}
	return oc, issuer, nil
}

func validateOAuthEndpoint(raw string, issuer *url.URL, authorize bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || !sameOrigin(raw, issuer) {
		return fmt.Errorf("not on the issuer origin or contains userinfo or fragment")
	}
	if !authorize && (u.RawQuery != "" || u.ForceQuery) {
		return fmt.Errorf("query parameters are not allowed")
	}
	if authorize {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return fmt.Errorf("invalid authorization query")
		}
		for key := range query {
			if err := validateAuthorizeKey(key); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAuthorizeKey(key string) error {
	if key == "" || strings.TrimSpace(key) != key {
		return fmt.Errorf("authorization parameter name is empty or contains surrounding whitespace")
	}
	switch strings.ToLower(key) {
	case "state", "client_id", "client_secret", "redirect_uri", "response_type", "scope", "code_challenge", "code_challenge_method", "code_verifier", "code", "grant_type", "request", "request_uri", "response_mode", "access_token", "refresh_token", "id_token", "nonce":
		return fmt.Errorf("authorization parameter %q is owned by the OAuth protocol", key)
	}
	return nil
}

func validateAuthorizeParams(endpoint string, params map[string]string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid authorization URL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("invalid authorization query")
	}
	seen := map[string]bool{}
	for key, values := range query {
		if err := validateAuthorizeKey(key); err != nil {
			return err
		}
		folded := strings.ToLower(key)
		if seen[folded] || len(values) != 1 {
			return fmt.Errorf("duplicate authorization parameter %q", key)
		}
		seen[folded] = true
	}
	for key := range params {
		if err := validateAuthorizeKey(key); err != nil {
			return err
		}
		folded := strings.ToLower(key)
		if seen[folded] {
			return fmt.Errorf("duplicate authorization parameter %q", key)
		}
		seen[folded] = true
	}
	return nil
}

func callbackURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Path == "" || !strings.HasPrefix(u.Path, "/") {
		return nil, fmt.Errorf("redirect_uri must be an http loopback URL with an explicit port and callback path, without userinfo, query or fragment")
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("redirect_uri host must be localhost or a loopback IP")
		}
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("redirect_uri requires an explicit nonzero port")
	}
	return u, nil
}

func pkceListener(raw string) (net.Listener, string, error) {
	if raw == "" {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, "", err
		}
		return ln, "http://" + ln.Addr().String() + pkceCallbackPath, nil
	}
	u, err := callbackURL(raw)
	if err != nil {
		return nil, "", err
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, u.Port()))
	if err != nil {
		return nil, "", err
	}
	return ln, raw, nil
}

func (c PKCEConfig) validate() error {
	if c.RedirectURI != "" {
		if _, err := callbackURL(c.RedirectURI); err != nil {
			return err
		}
	}
	if err := validateAuthorizeParams(c.AuthorizeURL, c.AuthorizeParams); err != nil {
		return err
	}
	return nil
}
