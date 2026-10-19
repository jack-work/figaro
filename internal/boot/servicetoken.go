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
	serviceTokens   = map[string]*auth.ClientCredentials{}
)

func ServiceToken(loaded *config.Loaded, name string) (auth.TokenResolver, error) {
	st, ok := loaded.Config.ServiceTokens[name]
	if !ok {
		return nil, fmt.Errorf("service token %q is not configured ([service_tokens.%s] in config.toml)", name, name)
	}
	secret, err := CredentialPath(st.SecretCredential)
	if err != nil {
		return nil, fmt.Errorf("service token %q: secret_credential %w", name, err)
	}
	if st.Issuer == "" && st.TokenURL == "" {
		return nil, fmt.Errorf("service token %q names neither an issuer nor a token_url", name)
	}
	key := fmt.Sprintf("%s\x00%s\x00%#v", name, secret, st)
	serviceTokensMu.Lock()
	defer serviceTokensMu.Unlock()
	if cc, ok := serviceTokens[key]; ok {
		return cc, nil
	}
	cc := &auth.ClientCredentials{
		Issuer:     st.Issuer,
		TokenURL:   st.TokenURL,
		ClientID:   st.ClientID,
		Secret:     &auth.File{Path: secret, Required: true},
		AuthMethod: st.AuthMethod,
		Audience:   st.Audience,
		Scopes:     st.Scopes,
	}
	serviceTokens[key] = cc
	return cc, nil
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
