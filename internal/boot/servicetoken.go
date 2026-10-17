package boot

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

func ServiceToken(loaded *config.Loaded, name string) (auth.TokenResolver, error) {
	st, ok := loaded.Config.ServiceTokens[name]
	if !ok {
		return nil, fmt.Errorf("service token %q is not configured ([service_tokens.%s] in config.toml)", name, name)
	}
	secret, err := credentialID(st.SecretCredential)
	if err != nil {
		return nil, fmt.Errorf("service token %q: %w", name, err)
	}
	if st.Issuer == "" && st.TokenURL == "" {
		return nil, fmt.Errorf("service token %q names neither an issuer nor a token_url", name)
	}
	return &auth.ClientCredentials{
		Issuer:     st.Issuer,
		TokenURL:   st.TokenURL,
		ClientID:   st.ClientID,
		Secret:     &auth.File{Path: secret, Required: true},
		AuthMethod: st.AuthMethod,
		Audience:   st.Audience,
		Scopes:     st.Scopes,
	}, nil
}

func credentialID(id string) (string, error) {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return "", fmt.Errorf("secret_credential %q must be a bare credential id under $CREDENTIALS_DIRECTORY", id)
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return "", fmt.Errorf("secret_credential %q needs $CREDENTIALS_DIRECTORY, which is unset", id)
	}
	return filepath.Join(dir, id), nil
}
