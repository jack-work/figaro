package auth

// TokenResolver provides an API token to the provider.
type TokenResolver interface {
	// Resolve returns a valid API token.
	Resolve() (string, error)

	// Invalidate signals a token was rejected (e.g. 401). A non-nil
	// error means invalidation itself failed (e.g. an OAuth refresh
	// was rejected) and the next Resolve cannot do better.
	Invalidate(token string) error
}
