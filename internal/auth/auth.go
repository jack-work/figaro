package auth

type OAuthConfig struct {
	ProviderName     string
	AuthorizeURL     string
	TokenURL         string
	RedirectURI      string
	ClientID         string
	ClientSecretFile string
	Scopes           string
}
