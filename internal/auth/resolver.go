package auth

// TokenResolver provides an API token to the provider.
// The provider calls Resolve() before each request batch.
// Implementations handle caching, refresh, and decryption internally.
type TokenResolver interface {
	// Resolve returns a valid API token. It may refresh expired
	// OAuth tokens or simply return a static key.
	Resolve() (string, error)

	// Invalidate signals that the given token was rejected by the
	// upstream API (e.g. 401). Implementations must compare the
	// supplied token against any cached value and only clear on
	// match — concurrent callers may have already advanced past it.
	Invalidate(token string)
}
