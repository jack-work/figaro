package auth

type TokenResolver interface {
	Resolve() (string, error)

	// Invalidate signals a rejected token. A non-nil error means the next
	// Resolve cannot do better.
	Invalidate(token string) error
}
