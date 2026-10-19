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
	path, build, err := serviceGrant(st)
	if err != nil {
		return nil, fmt.Errorf("service token %q: %w", name, err)
	}
	if st.Issuer == "" && st.TokenURL == "" {
		return nil, fmt.Errorf("service token %q names neither an issuer nor a token_url", name)
	}
	key := fmt.Sprintf("%s\x00%s\x00%#v", name, path, st)
	serviceTokensMu.Lock()
	defer serviceTokensMu.Unlock()
	if r, ok := serviceTokens[key]; ok {
		return r, nil
	}
	r := build()
	serviceTokens[key] = r
	return r, nil
}

func serviceGrant(st config.ServiceToken) (string, func() auth.TokenResolver, error) {
	switch st.Grant {
	case "", auth.GrantClientCredentials:
		if st.SubjectCredential != "" || st.SubjectTokenType != "" {
			return "", nil, fmt.Errorf("subject_credential and subject_token_type belong to grant = %q", auth.GrantTokenExchange)
		}
		secret, err := CredentialPath(st.SecretCredential)
		if err != nil {
			return "", nil, fmt.Errorf("secret_credential %w", err)
		}
		return secret, func() auth.TokenResolver {
			return &auth.ClientCredentials{
				Issuer:     st.Issuer,
				TokenURL:   st.TokenURL,
				ClientID:   st.ClientID,
				Secret:     &auth.File{Path: secret, Required: true},
				AuthMethod: st.AuthMethod,
				Audience:   st.Audience,
				Scopes:     st.Scopes,
			}
		}, nil
	case auth.GrantTokenExchange:
		if st.SecretCredential != "" || st.AuthMethod != "" {
			return "", nil, fmt.Errorf("grant = %q is a public client and sends no secret: drop secret_credential and auth_method", auth.GrantTokenExchange)
		}
		if st.ClientID == "" {
			return "", nil, fmt.Errorf("grant = %q needs client_id", auth.GrantTokenExchange)
		}
		subject, err := CredentialPath(st.SubjectCredential)
		if err != nil {
			return "", nil, fmt.Errorf("subject_credential %w", err)
		}
		return subject, func() auth.TokenResolver {
			return &auth.TokenExchange{
				Issuer:           st.Issuer,
				TokenURL:         st.TokenURL,
				ClientID:         st.ClientID,
				Subject:          &auth.File{Path: subject, Required: true},
				SubjectTokenType: st.SubjectTokenType,
				Audience:         st.Audience,
				Scopes:           st.Scopes,
			}
		}, nil
	}
	return "", nil, fmt.Errorf("grant %q is not %q or %q", st.Grant, auth.GrantClientCredentials, auth.GrantTokenExchange)
}

func serviceCredential(st config.ServiceToken) (string, string) {
	if st.Grant == auth.GrantTokenExchange {
		return "subject", st.SubjectCredential
	}
	return "secret", st.SecretCredential
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

func CredentialPath(id string) (string, error) {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return "", fmt.Errorf("%q must be a bare credential id under $CREDENTIALS_DIRECTORY", id)
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return "", fmt.Errorf("%q needs $CREDENTIALS_DIRECTORY, which is unset", id)
	}
	return filepath.Join(dir, id), nil
}
