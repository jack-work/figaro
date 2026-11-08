package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	hush "github.com/jack-work/hush/client"
)

// Login runs the PKCE flow and registers the result with the hush agent,
// which owns refresh from then on.
func Login(ctx context.Context, hushClient *hush.Client, cfg OAuthConfig, c Console) error {
	if err := hushClient.Ping(); err != nil {
		return fmt.Errorf("hush agent is not running. Start it: hush up -d")
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}

	params := url.Values{
		"code":                  {"true"},
		"client_id":             {cfg.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {cfg.RedirectURI},
		"scope":                 {cfg.Scopes},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {pkce.Verifier},
	}
	authURL := cfg.AuthorizeURL + "?" + params.Encode()

	c.Note("Sign in at this link, then paste the code it gives you:")
	c.Link(authURL)
	c.Open(authURL)

	pasted, err := c.AskSecret(ctx, "Paste the code here: ")
	if err != nil {
		return fmt.Errorf("read code: %w", err)
	}
	codeInput := strings.TrimSpace(string(pasted))
	clear(pasted)

	parts := strings.SplitN(codeInput, "#", 2)
	code := parts[0]
	state := ""
	if len(parts) > 1 {
		state = parts[1]
	}

	tokenBody, _ := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     cfg.ClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  cfg.RedirectURI,
		"code_verifier": pkce.Verifier,
	})

	resp, err := http.Post(cfg.TokenURL, "application/json", strings.NewReader(string(tokenBody)))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody json.RawMessage
		json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(errBody))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("parse token response: %w", err)
	}

	if err := hushClient.OAuthRegister(hush.OAuthRegisterRequest{
		Name:         cfg.ProviderName,
		AuthorizeURL: cfg.AuthorizeURL,
		TokenURL:     cfg.TokenURL,
		RedirectURI:  cfg.RedirectURI,
		ClientID:     cfg.ClientID,
		Scopes:       cfg.Scopes,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
	}); err != nil {
		return fmt.Errorf("register with hush: %w", err)
	}

	c.Done(fmt.Sprintf("Logged in · token expires in %ds", tokenResp.ExpiresIn))
	return nil
}
