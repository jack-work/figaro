package auth

// TokenResolver provides an API token to the provider.
type TokenResolver interface {
	// Resolve returns a valid API token.
	Resolve() (string, error)

	// Invalidate signals a token was rejected (e.g. 401).
	Invalidate(token string)
}
