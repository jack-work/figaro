package auth

// TokenResolver provides an API token to the provider.
// The provider calls Resolve() before each request batch.
// Implementations handle caching, refresh, and decryption internally.
type TokenResolver interface {
	// Resolve returns a valid API token. It may refresh expired
	// OAuth tokens or simply return a static key.
	Resolve() (string, error)
}

// StaticKey is a TokenResolver that always returns the same API key.
type StaticKey struct {
	Key string
}

func (s *StaticKey) Resolve() (string, error) {
	return s.Key, nil
}

// OAuthResolver wraps a TokenManager as a TokenResolver.
// It handles decryption via hush and automatic refresh.
type OAuthResolver struct {
	Manager *TokenManager
}

func (o *OAuthResolver) Resolve() (string, error) {
	return o.Manager.AccessToken()
}
