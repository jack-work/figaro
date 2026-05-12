// Package auth resolves API credentials for providers. OAuth tokens are
// stored and refreshed by the hush agent; this package only carries the
// per-provider OAuth metadata and the strategies that walk env, config,
// and hush in priority order.
package auth

// OAuthConfig describes an OAuth provider's endpoints. Used by the login
// flow to drive the PKCE handshake and seed hush with the result.
type OAuthConfig struct {
	ProviderName string
	AuthorizeURL string
	TokenURL     string
	RedirectURI  string
	ClientID     string
	Scopes       string
}
