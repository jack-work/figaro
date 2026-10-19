package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

var (
	serviceTokensMu sync.Mutex
	serviceTokens   = map[string]auth.TokenResolver{}
)

func ServiceToken(loaded *config.Loaded, name string) (auth.TokenResolver, error) {
	st, ok := loaded.Config.ServiceTokens[name]
	if !ok {
		return nil, fmt.Errorf("service token %q is not configured ([service_tokens.%s] in config.toml)", name, name)
	}
	if st.Issuer == "" && st.TokenURL == "" {
		return nil, fmt.Errorf("service token %q names neither an issuer nor a token_url", name)
	}
	var (
		make func() auth.TokenResolver
		path string
	)
	switch st.Grant {
	case "", auth.GrantClientCredentials:
		if st.SubjectCredential != "" {
			return nil, fmt.Errorf("service token %q: subject_credential belongs to grant = %q", name, auth.GrantTokenExchange)
		}
		secret, err := credentialID("secret_credential", st.SecretCredential)
		if err != nil {
			return nil, fmt.Errorf("service token %q: %w", name, err)
		}
		path = secret
		make = func() auth.TokenResolver {
			return &auth.ClientCredentials{
				Issuer:     st.Issuer,
				TokenURL:   st.TokenURL,
				ClientID:   st.ClientID,
				Secret:     &auth.File{Path: secret, Required: true},
				AuthMethod: st.AuthMethod,
				Audience:   st.Audience,
				Scopes:     st.Scopes,
			}
		}
	case auth.GrantTokenExchange:
		// A public client: an IdP that holds it as public refuses a secret.
		if st.SecretCredential != "" || st.AuthMethod != "" {
			return nil, fmt.Errorf("service token %q: token-exchange is a public client; drop secret_credential and auth_method", name)
		}
		if st.ClientID == "" {
			return nil, fmt.Errorf("service token %q: token-exchange needs client_id", name)
		}
		subject, err := credentialID("subject_credential", st.SubjectCredential)
		if err != nil {
			return nil, fmt.Errorf("service token %q: %w", name, err)
		}
		path = subject
		make = func() auth.TokenResolver {
			return &auth.TokenExchange{
				Issuer:           st.Issuer,
				TokenURL:         st.TokenURL,
				ClientID:         st.ClientID,
				Subject:          &auth.File{Path: subject, Required: true},
				SubjectTokenType: st.SubjectTokenType,
				Audience:         st.Audience,
				Scopes:           st.Scopes,
			}
		}
	default:
		return nil, fmt.Errorf("service token %q: grant %q is not %q or %q", name, st.Grant, auth.GrantClientCredentials, auth.GrantTokenExchange)
	}
	key := fmt.Sprintf("%s\x00%s\x00%#v", name, path, st)
	serviceTokensMu.Lock()
	defer serviceTokensMu.Unlock()
	if r, ok := serviceTokens[key]; ok {
		return r, nil
	}
	r := make()
	serviceTokens[key] = r
	return r, nil
}

func ServiceTokenOf(loaded *config.Loaded, provider string) (string, error) {
	if loaded == nil {
		return "", nil
	}
	var pa config.ProviderAuth
	if err := loaded.LoadProviderAuth(provider, &pa); err != nil {
		return "", err
	}
	return strings.TrimSpace(pa.ServiceToken), nil
}

func credentialID(key, id string) (string, error) {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return "", fmt.Errorf("%s %q must be a bare credential id under $CREDENTIALS_DIRECTORY", key, id)
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return "", fmt.Errorf("%s %q needs $CREDENTIALS_DIRECTORY, which is unset", key, id)
	}
	return filepath.Join(dir, id), nil
}
