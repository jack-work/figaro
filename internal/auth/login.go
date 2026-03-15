package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// Login performs the full OAuth PKCE login flow:
// 1. Generate PKCE verifier/challenge
// 2. Open browser to authorize URL
// 3. Prompt user to paste authorization code
// 4. Exchange code for tokens
// 5. Encrypt and save via hush
func Login(mgr *TokenManager, promptCode func() (string, error)) error {
	cfg := mgr.config

	// Check hush agent
	if err := mgr.hush.Ping(); err != nil {
		return fmt.Errorf("hush agent is not running. Start it: hush up -d")
	}

	// Generate PKCE
	pkce, err := GeneratePKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}

	// Build authorization URL
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

	// Open browser
	fmt.Println("Opening browser for login...")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	openBrowser(authURL)

	// Prompt for code
	fmt.Print("Paste the authorization code: ")
	codeInput, err := promptCode()
	if err != nil {
		return fmt.Errorf("read code: %w", err)
	}
	codeInput = strings.TrimSpace(codeInput)

	// Parse code#state format
	parts := strings.SplitN(codeInput, "#", 2)
	code := parts[0]
	state := ""
	if len(parts) > 1 {
		state = parts[1]
	}

	// Exchange code for tokens
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

	// Save encrypted via hush
	if err := mgr.SaveLogin(tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	fmt.Printf("Logged in. Access token expires in %d seconds.\n", tokenResp.ExpiresIn)
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
