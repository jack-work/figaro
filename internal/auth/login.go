package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	hush "github.com/jack-work/hush/client"
)

func Login(ctx context.Context, hushClient *hush.Client, cfg OAuthConfig, c Console) error {
	if err := hushClient.Ping(); err != nil {
		return fmt.Errorf("hush agent is not running. Start it: hush up -d")
	}
	if err := validateHTTPSTokenEndpoint(cfg.TokenURL); err != nil {
		return fmt.Errorf("token endpoint: %w", err)
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

	exchangeReq := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     cfg.ClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  cfg.RedirectURI,
		"code_verifier": pkce.Verifier,
	}
	if cfg.ClientSecretFile != "" {
		secret, err := readClientSecret(cfg.ClientSecretFile)
		if err != nil {
			return err
		}
		exchangeReq["client_secret"] = secret
	}
	tokenBody, _ := json.Marshal(exchangeReq)
	defer clear(tokenBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, bytes.NewReader(tokenBody))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := oauthClient().Do(req)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	defer clear(body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token exchange failed: HTTP %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("parse token response: %w", err)
	}

	if strings.TrimSpace(cfg.ClientSecretFile) != "" {
		if err := RegisterOAuth(hushClient, cfg.ProviderName, OIDCSignIn{
			TokenURL:         cfg.TokenURL,
			ClientID:         cfg.ClientID,
			ClientSecretFile: cfg.ClientSecretFile,
			Scope:            cfg.Scopes,
			Tokens: DeviceTokens{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ExpiresIn:    tokenResp.ExpiresIn,
			},
		}); err != nil {
			return fmt.Errorf("register with hush: %w", err)
		}
	} else if err := hushClient.OAuthRegister(hush.OAuthRegisterRequest{
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
