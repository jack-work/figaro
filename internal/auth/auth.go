// Package auth resolves provider credentials; OAuth tokens are stored and
// refreshed by the hush agent.
package auth

// OAuthConfig describes an OAuth provider's endpoints.
type OAuthConfig struct {
	ProviderName string
	AuthorizeURL string
	TokenURL     string
	RedirectURI  string
	ClientID     string
	Scopes       string
}
