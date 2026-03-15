package auth

// AnthropicOAuth is the OAuth configuration for Anthropic (Claude Pro/Max).
var AnthropicOAuth = OAuthConfig{
	ProviderName: "anthropic",
	AuthorizeURL: "https://claude.ai/oauth/authorize",
	TokenURL:     "https://console.anthropic.com/v1/oauth/token",
	RedirectURI:  "https://console.anthropic.com/oauth/code/callback",
	ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
	Scopes:       "org:create_api_key user:profile user:inference",
}
