package boot

import (
	"fmt"
	"sync"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

var (
	credResolversMu sync.Mutex
	credResolvers   = map[string]*auth.CredSocket{}
)

func CredOf(loaded *config.Loaded, provider string) (*config.CredConfig, error) {
	if loaded == nil {
		return nil, nil
	}
	var pa config.ProviderAuth
	if err := loaded.LoadProviderAuth(provider, &pa); err != nil {
		return nil, err
	}
	if err := pa.ValidateAuthSources(); err != nil {
		return nil, fmt.Errorf("%s: %w", loaded.ProviderAuthPath(provider), err)
	}
	if pa.Cred == nil {
		return nil, nil
	}
	norm := pa.Cred.Normalized()
	return &norm, nil
}

func CredResolver(cred config.CredConfig) (auth.TokenResolver, error) {
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	norm := cred.Normalized()
	key := norm.Socket + "\x00" + norm.Name
	credResolversMu.Lock()
	defer credResolversMu.Unlock()
	if r, ok := credResolvers[key]; ok {
		return r, nil
	}
	r := &auth.CredSocket{
		Socket: norm.Socket,
		Name:   norm.Name,
	}
	credResolvers[key] = r
	return r, nil
}
